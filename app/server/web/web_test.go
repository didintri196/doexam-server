package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v2"
)

func build() fstest.MapFS {
	return fstest.MapFS{
		"exam/index.html":           {Data: []byte("<html>exam</html>")},
		"exam/sw.js":                {Data: []byte("self.addEventListener('fetch',()=>{})")},
		"exam/manifest.webmanifest": {Data: []byte(`{"name":"Ujian"}`)},
		"app/index.html":            {Data: []byte("<html>panel</html>")},
		"assets/index-abc123.js":    {Data: []byte("console.log('plain')")},
		"assets/index-abc123.js.br": {Data: []byte("BROTLI")},
		"assets/index-abc123.js.gz": {Data: []byte("GZIP")},
		"assets/style-def456.css":   {Data: []byte("body{}")},
		"favicon.svg":               {Data: []byte("<svg/>")},
	}
}

func newApp(t *testing.T, files fstest.MapFS) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Get("/api/v1/ping", func(c *fiber.Ctx) error { return c.SendString("pong") })
	Register(app, files)
	return app
}

type result struct {
	status int
	header http.Header
	body   string
}

func get(t *testing.T, app *fiber.App, target string, headers ...string) result {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	return result{res.StatusCode, res.Header, string(body)}
}

func TestRegisterWithoutBuildServesAPIOnly(t *testing.T) {
	app := fiber.New()
	if Register(app, fstest.MapFS{".keep": {Data: []byte("placeholder")}}) {
		t.Fatal("a placeholder-only build must not register the frontend")
	}
	if r := get(t, app, "/exam/"); r.status != http.StatusNotFound {
		t.Fatalf("/exam/ without a build: %d", r.status)
	}
}

func TestEmbeddedPlaceholderCompiles(t *testing.T) {
	// the repository ships dist/.keep so go:embed compiles; Register must then be a no-op
	if Register(fiber.New(), Dist()) && !isFile(Dist(), "exam/index.html") && !isFile(Dist(), "app/index.html") {
		t.Fatal("registered without an entry")
	}
}

func TestRedirects(t *testing.T) {
	app := newApp(t, build())
	for target, want := range map[string]string{"/": "/exam/", "/exam": "/exam/", "/app": "/app/"} {
		r := get(t, app, target)
		if r.status != http.StatusFound || r.header.Get("Location") != want {
			t.Fatalf("%s → %d %q, want 302 %q", target, r.status, r.header.Get("Location"), want)
		}
	}
}

func TestEntryPagesAndClientRoutes(t *testing.T) {
	app := newApp(t, build())
	tests := []struct {
		target, body, cspHas string
	}{
		{"/exam/", "exam", "worker-src 'self'"},
		{"/exam/attempt/0192a000-0000-7000-8000-000000000001", "exam", "manifest-src 'self'"},
		{"/app/", "panel", "frame-ancestors 'none'"},
		{"/app/sessions/x/monitor", "panel", "object-src 'none'"},
	}
	for _, tt := range tests {
		r := get(t, app, tt.target)
		if r.status != 200 || !strings.Contains(r.body, tt.body) {
			t.Fatalf("%s → %d %q", tt.target, r.status, r.body)
		}
		if r.header.Get("Cache-Control") != "no-cache" || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s headers: %v", tt.target, r.header)
		}
		if !strings.Contains(r.header.Get("Content-Security-Policy"), tt.cspHas) {
			t.Fatalf("%s CSP %q", tt.target, r.header.Get("Content-Security-Policy"))
		}
		for h, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "same-origin"} {
			if r.header.Get(h) != v {
				t.Fatalf("%s %s = %q", tt.target, h, r.header.Get(h))
			}
		}
	}
	// the panel never gets the exam client's worker policy
	if strings.Contains(get(t, app, "/app/").header.Get("Content-Security-Policy"), "worker-src") {
		t.Fatal("panel CSP must not allow workers")
	}
}

func TestServiceWorkerAndManifest(t *testing.T) {
	app := newApp(t, build())
	sw := get(t, app, "/exam/sw.js")
	if sw.status != 200 || sw.header.Get("Service-Worker-Allowed") != "/exam/" || sw.header.Get("Cache-Control") != "no-cache" ||
		!strings.Contains(sw.header.Get("Content-Type"), "javascript") {
		t.Fatalf("sw.js: %d %v", sw.status, sw.header)
	}
	mf := get(t, app, "/exam/manifest.webmanifest")
	if mf.status != 200 || mf.header.Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest: %d %q", mf.status, mf.header.Get("Content-Type"))
	}
}

func TestAssets(t *testing.T) {
	app := newApp(t, build())
	r := get(t, app, "/assets/style-def456.css")
	if r.status != 200 || r.header.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		!strings.HasPrefix(r.header.Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d %v", r.status, r.header)
	}
	if r := get(t, app, "/assets/missing-000.js"); r.status != http.StatusNotFound {
		t.Fatalf("missing asset: %d (must not fall back to HTML)", r.status)
	}
	for _, target := range []string{"/assets/../../etc/passwd", "/assets/%2e%2e/%2e%2e/etc/passwd"} {
		if r := get(t, app, target); r.status == 200 {
			t.Fatalf("%s escaped the build: %q", target, r.body)
		}
	}
}

func TestPrecompressedAndETag(t *testing.T) {
	app := newApp(t, build())
	br := get(t, app, "/assets/index-abc123.js", "Accept-Encoding", "gzip, deflate, br")
	if br.body != "BROTLI" || br.header.Get("Content-Encoding") != "br" || br.header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("br: %q %v", br.body, br.header)
	}
	gz := get(t, app, "/assets/index-abc123.js", "Accept-Encoding", "gzip")
	if gz.body != "GZIP" || gz.header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %q %v", gz.body, gz.header)
	}
	plain := get(t, app, "/assets/index-abc123.js")
	if plain.body != "console.log('plain')" || plain.header.Get("Content-Encoding") != "" {
		t.Fatalf("plain: %q %v", plain.body, plain.header)
	}
	// the same ETag whatever the encoding, and a matching If-None-Match gets 304
	if br.header.Get("ETag") == "" || br.header.Get("ETag") != plain.header.Get("ETag") {
		t.Fatalf("etags %q vs %q", br.header.Get("ETag"), plain.header.Get("ETag"))
	}
	if r := get(t, app, "/exam/", "If-None-Match", get(t, app, "/exam/").header.Get("ETag")); r.status != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", r.status)
	}
}

func TestAPIUnaffected(t *testing.T) {
	app := newApp(t, build())
	if r := get(t, app, "/api/v1/ping"); r.status != 200 || r.body != "pong" {
		t.Fatalf("api: %d %q", r.status, r.body)
	}
	if r := get(t, app, "/api/v1/unknown"); r.status != http.StatusNotFound {
		t.Fatalf("unknown api route: %d (must not be the SPA)", r.status)
	}
}
