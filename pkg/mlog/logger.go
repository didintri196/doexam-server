package mlog

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// LoggerConfig holds logger configuration.
type LoggerConfig struct {
	Environment string
	Level       slog.Level
}

// New creates a new structured slog logger with OTEL trace ID injection.
func New(cfg LoggerConfig) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: cfg.Level,
	}

	var base slog.Handler
	if cfg.Environment == "development" {
		base = slog.NewTextHandler(os.Stdout, opts)
	} else {
		base = slog.NewJSONHandler(os.Stdout, opts)
	}

	// Wrap with TraceHandler to auto-inject trace_id from OTEL context
	return slog.New(&TraceHandler{handler: base})
}

// TraceHandler wraps a slog.Handler and injects OTEL trace_id into every record.
type TraceHandler struct {
	handler slog.Handler
}

func (h *TraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *TraceHandler) Handle(ctx context.Context, record slog.Record) error {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().TraceID().IsValid() {
		record.AddAttrs(slog.String("trace_id", span.SpanContext().TraceID().String()))
	}
	return h.handler.Handle(ctx, record)
}

func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{handler: h.handler.WithAttrs(attrs)}
}

func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{handler: h.handler.WithGroup(name)}
}
