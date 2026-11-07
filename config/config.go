package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const defaultDatabaseURL = "postgres://doexam:secret@localhost:5433/doexam?sslmode=disable"

// Config holds all application configuration.
type Config struct {
	DoExam DoExamConfig
}

// DoExamConfig holds API-specific configuration.
type DoExamConfig struct {
	ServerPort  string
	Environment string

	// Database
	DatabaseURL string

	// Auth
	SessionTTL time.Duration

	// File storage (uploads); local directory for now
	StorageDir string
	// MediaSigningKey signs media links (hex/base64, ≥16 bytes). Empty: a key is generated
	// once and kept in storage (system/media-signing.key).
	MediaSigningKey string

	// Item content cache: Redis when RedisURL is set (shared by every node), otherwise
	// an in-memory LRU of ContentCacheMB megabytes.
	RedisURL       string
	ContentCacheMB int

	// OTEL (tracing disabled when OTELEndpoint is empty)
	OTELEndpoint string
	OTELService  string
}

// Load reads configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		DoExam: DoExamConfig{
			ServerPort:      getEnv("PORT", "8080"),
			Environment:     getEnv("APP_ENV", "development"),
			DatabaseURL:     getEnv("DATABASE_URL", defaultDatabaseURL),
			SessionTTL:      getDuration("AUTH_SESSION_TTL", 12*time.Hour),
			StorageDir:      getEnv("STORAGE_DIR", "./data/storage"),
			MediaSigningKey: os.Getenv("MEDIA_SIGNING_KEY"),
			RedisURL:        os.Getenv("REDIS_URL"),
			ContentCacheMB:  getInt("CONTENT_CACHE_MB", 64),
			OTELEndpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			OTELService:     getEnv("OTEL_SERVICE_NAME", "doexam-api"),
		},
	}
}

// Validate checks that required config is set for production.
func (c *DoExamConfig) Validate() error {
	switch c.Environment {
	case "development", "staging", "production":
	default:
		return fmt.Errorf("APP_ENV must be one of development/staging/production, got %q", c.Environment)
	}
	if c.Environment == "production" && c.DatabaseURL == defaultDatabaseURL {
		return fmt.Errorf("DATABASE_URL must be set in production")
	}
	if c.ContentCacheMB <= 0 {
		return fmt.Errorf("CONTENT_CACHE_MB must be a positive number of megabytes")
	}
	if c.SessionTTL <= 0 {
		return fmt.Errorf("AUTH_SESSION_TTL must be a positive duration (e.g. 12h)")
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getInt parses an integer. Invalid values become 0 and fail Validate.
func getInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// getDuration parses a Go duration ("12h", "90m"). Invalid values become 0 and fail Validate.
func getDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}
