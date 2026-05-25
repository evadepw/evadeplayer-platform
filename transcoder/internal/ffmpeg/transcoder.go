package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Quality struct {
	Name     string
	Height   int
	Width    int    // actual source width; 0 = approximate from height (16:9)
	Bitrate  string // empty = encoder default (used for "original" quality)
	ABitrate string
	Original bool // if true, video is not scaled (source resolution is kept)
}

type Codec struct {
	Name        string
	InputArgs   []string
	VideoEnc    string
	AudioEnc    string
	ExtraArgs   []string
	ScaleFilter string
	SegmentType string // "mpegts" or "fmp4"
	SegmentExt  string // "ts" or "m4s"
	HLSCodecs   string // CODECS= value for master manifest
	Optional    bool   // if true, failure skips the codec rather than aborting
	CRF         int    // >0 enables quality-based mode (bitrate fields ignored)
}

type Variant struct {
	Codec   *Codec
	Quality *Quality
}

// Qualities defines default per-resolution encoding parameters (YouTube-equivalent defaults).
var Qualities = []Quality{
	{Name: "360p", Height: 360, Bitrate: "1000k", ABitrate: "128k"},
	{Name: "720p", Height: 720, Bitrate: "5000k", ABitrate: "192k"},
	{Name: "1080p", Height: 1080, Bitrate: "8000k", ABitrate: "192k"},
	{Name: "1440p", Height: 1440, Bitrate: "16000k", ABitrate: "192k"},
}

var Codecs = []Codec{
	{
		Name:     "h264",
		VideoEnc: "libx264",
		AudioEnc: "aac",
		ExtraArgs: []string{
			"-preset", "fast",
			"-profile:v", "high",
			"-level", "4.0",
			"-sc_threshold", "0",
		},
		ScaleFilter: "scale=-2:%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "avc1.640028,mp4a.40.2",
	},
	{
		Name:     "h265",
		VideoEnc: "libx265",
		AudioEnc: "aac",
		ExtraArgs: []string{
			"-preset", "fast",
			"-tag:v", "hvc1",
			"-sc_threshold", "0",
			"-x265-params", "pools=none",
		},
		ScaleFilter: "scale=-2:%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "hvc1.1.6.L120.90,mp4a.40.2",
		Optional:    true,
	},
	{
		Name:     "av1",
		VideoEnc: "libaom-av1",
		AudioEnc: "aac",
		ExtraArgs: []string{
			"-cpu-used", "8",
			"-row-mt", "1",
			"-tiles", "2x2",
		},
		ScaleFilter: "scale=-2:%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "av01.0.04M.08,mp4a.40.2",
		Optional:    true,
		CRF:         30,
	},
}

var nvidiaCodecs = []Codec{
	{
		Name:      "h264",
		InputArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:  "h264_nvenc",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-preset", "p4",
			"-profile:v", "high",
		},
		ScaleFilter: "scale_cuda=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "avc1.640028,mp4a.40.2",
	},
	{
		Name:      "h265",
		InputArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:  "hevc_nvenc",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-preset", "p4",
			"-tag:v", "hvc1",
		},
		ScaleFilter: "scale_cuda=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "hvc1.1.6.L120.90,mp4a.40.2",
		Optional:    true,
	},
	{
		Name:      "av1",
		InputArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:  "av1_nvenc",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-preset", "p4",
			"-sc_threshold", "0",
		},
		ScaleFilter: "scale_cuda=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "av01.0.04M.08,mp4a.40.2",
		Optional:    true,
	},
}

