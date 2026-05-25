package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/evadeplayer/transcoder/internal/ffmpeg"
)

type Config struct {
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	RedisAddr     string
	RedisQueueKey string

	SeaweedFSMaster string
	SeaweedFSFiler  string

	Workers           int
	TempDir           string
	HLSSegmentSeconds int
	Accel             string
	Codecs            []string
	Qualities         []ffmpeg.Quality
	Thumbnail         ffmpeg.ThumbnailConfig
	Encoding          ffmpeg.EncodingConfig
}

func Load() (*Config, error) {
	var missing []string
	req := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	cfg := &Config{
		DBHost:            getEnv("DB_HOST", "localhost"),
		DBPort:            getEnv("DB_PORT", "5432"),
		DBUser:            req("POSTGRES_USER"),
		DBPassword:        req("POSTGRES_PASSWORD"),
		DBName:            req("POSTGRES_DB"),
		DBSSLMode:         getEnv("DB_SSLMODE", "disable"),
		RedisAddr:         getEnv("REDIS_ADDR", "localhost:6379"),
		RedisQueueKey:     getEnv("REDIS_QUEUE_KEY", "transcoding_queue"),
		SeaweedFSMaster:   getEnv("SEAWEEDFS_MASTER", "http://localhost:9333"),
		SeaweedFSFiler:    getEnv("SEAWEEDFS_FILER", "http://localhost:8888"),
		Workers:           getEnvPositiveInt("TRANSCODE_WORKERS", 2),
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
			AudioSampleRate: getEnvPositiveInt("TRANSCODE_AUDIO_SAMPLE_RATE", 48000),
			SceneCut:        getEnvBool("TRANSCODE_SCENE_CUT", false),
		},
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	switch cfg.Accel {
	case "cpu", "nvidia", "vaapi":
	default:
		return nil, fmt.Errorf("TRANSCODE_ACCEL must be one of: cpu, nvidia, vaapi (got %q)", cfg.Accel)
	}

	qualityNames := getEnvList("TRANSCODE_QUALITIES", "360p,720p,1080p,1440p")
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

func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode,
	)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvPositiveInt(key string, fallback int) int {
	n := getEnvInt(key, fallback)
	if n < 1 {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func getEnvList(key, fallback string) []string {
	value := getEnv(key, fallback)
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
