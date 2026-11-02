package middleware

import (
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/trace"
)

// GetTraceID extracts the OTEL trace ID from the Fiber context.
func GetTraceID(c *fiber.Ctx) string {
	span := trace.SpanFromContext(c.UserContext())
	if !span.SpanContext().TraceID().IsValid() {
		return ""
	}
	return span.SpanContext().TraceID().String()
}
