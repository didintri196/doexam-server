package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelUser "doexam/models/user"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: UserRepo must satisfy UserStorer.
var _ UserStorer = (*UserRepo)(nil)

// UserRepo is the GORM implementation of UserStorer.
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo returns a concrete UserRepo.
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) ListRoles(ctx context.Context) ([]modelAuth.RoleEntity, error) {
	var roles []modelAuth.RoleEntity
	if err := dbpg.Conn(ctx, r.db).Order("is_system DESC, code").Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return roles, nil
}

func (r *UserRepo) FindRolesByCodes(ctx context.Context, codes []string) ([]modelAuth.RoleEntity, error) {
	var roles []modelAuth.RoleEntity
	if len(codes) == 0 {
		return roles, nil
	}
	if err := dbpg.Conn(ctx, r.db).Where("code IN ?", codes).Order("code").Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("find roles by codes: %w", err)
	}
	return roles, nil
}

func (r *UserRepo) RolesOfUsers(ctx context.Context, userIDs []string) (map[string][]modelAuth.RoleEntity, error) {
	out := make(map[string][]modelAuth.RoleEntity, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		UserID string
		modelAuth.RoleEntity
	}
	err := dbpg.Conn(ctx, r.db).Table("user_roles ur").
		Select("ur.user_id, r.id, r.tenant_id, r.code, r.name, r.permissions, r.is_system").
		Joins("JOIN roles r ON r.id = ur.role_id").
		Where("ur.user_id IN ?", userIDs).
		Order("r.code").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("roles of users: %w", err)
	}
	for _, row := range rows {
		out[row.UserID] = append(out[row.UserID], row.RoleEntity)
	}
	return out, nil
}

func (r *UserRepo) GroupsOfUser(ctx context.Context, userID string) ([]modelUser.UserGroupDTO, error) {
	groups := []modelUser.UserGroupDTO{}
	err := dbpg.Conn(ctx, r.db).Table("group_members gm").
		Select("g.id, g.name, g.kind, g.period, gm.member_role").
		Joins("JOIN groups g ON g.id = gm.group_id AND g.deleted_at IS NULL").
		Where("gm.user_id = ?", userID).
		Order("g.name, g.id").
		Scan(&groups).Error
	if err != nil {
		return nil, fmt.Errorf("groups of user %s: %w", userID, err)
	}
	return groups, nil
}

func (r *UserRepo) List(ctx context.Context, f modelUser.ListFilter) ([]modelAuth.UserEntity, int64, error) {
	q := dbpg.Conn(ctx, r.db).Model(&modelAuth.UserEntity{})
	if f.Query != "" {
		p := dbpg.ContainsPattern(f.Query)
		q = q.Where("(username ILIKE ? OR full_name ILIKE ? OR external_id ILIKE ? OR email ILIKE ?)", p, p, p, p)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.RoleCode != "" {
		q = q.Where(`EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
			WHERE ur.user_id = users.id AND r.code = ?)`, f.RoleCode)
	}
	if f.GroupID != "" {
		q = q.Where("EXISTS (SELECT 1 FROM group_members gm WHERE gm.user_id = users.id AND gm.group_id = ?)", f.GroupID)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	var users []modelAuth.UserEntity
	err := q.Session(&gorm.Session{}).Order("full_name, id").
		Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Find(&users).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	return users, total, nil
}

func (r *UserRepo) FindByID(ctx context.Context, id string) (modelAuth.UserEntity, error) {
	var u modelAuth.UserEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelAuth.UserEntity{}, errr.WithMessage(errr.ErrNotFound, "user not found")
		}
		return modelAuth.UserEntity{}, fmt.Errorf("find user %s: %w", id, err)
	}
	return u, nil
}

func (r *UserRepo) Create(ctx context.Context, u *modelAuth.UserEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(u).Error; err != nil {
		return translateUserErr(err, "create user")
	}
	return nil
}

func (r *UserRepo) Update(ctx context.Context, u *modelAuth.UserEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(u).
		Select("full_name", "email", "external_id", "status").
		Updates(u).Error
	if err != nil {
		return translateUserErr(err, "update user "+u.ID)
	}
	return nil
}

func (r *UserRepo) SoftDelete(ctx context.Context, id string) error {
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelAuth.UserEntity{}).Error; err != nil {
		return fmt.Errorf("delete user %s: %w", id, err)
	}
	return nil
}

func (r *UserRepo) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.UserEntity{}).Where("id = ?", id).
		Updates(map[string]any{"password_hash": hash, "must_change_password": mustChange}).Error
	if err != nil {
		return fmt.Errorf("set password of user %s: %w", id, err)
	}
	return nil
}

func (r *UserRepo) ReplaceRoles(ctx context.Context, tenantID, userID string, roleIDs []string) error {
	conn := dbpg.Conn(ctx, r.db)
	if err := conn.Where("user_id = ?", userID).Delete(&modelAuth.UserRoleEntity{}).Error; err != nil {
		return fmt.Errorf("clear roles of user %s: %w", userID, err)
	}
	if len(roleIDs) == 0 {
		return nil
	}
	links := make([]modelAuth.UserRoleEntity, len(roleIDs))
	for i, id := range roleIDs {
		links[i] = modelAuth.UserRoleEntity{UserID: userID, RoleID: id, TenantID: tenantID}
	}
	if err := conn.Create(&links).Error; err != nil {
		return fmt.Errorf("assign roles of user %s: %w", userID, err)
	}
	return nil
}

func (r *UserRepo) RevokeSessions(ctx context.Context, userID string, at time.Time) error {
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.AuthSessionEntity{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", at).Error
	if err != nil {
		return fmt.Errorf("revoke sessions of user %s: %w", userID, err)
	}
	return nil
}

func (r *UserRepo) LockAdminRole(ctx context.Context) error {
	if err := dbpg.Conn(ctx, r.db).Exec("SELECT 1 FROM roles WHERE code = ? FOR UPDATE", modelAuth.RoleAdmin).Error; err != nil {
		return fmt.Errorf("lock admin role: %w", err)
	}
	return nil
}

func (r *UserRepo) CountActiveAdmins(ctx context.Context, excludeUserID string) (int64, error) {
	var n int64
	err := dbpg.Conn(ctx, r.db).Model(&modelAuth.UserEntity{}).
		Joins("JOIN user_roles ur ON ur.user_id = users.id").
		Joins("JOIN roles r ON r.id = ur.role_id").
		Where("r.code = ? AND users.status = ? AND users.id <> ?", modelAuth.RoleAdmin, modelAuth.UserStatusActive, excludeUserID).
		Distinct("users.id").
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count active admins: %w", err)
	}
	return n, nil
}

func (r *UserRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	if err := dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error; err != nil {
		return fmt.Errorf("create audit log: %w", err)
	}
	return nil
}

// translateUserErr maps unique violations of users to field validation errors.
func translateUserErr(err error, op string) error {
	if dbpg.IsUniqueViolation(err) {
		field := map[string]string{
			"users_tenant_username_uq": "username",
			"users_tenant_email_uq":    "email",
			"users_tenant_extid_uq":    "external_id",
		}[dbpg.ConstraintName(err)]
		if field != "" {
			return &errr.ValidationError{Fields: map[string]string{field: "already in use"}}
		}
		return fmt.Errorf("%s: %w", op, errr.ErrAlreadyExists)
	}
	return fmt.Errorf("%s: %w", op, err)
}
