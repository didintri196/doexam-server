package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestParsePage(t *testing.T) {
	tests := []struct {
		query       string
		wantPage    int
		wantPerPage int
		wantErr     bool
	}{
		{query: "", wantPage: 1, wantPerPage: 20},
		{query: "?page=3&per_page=50", wantPage: 3, wantPerPage: 50},
		{query: "?per_page=1000", wantPage: 1, wantPerPage: 100},
		{query: "?page=0", wantErr: true},
		{query: "?per_page=abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			app := fiber.New()
			var page, perPage int
			var err error
			app.Get("/", func(c *fiber.Ctx) error {
				page, perPage, err = parsePage(c)
				return nil
			})
			if _, e := app.Test(httptest.NewRequest(fiber.MethodGet, "/"+tt.query, nil)); e != nil {
				t.Fatal(e)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && (page != tt.wantPage || perPage != tt.wantPerPage) {
				t.Fatalf("got %d/%d, want %d/%d", page, perPage, tt.wantPage, tt.wantPerPage)
			}
		})
	}
}

func TestMonitorPageSize(t *testing.T) {
	for query, want := range map[string]int{"": 10, "?per_page=5": 5, "?per_page=50": 10} {
		app := fiber.New()
		var perPage int
		app.Get("/", func(c *fiber.Ctx) error {
			_, perPage, _ = parsePageSized(c, monitorPerPage, monitorPerPage)
			return nil
		})
		if _, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/"+query, nil)); err != nil {
			t.Fatal(err)
		}
		if perPage != want {
			t.Errorf("%q: per_page = %d, want %d", query, perPage, want)
		}
	}
}
