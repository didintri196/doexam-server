package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"doexam/pkg/metrics"
)

// unmatchedRoute labels requests that matched no route, so scanners probing random
// paths cannot create a series per path.
const unmatchedRoute = "unmatched"

// HTTPMetrics holds the instruments of the HTTP server (OTel semantic conventions).
type HTTPMetrics struct {
	duration  metric.Float64Histogram
	active    metric.Int64UpDownCounter
	reqSize   metric.Int64Histogram
	respSize  metric.Int64Histogram
	panics    metric.Int64Counter
	rateLimit metric.Int64Counter
}

// NewHTTPMetrics creates the HTTP instruments on the global meter provider; call it
// after metrics.Setup.
func NewHTTPMetrics() *HTTPMetrics {
	m := otel.Meter("doexam.http")
	h := &HTTPMetrics{}
	var errs [6]error
	h.duration, errs[0] = m.Float64Histogram("http.server.request.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of HTTP server requests."), metric.WithExplicitBucketBoundaries(metrics.LatencyBuckets()...))
	h.active, errs[1] = m.Int64UpDownCounter("http.server.active_requests", metric.WithUnit("{request}"),
		metric.WithDescription("Requests being processed."))
	h.reqSize, errs[2] = m.Int64Histogram("http.server.request.body.size", metric.WithUnit("By"),
		metric.WithDescription("Size of HTTP request bodies (from Content-Length)."), metric.WithExplicitBucketBoundaries(metrics.SizeBuckets()...))
	h.respSize, errs[3] = m.Int64Histogram("http.server.response.body.size", metric.WithUnit("By"),
		metric.WithDescription("Size of HTTP response bodies (from Content-Length)."), metric.WithExplicitBucketBoundaries(metrics.SizeBuckets()...))
	h.panics, errs[4] = m.Int64Counter("doexam.http.panics", metric.WithUnit("{panic}"),
		metric.WithDescription("Panics recovered while serving a request."))
	h.rateLimit, errs[5] = m.Int64Counter("doexam.rate_limit.hits", metric.WithUnit("{request}"),
		metric.WithDescription("Requests rejected by a rate limiter."))
	metrics.Report(errs[:]...)
	return h
}

// Handler records RED metrics per route template (never the raw path). Register it
// first, before Recover, so panics are counted as 500. Paths in skip (probes) are not
// recorded.
func (inst *HTTPMetrics) Handler(app *fiber.App, skip ...string) fiber.Handler {
	skipped := make(map[string]bool, len(skip))
	for _, p := range skip {
		skipped[p] = true
	}
	var (
		routesOnce sync.Once
		endpoints  map[string]bool // "METHOD path" of handlers
		prefixes   map[string]bool // paths of group middleware (e.g. requireAuth on /api/v1/attempts)
	)
	label := func(method, path string, status int) string {
		routesOnce.Do(func() { // every route is registered before the first request
			endpoints, prefixes = map[string]bool{}, map[string]bool{}
			for _, r := range app.GetRoutes(true) {
				endpoints[r.Method+" "+r.Path] = true
			}
			for _, r := range app.GetRoutes(false) {
				if !endpoints[r.Method+" "+r.Path] && r.Path != "/" {
					prefixes[r.Path] = true
				}
			}
		})
		switch {
		case endpoints[method+" "+path]:
			return path
		case prefixes[path] && status != fiber.StatusNotFound:
			// stopped by group middleware before reaching the handler (e.g. 401)
			return path + "/*"
		}
		return unmatchedRoute
	}
	return func(c *fiber.Ctx) error {
		if skipped[c.Path()] {
			return c.Next()
		}
		ctx := c.UserContext()
		method := strings.Clone(c.Method()) // Fiber reuses the buffer behind c.Method()
		methodAttr := metric.WithAttributes(attribute.String("http.request.method", method))
		inst.active.Add(ctx, 1, methodAttr)
		start := time.Now()

		err := c.Next()

		elapsed := time.Since(start).Seconds()
		inst.active.Add(ctx, -1, methodAttr)
		status := c.Response().StatusCode()
		if err != nil { // the error handler writes the status after the chain returns
			status = fiber.StatusInternalServerError
			var fe *fiber.Error
			if errors.As(err, &fe) {
				status = fe.Code
			}
		}
		route := label(method, c.Route().Path, status)
		routeAttr := attribute.String("http.route", route)
		inst.duration.Record(ctx, elapsed, metric.WithAttributes(
			attribute.String("http.request.method", method), routeAttr, attribute.Int("http.response.status_code", status)))
		if n := c.Request().Header.ContentLength(); n > 0 {
			inst.reqSize.Record(ctx, int64(n), metric.WithAttributes(routeAttr))
		}
		if n := c.Response().Header.ContentLength(); n >= 0 {
			inst.respSize.Record(ctx, int64(n), metric.WithAttributes(routeAttr))
		}
		return err
	}
}

// Recover turns a panic into a 500, logs it with its stack trace and counts it in
// doexam_http_panics_total.
func (inst *HTTPMetrics) Recover(logger *slog.Logger) fiber.Handler {
	return recover.New(recover.Config{
		EnableStackTrace: true,
		StackTraceHandler: func(c *fiber.Ctx, e any) {
			inst.panics.Add(c.UserContext(), 1)
			logger.ErrorContext(c.UserContext(), "panic recovered",
				slog.String("method", c.Method()), slog.String("path", c.Path()),
				slog.String("panic", fmt.Sprint(e)), slog.String("stack", string(debug.Stack())))
		},
	})
}

// RateLimited counts a request rejected by the named limiter (doexam_rate_limit_hits_total).
func (inst *HTTPMetrics) RateLimited(ctx context.Context, limiter string) {
	inst.rateLimit.Add(ctx, 1, metric.WithAttributes(attribute.String("limiter", limiter)))
}
