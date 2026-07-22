package config

import (
	"fmt"

	"github.com/evadepw/evadeplayer-platform/internal/ffmpeg"
)

// Transcoder is the configuration for the transcoder worker.
type Transcoder struct {
	DB DB

	SeaweedFSFiler string

	Workers           int
	MaxAttempts       int
	TempDir           string
	HLSSegmentSeconds int
	Accel             string
	Codecs            []string
	Qualities         []ffmpeg.Quality
	Thumbnail         ffmpeg.ThumbnailConfig
	Encoding          ffmpeg.EncodingConfig
}

func LoadTranscoder() (*Transcoder, error) {
	var missing []string

	cfg := &Transcoder{
		DB:                loadDB(&missing),
		SeaweedFSFiler:    getEnv("SEAWEEDFS_FILER", "http://localhost:8888"),
		Workers:           getEnvPositiveInt("TRANSCODE_WORKERS", 2),
		MaxAttempts:       getEnvPositiveInt("TRANSCODE_MAX_ATTEMPTS", 3),
		TempDir:           getEnv("TRANSCODE_TEMP_DIR", "/tmp/evadeplayer"),
		HLSSegmentSeconds: getEnvPositiveInt("TRANSCODE_HLS_SEGMENT_SECONDS", 4),
		Accel:             getEnv("TRANSCODE_ACCEL", "cpu"),
		Codecs:            getEnvList("TRANSCODE_CODECS", "h264,h265,av1"),
		Thumbnail: ffmpeg.ThumbnailConfig{
			SpriteColumns:         getEnvPositiveInt("TRANSCODE_SPRITE_COLUMNS", 10),
			SpriteIntervalSeconds: getEnvPositiveInt("TRANSCODE_SPRITE_INTERVAL_SECONDS", 10),
			SpriteWidth:           getEnvPositiveInt("TRANSCODE_SPRITE_WIDTH", 320),
			SpriteHeight:          getEnvPositiveInt("TRANSCODE_SPRITE_HEIGHT", 180),
			PreviewWidth:          getEnvPositiveInt("TRANSCODE_PREVIEW_WIDTH", 640),
			PreviewHeight:         getEnvPositiveInt("TRANSCODE_PREVIEW_HEIGHT", 360),
			ImageStreamBandwidth:  getEnvPositiveInt("TRANSCODE_IMAGE_STREAM_BANDWIDTH", 30000),
		},
		Encoding: ffmpeg.EncodingConfig{
			CPUPreset:       getEnv("TRANSCODE_PRESET", "slow"),
			NvidiaPreset:    getEnv("TRANSCODE_NVIDIA_PRESET", "p5"),
			AV1CPUUsed:      getEnvPositiveInt("TRANSCODE_AV1_CPU_USED", 4),
			AV1CRF:          getEnvPositiveInt("TRANSCODE_AV1_CRF", 30),
			H264CRF:         getEnvInt("TRANSCODE_H264_CRF", 0),
			H265CRF:         getEnvInt("TRANSCODE_H265_CRF", 0),
			AudioBitrate:    getEnv("TRANSCODE_AUDIO_BITRATE", "128k"),
			AudioSampleRate: getEnvPositiveInt("TRANSCODE_AUDIO_SAMPLE_RATE", 48000),
			SceneCut:        getEnvBool("TRANSCODE_SCENE_CUT", false),
		},
	}

	if err := missingErr(missing); err != nil {
		return nil, err
	}

	switch cfg.Accel {
	case "cpu", "nvidia", "vaapi":
	default:
		return nil, fmt.Errorf("TRANSCODE_ACCEL must be one of: cpu, nvidia, vaapi (got %q)", cfg.Accel)
	}

	qualityNames := getEnvList("TRANSCODE_QUALITIES", "360p,720p,1080p,1440p,original")
	// Per-quality video bitrate overrides. Empty string = use the default.
	bitrateOverrides := map[string]string{
		"360p":  getEnv("TRANSCODE_QUALITY_360P_BITRATE", ""),
		"720p":  getEnv("TRANSCODE_QUALITY_720P_BITRATE", ""),
		"1080p": getEnv("TRANSCODE_QUALITY_1080P_BITRATE", ""),
		"1440p": getEnv("TRANSCODE_QUALITY_1440P_BITRATE", ""),
	}
	qualities, err := ffmpeg.BuildQualities(qualityNames, bitrateOverrides)
	if err != nil {
		return nil, fmt.Errorf("TRANSCODE_QUALITIES: %w", err)
	}
	cfg.Qualities = qualities

	return cfg, nil
}
