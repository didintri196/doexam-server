package importer

import (
	"context"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelGroup "doexam/models/group"
	modelImport "doexam/models/importer"
	modelMedia "doexam/models/media"
)

// ImportStorer is the persistence port for imports. Every method must run inside
// TxManager.WithTenant.
type ImportStorer interface {
	CreateMediaFile(ctx context.Context, f *modelMedia.MediaFileEntity) error
	CreateJob(ctx context.Context, j *modelImport.ImportJobEntity) error
	FindJob(ctx context.Context, id string) (modelImport.JobRow, error)
	ListJobs(ctx context.Context, kinds []modelImport.Kind, f modelImport.ListFilter) ([]modelImport.JobRow, int64, error)
	// UpdateJob writes status, counters, errors and finished_at of j.
	UpdateJob(ctx context.Context, j *modelImport.ImportJobEntity) error
	// ClaimJob moves a job from ready to importing; false when it was not ready.
	ClaimJob(ctx context.Context, id string) (bool, error)

	// Existing* return which of the given values are already used by a (non-deleted) user.
	ExistingUsernames(ctx context.Context, values []string) ([]string, error)
	ExistingEmails(ctx context.Context, values []string) ([]string, error)
	ExistingExternalIDs(ctx context.Context, values []string) ([]string, error)
	RolesByCodes(ctx context.Context, codes []string) ([]modelAuth.RoleEntity, error)
	GroupsByCodes(ctx context.Context, codes []string) ([]modelGroup.GroupEntity, error)

	CreateUsers(ctx context.Context, users []modelAuth.UserEntity) error
	CreateUserRoles(ctx context.Context, links []modelAuth.UserRoleEntity) error
	CreateMembers(ctx context.Context, members []modelGroup.GroupMemberEntity) error
	CreateGroups(ctx context.Context, groups []modelGroup.GroupEntity) error

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}

// FileStore keeps uploaded files.
type FileStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// PasswordHasher hashes passwords.
type PasswordHasher interface {
	Hash(ctx context.Context, pw string) (string, error)
}

// CredentialVault holds generated passwords in memory until they are downloaded once.
type CredentialVault interface {
	Put(key string, creds []modelImport.Credential)
	Take(key string) ([]modelImport.Credential, bool)
	Has(key string) bool
}
