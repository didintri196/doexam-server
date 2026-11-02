package group

import (
	"strings"
	"time"
)

// MaxMembersPerRequest bounds POST /groups/:id/members.
const MaxMembersPerRequest = 500

// RootParent is the parent_id filter value that selects top-level groups.
const RootParent = "root"

// ListFilter filters GET /api/v1/groups.
type ListFilter struct {
	Page     int
	PerPage  int
	ParentID string // UUID, RootParent, or "" = any
	Kind     Kind
	Period   string
	Query    string // matches name and code
}

// CreateInput is the body of POST /api/v1/groups.
type CreateInput struct {
	ParentID *string `json:"parent_id"`
	Kind     Kind    `json:"kind"`
	Code     *string `json:"code"`
	Name     string  `json:"name"`
	Period   *string `json:"period"`
}

// UpdateInput is the body of PATCH /api/v1/groups/:id.
// A missing field is left unchanged; "" clears parent_id, code and period.
type UpdateInput struct {
	ParentID *string `json:"parent_id"`
	Kind     *Kind   `json:"kind"`
	Code     *string `json:"code"`
	Name     *string `json:"name"`
	Period   *string `json:"period"`
}

// AddMembersInput is the body of POST /api/v1/groups/:id/members.
// Existing members get their member_role updated.
type AddMembersInput struct {
	UserIDs    []string   `json:"user_ids"`
	MemberRole MemberRole `json:"member_role"` // default: member
}

// MemberFilter pages GET /api/v1/groups/:id/members.
type MemberFilter struct {
	Page    int
	PerPage int
}

// Normalize trims text fields; empty optional fields become nil.
func (in *CreateInput) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.ParentID = trimOptional(in.ParentID)
	in.Code = trimOptional(in.Code)
	in.Period = trimOptional(in.Period)
}

// Normalize trims text fields; "" is kept to mean "clear".
func (in *UpdateInput) Normalize() {
	for _, p := range []**string{&in.ParentID, &in.Code, &in.Name, &in.Period} {
		if *p != nil {
			v := strings.TrimSpace(**p)
			*p = &v
		}
	}
}

// GroupDTO is a group with its member count. Clients build the tree from parent_id.
type GroupDTO struct {
	ID          string    `json:"id"`
	ParentID    *string   `json:"parent_id"`
	Kind        Kind      `json:"kind"`
	Code        *string   `json:"code"`
	Name        string    `json:"name"`
	Period      *string   `json:"period"`
	MemberCount int64     `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// MemberDTO is one member of a group.
type MemberDTO struct {
	UserID     string     `json:"user_id"`
	Username   string     `json:"username"`
	FullName   string     `json:"full_name"`
	ExternalID *string    `json:"external_id"`
	MemberRole MemberRole `json:"member_role"`
	JoinedAt   time.Time  `json:"joined_at"`
}

// ToDTO converts a GroupEntity to GroupDTO.
func ToDTO(g GroupEntity, memberCount int64) GroupDTO {
	return GroupDTO{
		ID: g.ID, ParentID: g.ParentID, Kind: g.Kind, Code: g.Code, Name: g.Name,
		Period: g.Period, MemberCount: memberCount, CreatedAt: g.CreatedAt,
	}
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
