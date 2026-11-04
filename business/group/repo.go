package group

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	modelAudit "doexam/models/audit"
	modelGroup "doexam/models/group"
	dbpg "doexam/pkg/db-pg"
	"doexam/pkg/errr"
)

// Compile-time check: GroupRepo must satisfy GroupStorer.
var _ GroupStorer = (*GroupRepo)(nil)

// GroupRepo is the GORM implementation of GroupStorer.
type GroupRepo struct {
	db *gorm.DB
}

// NewGroupRepo returns a concrete GroupRepo.
func NewGroupRepo(db *gorm.DB) *GroupRepo {
	return &GroupRepo{db: db}
}

func (r *GroupRepo) List(ctx context.Context, f modelGroup.ListFilter) ([]modelGroup.GroupEntity, int64, error) {
	q := dbpg.Conn(ctx, r.db).Model(&modelGroup.GroupEntity{})
	switch f.ParentID {
	case "":
	case modelGroup.RootParent:
		q = q.Where("parent_id IS NULL")
	default:
		q = q.Where("parent_id = ?", f.ParentID)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Period != "" {
		q = q.Where("period = ?", f.Period)
	}
	if f.Query != "" {
		p := dbpg.ContainsPattern(f.Query)
		q = q.Where("(name ILIKE ? OR code ILIKE ?)", p, p)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count groups: %w", err)
	}
	var groups []modelGroup.GroupEntity
	err := q.Session(&gorm.Session{}).Order("name, id").
		Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).Find(&groups).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list groups: %w", err)
	}
	return groups, total, nil
}

func (r *GroupRepo) FindByID(ctx context.Context, id string) (modelGroup.GroupEntity, error) {
	var g modelGroup.GroupEntity
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelGroup.GroupEntity{}, errr.WithMessage(errr.ErrNotFound, "group not found")
		}
		return modelGroup.GroupEntity{}, fmt.Errorf("find group %s: %w", id, err)
	}
	return g, nil
}

func (r *GroupRepo) MemberCounts(ctx context.Context, groupIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		GroupID string
		N       int64
	}
	err := dbpg.Conn(ctx, r.db).Table("group_members gm").
		Select("gm.group_id, count(*) AS n").
		Joins("JOIN users u ON u.id = gm.user_id AND u.deleted_at IS NULL").
		Where("gm.group_id IN ?", groupIDs).
		Group("gm.group_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("member counts: %w", err)
	}
	for _, row := range rows {
		out[row.GroupID] = row.N
	}
	return out, nil
}

func (r *GroupRepo) Create(ctx context.Context, g *modelGroup.GroupEntity) error {
	if err := dbpg.Conn(ctx, r.db).Create(g).Error; err != nil {
		return translateGroupErr(err, "create group")
	}
	return nil
}

func (r *GroupRepo) Update(ctx context.Context, g *modelGroup.GroupEntity) error {
	err := dbpg.Conn(ctx, r.db).Model(g).
		Select("parent_id", "kind", "code", "name", "period").
		Updates(g).Error
	if err != nil {
		return translateGroupErr(err, "update group "+g.ID)
	}
	return nil
}

func (r *GroupRepo) SoftDelete(ctx context.Context, id string) error {
	if err := dbpg.Conn(ctx, r.db).Where("id = ?", id).Delete(&modelGroup.GroupEntity{}).Error; err != nil {
		return fmt.Errorf("delete group %s: %w", id, err)
	}
	return nil
}

func (r *GroupRepo) CountChildren(ctx context.Context, id string) (int64, error) {
	var n int64
	if err := dbpg.Conn(ctx, r.db).Model(&modelGroup.GroupEntity{}).Where("parent_id = ?", id).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count children of group %s: %w", id, err)
	}
	return n, nil
}

func (r *GroupRepo) AncestorIDs(ctx context.Context, id string) ([]string, error) {
	var ids []string
	// depth guard: a corrupted tree with a cycle must not loop forever.
	err := dbpg.Conn(ctx, r.db).Raw(`
		WITH RECURSIVE up AS (
			SELECT parent_id, 1 AS depth FROM groups WHERE id = ?
			UNION ALL
			SELECT g.parent_id, up.depth + 1 FROM groups g JOIN up ON g.id = up.parent_id
			WHERE up.depth < 100
		)
		SELECT parent_id FROM up WHERE parent_id IS NOT NULL`, id).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("ancestors of group %s: %w", id, err)
	}
	return ids, nil
}

