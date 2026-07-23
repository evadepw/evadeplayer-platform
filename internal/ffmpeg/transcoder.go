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
	// OriginalFilter processes the unscaled "original" rung. It must still pin
	// the pixel format (and upload to GPU memory for hardware encoders) so that
	// 10-bit/4:2:2 sources encode to the 8-bit profiles declared in HLSCodecs.
	OriginalFilter string
	SegmentType    string // "mpegts" or "fmp4"
	SegmentExt     string // "ts" or "m4s"
	HLSCodecs      string // CODECS= value for master manifest
	Optional       bool   // if true, failure skips the codec rather than aborting
}

// EncodingConfig holds tunable encoder parameters exposed via environment variables.
type EncodingConfig struct {
	CPUPreset       string // libx264/libx265 preset (ultrafast…veryslow)
	NvidiaPreset    string // nvenc preset p1–p7
	AV1CPUUsed      int    // libaom-av1 cpu-used 0–8 (0=best quality, 8=fastest)
	AV1CRF          int    // libaom-av1 CRF value 0–63
	AV1Tiles        string // libaom-av1 tile layout "COLSxROWS" (more tiles = more threads); "" disables
	AV1RowMT        bool   // libaom-av1 row-based multithreading
	H264CRF         int    // libx264 CRF 0–51; 0=bitrate mode. Typical: 20–24
	H265CRF         int    // libx265 CRF 0–51; 0=bitrate mode. Typical: 24–28
	AudioBitrate    string // AAC bitrate for separate audio renditions (e.g. "128k")
	AudioSampleRate int    // output audio sample rate in Hz
	AudioChannels   int    // output audio channel count (2 = downmix to stereo)
	SceneCut        bool   // enable scene-cut keyframe insertion
	VAAPIDevice     string // DRM render node for VAAPI (default /dev/dri/renderD128)
}

func DefaultEncodingConfig() EncodingConfig {
	return EncodingConfig{
		CPUPreset:       "slow",
		NvidiaPreset:    "p5",
		AV1CPUUsed:      6,
		AV1CRF:          30,
		AV1Tiles:        "2x2",
		AV1RowMT:        true,
		H264CRF:         0,
		H265CRF:         0,
		AudioBitrate:    "128k",
		AudioSampleRate: 48000,
		AudioChannels:   2,
		SceneCut:        false,
		VAAPIDevice:     defaultVAAPIDevice,
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
		ScaleFilter:    "scale=-2:%d:flags=lanczos,format=yuv420p",
		OriginalFilter: "format=yuv420p",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "avc1.640033,mp4a.40.2",
	},
	{
		Name:     "h265",
		VideoEnc: "libx265",
		AudioEnc: "aac",
		ExtraArgs: []string{
			"-tag:v", "hvc1",
		},
		ScaleFilter:    "scale=-2:%d:flags=lanczos,format=yuv420p",
		OriginalFilter: "format=yuv420p",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "hvc1.1.6.L150.90,mp4a.40.2",
		Optional:       true,
	},
	{
		Name:           "av1",
		VideoEnc:       "libaom-av1",
		AudioEnc:       "aac",
		ScaleFilter:    "scale=-2:%d:flags=lanczos,format=yuv420p",
		OriginalFilter: "format=yuv420p",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "av01.0.08M.08,mp4a.40.2",
		Optional:       true,
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
		ScaleFilter:    "scale_cuda=w=-2:h=%d:format=nv12",
		OriginalFilter: "scale_cuda=w=iw:h=ih:format=nv12",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "avc1.640033,mp4a.40.2",
	},
	{
		Name:      "h265",
		InputArgs: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:  "hevc_nvenc",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-tag:v", "hvc1",
		},
		ScaleFilter:    "scale_cuda=w=-2:h=%d:format=nv12",
		OriginalFilter: "scale_cuda=w=iw:h=ih:format=nv12",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "hvc1.1.6.L150.90,mp4a.40.2",
		Optional:       true,
	},
	{
		Name:           "av1",
		InputArgs:      []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
		VideoEnc:       "av1_nvenc",
		AudioEnc:       "aac",
		ScaleFilter:    "scale_cuda=w=-2:h=%d:format=nv12",
		OriginalFilter: "scale_cuda=w=iw:h=ih:format=nv12",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "av01.0.08M.08,mp4a.40.2",
		Optional:       true,
	},
}