var vaapiCodecs = []Codec{
	{
		Name:      "h264",
		InputArgs: []string{"-vaapi_device", "/dev/dri/renderD128"},
		VideoEnc:  "h264_vaapi",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-profile:v", "high",
		},
		ScaleFilter: "format=nv12,hwupload,scale_vaapi=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "avc1.640028,mp4a.40.2",
	},
	{
		Name:      "h265",
		InputArgs: []string{"-vaapi_device", "/dev/dri/renderD128"},
		VideoEnc:  "hevc_vaapi",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-tag:v", "hvc1",
		},
		ScaleFilter: "format=nv12,hwupload,scale_vaapi=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "hvc1.1.6.L120.90,mp4a.40.2",
		Optional:    true,
	},
	{
		Name:        "av1",
		InputArgs:   []string{"-vaapi_device", "/dev/dri/renderD128"},
		VideoEnc:    "av1_vaapi",
		AudioEnc:    "aac",
		ScaleFilter: "format=nv12,hwupload,scale_vaapi=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "av01.0.04M.08,mp4a.40.2",
		Optional:    true,
	},
}

func CodecsForAccel(accel string) []Codec {
	switch accel {
	case "nvidia":
		return nvidiaCodecs
	case "vaapi":
		return vaapiCodecs
	default:
		return Codecs
	}
}

type AudioStream struct {
	TypeIndex int    // position among audio streams (used in ffmpeg: 0:a:<TypeIndex>)
	Language  string // from stream tags, empty if not set
	Title     string // from stream tags, empty if not set
}

type SubtitleStream struct {
	TypeIndex int
	Language  string
	Title     string
	Codec     string // e.g. subrip, ass, hdmv_pgs_subtitle
}

type ProbeResult struct {
	Duration  float64
	Width     int
	Height    int
	Audio     []AudioStream
	Subtitles []SubtitleStream
}

