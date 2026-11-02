package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: AuthRepo must satisfy AuthStorer.
var _ AuthStorer = (*AuthRepo)(nil)

// AuthRepo is the GORM implementation of AuthStorer.
type AuthRepo struct {
	db *gorm.DB
}

// NewAuthRepo returns a concrete AuthRepo.
func NewAuthRepo(db *gorm.DB) *AuthRepo {
	return &AuthRepo{db: db}
}

func (r *AuthRepo) FindTenantBySlug(ctx context.Context, slug string) (modelTenant.TenantEntity, error) {
	var t modelTenant.TenantEntity
	if err := dbpg.Conn(ctx, r.db).Where("slug = ?", slug).First(&t).Error; err != nil {
		return modelTenant.TenantEntity{}, notFound(err, "find tenant by slug")
	}
	return t, nil
}

func (r *AuthRepo) ListActiveTenants(ctx context.Context, limit int) ([]modelTenant.TenantEntity, error) {
	var ts []modelTenant.TenantEntity
	if err := dbpg.Conn(ctx, r.db).Where("is_active").Order("created_at, id").Limit(limit).Find(&ts).Error; err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}
	return ts, nil
}

func (r *AuthRepo) FindTenantByID(ctx context.Context, id string) (modelTenant.TenantEntity, error) {
	var t modelTenant.TenantEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&t).Error; err != nil {
		return modelTenant.TenantEntity{}, notFound(err, "find tenant "+id)
	}
	return t, nil
}

func (r *AuthRepo) FindUserByUsername(ctx context.Context, username string) (modelAuth.UserEntity, error) {
	var u modelAuth.UserEntity
	if err := dbpg.Conn(ctx, r.db).Where("username = ?", username).First(&u).Error; err != nil {
		return modelAuth.UserEntity{}, notFound(err, "find user by username")
	}
	return u, nil
}

func (r *AuthRepo) FindUserByID(ctx context.Context, id string) (modelAuth.UserEntity, error) {
	var u modelAuth.UserEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&u).Error; err != nil {
		return modelAuth.UserEntity{}, notFound(err, "find user "+id)
	}
	return u, nil
}

func (r *AuthRepo) FindUserRoles(ctx context.Context, userID string) ([]modelAuth.RoleEntity, error) {
	var roles []modelAuth.RoleEntity
	err := dbpg.Conn(ctx, r.db).
		Joins("JOIN user_roles ur ON ur.role_id = roles.id").
		Where("ur.user_id = ?", userID).
		Order("roles.code").
		Find(&roles).Error
	if err != nil {
		return nil, fmt.Errorf("find roles of user %s: %w", userID, err)
	}
	return roles, nil
}

func (r *AuthRepo) UpdateLastLogin(ctx context.Context, userID string, at time.Time) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.UserEntity{}).
		Where("id = ?", userID).Update("last_login_at", at).Error
	if err != nil {
		return fmt.Errorf("update last login of user %s: %w", userID, err)
	}
	return nil
}

func (r *AuthRepo) UpdatePassword(ctx context.Context, userID, hash string) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.UserEntity{}).
		Where("id = ?", userID).
		Updates(map[string]any{"password_hash": hash, "must_change_password": false}).Error
	if err != nil {
		return fmt.Errorf("update password of user %s: %w", userID, err)
	}
	return nil
}

func (r *AuthRepo) CreateSession(ctx context.Context, s *modelAuth.AuthSessionEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(s).Error; err != nil {
		return fmt.Errorf("create auth session: %w", err)
	}
	return nil
}

func (r *AuthRepo) FindSessionByHash(ctx context.Context, tokenHash []byte) (modelAuth.AuthSessionEntity, error) {
	var s modelAuth.AuthSessionEntity
	// ip is not needed for authentication; skipping it also avoids scanning inet.
	err := dbpg.Conn(ctx, r.db).
		Select("id", "tenant_id", "user_id", "token_hash", "created_at", "last_used_at", "expires_at", "revoked_at").
		Where("token_hash = ?", tokenHash).
		First(&s).Error
	if err != nil {
		return modelAuth.AuthSessionEntity{}, notFound(err, "find auth session")
	}
	return s, nil
}

func (r *AuthRepo) TouchSession(ctx context.Context, id string, at time.Time) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.AuthSessionEntity{}).
		Where("id = ?", id).Update("last_used_at", at).Error
	if err != nil {
		return fmt.Errorf("touch auth session %s: %w", id, err)
	}
	return nil
}

func (r *AuthRepo) RevokeSession(ctx context.Context, id string, at time.Time) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.AuthSessionEntity{}).
		Where("id = ? AND revoked_at IS NULL", id).Update("revoked_at", at).Error
	if err != nil {
		return fmt.Errorf("revoke auth session %s: %w", id, err)
	}
	return nil
}

func (r *AuthRepo) RevokeUserSessions(ctx context.Context, userID, exceptID string, at time.Time) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.AuthSessionEntity{}).
		Where("user_id = ? AND id <> ? AND revoked_at IS NULL", userID, exceptID).
		Update("revoked_at", at).Error
	if err != nil {
		return fmt.Errorf("revoke sessions of user %s: %w", userID, err)
	}
	return nil
}

func (r *AuthRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	if err := dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error; err != nil {
		return fmt.Errorf("create audit log: %w", err)
	}
	return nil
}

func notFound(err error, op string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errr.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}
