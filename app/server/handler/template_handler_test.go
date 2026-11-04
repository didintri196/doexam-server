package handler

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestPublicTemplate(t *testing.T) {
	app := fiber.New()
	app.Get("/t/:name", PublicTemplate)
	cases := []struct {
		name, mime string
		status     int
	}{
		{"template-impor-soal.docx", docxMIME, 200},
		{"template-impor-users.xlsx", xlsxMIME, 200},
		{"template-impor-groups.xlsx", xlsxMIME, 200},
		{"../etc/passwd", "", 404},
		{"rahasia.xlsx", "", 404},
	}
	for _, tc := range cases {
		res, err := app.Test(httptest.NewRequest("GET", "/t/"+tc.name, nil))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != tc.status {
			t.Fatalf("%s: status %d", tc.name, res.StatusCode)
		}
		if tc.status != 200 {
			continue
		}
		if res.Header.Get("Content-Type") != tc.mime || res.Header.Get("Content-Disposition") != `attachment; filename="`+tc.name+`"` || len(body) < 500 {
			t.Fatalf("%s: type %q disposition %q size %d", tc.name, res.Header.Get("Content-Type"), res.Header.Get("Content-Disposition"), len(body))
		}
	}
}