func Probe(ctx context.Context, inputPath string) (*ProbeResult, error) {
	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		inputPath,
	}
	out, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}

	var data struct {
		Streams []struct {
			CodecType string            `json:"codec_type"`
			CodecName string            `json:"codec_name"`
			Width     int               `json:"width"`
			Height    int               `json:"height"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	result := &ProbeResult{}
	audioIdx, subIdx := 0, 0
	for _, s := range data.Streams {
		switch s.CodecType {
		case "video":
			if s.Width > 0 && result.Width == 0 {
				result.Width = s.Width
				result.Height = s.Height
			}
		case "audio":
			result.Audio = append(result.Audio, AudioStream{
				TypeIndex: audioIdx,
				Language:  s.Tags["language"],
				Title:     s.Tags["title"],
			})
			audioIdx++
		case "subtitle":
			result.Subtitles = append(result.Subtitles, SubtitleStream{
				TypeIndex: subIdx,
				Language:  s.Tags["language"],
				Title:     s.Tags["title"],
				Codec:     s.CodecName,
			})
			subIdx++
		}
	}
	dur, _ := strconv.ParseFloat(data.Format.Duration, 64)
	result.Duration = dur
	return result, nil
}

// textSubtitleCodecs lists codecs that ffmpeg can convert to WebVTT.
// Bitmap-based codecs (hdmv_pgs_subtitle, dvd_subtitle, dvb_subtitle) are excluded.
var textSubtitleCodecs = map[string]bool{
	"subrip":   true,
	"ass":      true,
	"ssa":      true,
	"webvtt":   true,
	"mov_text": true,
	"text":     true,
	"microdvd": true,
}

// ExtractAudio creates audio-only HLS playlists for each stream under outputDir/audio/<typeIndex>/.
// Individual stream failures are logged and skipped; the returned slice contains only successful streams.
func ExtractAudio(ctx context.Context, inputPath, outputDir string, streams []AudioStream, segSecs int) ([]AudioStream, error) {
	if len(streams) == 0 {
		return nil, nil
	}
	type result struct {
		stream AudioStream
		ok     bool
	}
	results := make(chan result, len(streams))
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Add(1)
		go func(stream AudioStream) {
			defer wg.Done()
			dir := filepath.Join(outputDir, "audio", strconv.Itoa(stream.TypeIndex))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				log.Printf("skip audio stream %d: mkdir: %v", stream.TypeIndex, err)
				results <- result{stream, false}
				return
			}
			args := []string{
				"-i", inputPath,
				"-map", fmt.Sprintf("0:a:%d", stream.TypeIndex),
				"-c:a", "aac",
				"-b:a", "128k",
				"-ac", "2",
				"-ar", "48000",
				"-vn",
				"-f", "hls",
				"-hls_time", strconv.Itoa(segSecs),
				"-hls_playlist_type", "vod",
				"-hls_segment_type", "fmp4",
				"-hls_segment_filename", filepath.Join(dir, "%05d.m4s"),
				"-hls_fmp4_init_filename", "init.mp4",
				"-hls_flags", "independent_segments",
				filepath.Join(dir, "index.m3u8"),
			}
			cmd := exec.CommandContext(ctx, "ffmpeg", args...)
			cmd.Stdout = os.Stdout
			var stderr bytes.Buffer
			cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
			if err := cmd.Run(); err != nil {
				log.Printf("skip audio stream %d: %v: %s", stream.TypeIndex, err, lastLines(stderr.String(), 4))
				results <- result{stream, false}
				return
			}
			results <- result{stream, true}
		}(s)
	}
	wg.Wait()
	close(results)

	var produced []AudioStream
	for r := range results {
		if r.ok {
			produced = append(produced, r.stream)
		}
	}
	sort.Slice(produced, func(i, j int) bool { return produced[i].TypeIndex < produced[j].TypeIndex })
	return produced, nil
}

// ExtractSubtitles converts text-based subtitle streams to WebVTT and writes per-stream HLS playlists
// under outputDir/subs/<typeIndex>/. Bitmap subtitles are skipped with a log message.
func ExtractSubtitles(ctx context.Context, inputPath, outputDir string, streams []SubtitleStream, duration float64) ([]SubtitleStream, error) {
	var produced []SubtitleStream
	for _, s := range streams {
		if !textSubtitleCodecs[s.Codec] {
			log.Printf("skip subtitle stream %d: codec %q is not text-based", s.TypeIndex, s.Codec)
			continue
		}
		dir := filepath.Join(outputDir, "subs", strconv.Itoa(s.TypeIndex))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("skip subtitle stream %d: mkdir: %v", s.TypeIndex, err)
			continue
		}
		vttPath := filepath.Join(dir, "sub.vtt")
		args := []string{
			"-i", inputPath,
			"-map", fmt.Sprintf("0:s:%d", s.TypeIndex),
			"-c:s", "webvtt",
			vttPath,
		}
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		cmd.Stdout = os.Stdout
		var stderr bytes.Buffer
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
		if err := cmd.Run(); err != nil {
			log.Printf("skip subtitle stream %d: %v: %s", s.TypeIndex, err, lastLines(stderr.String(), 4))
			continue
		}
		targetDur := int(math.Ceil(duration))
		if targetDur < 1 {
			targetDur = 1
		}
		playlist := fmt.Sprintf("#EXTM3U\n#EXT-X-TARGETDURATION:%d\n#EXT-X-VERSION:3\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:%d,\nsub.vtt\n#EXT-X-ENDLIST\n",
			targetDur, targetDur)
		if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(playlist), 0o644); err != nil {
			log.Printf("skip subtitle stream %d: write playlist: %v", s.TypeIndex, err)
			continue
		}
		produced = append(produced, s)
	}
	return produced, nil
}

// TranscodeHLS transcodes the input to HLS for every Codec × Quality combination.
// Optional codecs (H.265, AV1) are skipped on encoding failure; H.264 failure is fatal.
// videoWidth and videoHeight come from Probe and are used to resolve the "original" quality.
// TranscodeHLS transcodes the input to HLS for every Codec × Quality combination.
// Optional codecs (H.265, AV1) are skipped on encoding failure; H.264 failure is fatal.
// qualities must be built via BuildQualities before calling.
// videoWidth and videoHeight come from Probe and are used to resolve the "original" quality.
// TranscodeHLS transcodes the input to HLS for every Codec × Quality combination.
// When separateAudio is true, audio is stripped from video segments (-an) because
// separate audio renditions will be muxed in by the master manifest. When false,
// the first audio track is muxed into the video segments as a fallback.
func TranscodeHLS(ctx context.Context, inputPath, outputDir string, videoWidth, videoHeight, hlsSegmentSeconds int, accel string, codecNames []string, qualities []Quality, separateAudio bool) ([]Variant, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	if hlsSegmentSeconds < 1 {
		hlsSegmentSeconds = 4
	}

	codecs, err := filterCodecs(CodecsForAccel(accel), codecNames)
	if err != nil {
		return nil, err
	}

	// Resolve "original" quality to actual source dimensions so downstream
	// code (scale filter skip, manifest resolution) has concrete values.
	resolved := make([]Quality, len(qualities))
	copy(resolved, qualities)
	for i := range resolved {
		if resolved[i].Original {
			resolved[i].Height = videoHeight
			resolved[i].Width = videoWidth
		}
	}
	qualities = resolved

	type codecResult struct {
		codec    *Codec
		variants []Variant
		err      error
	}
	results := make(chan codecResult, len(codecs))
	var wg sync.WaitGroup
	for ci := range codecs {
		c := &codecs[ci]
		wg.Add(1)
		go func(c *Codec) {
			defer wg.Done()
			v, err := transcodeCodec(ctx, inputPath, outputDir, c, qualities, videoHeight, hlsSegmentSeconds, separateAudio)
			results <- codecResult{codec: c, variants: v, err: err}
		}(c)
	}
	wg.Wait()
	close(results)

	var produced []Variant
	for r := range results {
		if r.err != nil {
			if r.codec.Optional {
				log.Printf("skip optional codec %s: %v", r.codec.Name, r.err)
				continue
			}
			return nil, fmt.Errorf("codec %s: %w", r.codec.Name, r.err)
		}
		produced = append(produced, r.variants...)
	}
	return produced, nil
}

func transcodeCodec(ctx context.Context, inputPath, outputDir string, c *Codec, qualities []Quality, videoHeight, hlsSegmentSeconds int, separateAudio bool) ([]Variant, error) {
	if !encoderAvailable(ctx, c.VideoEnc) {
		return nil, fmt.Errorf("ffmpeg encoder %q is not available in this container", c.VideoEnc)
	}

	var applicable []Quality
	for _, q := range qualities {
		if videoHeight > 0 && q.Height > videoHeight*120/100 {
			continue
		}
		applicable = append(applicable, q)
	}
	if len(applicable) == 0 {
		return nil, nil
	}

	for _, q := range applicable {
		if err := os.MkdirAll(filepath.Join(outputDir, c.Name, q.Name), 0o755); err != nil {
			return nil, fmt.Errorf("create dir: %w", err)
		}
	}

	scaleFilter := c.ScaleFilter
	if scaleFilter == "" {
		scaleFilter = "scale=-2:%d"
	}

	// Build filter_complex: decode once, split, scale each quality.
	var fc strings.Builder
	n := len(applicable)
	fc.WriteString(fmt.Sprintf("[0:v]split=%d", n))
	for i := range applicable {
		fmt.Fprintf(&fc, "[v%d]", i)
	}
	for i, q := range applicable {
		if q.Original {
			// No scaling: pass video through unchanged.
			fmt.Fprintf(&fc, ";[v%d]null[s%d]", i, i)
		} else {
			fmt.Fprintf(&fc, ";[v%d]%s[s%d]", i, fmt.Sprintf(scaleFilter, q.Height), i)
		}
	}

	args := append([]string{}, c.InputArgs...)
	args = append(args, "-i", inputPath, "-filter_complex", fc.String())

	for i, q := range applicable {
		qDir := filepath.Join(outputDir, c.Name, q.Name)
		segFilename := filepath.Join(qDir, fmt.Sprintf("%%05d.%s", c.SegmentExt))
		manifestPath := filepath.Join(qDir, "index.m3u8")

		args = append(args, "-map", fmt.Sprintf("[s%d]", i))
		if separateAudio {
			args = append(args, "-an")
		} else {
			args = append(args, "-map", "0:a:0?")
		}
		args = append(args, "-c:v", c.VideoEnc)
		args = append(args, c.ExtraArgs...)

		if c.CRF > 0 {
			args = append(args, "-crf", strconv.Itoa(c.CRF), "-b:v", "0")
		} else if q.Bitrate != "" {
			args = append(args,
				"-b:v", q.Bitrate,
				"-maxrate", q.Bitrate,
				"-bufsize", doubleRate(q.Bitrate),
			)
		}
		// empty Bitrate (original quality): rely on encoder defaults

		args = append(args,
			"-g", "48",
			"-keyint_min", "48",
			"-c:a", c.AudioEnc,
			"-b:a", q.ABitrate,
			"-ac", "2",
			"-ar", "44100",
			"-f", "hls",
			"-hls_time", strconv.Itoa(hlsSegmentSeconds),
			"-hls_playlist_type", "vod",
			"-hls_segment_type", c.SegmentType,
			"-hls_segment_filename", segFilename,
			"-hls_flags", "independent_segments",
		)
		if c.SegmentType == "fmp4" {
			args = append(args, "-hls_fmp4_init_filename", "init.mp4")
		}
		args = append(args, manifestPath)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = os.Stdout
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, lastLines(stderr.String(), 8))
	}

	var variants []Variant
	for i := range applicable {
		variants = append(variants, Variant{Codec: c, Quality: &applicable[i]})
	}
	return variants, nil
}

func filterCodecs(available []Codec, names []string) ([]Codec, error) {
	if len(names) == 0 {
		return available, nil
	}
	byName := make(map[string]Codec, len(available))
	for _, codec := range available {
		byName[codec.Name] = codec
	}
	var out []Codec
	for _, name := range names {
		codec, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown transcode codec %q", name)
		}
		out = append(out, codec)
	}
	return out, nil
}

// BuildQualities resolves quality names to Quality values.
// vBitrateOverrides optionally overrides the video bitrate per quality name
// (e.g. "1080p" → "10000k"). Pass nil or an empty map to use defaults.
// "original" is a special name: no scaling, no bitrate cap, encoder defaults apply.
func BuildQualities(names []string, vBitrateOverrides map[string]string) ([]Quality, error) {
	if len(names) == 0 {
		return append([]Quality(nil), Qualities...), nil
	}
	byName := make(map[string]Quality, len(Qualities)+1)
	for _, q := range Qualities {
		byName[q.Name] = q
	}
	byName["original"] = Quality{Name: "original", Original: true, ABitrate: "192k"}
	var out []Quality
	for _, name := range names {
		q, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown transcode quality %q", name)
		}
		if override := vBitrateOverrides[name]; override != "" {
			q.Bitrate = override
		}
		out = append(out, q)
	}
	return out, nil
}

func encoderAvailable(ctx context.Context, name string) bool {
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil {
		return false
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) >= 2 && string(fields[1]) == name {
			return true
		}
	}
	return false
}

func lastLines(s string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// WriteMasterManifest writes the master HLS playlist referencing all codec/quality variants,
// alternate audio renditions, and subtitle tracks.
func WriteMasterManifest(outputDir string, variants []Variant, audio []AudioStream, subtitles []SubtitleStream) error {
	return WriteMasterManifestWithConfig(outputDir, variants, audio, subtitles, DefaultThumbnailConfig())
}

func WriteMasterManifestWithConfig(outputDir string, variants []Variant, audio []AudioStream, subtitles []SubtitleStream, thumbnailCfg ThumbnailConfig) error {
	thumbnailCfg = thumbnailCfg.withDefaults()
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:6\n\n")

	for i, a := range audio {
		lang := a.Language
		if lang == "" {
			lang = "und"
		}
		name := mediaDisplayName(a.Title, a.Language, i)
		def := "NO"
		if i == 0 {
			def = "YES"
		}
		fmt.Fprintf(&sb, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",LANGUAGE=\"%s\",NAME=\"%s\",DEFAULT=%s,AUTOSELECT=YES,URI=\"audio/%d/index.m3u8\"\n",
			lang, name, def, a.TypeIndex)
	}
	if len(audio) > 0 {
		sb.WriteString("\n")
	}

	for i, s := range subtitles {
		lang := s.Language
		if lang == "" {
			lang = "und"
		}
		name := mediaDisplayName(s.Title, s.Language, i)
		fmt.Fprintf(&sb, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",LANGUAGE=\"%s\",NAME=\"%s\",DEFAULT=NO,FORCED=NO,URI=\"subs/%d/index.m3u8\"\n",
			lang, name, s.TypeIndex)
	}
	if len(subtitles) > 0 {
		sb.WriteString("\n")
	}

	if fileExists(filepath.Join(outputDir, "images", "index.m3u8")) {
		fmt.Fprintf(&sb, "#EXT-X-IMAGE-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"jpeg\",URI=\"images/index.m3u8\"\n\n",
			thumbnailCfg.ImageStreamBandwidth, thumbnailCfg.SpriteWidth, thumbnailCfg.SpriteHeight)
	}

	for _, v := range variants {
		videoBw := bitrateKbpsToInt(v.Quality.Bitrate)
		if videoBw == 0 {
			// "original" quality has no explicit bitrate; estimate from height.
			videoBw = bitrateEstimateForHeight(v.Quality.Height)
		}
		audioBw := bitrateKbpsToInt(v.Quality.ABitrate)
		var bw int
		if v.Codec.CRF > 0 {
			// AV1 ~40% of equivalent H.264 bitrate; use that as the BANDWIDTH hint.
			bw = videoBw*400 + audioBw*1000
		} else {
			bw = (videoBw + audioBw) * 1000
		}
		line := fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%s,CODECS=\"%s\",NAME=\"%s %s\"",
			bw, resolutionStr(v.Quality), v.Codec.HLSCodecs, v.Quality.Name, v.Codec.Name)
		if len(audio) > 0 {
			line += ",AUDIO=\"audio\""
		}
		if len(subtitles) > 0 {
			line += ",SUBTITLES=\"subs\""
		}
		fmt.Fprintf(&sb, "%s\n%s/%s/index.m3u8\n\n", line, v.Codec.Name, v.Quality.Name)
	}

	return os.WriteFile(filepath.Join(outputDir, "master.m3u8"), []byte(sb.String()), 0o644)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func mediaDisplayName(title, language string, idx int) string {
	if title != "" {
		return title
	}
	if language != "" {
		return language
	}
	return fmt.Sprintf("Track %d", idx+1)
}

func resolutionStr(q *Quality) string {
	w := q.Width
	if w == 0 {
		w = q.Height * 16 / 9
		if w%2 != 0 {
			w++
		}
	}
	return fmt.Sprintf("%dx%d", w, q.Height)
}

// bitrateEstimateForHeight returns a rough video bitrate (kbps) for a given height,
// used as a BANDWIDTH hint in the master manifest when the quality has no explicit bitrate.
func bitrateEstimateForHeight(h int) int {
	switch {
	case h >= 2160:
		return 15000
	case h >= 1440:
		return 8000
	case h >= 1080:
		return 5000
	case h >= 720:
		return 2500
	default:
		return 800
	}
}

func doubleRate(bitrate string) string {
	n := bitrateKbpsToInt(bitrate)
	return fmt.Sprintf("%dk", n*2)
}

func bitrateKbpsToInt(bitrate string) int {
	s := strings.TrimSuffix(bitrate, "k")
	n, _ := strconv.Atoi(s)
	return n
}
