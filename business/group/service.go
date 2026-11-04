package group

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelGroup "doexam/models/group"
	"doexam/pkg/errr"
)

var errGroupNotFound = errr.WithMessage(errr.ErrNotFound, "group not found")

// readPerms may list and view groups: exam organizers need groups to assign participants.
var readPerms = []string{modelAuth.PermGroupManage, modelAuth.PermUserManage, modelAuth.PermSessionManage}

// GroupService manages hierarchical groups (level → class, department → team) and memberships.
// Reading needs group.manage, user.manage or session.manage; changes need group.manage.
type GroupService struct {
	storer GroupStorer
	tx     TxManager
	now    func() time.Time
	tracer trace.Tracer
	logger *slog.Logger
}

// NewGroupService returns a concrete GroupService.
func NewGroupService(storer GroupStorer, tx TxManager, logger *slog.Logger) *GroupService {
	return &GroupService{
		storer: storer,
		tx:     tx,
		now:    time.Now,
		tracer: otel.Tracer("doexam.service.group"),
		logger: logger,
	}
}

// List returns one page of groups matching f, with member counts.
func (s *GroupService) List(ctx context.Context, p modelAuth.Principal, f modelGroup.ListFilter) ([]modelGroup.GroupDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "GroupService.List")
	defer span.End()

	v := errr.Validation{}
	v.Check(f.ParentID == "" || f.ParentID == modelGroup.RootParent || uuid.Validate(f.ParentID) == nil,
		"parent_id", "must be a UUID or \"root\"")
	v.Check(f.Kind == "" || modelGroup.ValidKind(f.Kind), "kind", "unknown kind")
	if err := v.Err(); err != nil {
		return nil, 0, err
	}

	var (
		out   []modelGroup.GroupDTO
		total int64
	)
	err := s.run(ctx, span, p, readPerms, func(ctx context.Context) error {
		groups, n, err := s.storer.List(ctx, f)
		if err != nil {
			return err
		}
		ids := make([]string, len(groups))
		for i, g := range groups {
			ids[i] = g.ID
		}
		counts, err := s.storer.MemberCounts(ctx, ids)
		if err != nil {
			return err
		}
		out = make([]modelGroup.GroupDTO, 0, len(groups))
		for _, g := range groups {
			out = append(out, modelGroup.ToDTO(g, counts[g.ID]))
		}
		total = n
		return nil
	})
	return out, total, err
}

// Get returns one group with its member count.
func (s *GroupService) Get(ctx context.Context, p modelAuth.Principal, id string) (modelGroup.GroupDTO, error) {
	ctx, span := s.tracer.Start(ctx, "GroupService.Get", trace.WithAttributes(attribute.String("group.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelGroup.GroupDTO{}, errGroupNotFound
	}
	var out modelGroup.GroupDTO
	err := s.run(ctx, span, p, readPerms, func(ctx context.Context) error {
		var err error
		out, err = s.load(ctx, id)
		return err
	})
	return out, err
}

// Create adds a group, optionally under a parent.
func (s *GroupService) Create(ctx context.Context, p modelAuth.Principal, in modelGroup.CreateInput, client modelAudit.Client) (modelGroup.GroupDTO, error) {
	ctx, span := s.tracer.Start(ctx, "GroupService.Create")
	defer span.End()

	in.Normalize()
	v := errr.Validation{}
	v.Check(modelGroup.ValidKind(in.Kind), "kind", "must be level, class, department, team, batch or other")
	v.Check(in.Name != "" && len(in.Name) <= 200, "name", "required, max 200 chars")
	v.Check(in.Code == nil || len(*in.Code) <= 50, "code", "max 50 chars")
	v.Check(in.Period == nil || len(*in.Period) <= 50, "period", "max 50 chars")
	v.Check(in.ParentID == nil || uuid.Validate(*in.ParentID) == nil, "parent_id", "must be a UUID")
	if err := v.Err(); err != nil {
		return modelGroup.GroupDTO{}, fail(span, err)
	}

	g := modelGroup.GroupEntity{
		ID: newID(), TenantID: p.TenantID, ParentID: in.ParentID, Kind: in.Kind,
		Code: in.Code, Name: in.Name, Period: in.Period, CreatedAt: s.now().UTC(),
	}
	var dto modelGroup.GroupDTO
	err := s.run(ctx, span, p, []string{modelAuth.PermGroupManage}, func(ctx context.Context) error {
		if g.ParentID != nil {
			if err := s.checkParentExists(ctx, *g.ParentID); err != nil {
				return err
			}
		}
		if err := s.storer.Create(ctx, &g); err != nil {
			return err
		}
		dto = modelGroup.ToDTO(g, 0)
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionGroupCreate,
			EntityType: "group", EntityID: g.ID, After: dto, Client: client,
		}))
	})
	return dto, err
}

