package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// API is the configuration for the API service.
type API struct {
	DB DB

	RedisAddr     string
	RedisQueueKey string

	SeaweedFSFiler string

	ServiceKey      string
	HLSTokenSecret  string
	HLSRequireToken bool
	PublicHost      string

	Port          string
	ReadPublic    bool // true = GET endpoints require no auth
	CORSOrigins   []string
	MaxUploadSize int64 // bytes

	SpriteIntervalSeconds int
	SpriteWidth           int
	SpriteHeight          int
	SpriteColumns         int
}

func LoadAPI() (*API, error) {
	var missing []string
	req := requireFn(&missing)

	cfg := &API{
		DB:              loadDB(&missing),
		RedisAddr:       getEnv("REDIS_ADDR", "localhost:6379"),
		RedisQueueKey:   getEnv("REDIS_QUEUE_KEY", "transcoding_queue"),
		SeaweedFSFiler:  getEnv("SEAWEEDFS_FILER", "http://localhost:8888"),
		ServiceKey:      req("SERVICE_KEY"),
		HLSTokenSecret:  getEnv("HLS_TOKEN_SECRET", ""),
		HLSRequireToken: getEnvBool("HLS_REQUIRE_TOKEN", true),
		Port:            getEnv("API_PORT", "8000"),
		ReadPublic:      getEnvBool("READ_PUBLIC", true),
		CORSOrigins:     parseCORSOrigins(getEnv("CORS_ORIGINS", "*")),
		MaxUploadSize:   getEnvInt64("MAX_UPLOAD_SIZE_GB", 50) << 30,

		SpriteIntervalSeconds: getEnvPositiveInt("SPRITE_INTERVAL_SECONDS", 10),
		SpriteWidth:           getEnvPositiveInt("SPRITE_WIDTH", 320),
		SpriteHeight:          getEnvPositiveInt("SPRITE_HEIGHT", 180),
		SpriteColumns:         getEnvPositiveInt("SPRITE_COLUMNS", 10),
	}

	if err := missingErr(missing); err != nil {
		return nil, err
	}
	if cfg.HLSRequireToken && cfg.HLSTokenSecret == "" {
		return nil, fmt.Errorf("required environment variable not set: HLS_TOKEN_SECRET (set HLS_REQUIRE_TOKEN=false to disable token enforcement)")
	}

	cfg.PublicHost = resolvePublicHost()
	return cfg, nil
}

func resolvePublicHost() string {
	if h := os.Getenv("PUBLIC_HOST"); h != "" {
		return strings.TrimRight(h, "/")
	}
	hlsURL := os.Getenv("PUBLIC_HLS_URL")
	u, err := url.Parse(hlsURL)
	if err == nil && u.Host != "" {
		return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	}
	s := strings.TrimRight(hlsURL, "/")
	return strings.TrimRight(strings.TrimSuffix(s, "/hls"), "/")
}

func parseCORSOrigins(s string) []string {
	if origins := splitList(s); len(origins) > 0 {
		return origins
	}
	return []string{"*"}
}
