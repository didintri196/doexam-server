package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"doexam/business/health"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(ctx context.Context) error { return f.err }

func newHealthApp(pingErr error) *fiber.App {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHealthHandler(health.NewHealthService(fakePinger{err: pingErr}, "test", logger))

	app := fiber.New()
	app.Get("/healthz", h.Healthz)
	app.Get("/readyz", h.Readyz)
	return app
}

func TestHealthEndpoints(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		pingErr     error
		wantCode    int
		wantSuccess bool
	}{
		{name: "healthz ok", path: "/healthz", wantCode: fiber.StatusOK, wantSuccess: true},
		{name: "healthz ok while db down", path: "/healthz", pingErr: errors.New("down"), wantCode: fiber.StatusOK, wantSuccess: true},
		{name: "readyz ok", path: "/readyz", wantCode: fiber.StatusOK, wantSuccess: true},
		{name: "readyz db down", path: "/readyz", pingErr: errors.New("down"), wantCode: fiber.StatusServiceUnavailable, wantSuccess: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := newHealthApp(tt.pingErr).Test(httptest.NewRequest(fiber.MethodGet, tt.path, nil))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantCode {
				t.Fatalf("status code = %d, want %d", resp.StatusCode, tt.wantCode)
			}

			var body APIResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("invalid JSON body: %v", err)
			}
			if body.Success != tt.wantSuccess {
				t.Fatalf("success = %v, want %v", body.Success, tt.wantSuccess)
			}
		})
	}
}
