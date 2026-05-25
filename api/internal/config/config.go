package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
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

	AuthMode       string // "standalone" | "backend"
	JWTSecret      string
	HLSTokenSecret string
	PublicHLSURL   string
	PublicHost     string // scheme+host used for manifest and preview URLs

	APIPort           string
	APIBaseURL        string
	AuthRequired      bool
	CORSOrigins       []string
	AllowRegistration bool
	ServiceKey        string
	UploadAuth        string // "jwt" | "service_key" | "any" — standalone only
	MaxUploadSize     int64  // bytes

	SpriteIntervalSeconds int
	SpriteWidth           int
	SpriteHeight          int
	SpriteColumns         int
}

func Load() (*Config, error) {
	authMode := getEnv("AUTH_MODE", "standalone")
	switch authMode {
	case "standalone", "backend":
	default:
		return nil, fmt.Errorf("AUTH_MODE must be standalone or backend (got %q)", authMode)
	}

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
		AuthMode:          authMode,
		JWTSecret:         getEnv("JWT_SECRET", ""),
		HLSTokenSecret:    req("HLS_TOKEN_SECRET"),
		PublicHLSURL:      getEnv("PUBLIC_HLS_URL", "http://localhost/hls"),
		APIPort:           getEnv("API_PORT", "8000"),
		APIBaseURL:        getEnv("API_BASE_URL", "http://localhost:8000"),
		AuthRequired:      getEnv("AUTH_REQUIRED", "true") != "false",
		CORSOrigins:       parseCORSOrigins(getEnv("CORS_ORIGINS", "*")),
		AllowRegistration: getEnv("ALLOW_REGISTRATION", "true") != "false",
		ServiceKey:        getEnv("SERVICE_KEY", ""),
		UploadAuth:        getEnv("UPLOAD_AUTH", "jwt"),
		MaxUploadSize:     getEnvInt64("MAX_UPLOAD_SIZE_GB", 50) << 30,

		SpriteIntervalSeconds: int(getEnvInt64("SPRITE_INTERVAL_SECONDS", 10)),
		SpriteWidth:           int(getEnvInt64("SPRITE_WIDTH", 320)),
		SpriteHeight:          int(getEnvInt64("SPRITE_HEIGHT", 180)),
		SpriteColumns:         int(getEnvInt64("SPRITE_COLUMNS", 10)),
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	switch cfg.AuthMode {
	case "standalone":
		if cfg.JWTSecret == "" {
			return nil, fmt.Errorf("JWT_SECRET is required in standalone mode")
		}
		switch cfg.UploadAuth {
		case "jwt", "service_key", "any":
		default:
			return nil, fmt.Errorf("UPLOAD_AUTH must be one of: jwt, service_key, any (got %q)", cfg.UploadAuth)
		}
		if (cfg.UploadAuth == "service_key" || cfg.UploadAuth == "any") && cfg.ServiceKey == "" {
			return nil, fmt.Errorf("SERVICE_KEY must be set when UPLOAD_AUTH=%s", cfg.UploadAuth)
		}
	case "backend":
		if cfg.ServiceKey == "" {
			return nil, fmt.Errorf("SERVICE_KEY is required in backend mode")
		}
	}

	cfg.PublicHost = resolvePublicHost(cfg.PublicHLSURL)
	return cfg, nil
}

// resolvePublicHost returns the scheme+host to use for building manifest and preview URLs.
// Reads PUBLIC_HOST first; falls back to stripping the /hls path from PUBLIC_HLS_URL.
func resolvePublicHost(hlsURL string) string {
	if h := os.Getenv("PUBLIC_HOST"); h != "" {
		return strings.TrimRight(h, "/")
	}
	u, err := url.Parse(hlsURL)
	if err == nil && u.Host != "" {
		return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	}
	s := strings.TrimRight(hlsURL, "/")
	return strings.TrimRight(strings.TrimSuffix(s, "/hls"), "/")
}

func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode,
	)
}

func getEnvInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseCORSOrigins(s string) []string {
	var origins []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}
