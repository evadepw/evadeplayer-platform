package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
}

// EncodingConfig holds tunable encoder parameters exposed via environment variables.
type EncodingConfig struct {
	CPUPreset       string // libx264/libx265 preset (ultrafast…veryslow)
	NvidiaPreset    string // nvenc preset p1–p7
	AV1CPUUsed      int    // libaom-av1 cpu-used 0–8 (0=best quality, 8=fastest)
	AV1CRF          int    // libaom-av1 CRF value 0–63
	H264CRF         int    // libx264 CRF 0–51; 0=bitrate mode. Typical: 20–24
	H265CRF         int    // libx265 CRF 0–51; 0=bitrate mode. Typical: 24–28
	AudioSampleRate int    // output audio sample rate in Hz
	SceneCut        bool   // enable scene-cut keyframe insertion
}

func DefaultEncodingConfig() EncodingConfig {
	return EncodingConfig{
		CPUPreset:       "slow",
		NvidiaPreset:    "p5",
		AV1CPUUsed:      4,
		AV1CRF:          30,
		H264CRF:         0,
		H265CRF:         0,
		AudioSampleRate: 48000,
		SceneCut:        false,
	}
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

// Software codecs. Presets and scene-cut are applied dynamically from EncodingConfig.
var Codecs = []Codec{
	{
		Name:     "h264",
		VideoEnc: "libx264",
		AudioEnc: "aac",
		ExtraArgs: []string{
			"-profile:v", "high",
			"-level:v", "5.1",
		},
		ScaleFilter: "scale=-2:%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "avc1.640033,mp4a.40.2",
	},
	{
		Name:     "h265",
		VideoEnc: "libx265",
		AudioEnc: "aac",
		ExtraArgs: []string{
			"-tag:v", "hvc1",
		},
		ScaleFilter: "scale=-2:%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "hvc1.1.6.L150.90,mp4a.40.2",
		Optional:    true,
	},
	{
		Name:     "av1",
		VideoEnc: "libaom-av1",
		AudioEnc: "aac",
		ExtraArgs: []string{
			"-row-mt", "1",
			"-tiles", "2x2",
		},
		ScaleFilter: "scale=-2:%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "av01.0.08M.08,mp4a.40.2",
		Optional:    true,
	},
}

var nvidiaCodecs = []Codec{
	{
		Name:      "h264",
		InputArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:  "h264_nvenc",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-profile:v", "high",
		},
		ScaleFilter: "scale_cuda=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "avc1.640033,mp4a.40.2",
	},
	{
		Name:      "h265",
		InputArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:  "hevc_nvenc",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-tag:v", "hvc1",
		},
		ScaleFilter: "scale_cuda=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "hvc1.1.6.L150.90,mp4a.40.2",
		Optional:    true,
	},
	{
		Name:        "av1",
		InputArgs:   []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:    "av1_nvenc",
		AudioEnc:    "aac",
		ScaleFilter: "scale_cuda=w=-2:h=%d",
		SegmentType: "fmp4",
		SegmentExt:  "m4s",
		HLSCodecs:   "av01.0.08M.08,mp4a.40.2",
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
		HLSCodecs:   "avc1.640033,mp4a.40.2",
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
		HLSCodecs:   "hvc1.1.6.L150.90,mp4a.40.2",
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
		HLSCodecs:   "av01.0.08M.08,mp4a.40.2",
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
	FrameRate float64 // frames per second parsed from r_frame_rate; 0 if unavailable
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
			CodecType  string            `json:"codec_type"`
			CodecName  string            `json:"codec_name"`
			Width      int               `json:"width"`
			Height     int               `json:"height"`
			RFrameRate string            `json:"r_frame_rate"`
			Tags       map[string]string `json:"tags"`
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
				result.FrameRate = parseFrameRate(s.RFrameRate)
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

// parseFrameRate parses an ffprobe r_frame_rate fraction (e.g. "30/1", "24000/1001").
func parseFrameRate(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
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
func ExtractAudio(ctx context.Context, inputPath, outputDir string, streams []AudioStream, segSecs int, cfg EncodingConfig) ([]AudioStream, error) {
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
				slog.Warn("skip audio stream: mkdir failed", "stream", stream.TypeIndex, "error", err)
				results <- result{stream, false}
				return
			}
			args := []string{
				"-i", inputPath,
				"-map", fmt.Sprintf("0:a:%d", stream.TypeIndex),
				"-c:a", "aac",
				"-b:a", "128k",
				"-ac", "2",
				"-ar", strconv.Itoa(cfg.AudioSampleRate),
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
				slog.Warn("skip audio stream: ffmpeg failed", "stream", stream.TypeIndex, "error", err, "stderr", lastLines(stderr.String(), 4))
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
			slog.Info("skip subtitle stream: codec is not text-based", "stream", s.TypeIndex, "codec", s.Codec)
			continue
		}
		dir := filepath.Join(outputDir, "subs", strconv.Itoa(s.TypeIndex))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Warn("skip subtitle stream: mkdir failed", "stream", s.TypeIndex, "error", err)
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
			slog.Warn("skip subtitle stream: ffmpeg failed", "stream", s.TypeIndex, "error", err, "stderr", lastLines(stderr.String(), 4))
			continue
		}
		targetDur := int(math.Ceil(duration))
		if targetDur < 1 {
			targetDur = 1
		}
		playlist := fmt.Sprintf(
			"#EXTM3U\n#EXT-X-TARGETDURATION:%d\n#EXT-X-VERSION:3\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:%.3f,\nsub.vtt\n#EXT-X-ENDLIST\n",
			targetDur, duration,
		)
		if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(playlist), 0o644); err != nil {
			slog.Warn("skip subtitle stream: write playlist failed", "stream", s.TypeIndex, "error", err)
			continue
		}
		produced = append(produced, s)
	}
	return produced, nil
}

