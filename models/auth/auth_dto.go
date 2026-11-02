package auth

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Password length limits (bytes are bounded too, so hashing cost stays predictable).
const (
	PasswordMinLen   = 8
	PasswordMaxBytes = 256
)

// LoginInput is the body of POST /api/v1/auth/login.
type LoginInput struct {
	Tenant   string `json:"tenant"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Normalize trims identifiers (never the password).
func (in *LoginInput) Normalize() {
	in.Tenant = strings.ToLower(strings.TrimSpace(in.Tenant))
	in.Username = strings.TrimSpace(in.Username)
}

// ChangePasswordInput is the body of POST /api/v1/auth/change-password.
type ChangePasswordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ValidPassword reports whether pw satisfies the password policy.
func ValidPassword(pw string) bool {
	return utf8.RuneCountInString(pw) >= PasswordMinLen && len(pw) <= PasswordMaxBytes
}

// TenantSummaryDTO is the tenant as shown to a logged-in user.
type TenantSummaryDTO struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Preset string `json:"preset"`
}

// MeDTO is the logged-in user with roles and effective permissions.
type MeDTO struct {
	ID                 string           `json:"id"`
	Username           string           `json:"username"`
	FullName           string           `json:"full_name"`
	Email              *string          `json:"email"`
	MustChangePassword bool             `json:"must_change_password"`
	Tenant             TenantSummaryDTO `json:"tenant"`
	Roles              []string         `json:"roles"`
	Permissions        []string         `json:"permissions"`
}

// LoginDTO is returned after a successful login. Token is shown exactly once.
type LoginDTO struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      MeDTO     `json:"user"`
}

// ToMeDTO builds MeDTO from its parts.
func ToMeDTO(u UserEntity, t TenantSummaryDTO, roles []RoleEntity) MeDTO {
	codes := make([]string, 0, len(roles))
	for _, r := range roles {
		codes = append(codes, r.Code)
	}
	perms := MergePermissions(roles)
	if perms == nil {
		perms = []string{}
	}
	return MeDTO{
		ID:                 u.ID,
		Username:           u.Username,
		FullName:           u.FullName,
		Email:              u.Email,
		MustChangePassword: u.MustChangePassword,
		Tenant:             t,
		Roles:              codes,
		Permissions:        perms,
	}
}

// usernamePattern: letters, digits and . _ @ - (NIS/NISN and e-mail style usernames fit).
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{3,100}$`)

// ValidUsername reports whether u is an acceptable username.
func ValidUsername(u string) bool { return usernamePattern.MatchString(u) }

// UsernameRule is the validation message for usernames.
const UsernameRule = "3-100 chars: letters, digits, . _ @ -"
