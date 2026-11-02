package tenant

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: TenantRepo must satisfy TenantStorer.
var _ TenantStorer = (*TenantRepo)(nil)

// setupLockKey is an arbitrary constant for pg_advisory_xact_lock.
const setupLockKey = 7_301_001

// TenantRepo is the GORM implementation of TenantStorer.
type TenantRepo struct {
	db *gorm.DB
}

// NewTenantRepo returns a concrete TenantRepo.
func NewTenantRepo(db *gorm.DB) *TenantRepo {
	return &TenantRepo{db: db}
}

func (r *TenantRepo) LockSetup(ctx context.Context) error {
	if err := dbpg.Conn(ctx, r.db).Exec("SELECT pg_advisory_xact_lock(?)", setupLockKey).Error; err != nil {
		return fmt.Errorf("lock setup: %w", err)
	}
	return nil
}

func (r *TenantRepo) CountTenants(ctx context.Context) (int64, error) {
	var n int64
	if err := dbpg.Conn(ctx, r.db).Model(&modelTenant.TenantEntity{}).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count tenants: %w", err)
	}
	return n, nil
}

func (r *TenantRepo) CreateTenant(ctx context.Context, t *modelTenant.TenantEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(t).Error; err != nil {
		return translate(err, "create tenant")
	}
	return nil
}

func (r *TenantRepo) CreateRoles(ctx context.Context, roles []modelAuth.RoleEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(&roles).Error; err != nil {
		return translate(err, "create roles")
	}
	return nil
}

func (r *TenantRepo) CreateUser(ctx context.Context, u *modelAuth.UserEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(u).Error; err != nil {
		return translate(err, "create user")
	}
	return nil
}

func (r *TenantRepo) AssignRoles(ctx context.Context, links []modelAuth.UserRoleEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(&links).Error; err != nil {
		return translate(err, "assign roles")
	}
	return nil
}

func (r *TenantRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	if err := dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error; err != nil {
		return fmt.Errorf("create audit log: %w", err)
	}
	return nil
}

func (r *TenantRepo) FindByID(ctx context.Context, id string) (modelTenant.TenantEntity, error) {
	var t modelTenant.TenantEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelTenant.TenantEntity{}, errr.ErrNotFound
		}
		return modelTenant.TenantEntity{}, fmt.Errorf("find tenant %s: %w", id, err)
	}
	return t, nil
}

// translate maps a unique violation to errr.ErrAlreadyExists.
func translate(err error, op string) error {
	if dbpg.IsUniqueViolation(err) {
		return fmt.Errorf("%s: %w", op, errr.ErrAlreadyExists)
	}
	return fmt.Errorf("%s: %w", op, err)
}
