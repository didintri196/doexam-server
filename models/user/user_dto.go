package user

import (
	"net/mail"
	"strings"
	"time"

	modelAuth "doexam/models/auth"
)

// ListFilter filters GET /api/v1/users.
type ListFilter struct {
	Page     int
	PerPage  int
	Query    string               // matches username, full_name, external_id, email
	RoleCode string               // has this role
	GroupID  string               // member of this group
	Status   modelAuth.UserStatus // "" = any
}

// CreateInput is the body of POST /api/v1/users.
// Password empty → a temporary password is generated and returned once.
type CreateInput struct {
	Username           string   `json:"username"`
	FullName           string   `json:"full_name"`
	Email              *string  `json:"email"`
	ExternalID         *string  `json:"external_id"`
	Password           string   `json:"password"`
	RoleCodes          []string `json:"role_codes"`           // default: ["participant"]
	MustChangePassword *bool    `json:"must_change_password"` // default: true
}

// UpdateInput is the body of PATCH /api/v1/users/:id.
// A missing field is left unchanged; "" clears email / external_id.
type UpdateInput struct {
	FullName   *string               `json:"full_name"`
	Email      *string               `json:"email"`
	ExternalID *string               `json:"external_id"`
	Status     *modelAuth.UserStatus `json:"status"`
	RoleCodes  *[]string             `json:"role_codes"` // replaces all roles
}

// Normalize trims text fields of CreateInput (never the password).
func (in *CreateInput) Normalize() {
	in.Username = strings.TrimSpace(in.Username)
	in.FullName = strings.TrimSpace(in.FullName)
	in.Email = trimOptional(in.Email)
	in.ExternalID = trimOptional(in.ExternalID)
	in.RoleCodes = normalizeCodes(in.RoleCodes)
}

// Normalize trims text fields of UpdateInput. Empty email/external_id stay as "" (= clear).
func (in *UpdateInput) Normalize() {
	if in.FullName != nil {
		v := strings.TrimSpace(*in.FullName)
		in.FullName = &v
	}
	for _, p := range []**string{&in.Email, &in.ExternalID} {
		if *p != nil {
			v := strings.TrimSpace(**p)
			*p = &v
		}
	}
	if in.RoleCodes != nil {
		codes := normalizeCodes(*in.RoleCodes)
		in.RoleCodes = &codes
	}
}

// ValidEmail reports whether e is a bare e-mail address.
func ValidEmail(e string) bool {
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e && len(e) <= 254
}

// RoleDTO is a role as listed for assignment.
type RoleDTO struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	IsSystem    bool     `json:"is_system"`
}

// UserDTO is a user as seen by an administrator. It never carries the password hash.
type UserDTO struct {
	ID                 string               `json:"id"`
	Username           string               `json:"username"`
	FullName           string               `json:"full_name"`
	Email              *string              `json:"email"`
	ExternalID         *string              `json:"external_id"`
	Status             modelAuth.UserStatus `json:"status"`
	MustChangePassword bool                 `json:"must_change_password"`
	Roles              []string             `json:"roles"`
	LastLoginAt        *time.Time           `json:"last_login_at"`
	CreatedAt          time.Time            `json:"created_at"`
}

// UserGroupDTO is a group membership shown on the user detail.
type UserGroupDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Period     *string `json:"period"`
	MemberRole string  `json:"member_role"`
}

// UserDetailDTO is a user with group memberships.
type UserDetailDTO struct {
	UserDTO
	Groups []UserGroupDTO `json:"groups"`
}

// CreatedDTO is returned by POST /api/v1/users.
type CreatedDTO struct {
	User              UserDTO `json:"user"`
	TemporaryPassword string  `json:"temporary_password,omitempty"`
}

// PasswordResetDTO is returned by POST /api/v1/users/:id/reset-password.
type PasswordResetDTO struct {
	TemporaryPassword string `json:"temporary_password"`
}

// ToDTO converts a user and its roles to UserDTO.
func ToDTO(u modelAuth.UserEntity, roles []modelAuth.RoleEntity) UserDTO {
	codes := make([]string, 0, len(roles))
	for _, r := range roles {
		codes = append(codes, r.Code)
	}
	return UserDTO{
		ID: u.ID, Username: u.Username, FullName: u.FullName, Email: u.Email, ExternalID: u.ExternalID,
		Status: u.Status, MustChangePassword: u.MustChangePassword, Roles: codes,
		LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt,
	}
}

// ToRoleDTO converts a RoleEntity to RoleDTO.
func ToRoleDTO(r modelAuth.RoleEntity) RoleDTO {
	perms := []string(r.Permissions)
	if perms == nil {
		perms = []string{}
	}
	return RoleDTO{ID: r.ID, Code: r.Code, Name: r.Name, Permissions: perms, IsSystem: r.IsSystem}
}

func trimOptional(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func normalizeCodes(codes []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range codes {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}
