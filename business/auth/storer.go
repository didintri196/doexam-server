package auth

import (
	"context"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
)

// AuthStorer is the persistence port for login sessions.
// Every method except the tenant lookups must run inside TxManager.WithTenant.
type AuthStorer interface {
	FindTenantBySlug(ctx context.Context, slug string) (modelTenant.TenantEntity, error)
	FindTenantByID(ctx context.Context, id string) (modelTenant.TenantEntity, error)

	FindUserByUsername(ctx context.Context, username string) (modelAuth.UserEntity, error)
	FindUserByID(ctx context.Context, id string) (modelAuth.UserEntity, error)
	FindUserRoles(ctx context.Context, userID string) ([]modelAuth.RoleEntity, error)
	UpdateLastLogin(ctx context.Context, userID string, at time.Time) error
	UpdatePassword(ctx context.Context, userID, hash string) error

	CreateSession(ctx context.Context, s *modelAuth.AuthSessionEntity) error
	FindSessionByHash(ctx context.Context, tokenHash []byte) (modelAuth.AuthSessionEntity, error)
	TouchSession(ctx context.Context, id string, at time.Time) error
	RevokeSession(ctx context.Context, id string, at time.Time) error
	// RevokeUserSessions revokes every active session of userID except exceptID.
	RevokeUserSessions(ctx context.Context, userID, exceptID string, at time.Time) error

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// PasswordHasher hashes and verifies passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, pw string) (string, error)
	Verify(ctx context.Context, encoded, pw string) (bool, error)
	// VerifyDummy burns the same time as Verify; used when the user does not exist.
	VerifyDummy(ctx context.Context, pw string)
}
