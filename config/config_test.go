package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateMetrics(t *testing.T) {
	base := func() DoExamConfig {
		return DoExamConfig{Environment: "production", DatabaseURL: "postgres://x", ContentCacheMB: 64, SessionTTL: time.Hour,
			MetricsAddr: "127.0.0.1:9464", OTELMetricsExporter: "none"}
	}
	tests := []struct {
		name    string
		mutate  func(*DoExamConfig)
		wantErr string
	}{
		{"loopback without token", func(*DoExamConfig) {}, ""},
		{"localhost without token", func(c *DoExamConfig) { c.MetricsAddr = "localhost:9464" }, ""},
		{"off", func(c *DoExamConfig) { c.MetricsAddr = "" }, ""},
		{"public without token in production", func(c *DoExamConfig) { c.MetricsAddr = ":9464" }, "METRICS_TOKEN"},
		{"public with token", func(c *DoExamConfig) { c.MetricsAddr = "0.0.0.0:9464"; c.MetricsToken = "t" }, ""},
		{"public without token in development", func(c *DoExamConfig) { c.MetricsAddr = ":9464"; c.Environment = "development" }, ""},
		{"malformed address", func(c *DoExamConfig) { c.MetricsAddr = "9464" }, "METRICS_ADDR"},
		{"otlp without endpoint", func(c *DoExamConfig) { c.OTELMetricsExporter = "otlp" }, "OTEL_EXPORTER_OTLP_ENDPOINT"},
		{"otlp with endpoint", func(c *DoExamConfig) { c.OTELMetricsExporter = "otlp"; c.OTELEndpoint = "collector:4317" }, ""},
		{"unknown exporter", func(c *DoExamConfig) { c.OTELMetricsExporter = "prometheus" }, "OTEL_METRICS_EXPORTER"},
		{"tls cert without key", func(c *DoExamConfig) { c.TLSCertFile = "server.crt" }, "TLS_KEY_FILE"},
		{"tls key without cert", func(c *DoExamConfig) { c.TLSKeyFile = "server.key" }, "TLS_CERT_FILE"},
		{"tls pair", func(c *DoExamConfig) { c.TLSCertFile, c.TLSKeyFile = "server.crt", "server.key" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want mention of %s", err, tt.wantErr)
			}
		})
	}
}

func TestMetricsAddrOff(t *testing.T) {
	t.Setenv("METRICS_ADDR", "OFF")
	if c := Load().DoExam; c.MetricsAddr != "" || c.MetricsEnabled() {
		t.Fatalf("METRICS_ADDR=off: addr %q, enabled %v", c.MetricsAddr, c.MetricsEnabled())
	}
	t.Setenv("METRICS_ADDR", "")
	if c := Load().DoExam; c.MetricsAddr != "127.0.0.1:9464" || !c.MetricsEnabled() {
		t.Fatalf("default addr %q", c.MetricsAddr)
	}
}
