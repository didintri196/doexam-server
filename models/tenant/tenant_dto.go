package tenant

import (
	"net/url"
	"regexp"
	"strings"
)

// SlugPattern: lowercase letters, digits and dashes, 3–63 chars, no leading/trailing dash.
var SlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// SetupInput is the body of POST /api/v1/setup (first tenant + its admin).
// TenantSlug is optional: it is derived from TenantName when empty.
type SetupInput struct {
	TenantSlug    string  `json:"tenant_slug"`
	TenantName    string  `json:"tenant_name"`
	Preset        Preset  `json:"preset"`
	AdminUsername string  `json:"admin_username"`
	AdminFullName string  `json:"admin_full_name"`
	AdminEmail    *string `json:"admin_email"`
	AdminPassword string  `json:"admin_password"`
}

// Normalize trims identifiers and applies defaults (never touches the password).
func (in *SetupInput) Normalize() {
	in.TenantSlug = strings.ToLower(strings.TrimSpace(in.TenantSlug))
	in.TenantName = strings.TrimSpace(in.TenantName)
	in.AdminUsername = strings.TrimSpace(in.AdminUsername)
	in.AdminFullName = strings.TrimSpace(in.AdminFullName)
	if in.AdminEmail != nil {
		e := strings.TrimSpace(*in.AdminEmail)
		if e == "" {
			in.AdminEmail = nil
		} else {
			in.AdminEmail = &e
		}
	}
	if in.Preset == "" {
		in.Preset = PresetCustom
	}
	if in.TenantSlug == "" {
		in.TenantSlug = SlugFromName(in.TenantName)
	}
}

// fallbackSlug is used when a name has too few latin letters or digits for a slug.
const fallbackSlug = "main"

// SlugFromName derives a slug from an institution name: "SMA Negeri 1 Malang" → "sma-negeri-1-malang".
func SlugFromName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 63 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if !SlugPattern.MatchString(slug) {
		return fallbackSlug
	}
	return slug
}

// UpdateInput is the body of PATCH /api/v1/tenant. Nil fields are left unchanged;
// an empty string resets a setting to its default (the name cannot be emptied).
type UpdateInput struct {
	Name         *string `json:"name"`
	Timezone     *string `json:"timezone"`
	Locale       *string `json:"locale"`
	PrimaryColor *string `json:"primary_color"`
	Tagline      *string `json:"tagline"`
	Letterhead   *string `json:"letterhead"`
	ExamRules    *string `json:"exam_rules"`
}

// Normalize trims every field.
func (in *UpdateInput) Normalize() {
	for _, f := range []*string{in.Name, in.Timezone, in.Locale, in.PrimaryColor, in.Tagline, in.Letterhead, in.ExamRules} {
		if f != nil {
			*f = strings.TrimSpace(*f)
		}
	}
	if in.PrimaryColor != nil {
		*in.PrimaryColor = strings.ToLower(*in.PrimaryColor)
	}
}

// TenantDTO is the tenant as shown to its members.
type TenantDTO struct {
	ID       string      `json:"id"`
	Slug     string      `json:"slug"`
	Name     string      `json:"name"`
	Preset   Preset      `json:"preset"`
	IsActive bool        `json:"is_active"`
	Settings SettingsDTO `json:"settings"`
}

// SettingsDTO shows the effective settings (defaults applied).
type SettingsDTO struct {
	Timezone     string  `json:"timezone"`
	Locale       string  `json:"locale"`
	PrimaryColor *string `json:"primary_color"`
	Tagline      string  `json:"tagline"`
	Letterhead   string  `json:"letterhead"`
	ExamRules    string  `json:"exam_rules"`
	LogoURL      *string `json:"logo_url"`
}

// PublicTenantDTO is what the login page may show before anyone signs in.
type PublicTenantDTO struct {
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Tagline      string  `json:"tagline"`
	Preset       Preset  `json:"preset"`
	Locale       string  `json:"locale"`
	PrimaryColor *string `json:"primary_color"`
	LogoURL      *string `json:"logo_url"`
}

// LogoFile is a logo ready to be served.
type LogoFile struct {
	Data     []byte
	MimeType string
	SHA256   []byte
}

// SetupDTO is returned after setup.
type SetupDTO struct {
	Tenant TenantDTO `json:"tenant"`
	Admin  struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"admin"`
}

// ToDTO converts a TenantEntity to TenantDTO.
func ToDTO(e TenantEntity) TenantDTO {
	s := DecodeSettings(e.Settings)
	return TenantDTO{
		ID: e.ID, Slug: e.Slug, Name: e.Name, Preset: e.Preset, IsActive: e.IsActive,
		Settings: SettingsDTO{
			Timezone: orDefault(s.Timezone, DefaultTimezone), Locale: orDefault(s.Locale, DefaultLocale),
			PrimaryColor: nonEmpty(s.PrimaryColor), Tagline: s.Tagline, Letterhead: s.Letterhead, ExamRules: s.ExamRules, LogoURL: LogoURL(e.Slug, s.LogoFileID),
		},
	}
}

// ToPublicDTO converts a TenantEntity to PublicTenantDTO.
func ToPublicDTO(e TenantEntity) PublicTenantDTO {
	s := DecodeSettings(e.Settings)
	return PublicTenantDTO{
		Slug: e.Slug, Name: e.Name, Tagline: s.Tagline, Preset: e.Preset, Locale: orDefault(s.Locale, DefaultLocale),
		PrimaryColor: nonEmpty(s.PrimaryColor), LogoURL: LogoURL(e.Slug, s.LogoFileID),
	}
}

// LogoURL is the public URL of a tenant's logo; the file id in the query busts caches
// when the logo changes. It returns nil when no logo is set.
func LogoURL(slug, fileID string) *string {
	if fileID == "" {
		return nil
	}
	u := "/api/v1/public/tenant/logo?" + url.Values{"tenant": {slug}, "v": {fileID}}.Encode()
	return &u
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func nonEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
