package group

import (
	"context"

	modelAudit "doexam/models/audit"
	modelGroup "doexam/models/group"
)

// GroupStorer is the persistence port for groups and memberships.
// Every method must run inside TxManager.WithTenant.
type GroupStorer interface {
	List(ctx context.Context, f modelGroup.ListFilter) ([]modelGroup.GroupEntity, int64, error)
	FindByID(ctx context.Context, id string) (modelGroup.GroupEntity, error)
	MemberCounts(ctx context.Context, groupIDs []string) (map[string]int64, error)
	Create(ctx context.Context, g *modelGroup.GroupEntity) error
	// Update writes parent_id, kind, code, name and period of g.
	Update(ctx context.Context, g *modelGroup.GroupEntity) error
	SoftDelete(ctx context.Context, id string) error
	CountChildren(ctx context.Context, id string) (int64, error)
	// AncestorIDs returns the ids of id's parent, grandparent, ... up to the root.
	AncestorIDs(ctx context.Context, id string) ([]string, error)

	ListMembers(ctx context.Context, groupID string, f modelGroup.MemberFilter) ([]modelGroup.MemberDTO, int64, error)
	// ExistingUserIDs returns which of ids are users (not deleted) of the tenant.
	ExistingUserIDs(ctx context.Context, ids []string) ([]string, error)
	UpsertMembers(ctx context.Context, members []modelGroup.GroupMemberEntity) error
	// InsertMissingMembers adds members that are not in the group yet; existing ones are untouched.
	InsertMissingMembers(ctx context.Context, members []modelGroup.GroupMemberEntity) error
	// MemberUserIDs returns the user ids of all members of groupID.
	MemberUserIDs(ctx context.Context, groupID string) ([]string, error)
	// ExistingMemberIDs returns which of userIDs are already members of groupID.
	ExistingMemberIDs(ctx context.Context, groupID string, userIDs []string) ([]string, error)
	// RemoveMember reports whether a membership was deleted.
	RemoveMember(ctx context.Context, groupID, userID string) (bool, error)
	// RemoveMembers deletes the memberships of userIDs in groupID and returns how many.
	RemoveMembers(ctx context.Context, groupID string, userIDs []string) (int64, error)

	CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error
}

// TxManager runs fn inside a transaction scoped to tenantID.
type TxManager interface {
	WithTenant(ctx context.Context, tenantID string, fn func(ctx context.Context) error) error
}
