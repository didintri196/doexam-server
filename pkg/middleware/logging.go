package middleware

import (
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"
)

// RequestLogger returns a middleware that:
// 1. Creates a per-request child logger with method & path
// 2. Stores it in Fiber context (accessible via GetLogger)
// 3. Logs the HTTP request after response
//
// trace_id is not added here: mlog.TraceHandler injects it from ctx,
// so always log with the ...Context variants and c.UserContext().
func RequestLogger(logger *slog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		reqLogger := logger.With(
			slog.String("method", c.Method()),
			slog.String("path", c.Path()),
		)

		// Store in context for handlers to use
		SetLogger(c, reqLogger)

		err := c.Next()

		reqLogger.InfoContext(c.UserContext(), "http request",
			slog.Int("status", c.Response().StatusCode()),
			slog.Duration("duration", time.Since(start)),
			slog.String("ip", c.IP()),
		)

		return err
	}
}
