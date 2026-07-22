package config

import (
	"log/slog"
	"os"
	"strings"
)

// SetupLogging installs the process-wide slog default: key=value text on
// stdout, level from LOG_LEVEL (debug, info, warn, error; default info).
func SetupLogging() {
	var level slog.Level
	switch strings.ToLower(getEnv("LOG_LEVEL", "info")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}
