package middleware

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"doexam/pkg/metrics/metricstest"
)

func TestHTTPMetrics(t *testing.T) {
	r := metricstest.Install(t)
	m := NewHTTPMetrics()
	app := fiber.New()
	app.Use(m.Handler(app, "/healthz"))
	app.Use(m.Recover(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/healthz", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/items/:id", func(c *fiber.Ctx) error { return c.SendString("item") })
	app.Get("/boom", func(*fiber.Ctx) error { panic("boom") })
	g := app.Group("/api", func(c *fiber.Ctx) error { return c.Next() })
	g.Get("/x", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	g.Post("/x", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusCreated) })
	private := app.Group("/private", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusUnauthorized) })
	private.Get("/:id", func(c *fiber.Ctx) error { return c.SendString("secret") })

	for _, req := range []string{"GET /items/1", "GET /items/2", "GET /items/3", "GET /nope/1", "GET /nope/2", "GET /api/missing",
		"GET /api/x", "POST /api/x", "POST /api/x", "GET /private/1", "GET /private/2", "GET /healthz", "GET /boom"} {
		method, path, _ := strings.Cut(req, " ")
		resp, err := app.Test(httptest.NewRequest(method, path, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	const dur = "http.server.request.duration"
	tests := []struct {
		name  string
		match map[string]string
		want  float64
	}{
		{"route template, not raw path", map[string]string{"http.route": "/items/:id", "http.response.status_code": "200"}, 3},
		{"unknown paths share one label", map[string]string{"http.route": unmatchedRoute, "http.response.status_code": "404"}, 3},
		{"group route", map[string]string{"http.route": "/api/x", "http.response.status_code": "204"}, 1},
		{"method label is intact", map[string]string{"http.request.method": "POST", "http.route": "/api/x", "http.response.status_code": "201"}, 2},
		{"stopped by group middleware", map[string]string{"http.route": "/private/*", "http.response.status_code": "401"}, 2},
		{"panic is a 500", map[string]string{"http.route": "/boom", "http.response.status_code": "500"}, 1},
		{"skipped probe", map[string]string{"http.route": "/healthz"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Value(dur, tt.match); got != tt.want {
				t.Fatalf("count = %v, want %v (points %+v)", got, tt.want, r.Points(dur))
			}
		})
	}
	for _, p := range r.Points(dur) {
		if route := p.Attrs["http.route"]; route == "/items/1" || route == "/nope/1" {
			t.Fatalf("raw path leaked into labels: %q", route)
		}
	}
	if got := r.Value("doexam.http.panics", nil); got != 1 {
		t.Fatalf("panics = %v, want 1", got)
	}
	if got := r.Value("http.server.active_requests", nil); got != 0 {
		t.Fatalf("active requests = %v, want 0 after all requests finished", got)
	}
}
