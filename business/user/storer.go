package user

import (
	"context"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelUser "doexam/models/user"
)

// UserStorer is the persistence port for user management.
// Every method must run inside TxManager.WithTenant.
type UserStorer interface {
	ListRoles(ctx context.Context) ([]modelAuth.RoleEntity, error)
	FindRolesByCodes(ctx context.Context, codes []string) ([]modelAuth.RoleEntity, error)
	RolesOfUsers(ctx context.Context, userIDs []string) (map[string][]modelAuth.RoleEntity, error)
	GroupsOfUser(ctx context.Context, userID string) ([]modelUser.UserGroupDTO, error)

	List(ctx context.Context, f modelUser.ListFilter) ([]modelAuth.UserEntity, int64, error)
	FindByID(ctx context.Context, id string) (modelAuth.UserEntity, error)
	// FindByIdentifiers returns non-deleted users whose lower(username), lower(external_id)
	// or lower(email) is one of keys (already lowercased).
	FindByIdentifiers(ctx context.Context, keys []string) ([]modelAuth.UserEntity, error)
	Create(ctx context.Context, u *modelAuth.UserEntity) error
	// Update writes full_name, email, external_id and status of u.
	Update(ctx context.Context, u *modelAuth.UserEntity) error
	SoftDelete(ctx context.Context, id string) error
	SetPassword(ctx context.Context, id, hash string, mustChange bool) error
	// ReplaceRoles sets the user's roles to exactly roleIDs.
	ReplaceRoles(ctx context.Context, tenantID, userID string, roleIDs []string) error
	RevokeSessions(ctx context.Context, userID string, at time.Time) error

	// LockAdminRole serializes changes that could remove the last admin.
	LockAdminRole(ctx context.Context) error
	// CountActiveAdmins counts active users with the admin role, excluding excludeUserID.
	CountActiveAdmins(ctx context.Context, excludeUserID string) (int64, error)

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// PasswordHasher hashes passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, pw string) (string, error)
}

// SessionForgetter drops cached logins of a user (implemented by the auth domain). Call
// it after the transaction that revoked sessions or changed status or roles commits.
type SessionForgetter interface {
	ForgetUser(tenantID, userID string)
}
