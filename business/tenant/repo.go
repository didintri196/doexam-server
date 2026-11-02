package tenant

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelMedia "doexam/models/media"
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

func (r *TenantRepo) FindBySlug(ctx context.Context, slug string) (modelTenant.TenantEntity, error) {
	var t modelTenant.TenantEntity
	if err := dbpg.Conn(ctx, r.db).Where("slug = ?", slug).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelTenant.TenantEntity{}, errr.ErrNotFound
		}
		return modelTenant.TenantEntity{}, fmt.Errorf("find tenant by slug: %w", err)
	}
	return t, nil
}

func (r *TenantRepo) ListActive(ctx context.Context, limit int) ([]modelTenant.TenantEntity, error) {
	var ts []modelTenant.TenantEntity
	if err := dbpg.Conn(ctx, r.db).Where("is_active").Order("created_at, id").Limit(limit).Find(&ts).Error; err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}
	return ts, nil
}

func (r *TenantRepo) FindForUpdate(ctx context.Context, id string) (modelTenant.TenantEntity, error) {
	var t modelTenant.TenantEntity
	err := dbpg.Conn(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelTenant.TenantEntity{}, errr.ErrNotFound
		}
		return modelTenant.TenantEntity{}, fmt.Errorf("lock tenant %s: %w", id, err)
	}
	return t, nil
}

func (r *TenantRepo) UpdateTenant(ctx context.Context, id, name string, settings []byte) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelTenant.TenantEntity{}).Where("id = ?", id).
		Updates(map[string]any{"name": name, "settings": settings}).Error
	if err != nil {
		return fmt.Errorf("update tenant %s: %w", id, err)
	}
	return nil
}

func (r *TenantRepo) CreateMediaFile(ctx context.Context, f *modelMedia.MediaFileEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(f).Error; err != nil {
		return translate(err, "create media file")
	}
	return nil
}

func (r *TenantRepo) FindMediaFile(ctx context.Context, id string) (modelMedia.MediaFileEntity, error) {
	var f modelMedia.MediaFileEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelMedia.MediaFileEntity{}, errr.ErrNotFound
		}
		return modelMedia.MediaFileEntity{}, fmt.Errorf("find media file %s: %w", id, err)
	}
	return f, nil
}

// translate maps a unique violation to errr.ErrAlreadyExists.
func translate(err error, op string) error {
	if dbpg.IsUniqueViolation(err) {
		return fmt.Errorf("%s: %w", op, errr.ErrAlreadyExists)
	}
	return fmt.Errorf("%s: %w", op, err)
}
