package middleware

import (
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
)

// Tracing returns a middleware that adds OTEL tracing to requests.
func Tracing(serviceName string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := otel.GetTextMapPropagator().Extract(c.UserContext(), propagation.HeaderCarrier(c.GetReqHeaders()))

		tracer := otel.Tracer(serviceName)
		ctx, span := tracer.Start(ctx, c.Method()+" "+c.Path())
		defer span.End()

		span.SetAttributes(
			attribute.String("http.method", c.Method()),
			attribute.String("http.url", c.OriginalURL()),
		)

		c.SetUserContext(ctx)
		err := c.Next()

		span.SetAttributes(
			attribute.Int("http.status_code", c.Response().StatusCode()),
		)

		return err
	}
}
