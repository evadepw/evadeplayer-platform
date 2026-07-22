// Package config loads service configuration from environment variables.
//
// Each binary has its own top-level config (API, Transcoder) sharing the
// common DB block and the env helpers below. Required variables are collected
// and reported together so a misconfigured deployment fails with one clear
// message instead of dying on the first missing key.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DB holds PostgreSQL connection settings shared by all binaries.
type DB struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

func (d DB) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// loadDB reads the common PostgreSQL variables. Missing required keys are
// appended to missing.
func loadDB(missing *[]string) DB {
	req := requireFn(missing)
	return DB{
		Host:     getEnv("DB_HOST", "localhost"),
		Port:     getEnv("DB_PORT", "5432"),
		User:     req("POSTGRES_USER"),
		Password: req("POSTGRES_PASSWORD"),
		Name:     req("POSTGRES_DB"),
		SSLMode:  getEnv("DB_SSLMODE", "disable"),
	}
}

func requireFn(missing *[]string) func(string) string {
	return func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			*missing = append(*missing, key)
		}
		return v
	}
}

func missingErr(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
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
	if n := getEnvInt(key, fallback); n >= 1 {
		return n
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
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
	return splitList(getEnv(key, fallback))
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
