package handler

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestPollParams(t *testing.T) {
	tests := []struct {
		query string
		after int64
		wait  time.Duration
	}{
		{"", 0, 0},
		{"?after=42&wait=25", 42, 25 * time.Second},
		{"?after=-3&wait=x", 0, 0}, // the service caps wait to MaxPollWait
	}
	for _, tt := range tests {
		app := fiber.New()
		app.Get("/", func(c *fiber.Ctx) error {
			after, wait := pollParams(c)
			return c.SendString(fmt.Sprintf("%d %s", after, wait))
		})
		resp, err := app.Test(httptest.NewRequest("GET", "/"+tt.query, nil))
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		if want := fmt.Sprintf("%d %s", tt.after, tt.wait); string(buf[:n]) != want {
			t.Errorf("%q → %q, want %q", tt.query, buf[:n], want)
		}
	}
}
