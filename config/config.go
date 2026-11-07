package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
	// AuthCacheTTL is how long a resolved login token is trusted without the database
	// (0 disables). Revocations apply at once on this node, on others within this time.
	AuthCacheTTL time.Duration

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

	// Metrics: Prometheus /metrics on its own listener (empty = off), optionally behind a
	// bearer token; OTELMetricsExporter "otlp" also pushes them to OTELEndpoint.
	MetricsAddr         string
	MetricsToken        string
	OTELMetricsExporter string
}

// MetricsEnabled reports whether metrics are collected at all.
func (c *DoExamConfig) MetricsEnabled() bool {
	return c.MetricsAddr != "" || c.OTELMetricsExporter == "otlp"
}

// Load reads configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		DoExam: DoExamConfig{
			ServerPort:          getEnv("PORT", "8080"),
			Environment:         getEnv("APP_ENV", "development"),
			DatabaseURL:         getEnv("DATABASE_URL", defaultDatabaseURL),
			SessionTTL:          getDuration("AUTH_SESSION_TTL", 12*time.Hour),
			AuthCacheTTL:        getOptionalDuration("AUTH_CACHE_TTL", 30*time.Second),
			StorageDir:          getEnv("STORAGE_DIR", "./data/storage"),
			MediaSigningKey:     os.Getenv("MEDIA_SIGNING_KEY"),
			RedisURL:            os.Getenv("REDIS_URL"),
			ContentCacheMB:      getInt("CONTENT_CACHE_MB", 64),
			OTELEndpoint:        os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			OTELService:         getEnv("OTEL_SERVICE_NAME", "doexam-api"),
			MetricsAddr:         metricsAddr(getEnv("METRICS_ADDR", "127.0.0.1:9464")),
			MetricsToken:        os.Getenv("METRICS_TOKEN"),
			OTELMetricsExporter: strings.ToLower(getEnv("OTEL_METRICS_EXPORTER", "none")),
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
	if c.AuthCacheTTL < 0 || c.AuthCacheTTL > 5*time.Minute {
		return fmt.Errorf("AUTH_CACHE_TTL must be a duration from 0 (off) to 5m (e.g. 30s)")
	}
	if c.SessionTTL <= 0 {
		return fmt.Errorf("AUTH_SESSION_TTL must be a positive duration (e.g. 12h)")
	}
	if c.MetricsAddr != "" {
		if _, _, err := net.SplitHostPort(c.MetricsAddr); err != nil {
			return fmt.Errorf("METRICS_ADDR must be host:port (e.g. 127.0.0.1:9464) or off, got %q", c.MetricsAddr)
		}
		if c.Environment == "production" && c.MetricsToken == "" && !isLoopback(c.MetricsAddr) {
			return fmt.Errorf("METRICS_TOKEN must be set in production when METRICS_ADDR is not a loopback address")
		}
	}
	switch c.OTELMetricsExporter {
	case "none":
	case "otlp":
		if c.OTELEndpoint == "" {
			return fmt.Errorf("OTEL_METRICS_EXPORTER=otlp needs OTEL_EXPORTER_OTLP_ENDPOINT")
		}
	default:
		return fmt.Errorf("OTEL_METRICS_EXPORTER must be none or otlp, got %q", c.OTELMetricsExporter)
	}
	return nil
}

// metricsAddr turns "off" into "" (disabled).
func metricsAddr(v string) string {
	if strings.EqualFold(v, "off") {
		return ""
	}
	return v
}

// isLoopback reports whether a listen address only accepts connections from this host.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// getOptionalDuration is getDuration where 0 is a valid value ("off"). Invalid values
// become -1 and fail Validate.
func getOptionalDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return -1
	}
	return d
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
