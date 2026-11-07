// Package web serves the frontend build (exam client at /exam/, staff panel at /app/)
// embedded in the binary, so one `doexam` file is the whole product (DOCS/PLAN/17 §3.2).
// It mirrors the nginx config of apps/client/docker/nginx: strict CSP per entry, hashed
// assets cached for a year, entry pages and the service worker always revalidated, and
// unknown paths under an entry answered with its index.html (client-side routes).
//
// The build is copied into dist/ before `go build`; with only the placeholder there the
// binary serves the API alone (development, and the API-only image).
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"path"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
)

//go:embed all:dist
var embedded embed.FS

// Dist is the embedded build (may hold only the placeholder).
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // the directory is part of the source tree
	}
	return sub
}

const (
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
	cacheShort      = "public, max-age=3600"
)

// Content-Security-Policy per entry, as in the nginx config (09-FRONTEND §7).
var csp = map[string]string{
	"exam": "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'",
	"app":  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'",
}

// entries are the two single-page apps, in redirect priority ("/" goes to the first).
var entries = []string{"exam", "app"}

func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

type ui struct {
	files fs.FS
	etags sync.Map // file name → quoted ETag of its uncompressed content
}

// Register mounts the frontend routes when files holds a build (an entry index.html) and
// reports whether it did. Call it after the API routes.
func Register(app *fiber.App, files fs.FS) bool {
	found := false
	for _, e := range entries {
		found = found || isFile(files, e+"/index.html")
	}
	if !found {
		return false
	}
	u := &ui{files: files}
	app.Get("/", redirect("/"+entries[0]+"/"))
	for _, e := range entries {
		app.Get("/"+e+"/*", u.entry(e)) // also matches "/exam" itself (non-strict routing)
	}
	app.Get("/assets/*", u.asset)
	app.Get("/favicon.svg", func(c *fiber.Ctx) error { return u.serve(c, "favicon.svg", cacheShort, "") })
	return true
}

func redirect(to string) fiber.Handler {
	return func(c *fiber.Ctx) error { return c.Redirect(to, fiber.StatusFound) }
}

// clean turns the wildcard part of a URL into a file name inside the build, or "" when
// it would escape it.
func clean(p string) string {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" || name == "." {
		return ""
	}
	if !fs.ValidPath(name) {
		return ""
	}
	return name
}

func (u *ui) entry(e string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// the router uses /exam (no trailing slash) as its root URL: a reload must not 404
		if c.Path() == "/"+e {
			return c.Redirect("/"+e+"/", fiber.StatusFound)
		}
		rel := clean(c.Params("*"))
		name := e + "/" + rel
		if rel == "" || !isFile(u.files, name) {
			name = e + "/index.html" // client-side route (or the entry root)
		}
		if rel == "sw.js" {
			// the service worker must always be revalidated so a new version is noticed
			c.Set("Service-Worker-Allowed", "/"+e+"/")
			return u.serve(c, name, cacheRevalidate, "")
		}
		return u.serve(c, name, cacheRevalidate, csp[e])
	}
}

func (u *ui) asset(c *fiber.Ctx) error {
	rel := clean(c.Params("*"))
	if rel == "" || !isFile(u.files, "assets/"+rel) {
		return fiber.ErrNotFound
	}
	// hashed build assets never change
	return u.serve(c, "assets/"+rel, cacheImmutable, "")
}

// serve writes one file with its headers, preferring a precompressed .br/.gz copy the
// build may have produced next to it.
func (u *ui) serve(c *fiber.Ctx, name, cacheControl, policy string) error {
	data, err := fs.ReadFile(u.files, name)
	if err != nil {
		return fiber.ErrNotFound
	}
	setSecurityHeaders(c)
	if policy != "" {
		c.Set("Content-Security-Policy", policy)
	}
	c.Set(fiber.HeaderCacheControl, cacheControl)
	c.Set(fiber.HeaderVary, fiber.HeaderAcceptEncoding)
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	c.Set(fiber.HeaderContentType, ctype)

	etag := u.etag(name, data)
	c.Set(fiber.HeaderETag, etag)
	if c.Get(fiber.HeaderIfNoneMatch) == etag {
		return c.SendStatus(fiber.StatusNotModified)
	}

	accept := c.Get(fiber.HeaderAcceptEncoding)
	for _, enc := range []struct{ token, ext string }{{"br", ".br"}, {"gzip", ".gz"}} {
		if !strings.Contains(accept, enc.token) {
			continue
		}
		if z, err := fs.ReadFile(u.files, name+enc.ext); err == nil {
			c.Set(fiber.HeaderContentEncoding, enc.token)
			return c.Send(z)
		}
	}
	return c.Send(data)
}

func (u *ui) etag(name string, data []byte) string {
	if v, ok := u.etags.Load(name); ok {
		return v.(string)
	}
	sum := sha256.Sum256(data)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	u.etags.Store(name, tag)
	return tag
}

// setSecurityHeaders matches apps/client/docker/nginx/security-headers.conf.
func setSecurityHeaders(c *fiber.Ctx) {
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	c.Set(fiber.HeaderXFrameOptions, "DENY")
	c.Set(fiber.HeaderReferrerPolicy, "same-origin")
	c.Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=(), display-capture=(self)")
}

func isFile(files fs.FS, name string) bool {
	st, err := fs.Stat(files, name)
	return err == nil && !st.IsDir()
}