var vaapiCodecs = []Codec{
	{
		Name:      "h264",
		InputArgs: []string{"-vaapi_device", defaultVAAPIDevice},
		VideoEnc:  "h264_vaapi",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-profile:v", "high",
		},
		ScaleFilter:    "format=nv12,hwupload,scale_vaapi=w=-2:h=%d",
		OriginalFilter: "format=nv12,hwupload",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "avc1.640033,mp4a.40.2",
	},
	{
		Name:      "h265",
		InputArgs: []string{"-vaapi_device", defaultVAAPIDevice},
		VideoEnc:  "hevc_vaapi",
		AudioEnc:  "aac",
		ExtraArgs: []string{
			"-tag:v", "hvc1",
		},
		ScaleFilter:    "format=nv12,hwupload,scale_vaapi=w=-2:h=%d",
		OriginalFilter: "format=nv12,hwupload",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "hvc1.1.6.L150.90,mp4a.40.2",
		Optional:       true,
	},
	{
		Name:           "av1",
		InputArgs:      []string{"-vaapi_device", defaultVAAPIDevice},
		VideoEnc:       "av1_vaapi",
		AudioEnc:       "aac",
		ScaleFilter:    "format=nv12,hwupload,scale_vaapi=w=-2:h=%d",
		OriginalFilter: "format=nv12,hwupload",
		SegmentType:    "fmp4",
		SegmentExt:     "m4s",
		HLSCodecs:      "av01.0.08M.08,mp4a.40.2",
		Optional:       true,
	},
}

const defaultVAAPIDevice = "/dev/dri/renderD128"

