package middleware

import (
	"log/slog"

	"github.com/gofiber/fiber/v2"
)

const loggerLocalsKey = "logger"

// GetLogger extracts the request-scoped logger from Fiber context.
func GetLogger(c *fiber.Ctx) *slog.Logger {
	if l, ok := c.Locals(loggerLocalsKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// SetLogger stores the request-scoped logger in Fiber context.
func SetLogger(c *fiber.Ctx, logger *slog.Logger) {
	c.Locals(loggerLocalsKey, logger)
}