// TranscodeHLS transcodes the input to HLS for every Codec × Quality combination.
// Optional codecs (H.265, AV1) are skipped on encoding failure; H.264 failure is fatal.
// When separateAudio is true, audio is stripped from video segments because separate
// audio renditions will be muxed in by the master manifest.
// onProgress is called with a fraction 0..1 as encoding proceeds; may be nil.
func TranscodeHLS(
	ctx context.Context,
	inputPath, outputDir string,
	videoWidth, videoHeight, hlsSegmentSeconds int,
	frameRate float64,
	duration float64,
	accel string,
	codecNames []string,
	qualities []Quality,
	separateAudio bool,
	cfg EncodingConfig,
	onProgress func(float64),
) ([]Variant, error) {
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

	// Per-codec progress tracking: report the average across all running codecs.
	var progressMu sync.Mutex
	codecProgress := make(map[string]float64, len(codecs))
	for ci := range codecs {
		codecProgress[codecs[ci].Name] = 0
	}
	reportProgress := func(name string, f float64) {
		if onProgress == nil {
			return
		}
		progressMu.Lock()
		codecProgress[name] = f
		total := 0.0
		for _, v := range codecProgress {
			total += v
		}
		avg := total / float64(len(codecProgress))
		progressMu.Unlock()
		onProgress(avg)
	}

	var wg sync.WaitGroup
	for ci := range codecs {
		c := &codecs[ci]
		wg.Add(1)
		go func(c *Codec) {
			defer wg.Done()
			cb := func(f float64) { reportProgress(c.Name, f) }
			v, err := transcodeCodec(ctx, inputPath, outputDir, c, qualities, videoHeight, hlsSegmentSeconds, frameRate, duration, separateAudio, cfg, cb)
			results <- codecResult{codec: c, variants: v, err: err}
		}(c)
	}
	wg.Wait()
	close(results)

	var produced []Variant
	for r := range results {
		if r.err != nil {
			if r.codec.Optional {
				slog.Warn("skip optional codec", "codec", r.codec.Name, "error", r.err)
				continue
			}
			return nil, fmt.Errorf("codec %s: %w", r.codec.Name, r.err)
		}
		produced = append(produced, r.variants...)
	}
	return produced, nil
}