// CodecsForAccel returns the codec set for a hardware acceleration mode.
// vaapiDevice overrides the DRM render node for VAAPI; "" keeps the default.
func CodecsForAccel(accel, vaapiDevice string) []Codec {
	switch accel {
	case "nvidia":
		return nvidiaCodecs
	case "vaapi":
		if vaapiDevice == "" || vaapiDevice == defaultVAAPIDevice {
			return vaapiCodecs
		}
		out := make([]Codec, len(vaapiCodecs))
		copy(out, vaapiCodecs)
		for i := range out {
			out[i].InputArgs = []string{"-vaapi_device", vaapiDevice}
		}
		return out
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
	abitrate := cfg.AudioBitrate
	if abitrate == "" {
		abitrate = "128k"
	}
	channels := cfg.AudioChannels
	if channels < 1 {
		channels = 2
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
				"-b:a", abitrate,
				"-ac", strconv.Itoa(channels),
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

	codecs, err := filterCodecs(CodecsForAccel(accel, cfg.VAAPIDevice), codecNames)
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
	sortVariants(produced, codecs, qualities)
	return produced, nil
}

// sortVariants orders variants by configured codec order, then configured quality
// order. Goroutine completion order must not leak into the master playlist: its
// first variant is what players start with, so the first configured codec and
// quality (lowest rung by default) comes first for fast, universal startup.
func sortVariants(variants []Variant, codecs []Codec, qualities []Quality) {
	codecIdx := make(map[string]int, len(codecs))
	for i := range codecs {
		codecIdx[codecs[i].Name] = i
	}
	qualityIdx := make(map[string]int, len(qualities))
	for i := range qualities {
		qualityIdx[qualities[i].Name] = i
	}
	sort.Slice(variants, func(i, j int) bool {
		if a, b := codecIdx[variants[i].Codec.Name], codecIdx[variants[j].Codec.Name]; a != b {
			return a < b
		}
		return qualityIdx[variants[i].Quality.Name] < qualityIdx[variants[j].Quality.Name]
	})
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
	originalFilter := c.OriginalFilter
	if originalFilter == "" {
		originalFilter = "null"
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
			fmt.Fprintf(&fc, ";[v%d]%s[s%d]", i, originalFilter, i)
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

		// Force an IDR exactly at every segment boundary so all variants and
		// codecs share identical segment start times (clean ABR switching, and
		// required by Apple's HLS authoring spec). This is independent of scene-cut
		// detection — relying on -g/scenecut alone left segments misaligned because
		// libx265 ignored scenecut=0. Extra scene-cut keyframes (when enabled) then
		// fall inside segments and only improve seeking.
		args = append(args,
			"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", hlsSegmentSeconds),
			"-g", strconv.Itoa(gopSize),
			"-keyint_min", strconv.Itoa(gopSize),
		)

		if !separateAudio {
			channels := cfg.AudioChannels
			if channels < 1 {
				channels = 2
			}
			args = append(args,
				"-c:a", c.AudioEnc,
				"-b:a", q.ABitrate,
				"-ac", strconv.Itoa(channels),
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
			_ = pw.Close()
			_ = pr.Close()
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
		_ = pw.Close()
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
		if !cfg.SceneCut {
			args = append(args, "-x265-params", "scenecut=0")
		}
	case "libaom-av1":
		args = append(args, "-cpu-used", strconv.Itoa(cfg.AV1CPUUsed))
		if cfg.AV1RowMT {
			args = append(args, "-row-mt", "1")
		}
		if cfg.AV1Tiles != "" {
			args = append(args, "-tiles", cfg.AV1Tiles)
		}
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

// MasterParams describes everything referenced by the master playlist.
type MasterParams struct {
	Variants  []Variant
	Audio     []AudioStream
	Subtitles []SubtitleStream
	Thumbnail ThumbnailConfig // zero value = defaults
	FrameRate float64         // source frame rate; 0 = unknown (omits FRAME-RATE, assumes 30 for levels)
}

// nameSet hands out NAME values that are unique within one rendition group,
// as RFC 8216 §4.3.4.1 requires.
type nameSet map[string]int

func (s nameSet) unique(base string) string {
	s[base]++
	if s[base] == 1 {
		return base
	}
	return fmt.Sprintf("%s %d", base, s[base])
}

// WriteMasterManifest writes the master HLS playlist referencing all codec/quality
// variants, alternate audio renditions, and subtitle tracks. When the variant media
// playlists exist under outputDir, BANDWIDTH and AVERAGE-BANDWIDTH are measured from
// the encoded segments as RFC 8216 requires; otherwise ladder estimates are used.
func WriteMasterManifest(outputDir string, p MasterParams) error {
	thumbnailCfg := p.Thumbnail.WithDefaults()
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:6\n")
	sb.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n\n")

	audioNames := nameSet{}
	audioLangs := map[string]bool{}
	for i, a := range p.Audio {
		lang := a.Language
		if lang == "" {
			lang = "und"
		}
		def := "NO"
		if i == 0 {
			def = "YES"
		}
		// Renditions with AUTOSELECT=YES must be distinguishable by language
		// (RFC 8216 §4.3.4.1.1), so only the first track per language gets it.
		autoselect := "NO"
		if !audioLangs[lang] || i == 0 {
			audioLangs[lang] = true
			autoselect = "YES"
		}
		fmt.Fprintf(&sb, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",LANGUAGE=\"%s\",NAME=\"%s\",DEFAULT=%s,AUTOSELECT=%s,URI=\"audio/%d/index.m3u8\"\n",
			lang, audioNames.unique(mediaDisplayName(a.Title, a.Language, i)), def, autoselect, a.TypeIndex)
	}
	if len(p.Audio) > 0 {
		sb.WriteString("\n")
	}

	subNames := nameSet{}
	subLangs := map[string]bool{}
	for i, t := range p.Subtitles {
		lang := t.Language
		if lang == "" {
			lang = "und"
		}
		autoselect := "NO"
		if !subLangs[lang] {
			subLangs[lang] = true
			autoselect = "YES"
		}
		fmt.Fprintf(&sb, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",LANGUAGE=\"%s\",NAME=\"%s\",DEFAULT=NO,AUTOSELECT=%s,FORCED=NO,URI=\"subs/%d/index.m3u8\"\n",
			lang, subNames.unique(mediaDisplayName(t.Title, t.Language, i)), autoselect, t.TypeIndex)
	}
	if len(p.Subtitles) > 0 {
		sb.WriteString("\n")
	}

	if fileExists(filepath.Join(outputDir, "images", "index.m3u8")) {
		fmt.Fprintf(&sb, "#EXT-X-IMAGE-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"jpeg\",URI=\"images/index.m3u8\"\n\n",
			thumbnailCfg.ImageStreamBandwidth, thumbnailCfg.SpriteWidth, thumbnailCfg.SpriteHeight)
	}

	// With demuxed audio the variant BANDWIDTH must cover video plus the audio
	// rendition the player will fetch; use the heaviest rendition as the bound.
	audioPeak, audioAvg := 0, 0
	for _, a := range p.Audio {
		if pk, av, ok := measuredBandwidth(filepath.Join(outputDir, "audio", strconv.Itoa(a.TypeIndex), "index.m3u8")); ok {
			if pk > audioPeak {
				audioPeak = pk
			}
			if av > audioAvg {
				audioAvg = av
			}
		}
	}

	for _, v := range p.Variants {
		bw, avgBw := variantBandwidth(outputDir, v, audioPeak, audioAvg)
		line := fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d", bw)
		if avgBw > 0 {
			line += fmt.Sprintf(",AVERAGE-BANDWIDTH=%d", avgBw)
		}
		line += fmt.Sprintf(",RESOLUTION=%s,CODECS=\"%s\"", resolutionStr(v.Quality), variantCodecs(v, p.FrameRate))
		if p.FrameRate > 0 {
			line += fmt.Sprintf(",FRAME-RATE=%.3f", p.FrameRate)
		}
		if len(p.Audio) > 0 {
			line += ",AUDIO=\"audio\""
		}
		if len(p.Subtitles) > 0 {
			line += ",SUBTITLES=\"subs\""
		}
		line += ",CLOSED-CAPTIONS=NONE"
		// NAME is not an RFC 8216 STREAM-INF attribute, but hls.js reads it for
		// quality labels; conforming clients must ignore unknown attributes.
		line += fmt.Sprintf(",NAME=\"%s %s\"", v.Quality.Name, v.Codec.Name)
		fmt.Fprintf(&sb, "%s\n%s/%s/index.m3u8\n\n", line, v.Codec.Name, v.Quality.Name)
	}

	return os.WriteFile(filepath.Join(outputDir, "master.m3u8"), []byte(sb.String()), 0o644)
}

// variantBandwidth returns the BANDWIDTH/AVERAGE-BANDWIDTH pair for a variant.
// Measured values from the encoded output are preferred; the fallback estimate
// (used when segments are not on disk) reports no average.
func variantBandwidth(outputDir string, v Variant, audioPeak, audioAvg int) (bw, avgBw int) {
	if peak, avg, ok := measuredBandwidth(filepath.Join(outputDir, v.Codec.Name, v.Quality.Name, "index.m3u8")); ok {
		return peak + audioPeak, avg + audioAvg
	}
	videoBw := bitrateKbpsToInt(v.Quality.Bitrate)
	if videoBw == 0 {
		videoBw = bitrateEstimateForHeight(v.Quality.Height)
	}
	audioBw := bitrateKbpsToInt(v.Quality.ABitrate)
	if v.Codec.VideoEnc == "libaom-av1" {
		// AV1 CRF mode: estimate ~40% of equivalent H.264 bitrate.
		return videoBw*400 + audioBw*1000, 0
	}
	return (videoBw + audioBw) * 1000, 0
}

// measuredBandwidth computes peak and average bits per second of a media
// playlist from the segment files next to it. ok is false when the playlist or
// any referenced segment cannot be read.
func measuredBandwidth(playlistPath string) (peak, avg int, ok bool) {
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		return 0, 0, false
	}
	dir := filepath.Dir(playlistPath)
	segDur := -1.0
	var totalBytes int64
	var totalDur, peakBps float64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			val := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.Index(val, ","); i >= 0 {
				val = val[:i]
			}
			d, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return 0, 0, false
			}
			segDur = d
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			uri := attrValue(line, "URI")
			if uri == "" {
				return 0, 0, false
			}
			info, err := os.Stat(filepath.Join(dir, uri))
			if err != nil {
				return 0, 0, false
			}
			totalBytes += info.Size()
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			info, err := os.Stat(filepath.Join(dir, line))
			if err != nil {
				return 0, 0, false
			}
			totalBytes += info.Size()
			if segDur > 0 {
				if bps := float64(info.Size()) * 8 / segDur; bps > peakBps {
					peakBps = bps
				}
				totalDur += segDur
			}
			segDur = -1
		}
	}
	if totalDur <= 0 {
		return 0, 0, false
	}
	return int(math.Ceil(peakBps)), int(math.Ceil(float64(totalBytes) * 8 / totalDur)), true
}

// attrValue extracts a quoted attribute value (NAME="...") from a playlist tag line.
func attrValue(line, name string) string {
	key := name + `="`
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// hevcLevels maps HEVC levels (Main tier) to their max luma picture size and
// max luma sample rate, per ITU-T H.265 table A.8. The CODECS level must not
// understate the bitstream or players will refuse variants they could play.
var hevcLevels = []struct {
	level int
	maxPS int64
	maxSR int64
}{
	{90, 552960, 16588800},     // 3.0
	{93, 983040, 33177600},     // 3.1
	{120, 2228224, 66846720},   // 4.0
	{123, 2228224, 133693440},  // 4.1
	{150, 8912896, 267386880},  // 5.0
	{153, 8912896, 534773760},  // 5.1
	{156, 8912896, 1069547520}, // 5.2
}

// av1Levels maps AV1 seq_level_idx to max picture size and max display rate,
// per the AV1 spec annex A.3.
var av1Levels = []struct {
	idx   int
	maxPS int64
	maxSR int64
}{
	{0, 147456, 4423680},     // 2.0
	{1, 278784, 8363520},     // 2.1
	{4, 665856, 19975680},    // 3.0
	{5, 1065024, 31950720},   // 3.1
	{8, 2359296, 70778880},   // 4.0
	{9, 2359296, 141557760},  // 4.1
	{12, 8912896, 267386880}, // 5.0
	{13, 8912896, 534773760}, // 5.1
}

// variantCodecs returns the CODECS attribute for a variant. H.264 is fixed at
// High@5.1 because the encoder is forced to that level; HEVC and AV1 levels are
// derived from the variant resolution and frame rate, matching what the
// encoders signal automatically.
func variantCodecs(v Variant, frameRate float64) string {
	w := v.Quality.Width
	h := v.Quality.Height
	if w == 0 {
		w = h * 16 / 9
	}
	fps := frameRate
	if fps <= 0 {
		fps = 30
	}
	ps := int64(w) * int64(h)
	sr := int64(float64(ps) * fps)
	switch v.Codec.Name {
	case "h265":
		level := hevcLevels[len(hevcLevels)-1].level
		for _, l := range hevcLevels {
			if ps <= l.maxPS && sr <= l.maxSR {
				level = l.level
				break
			}
		}
		return fmt.Sprintf("hvc1.1.6.L%d.90,mp4a.40.2", level)
	case "av1":
		idx := av1Levels[len(av1Levels)-1].idx
		for _, l := range av1Levels {
			if ps <= l.maxPS && sr <= l.maxSR {
				idx = l.idx
				break
			}
		}
		return fmt.Sprintf("av01.0.%02dM.08,mp4a.40.2", idx)
	default:
		return v.Codec.HLSCodecs
	}
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
