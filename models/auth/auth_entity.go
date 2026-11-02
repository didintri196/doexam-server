package auth

import (
	"time"

	"gorm.io/gorm"

	"doexam/models/pgtype"
)

// TextArray is kept as an alias so existing code keeps compiling.
type TextArray = pgtype.TextArray

// UserStatus mirrors the CHECK constraint on users.status.
type UserStatus string

const (
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
)

// UserEntity mirrors the users table (profile is left to its DB default).
type UserEntity struct {
	ID                 string `gorm:"type:uuid;primaryKey"`
	TenantID           string `gorm:"type:uuid;not null"`
	Username           string `gorm:"not null"`
	Email              *string
	PasswordHash       *string
	FullName           string `gorm:"not null"`
	ExternalID         *string
	Status             UserStatus `gorm:"not null"`
	MustChangePassword bool
	LastLoginAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          gorm.DeletedAt
}

func (UserEntity) TableName() string { return "users" }

// RoleEntity mirrors the roles table.
type RoleEntity struct {
	ID          string    `gorm:"type:uuid;primaryKey"`
	TenantID    string    `gorm:"type:uuid;not null"`
	Code        string    `gorm:"not null"`
	Name        string    `gorm:"not null"`
	Permissions TextArray `gorm:"type:text[];not null"`
	IsSystem    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (RoleEntity) TableName() string { return "roles" }

// UserRoleEntity mirrors the user_roles table.
type UserRoleEntity struct {
	UserID   string `gorm:"type:uuid;primaryKey"`
	RoleID   string `gorm:"type:uuid;primaryKey"`
	TenantID string `gorm:"type:uuid;not null"`
}

func (UserRoleEntity) TableName() string { return "user_roles" }

// AuthSessionEntity mirrors the auth_sessions table (login session, not exam session).
type AuthSessionEntity struct {
	ID         string  `gorm:"type:uuid;primaryKey"`
	TenantID   string  `gorm:"type:uuid;not null"`
	UserID     string  `gorm:"type:uuid;not null"`
	TokenHash  []byte  `gorm:"not null"`
	IP         *string `gorm:"type:inet"`
	UserAgent  *string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

func (AuthSessionEntity) TableName() string { return "auth_sessions" }