// Update changes a group. Moving it under one of its own descendants is rejected.
func (s *GroupService) Update(ctx context.Context, p modelAuth.Principal, id string, in modelGroup.UpdateInput, client modelAudit.Client) (modelGroup.GroupDTO, error) {
	ctx, span := s.tracer.Start(ctx, "GroupService.Update", trace.WithAttributes(attribute.String("group.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return modelGroup.GroupDTO{}, errGroupNotFound
	}
	in.Normalize()
	v := errr.Validation{}
	v.Check(in.Kind == nil || modelGroup.ValidKind(*in.Kind), "kind", "must be level, class, department, team, batch or other")
	v.Check(in.Name == nil || (*in.Name != "" && len(*in.Name) <= 200), "name", "required, max 200 chars")
	v.Check(in.Code == nil || len(*in.Code) <= 50, "code", "max 50 chars")
	v.Check(in.Period == nil || len(*in.Period) <= 50, "period", "max 50 chars")
	v.Check(in.ParentID == nil || *in.ParentID == "" || uuid.Validate(*in.ParentID) == nil, "parent_id", "must be a UUID")
	v.Check(in.ParentID == nil || *in.ParentID != id, "parent_id", "a group cannot be its own parent")
	if err := v.Err(); err != nil {
		return modelGroup.GroupDTO{}, fail(span, err)
	}

	var dto modelGroup.GroupDTO
	err := s.run(ctx, span, p, []string{modelAuth.PermGroupManage}, func(ctx context.Context) error {
		g, err := s.storer.FindByID(ctx, id)
		if err != nil {
			return err
		}
		before, err := s.load(ctx, id)
		if err != nil {
			return err
		}

		if in.ParentID != nil {
			g.ParentID = optional(*in.ParentID)
			if g.ParentID != nil {
				if err := s.checkParentExists(ctx, *g.ParentID); err != nil {
					return err
				}
				ancestors, err := s.storer.AncestorIDs(ctx, *g.ParentID)
				if err != nil {
					return err
				}
				if slices.Contains(ancestors, id) {
					return &errr.ValidationError{Fields: map[string]string{"parent_id": "cannot move a group under its own subgroup"}}
				}
			}
		}
		if in.Kind != nil {
			g.Kind = *in.Kind
		}
		if in.Name != nil {
			g.Name = *in.Name
		}
		if in.Code != nil {
			g.Code = optional(*in.Code)
		}
		if in.Period != nil {
			g.Period = optional(*in.Period)
		}
		if err := s.storer.Update(ctx, &g); err != nil {
			return err
		}
		dto = modelGroup.ToDTO(g, before.MemberCount)
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionGroupUpdate,
			EntityType: "group", EntityID: id, Before: before, After: dto, Client: client,
		}))
	})
	return dto, err
}

// Delete soft-deletes a group that has no subgroups.
func (s *GroupService) Delete(ctx context.Context, p modelAuth.Principal, id string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "GroupService.Delete", trace.WithAttributes(attribute.String("group.id", id)))
	defer span.End()

	if uuid.Validate(id) != nil {
		return errGroupNotFound
	}
	return s.run(ctx, span, p, []string{modelAuth.PermGroupManage}, func(ctx context.Context) error {
		before, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		n, err := s.storer.CountChildren(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return errr.WithMessage(errr.ErrConflict, "group still has subgroups")
		}
		if err := s.storer.SoftDelete(ctx, id); err != nil {
			return err
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionGroupDelete,
			EntityType: "group", EntityID: id, Before: before, Client: client,
		}))
	})
}

// ListMembers returns one page of a group's members, leaders first.
func (s *GroupService) ListMembers(ctx context.Context, p modelAuth.Principal, groupID string, f modelGroup.MemberFilter) ([]modelGroup.MemberDTO, int64, error) {
	ctx, span := s.tracer.Start(ctx, "GroupService.ListMembers", trace.WithAttributes(attribute.String("group.id", groupID)))
	defer span.End()

	if uuid.Validate(groupID) != nil {
		return nil, 0, errGroupNotFound
	}
	var (
		out   []modelGroup.MemberDTO
		total int64
	)
	err := s.run(ctx, span, p, readPerms, func(ctx context.Context) error {
		if _, err := s.storer.FindByID(ctx, groupID); err != nil {
			return err
		}
		var err error
		out, total, err = s.storer.ListMembers(ctx, groupID, f)
		return err
	})
	return out, total, err
}

