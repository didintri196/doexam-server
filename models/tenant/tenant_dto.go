package tenant

import (
	"regexp"
	"strings"
)

// SlugPattern: lowercase letters, digits and dashes, 3–63 chars, no leading/trailing dash.
var SlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// SetupInput is the body of POST /api/v1/setup (first tenant + its admin).
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
		in.Preset = PresetSchool
	}
}

// TenantDTO is the public shape of a tenant.
type TenantDTO struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Preset   Preset `json:"preset"`
	IsActive bool   `json:"is_active"`
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
	return TenantDTO{ID: e.ID, Slug: e.Slug, Name: e.Name, Preset: e.Preset, IsActive: e.IsActive}
}
