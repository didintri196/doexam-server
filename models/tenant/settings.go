package tenant

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// Defaults used while a setting is unset.
const (
	DefaultTimezone = "Asia/Jakarta"
	DefaultLocale   = "id"
)

// Limits for institution settings and the logo.
const (
	MaxNameLen       = 200
	MaxLetterheadLen = 500
	MaxTaglineLen    = 100
	MaxLogoBytes     = 1 << 20 // 1 MiB
)

// Locales the clients ship translations for.
var Locales = []string{"id", "en"}

// ColorPattern accepts a #RRGGBB hex color.
var ColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// LogoTypes maps the sniffed content type of an allowed logo to its file extension.
// SVG is not allowed: it can carry scripts.
var LogoTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
}

// Settings is the content of tenants.settings. Empty fields mean "use the default".
type Settings struct {
	Timezone     string `json:"timezone,omitempty"`      // IANA name, e.g. Asia/Makassar
	Locale       string `json:"locale,omitempty"`        // one of Locales
	PrimaryColor string `json:"primary_color,omitempty"` // #RRGGBB
	Tagline      string `json:"tagline,omitempty"`       // plain text under the name in the clients' header
	Letterhead   string `json:"letterhead,omitempty"`    // plain text for exam cards and printed reports
	LogoFileID   string `json:"logo_file_id,omitempty"`  // media_files.id
}

// DecodeSettings parses tenants.settings; unknown or malformed content yields zero settings.
func DecodeSettings(raw []byte) Settings {
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return Settings{} // the column is only written by Encode; never fail a read over it
	}
	return s
}

// Encode returns the JSON stored in tenants.settings.
func (s Settings) Encode() []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // plain strings: cannot fail
	return bytes.TrimSpace(buf.Bytes())
}