// AddMembers adds users to a group: user_ids and, with from_group_id, every member of
// another group (up to MaxMembersPerRequest users in total). Users listed in user_ids that
// are already members get their member_role updated; copied members that are already in
// the group keep their role.
func (s *GroupService) AddMembers(ctx context.Context, p modelAuth.Principal, groupID string, in modelGroup.AddMembersInput, client modelAudit.Client) (modelGroup.AddResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "GroupService.AddMembers", trace.WithAttributes(attribute.String("group.id", groupID)))
	defer span.End()

	if uuid.Validate(groupID) != nil {
		return modelGroup.AddResultDTO{}, errGroupNotFound
	}
	if in.MemberRole == "" {
		in.MemberRole = modelGroup.MemberRoleMember
	}
	from := ""
	if in.FromGroupID != nil {
		from = strings.ToLower(strings.TrimSpace(*in.FromGroupID))
	}
	ids := dedupe(in.UserIDs)
	v := errr.Validation{}
	v.Check(len(ids) > 0 || from != "", "user_ids", "give user_ids and/or from_group_id")
	v.Check(len(ids) <= modelGroup.MaxMembersPerRequest, "user_ids",
		fmt.Sprintf("max %d user ids", modelGroup.MaxMembersPerRequest))
	v.Check(!slices.ContainsFunc(ids, func(id string) bool { return uuid.Validate(id) != nil }), "user_ids", "must be UUIDs")
	v.Check(from == "" || uuid.Validate(from) == nil, "from_group_id", "must be a UUID")
	v.Check(from != groupID, "from_group_id", "must be another group")
	v.Check(in.MemberRole == modelGroup.MemberRoleMember || in.MemberRole == modelGroup.MemberRoleLeader,
		"member_role", "must be member or leader")
	if err := v.Err(); err != nil {
		return modelGroup.AddResultDTO{}, fail(span, err)
	}

	var out modelGroup.AddResultDTO
	err := s.run(ctx, span, p, []string{modelAuth.PermGroupManage}, func(ctx context.Context) error {
		if _, err := s.storer.FindByID(ctx, groupID); err != nil {
			return err
		}
		var copied []string
		if from != "" {
			if _, err := s.storer.FindByID(ctx, from); err != nil {
				if errors.Is(err, errr.ErrNotFound) {
					return &errr.ValidationError{Fields: map[string]string{"from_group_id": "group not found"}}
				}
				return err
			}
			srcIDs, err := s.storer.MemberUserIDs(ctx, from)
			if err != nil {
				return err
			}
			copied = diff(srcIDs, ids)
		}
		if len(ids)+len(copied) > modelGroup.MaxMembersPerRequest {
			return &errr.ValidationError{Fields: map[string]string{"from_group_id": fmt.Sprintf("max %d users per request", modelGroup.MaxMembersPerRequest)}}
		}
		if len(ids) > 0 {
			found, err := s.storer.ExistingUserIDs(ctx, ids)
			if err != nil {
				return err
			}
			if missing := diff(ids, found); len(missing) > 0 {
				if len(missing) > 5 {
					missing = append(missing[:5], "...")
				}
				return &errr.ValidationError{Fields: map[string]string{"user_ids": "unknown users: " + strings.Join(missing, ", ")}}
			}
		}
		existing, err := s.storer.ExistingMemberIDs(ctx, groupID, append(slices.Clone(ids), copied...))
		if err != nil {
			return err
		}
		now := s.now().UTC()
		rows := func(uids []string) []modelGroup.GroupMemberEntity {
			out := make([]modelGroup.GroupMemberEntity, len(uids))
			for i, uid := range uids {
				out[i] = modelGroup.GroupMemberEntity{GroupID: groupID, UserID: uid, TenantID: p.TenantID, MemberRole: in.MemberRole, JoinedAt: now}
			}
			return out
		}
		if err := s.storer.UpsertMembers(ctx, rows(ids)); err != nil {
			return err
		}
		if err := s.storer.InsertMissingMembers(ctx, rows(copied)); err != nil {
			return err
		}
		out = modelGroup.AddResultDTO{Requested: len(ids) + len(copied), Added: len(ids) + len(copied) - len(existing)}
		after := map[string]any{"user_ids": ids, "member_role": in.MemberRole}
		if from != "" {
			after["from_group_id"] = from
			after["copied"] = len(copied)
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionGroupMemberAdd,
			EntityType: "group", EntityID: groupID, Client: client, After: after,
		}))
	})
	return out, err
}

