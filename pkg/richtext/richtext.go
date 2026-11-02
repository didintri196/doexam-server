// Package richtext sanitizes rich text (item stems, options, explanations) to the
// allowlist in DOCS/PLAN/08-DATABASE-MVP.md §4.1 and extracts plain text for search.
// Never trust HTML from the client: always store the output of Sanitize.
package richtext

import (
	"errors"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// MaxBytes is the largest sanitized field we accept (Word pastes balloon quickly).
const MaxBytes = 64 << 10

// maxInputBytes bounds work before sanitizing.
const maxInputBytes = 1 << 20

// ErrTooLarge is returned when a field exceeds the size limits.
var ErrTooLarge = errors.New("rich text too large (max 64 KB)")

var (
	uuidRe  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	classRe = regexp.MustCompile(`^(align-(left|center|right|justify))( align-(left|center|right|justify))*$`)
	spanRe  = regexp.MustCompile(`^[1-9][0-9]?$`)
	dirRe   = regexp.MustCompile(`^(ltr|rtl|auto)$`)
	langRe  = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})?$`)
	anyRe   = regexp.MustCompile(`(?s)^.{0,1000}$`) // RE2 caps repeat counts at 1000
	spaceRe = regexp.MustCompile(`\s+`)
)

var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "strong", "em", "u", "s", "sub", "sup", "ul", "ol", "li", "blockquote",
		"table", "thead", "tbody", "tr", "th", "td", "span", "div", "pre", "code",
		"img", "audio", "video", "source")
	p.AllowAttrs("data-math").Matching(anyRe).OnElements("span")
	p.AllowAttrs("data-math-block").Matching(anyRe).OnElements("div")
	// media are referenced by id only; the server fills src with a signed URL when delivering
	p.AllowAttrs("data-media-id").Matching(uuidRe).OnElements("img", "audio", "video", "source")
	p.AllowAttrs("alt").Matching(anyRe).OnElements("img")
	p.AllowAttrs("controls").OnElements("audio", "video")
	p.AllowAttrs("colspan", "rowspan").Matching(spanRe).OnElements("td", "th")
	p.AllowAttrs("dir").Matching(dirRe).Globally()
	p.AllowAttrs("lang").Matching(langRe).Globally()
	p.AllowAttrs("class").Matching(classRe).Globally()
	return p
}

// Sanitize returns s reduced to the allowlist. Everything else (script, style,
// iframe, on* handlers, href, src, inline styles) is removed.
func Sanitize(s string) (string, error) {
	if len(s) > maxInputBytes {
		return "", ErrTooLarge
	}
	out := strings.TrimSpace(policy.Sanitize(s))
	if len(out) > MaxBytes {
		return "", ErrTooLarge
	}
	return out, nil
}

// PlainText returns the visible text of sanitized HTML plus raw LaTeX of formulas,
// with whitespace collapsed. Used for items' search_text.
func PlainText(s string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(spaceRe.ReplaceAllString(b.String(), " "))
		case html.TextToken:
			b.Write(z.Text())
			b.WriteByte(' ')
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			for hasAttr {
				var key, val []byte
				key, val, hasAttr = z.TagAttr()
				if k := string(key); k == "data-math" || k == "data-math-block" || (k == "alt" && string(name) == "img") {
					b.Write(val)
					b.WriteByte(' ')
				}
			}
			if isBlock(string(name)) {
				b.WriteByte(' ')
			}
		}
	}
}

// IsBlank reports whether sanitized HTML shows nothing: no text, formula or media.
func IsBlank(s string) bool {
	if PlainText(s) != "" {
		return false
	}
	return !strings.Contains(s, "data-media-id=")
}

func isBlock(tag string) bool {
	switch tag {
	case "p", "br", "li", "div", "tr", "td", "th", "blockquote", "pre":
		return true
	}
	return false
}