func transcodeCodec(
	ctx context.Context,
	inputPath, outputDir string,
	c *Codec,
	qualities []Quality,
	videoHeight, hlsSegmentSeconds int,
	frameRate, duration float64,
	separateAudio bool,
	cfg EncodingConfig,
	onProgress func(float64),
) ([]Variant, error) {
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

	// GOP aligned to segment duration: keyframe every segment so players can seek cleanly.
	fps := frameRate
	if fps <= 0 {
		fps = 25
	}
	gopSize := int(math.Round(fps)) * hlsSegmentSeconds

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
		if !separateAudio {
			args = append(args, "-map", "0:a:0?")
		}

		args = append(args, "-c:v", c.VideoEnc)
		args = append(args, c.ExtraArgs...)
		args = append(args, dynamicEncoderArgs(c, cfg)...)

		switch c.VideoEnc {
		case "libx264":
			if cfg.H264CRF > 0 {
				// Capped CRF: constant quality with peak bitrate cap for ABR predictability.
				args = append(args, "-crf", strconv.Itoa(cfg.H264CRF))
				if q.Bitrate != "" {
					args = append(args, "-maxrate", q.Bitrate, "-bufsize", doubleRate(q.Bitrate))
				}
			} else if q.Bitrate != "" {
				args = append(args, "-b:v", q.Bitrate, "-maxrate", q.Bitrate, "-bufsize", doubleRate(q.Bitrate))
			}
		case "libx265":
			if cfg.H265CRF > 0 {
				args = append(args, "-crf", strconv.Itoa(cfg.H265CRF))
				if q.Bitrate != "" {
					args = append(args, "-maxrate", q.Bitrate, "-bufsize", doubleRate(q.Bitrate))
				}
			} else if q.Bitrate != "" {
				args = append(args, "-b:v", q.Bitrate, "-maxrate", q.Bitrate, "-bufsize", doubleRate(q.Bitrate))
			}
		case "libaom-av1":
			args = append(args, "-crf", strconv.Itoa(cfg.AV1CRF), "-b:v", "0")
		default:
			// Hardware encoders (nvenc, vaapi): bitrate mode only.
			if q.Bitrate != "" {
				args = append(args, "-b:v", q.Bitrate, "-maxrate", q.Bitrate, "-bufsize", doubleRate(q.Bitrate))
			}
		}

		args = append(args,
			"-g", strconv.Itoa(gopSize),
			"-keyint_min", strconv.Itoa(gopSize),
		)

		if !separateAudio {
			args = append(args,
				"-c:a", c.AudioEnc,
				"-b:a", q.ABitrate,
				"-ac", "2",
				"-ar", strconv.Itoa(cfg.AudioSampleRate),
			)
		}

		args = append(args,
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

	var stderrBuf bytes.Buffer
	var runErr error

	if onProgress != nil && duration > 0 {
		pr, pw := io.Pipe()
		cmd.Stderr = io.MultiWriter(&stderrBuf, os.Stderr, pw)

		if err := cmd.Start(); err != nil {
			pw.Close()
			pr.Close()
			return nil, fmt.Errorf("ffmpeg start: %w", err)
		}

		var parseWg sync.WaitGroup
		parseWg.Add(1)
		go func() {
			defer parseWg.Done()
			scanner := bufio.NewScanner(pr)
			for scanner.Scan() {
				if t := parseFFmpegTime(scanner.Text()); t >= 0 {
					f := t / duration
					if f > 1 {
						f = 1
					}
					onProgress(f)
				}
			}
		}()

		runErr = cmd.Wait()
		pw.Close()
		parseWg.Wait()
	} else {
		cmd.Stderr = io.MultiWriter(&stderrBuf, os.Stderr)
		runErr = cmd.Run()
	}

	if runErr != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", runErr, lastLines(stderrBuf.String(), 8))
	}

	var variants []Variant
	for i := range applicable {
		variants = append(variants, Variant{Codec: c, Quality: &applicable[i]})
	}
	return variants, nil
}

// parseFFmpegTime extracts the current encoding position (seconds) from an ffmpeg
// progress line containing "time=HH:MM:SS.ss". Returns -1 if not found or N/A.
func parseFFmpegTime(line string) float64 {
	const prefix = "time="
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return -1
	}
	s := line[idx+len(prefix):]
	if strings.HasPrefix(s, "N/A") {
		return -1
	}
	if i := strings.IndexAny(s, " \t\r\n"); i > 0 {
		s = s[:i]
	}
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return -1
	}
	h, err1 := strconv.ParseFloat(parts[0], 64)
	m, err2 := strconv.ParseFloat(parts[1], 64)
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return -1
	}
	return h*3600 + m*60 + sec
}

// dynamicEncoderArgs returns encoder-specific args derived from EncodingConfig:
// preset for CPU/NVIDIA encoders, scene-cut control, and AV1 cpu-used.
func dynamicEncoderArgs(c *Codec, cfg EncodingConfig) []string {
	var args []string
	switch c.VideoEnc {
	case "libx264":
		if cfg.CPUPreset != "" {
			args = append(args, "-preset", cfg.CPUPreset)
		}
		if !cfg.SceneCut {
			args = append(args, "-sc_threshold", "0")
		}
	case "libx265":
		if cfg.CPUPreset != "" {
			args = append(args, "-preset", cfg.CPUPreset)
		}
		x265p := "pools=none"
		if !cfg.SceneCut {
			x265p += ":scenecut=0"
		}
		args = append(args, "-x265-params", x265p)
	case "libaom-av1":
		args = append(args, "-cpu-used", strconv.Itoa(cfg.AV1CPUUsed))
	case "h264_nvenc", "hevc_nvenc", "av1_nvenc":
		if cfg.NvidiaPreset != "" {
			args = append(args, "-preset", cfg.NvidiaPreset)
		}
	}
	return args
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
	thumbnailCfg = thumbnailCfg.WithDefaults()
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
			videoBw = bitrateEstimateForHeight(v.Quality.Height)
		}
		audioBw := bitrateKbpsToInt(v.Quality.ABitrate)
		var bw int
		if v.Codec.VideoEnc == "libaom-av1" {
			// AV1 CRF mode: estimate ~40% of equivalent H.264 bitrate.
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