// RemoveMembers removes up to MaxMembersPerRequest users from a group at once.
func (s *GroupService) RemoveMembers(ctx context.Context, p modelAuth.Principal, groupID string, in modelGroup.RemoveMembersInput, client modelAudit.Client) (modelGroup.RemoveResultDTO, error) {
	ctx, span := s.tracer.Start(ctx, "GroupService.RemoveMembers", trace.WithAttributes(attribute.String("group.id", groupID)))
	defer span.End()

	if uuid.Validate(groupID) != nil {
		return modelGroup.RemoveResultDTO{}, errGroupNotFound
	}
	ids := dedupe(in.UserIDs)
	v := errr.Validation{}
	v.Check(len(ids) > 0 && len(ids) <= modelGroup.MaxMembersPerRequest, "user_ids",
		fmt.Sprintf("1-%d user ids", modelGroup.MaxMembersPerRequest))
	v.Check(!slices.ContainsFunc(ids, func(id string) bool { return uuid.Validate(id) != nil }), "user_ids", "must be UUIDs")
	if err := v.Err(); err != nil {
		return modelGroup.RemoveResultDTO{}, fail(span, err)
	}

	var out modelGroup.RemoveResultDTO
	err := s.run(ctx, span, p, []string{modelAuth.PermGroupManage}, func(ctx context.Context) error {
		if _, err := s.storer.FindByID(ctx, groupID); err != nil {
			return err
		}
		n, err := s.storer.RemoveMembers(ctx, groupID, ids)
		if err != nil {
			return err
		}
		out.Removed = n
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionGroupMemberRemove,
			EntityType: "group", EntityID: groupID, Client: client,
			Before: map[string]any{"user_ids": ids, "removed": n},
		}))
	})
	return out, err
}

// RemoveMember removes one user from a group.
func (s *GroupService) RemoveMember(ctx context.Context, p modelAuth.Principal, groupID, userID string, client modelAudit.Client) error {
	ctx, span := s.tracer.Start(ctx, "GroupService.RemoveMember", trace.WithAttributes(attribute.String("group.id", groupID)))
	defer span.End()

	if uuid.Validate(groupID) != nil || uuid.Validate(userID) != nil {
		return errr.WithMessage(errr.ErrNotFound, "member not found")
	}
	return s.run(ctx, span, p, []string{modelAuth.PermGroupManage}, func(ctx context.Context) error {
		removed, err := s.storer.RemoveMember(ctx, groupID, userID)
		if err != nil {
			return err
		}
		if !removed {
			return errr.WithMessage(errr.ErrNotFound, "member not found")
		}
		return s.storer.CreateAuditLog(ctx, modelAudit.New(modelAudit.Entry{
			TenantID: p.TenantID, ActorID: p.UserID, Action: modelAudit.ActionGroupMemberRemove,
			EntityType: "group", EntityID: groupID, Client: client,
			Before: map[string]any{"user_id": userID},
		}))
	})
}

// run checks that p holds one of perms and executes fn in p's tenant transaction.
func (s *GroupService) run(ctx context.Context, span trace.Span, p modelAuth.Principal, perms []string, fn func(ctx context.Context) error) error {
	if !slices.ContainsFunc(perms, p.Has) {
		return fail(span, errr.ErrForbidden)
	}
	if err := s.tx.WithTenant(ctx, p.TenantID, fn); err != nil {
		return fail(span, err)
	}
	return nil
}

func (s *GroupService) load(ctx context.Context, id string) (modelGroup.GroupDTO, error) {
	g, err := s.storer.FindByID(ctx, id)
	if err != nil {
		return modelGroup.GroupDTO{}, err
	}
	counts, err := s.storer.MemberCounts(ctx, []string{id})
	if err != nil {
		return modelGroup.GroupDTO{}, err
	}
	return modelGroup.ToDTO(g, counts[id]), nil
}

func (s *GroupService) checkParentExists(ctx context.Context, parentID string) error {
	if _, err := s.storer.FindByID(ctx, parentID); err != nil {
		if errors.Is(err, errr.ErrNotFound) {
			return &errr.ValidationError{Fields: map[string]string{"parent_id": "group not found"}}
		}
		return err
	}
	return nil
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// diff returns the elements of want that are not in have.
func diff(want, have []string) []string {
	var out []string
	for _, w := range want {
		if !slices.Contains(have, w) {
			out = append(out, w)
		}
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fail(span trace.Span, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	return err
}

// newID returns a UUIDv7 (time-ordered, index friendly).
func newID() string {
	return uuid.Must(uuid.NewV7()).String()
}
