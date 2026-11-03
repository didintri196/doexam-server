package group

import (
	"time"

	"gorm.io/gorm"
)

// Kind mirrors the CHECK constraint on groups.kind.
type Kind string

const (
	KindLevel      Kind = "level"
	KindClass      Kind = "class"
	KindDepartment Kind = "department"
	KindTeam       Kind = "team"
	KindBatch      Kind = "batch"
	KindOther      Kind = "other"
)

// ValidKind reports whether k is an allowed group kind.
func ValidKind(k Kind) bool {
	switch k {
	case KindLevel, KindClass, KindDepartment, KindTeam, KindBatch, KindOther:
		return true
	}
	return false
}

// MemberRole mirrors the CHECK constraint on group_members.member_role.
type MemberRole string

const (
	MemberRoleMember MemberRole = "member"
	MemberRoleLeader MemberRole = "leader" // pembina / ketua / atasan
)

// GroupEntity mirrors the groups table.
type GroupEntity struct {
	ID        string  `gorm:"type:uuid;primaryKey"`
	TenantID  string  `gorm:"type:uuid;not null"`
	ParentID  *string `gorm:"type:uuid"`
	Kind      Kind    `gorm:"not null"`
	Code      *string
	Name      string `gorm:"not null"`
	Period    *string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
}

func (GroupEntity) TableName() string { return "groups" }

// GroupMemberEntity mirrors the group_members table.
type GroupMemberEntity struct {
	GroupID    string     `gorm:"type:uuid;primaryKey"`
	UserID     string     `gorm:"type:uuid;primaryKey"`
	TenantID   string     `gorm:"type:uuid;not null"`
	MemberRole MemberRole `gorm:"not null"`
	JoinedAt   time.Time
}

func (GroupMemberEntity) TableName() string { return "group_members" }
