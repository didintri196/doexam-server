package tenant

import (
	"context"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
)

// TenantStorer is the persistence port for tenant setup.
type TenantStorer interface {
	// LockSetup serializes concurrent setup calls until the transaction ends.
	LockSetup(ctx context.Context) error
	CountTenants(ctx context.Context) (int64, error)
	CreateTenant(ctx context.Context, t *modelTenant.TenantEntity) error
	CreateRoles(ctx context.Context, roles []modelAuth.RoleEntity) error
	CreateUser(ctx context.Context, u *modelAuth.UserEntity) error
	AssignRoles(ctx context.Context, links []modelAuth.UserRoleEntity) error
	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
	FindByID(ctx context.Context, id string) (modelTenant.TenantEntity, error)
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// PasswordHasher hashes passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, pw string) (string, error)
}
