package tenant

import (
	"context"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelMedia "doexam/models/media"
	modelTenant "doexam/models/tenant"
)

// TenantStorer is the persistence port for tenants. The tenants table has no
// row-level security, so the Find* tenant lookups also work outside a tenant transaction.
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
	FindBySlug(ctx context.Context, slug string) (modelTenant.TenantEntity, error)
	// ListActive returns up to limit active tenants, oldest first.
	ListActive(ctx context.Context, limit int) ([]modelTenant.TenantEntity, error)
	// FindForUpdate locks the tenant row until the transaction ends.
	FindForUpdate(ctx context.Context, id string) (modelTenant.TenantEntity, error)
	UpdateTenant(ctx context.Context, id, name string, settings []byte) error

	// Media files are under row-level security: call these inside TxManager.WithTenant.
	CreateMediaFile(ctx context.Context, f *modelMedia.MediaFileEntity) error
	FindMediaFile(ctx context.Context, id string) (modelMedia.MediaFileEntity, error)
}

// FileStore keeps uploaded files.
type FileStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// LinkSigner makes signed, expiring media links (pkg/mediaurl), used for the card
// signature image, which must not be public like the logo.
type LinkSigner interface {
	Path(prefix, tenantID, fileID string, now time.Time) string
}

// PasswordHasher hashes passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, pw string) (string, error)
}