func (r *GroupRepo) ListMembers(ctx context.Context, groupID string, f modelGroup.MemberFilter) ([]modelGroup.MemberDTO, int64, error) {
	q := dbpg.Conn(ctx, r.db).Table("group_members gm").
		Joins("JOIN users u ON u.id = gm.user_id AND u.deleted_at IS NULL").
		Where("gm.group_id = ?", groupID)

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count members of group %s: %w", groupID, err)
	}
	members := []modelGroup.MemberDTO{}
	err := q.Session(&gorm.Session{}).
		Select("gm.user_id, u.username, u.full_name, u.external_id, gm.member_role, gm.joined_at").
		Order("gm.member_role, u.full_name, u.id"). // "leader" < "member": leaders first
		Limit(f.PerPage).Offset((f.Page - 1) * f.PerPage).
		Scan(&members).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list members of group %s: %w", groupID, err)
	}
	return members, total, nil
}

func (r *GroupRepo) ExistingUserIDs(ctx context.Context, ids []string) ([]string, error) {
	var found []string
	if len(ids) == 0 {
		return found, nil
	}
	err := dbpg.Conn(ctx, r.db).Table("users").
		Where("id IN ? AND deleted_at IS NULL", ids).
		Pluck("id", &found).Error
	if err != nil {
		return nil, fmt.Errorf("existing users: %w", err)
	}
	return found, nil
}

func (r *GroupRepo) UpsertMembers(ctx context.Context, members []modelGroup.GroupMemberEntity) error {
	if len(members) == 0 {
		return nil
	}
	err := dbpg.Conn(ctx, r.db).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "group_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"member_role"}),
	}).CreateInBatches(&members, 1000).Error
	if err != nil {
		return fmt.Errorf("upsert group members: %w", err)
	}
	return nil
}

func (r *GroupRepo) InsertMissingMembers(ctx context.Context, members []modelGroup.GroupMemberEntity) error {
	if len(members) == 0 {
		return nil
	}
	err := dbpg.Conn(ctx, r.db).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&members, 1000).Error
	if err != nil {
		return fmt.Errorf("insert group members: %w", err)
	}
	return nil
}

func (r *GroupRepo) MemberUserIDs(ctx context.Context, groupID string) ([]string, error) {
	var ids []string
	err := dbpg.Conn(ctx, r.db).Table("group_members gm").
		Joins("JOIN users u ON u.id = gm.user_id AND u.deleted_at IS NULL").
		Where("gm.group_id = ?", groupID).Order("gm.joined_at, gm.user_id").
		Pluck("gm.user_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("member ids of group %s: %w", groupID, err)
	}
	return ids, nil
}

func (r *GroupRepo) ExistingMemberIDs(ctx context.Context, groupID string, userIDs []string) ([]string, error) {
	var ids []string
	if len(userIDs) == 0 {
		return ids, nil
	}
	err := dbpg.Conn(ctx, r.db).Model(&modelGroup.GroupMemberEntity{}).
		Where("group_id = ? AND user_id IN ?", groupID, userIDs).Pluck("user_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("existing members of group %s: %w", groupID, err)
	}
	return ids, nil
}

func (r *GroupRepo) RemoveMembers(ctx context.Context, groupID string, userIDs []string) (int64, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}
	res := dbpg.Conn(ctx, r.db).Where("group_id = ? AND user_id IN ?", groupID, userIDs).
		Delete(&modelGroup.GroupMemberEntity{})
	if res.Error != nil {
		return 0, fmt.Errorf("remove members of group %s: %w", groupID, res.Error)
	}
	return res.RowsAffected, nil
}

func (r *GroupRepo) RemoveMember(ctx context.Context, groupID, userID string) (bool, error) {
	res := dbpg.Conn(ctx, r.db).Where("group_id = ? AND user_id = ?", groupID, userID).
		Delete(&modelGroup.GroupMemberEntity{})
	if res.Error != nil {
		return false, fmt.Errorf("remove member %s of group %s: %w", userID, groupID, res.Error)
	}
	return res.RowsAffected > 0, nil
}

func (r *GroupRepo) CreateAuditLog(ctx context.Context, log *modelAudit.AuditLogEntity) error {
	if err := dbpg.Conn(ctx, r.db).Omit("ID").Create(log).Error; err != nil {
		return fmt.Errorf("create audit log: %w", err)
	}
	return nil
}

func translateGroupErr(err error, op string) error {
	if dbpg.IsUniqueViolation(err) && dbpg.ConstraintName(err) == "groups_tenant_code_period_uq" {
		return &errr.ValidationError{Fields: map[string]string{"code": "already used in this period"}}
	}
	return fmt.Errorf("%s: %w", op, err)
}
