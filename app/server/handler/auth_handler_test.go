package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"doexam/business/auth"
	"doexam/pkg/errr"
)

// newAuthApp wires AuthHandler with nil dependencies: every case here is
// rejected before the service touches storage (full flows are covered by
// business/auth tests and the end-to-end check against PostgreSQL).
func newAuthApp() *fiber.App {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAuthHandler(auth.NewAuthService(nil, nil, nil, time.Hour, logger))

	app := fiber.New()
	app.Post("/login", h.Login)
	app.Get("/me", h.RequireAuth, h.Me)
	return app
}

func TestAuthHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		authHeader string
		wantCode   int
		wantMsg    string
		wantField  string
	}{
		{name: "me without header", method: fiber.MethodGet, path: "/me", wantCode: fiber.StatusUnauthorized, wantMsg: "unauthorized"},
		{name: "me with basic auth", method: fiber.MethodGet, path: "/me", authHeader: "Basic YTpi", wantCode: fiber.StatusUnauthorized, wantMsg: "unauthorized"},
		{name: "me with malformed token", method: fiber.MethodGet, path: "/me", authHeader: "Bearer garbage", wantCode: fiber.StatusUnauthorized, wantMsg: "unauthorized"},
		{name: "login invalid json", method: fiber.MethodPost, path: "/login", body: "{", wantCode: fiber.StatusBadRequest, wantMsg: "invalid request body"},
		{name: "login missing fields", method: fiber.MethodPost, path: "/login", body: `{"tenant":"sman1"}`, wantCode: fiber.StatusBadRequest, wantMsg: "invalid input", wantField: "username"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			if tt.authHeader != "" {
				req.Header.Set(fiber.HeaderAuthorization, tt.authHeader)
			}
			resp, err := newAuthApp().Test(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantCode)
			}
			var body struct {
				Success bool              `json:"success"`
				Message string            `json:"message"`
				Errors  map[string]string `json:"errors"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if body.Success || body.Message != tt.wantMsg {
				t.Fatalf("body = %+v, want message %q", body, tt.wantMsg)
			}
			if tt.wantField != "" && body.Errors[tt.wantField] == "" {
				t.Fatalf("want field error on %s, got %v", tt.wantField, body.Errors)
			}
		})
	}
}

func TestHandleError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "public message", err: errr.WithMessage(errr.ErrAlreadyExists, "setup already completed"), wantCode: 409, wantMsg: "setup already completed"},
		{name: "wrapped sentinel hides detail", err: fmt.Errorf("create user: %w", errr.ErrAlreadyExists), wantCode: 409, wantMsg: "already exists"},
		{name: "not found", err: errr.ErrNotFound, wantCode: 404, wantMsg: "not found"},
		{name: "forbidden", err: errr.ErrForbidden, wantCode: 403, wantMsg: "forbidden"},
		{name: "unexpected hides detail", err: errors.New("pq: password=secret"), wantCode: 500, wantMsg: "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New()
			app.Get("/", func(c *fiber.Ctx) error {
				return handleError(c, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", tt.err)
			})
			resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/", nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var body APIResponse
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if resp.StatusCode != tt.wantCode || body.Message != tt.wantMsg {
				t.Fatalf("got %d %q, want %d %q", resp.StatusCode, body.Message, tt.wantCode, tt.wantMsg)
			}
		})
	}
}
