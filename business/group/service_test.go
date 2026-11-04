package group

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelGroup "doexam/models/group"
	"doexam/pkg/errr"
)

const (
	tenantID = "0192a000-0000-7000-8000-000000000001"
	userA    = "0192a000-0000-7000-8000-0000000000b1"
	userB    = "0192a000-0000-7000-8000-0000000000b2"
	missing  = "0192a000-0000-7000-8000-0000000000ff"
)

// fakeStore is an in-memory GroupStorer.
type fakeStore struct {
	groups  map[string]modelGroup.GroupEntity
	members map[string]map[string]modelGroup.MemberRole // group → user → role
	users   []string
	audits  []modelAudit.AuditLogEntity
}

func newStore() *fakeStore {
	return &fakeStore{groups: map[string]modelGroup.GroupEntity{}, members: map[string]map[string]modelGroup.MemberRole{}, users: []string{userA, userB}}
}

func (f *fakeStore) List(context.Context, modelGroup.ListFilter) ([]modelGroup.GroupEntity, int64, error) {
	var out []modelGroup.GroupEntity
	for _, g := range f.groups {
		out = append(out, g)
	}
	return out, int64(len(out)), nil
}
func (f *fakeStore) FindByID(_ context.Context, id string) (modelGroup.GroupEntity, error) {
	if g, ok := f.groups[id]; ok {
		return g, nil
	}
	return modelGroup.GroupEntity{}, errGroupNotFound
}
func (f *fakeStore) MemberCounts(_ context.Context, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, id := range ids {
		out[id] = int64(len(f.members[id]))
	}
	return out, nil
}
func (f *fakeStore) Create(_ context.Context, g *modelGroup.GroupEntity) error {
	f.groups[g.ID] = *g
	return nil
}
func (f *fakeStore) Update(_ context.Context, g *modelGroup.GroupEntity) error {
	f.groups[g.ID] = *g
	return nil
}
func (f *fakeStore) SoftDelete(_ context.Context, id string) error { delete(f.groups, id); return nil }
func (f *fakeStore) CountChildren(_ context.Context, id string) (int64, error) {
	var n int64
	for _, g := range f.groups {
		if g.ParentID != nil && *g.ParentID == id {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) AncestorIDs(_ context.Context, id string) ([]string, error) {
	var out []string
	for g := f.groups[id]; g.ParentID != nil; g = f.groups[*g.ParentID] {
		out = append(out, *g.ParentID)
	}
	return out, nil
}
func (f *fakeStore) ListMembers(_ context.Context, gid string, _ modelGroup.MemberFilter) ([]modelGroup.MemberDTO, int64, error) {
	var out []modelGroup.MemberDTO
	for uid, role := range f.members[gid] {
		out = append(out, modelGroup.MemberDTO{UserID: uid, MemberRole: role})
	}
	return out, int64(len(out)), nil
}
func (f *fakeStore) ExistingUserIDs(_ context.Context, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if slices.Contains(f.users, id) {
			out = append(out, id)
		}
	}
	return out, nil
}
func (f *fakeStore) UpsertMembers(_ context.Context, ms []modelGroup.GroupMemberEntity) error {
	for _, m := range ms {
		if f.members[m.GroupID] == nil {
			f.members[m.GroupID] = map[string]modelGroup.MemberRole{}
		}
		f.members[m.GroupID][m.UserID] = m.MemberRole
	}
	return nil
}
func (f *fakeStore) InsertMissingMembers(_ context.Context, ms []modelGroup.GroupMemberEntity) error {
	for _, m := range ms {
		if f.members[m.GroupID] == nil {
			f.members[m.GroupID] = map[string]modelGroup.MemberRole{}
		}
		if _, ok := f.members[m.GroupID][m.UserID]; !ok {
			f.members[m.GroupID][m.UserID] = m.MemberRole
		}
	}
	return nil
}
func (f *fakeStore) MemberUserIDs(_ context.Context, gid string) ([]string, error) {
	var out []string
	for uid := range f.members[gid] {
		out = append(out, uid)
	}
	slices.Sort(out)
	return out, nil
}
func (f *fakeStore) ExistingMemberIDs(_ context.Context, gid string, uids []string) ([]string, error) {
	var out []string
	for _, uid := range uids {
		if _, ok := f.members[gid][uid]; ok {
			out = append(out, uid)
		}
	}
	return out, nil
}
func (f *fakeStore) RemoveMembers(_ context.Context, gid string, uids []string) (int64, error) {
	var n int64
	for _, uid := range uids {
		if _, ok := f.members[gid][uid]; ok {
			delete(f.members[gid], uid)
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) RemoveMember(_ context.Context, gid, uid string) (bool, error) {
	if _, ok := f.members[gid][uid]; !ok {
		return false, nil
	}
	delete(f.members[gid], uid)
	return true, nil
}
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, *l)
	return nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

var (
	managerP   = modelAuth.Principal{TenantID: tenantID, Permissions: []string{modelAuth.PermGroupManage}}
	organizerP = modelAuth.Principal{TenantID: tenantID, Permissions: []string{modelAuth.PermSessionManage}}
	studentP   = modelAuth.Principal{TenantID: tenantID, Permissions: []string{modelAuth.PermAttemptTake}}
)

func newService(store *fakeStore) *GroupService {
	return NewGroupService(store, fakeTx{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func ptr[T any](v T) *T { return &v }

func mustCreate(t *testing.T, svc *GroupService, name string, parent *string) string {
	t.Helper()
	dto, err := svc.Create(context.Background(), managerP,
		modelGroup.CreateInput{Kind: modelGroup.KindClass, Name: name, ParentID: parent}, modelAudit.Client{})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return dto.ID
}

func TestPermissions(t *testing.T) {
	store := newStore()
	svc, ctx := newService(store), context.Background()
	id := mustCreate(t, svc, "X IPA 1", nil)

	if _, err := svc.Get(ctx, organizerP, id); err != nil {
		t.Fatalf("session.manage may read groups: %v", err)
	}
	if _, err := svc.Get(ctx, studentP, id); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("participant read: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.Create(ctx, organizerP, modelGroup.CreateInput{Kind: modelGroup.KindClass, Name: "Y"}, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("organizer create: err = %v, want ErrForbidden", err)
	}
}

func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name      string
		in        modelGroup.CreateInput
		wantField string
	}{
		{name: "missing kind", in: modelGroup.CreateInput{Name: "X"}, wantField: "kind"},
		{name: "missing name", in: modelGroup.CreateInput{Kind: modelGroup.KindTeam, Name: "  "}, wantField: "name"},
		{name: "bad parent id", in: modelGroup.CreateInput{Kind: modelGroup.KindTeam, Name: "X", ParentID: ptr("nope")}, wantField: "parent_id"},
		{name: "unknown parent", in: modelGroup.CreateInput{Kind: modelGroup.KindTeam, Name: "X", ParentID: ptr(missing)}, wantField: "parent_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newService(newStore()).Create(context.Background(), managerP, tt.in, modelAudit.Client{})
			var ve *errr.ValidationError
			if !errors.As(err, &ve) || ve.Fields[tt.wantField] == "" {
				t.Fatalf("want field error %s, got %v", tt.wantField, err)
			}
		})
	}
}

func TestUpdateRejectsCycles(t *testing.T) {
	store := newStore()
	svc, ctx := newService(store), context.Background()
	level := mustCreate(t, svc, "Kelas X", nil)
	class := mustCreate(t, svc, "X IPA 1", &level)
	sub := mustCreate(t, svc, "X IPA 1 - A", &class)

	for name, parent := range map[string]string{"self": level, "child": class, "grandchild": sub} {
		_, err := svc.Update(ctx, managerP, level, modelGroup.UpdateInput{ParentID: &parent}, modelAudit.Client{})
		var ve *errr.ValidationError
		if !errors.As(err, &ve) || ve.Fields["parent_id"] == "" {
			t.Errorf("move under %s: err = %v, want parent_id validation error", name, err)
		}
	}

	// Moving a leaf to the root and back is fine.
	if _, err := svc.Update(ctx, managerP, sub, modelGroup.UpdateInput{ParentID: ptr("")}, modelAudit.Client{}); err != nil {
		t.Fatalf("move to root: %v", err)
	}
	if store.groups[sub].ParentID != nil {
		t.Fatal("parent_id must be cleared")
	}
	if _, err := svc.Update(ctx, managerP, sub, modelGroup.UpdateInput{ParentID: &level}, modelAudit.Client{}); err != nil {
		t.Fatalf("move under level: %v", err)
	}
}

func TestDeleteWithChildren(t *testing.T) {
	store := newStore()
	svc, ctx := newService(store), context.Background()
	parent := mustCreate(t, svc, "Kelas X", nil)
	child := mustCreate(t, svc, "X IPA 1", &parent)

	if err := svc.Delete(ctx, managerP, parent, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("delete parent: err = %v, want ErrConflict", err)
	}
	if err := svc.Delete(ctx, managerP, child, modelAudit.Client{}); err != nil {
		t.Fatalf("delete leaf: %v", err)
	}
	if err := svc.Delete(ctx, managerP, parent, modelAudit.Client{}); err != nil {
		t.Fatalf("delete parent after child: %v", err)
	}
}

func TestMembers(t *testing.T) {
	store := newStore()
	svc, ctx := newService(store), context.Background()
	gid := mustCreate(t, svc, "X IPA 1", nil)

	// duplicates and case are normalised
	_, err := svc.AddMembers(ctx, managerP, gid, modelGroup.AddMembersInput{UserIDs: []string{userA, userA, " " + userB + " "}}, modelAudit.Client{})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(store.members[gid]) != 2 || store.members[gid][userA] != modelGroup.MemberRoleMember {
		t.Fatalf("members = %v", store.members[gid])
	}

	// re-adding updates the role (upsert)
	if _, err := svc.AddMembers(ctx, managerP, gid, modelGroup.AddMembersInput{UserIDs: []string{userA}, MemberRole: modelGroup.MemberRoleLeader}, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	if store.members[gid][userA] != modelGroup.MemberRoleLeader {
		t.Fatal("member_role must be updated")
	}

	_, err = svc.AddMembers(ctx, managerP, gid, modelGroup.AddMembersInput{UserIDs: []string{userA, missing}}, modelAudit.Client{})
	var ve *errr.ValidationError
	if !errors.As(err, &ve) || ve.Fields["user_ids"] == "" {
		t.Fatalf("unknown user: err = %v, want user_ids validation error", err)
	}
	if _, err := svc.AddMembers(ctx, managerP, gid, modelGroup.AddMembersInput{}, modelAudit.Client{}); !errors.Is(err, errr.ErrInvalidInput) {
		t.Fatalf("empty: err = %v, want ErrInvalidInput", err)
	}

	if err := svc.RemoveMember(ctx, managerP, gid, userB, modelAudit.Client{}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := svc.RemoveMember(ctx, managerP, gid, userB, modelAudit.Client{}); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("remove again: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.AddMembers(ctx, managerP, missing, modelGroup.AddMembersInput{UserIDs: []string{userA}}, modelAudit.Client{}); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("unknown group: err = %v, want ErrNotFound", err)
	}
}

func TestCopyAndBulkRemoveMembers(t *testing.T) {
	store := newStore()
	svc, ctx := newService(store), context.Background()
	src := mustCreate(t, svc, "Angkatan 2026", nil)
	dst := mustCreate(t, svc, "Kelompok A", nil)

	if _, err := svc.AddMembers(ctx, managerP, src, modelGroup.AddMembersInput{UserIDs: []string{userA, userB}}, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	// userA is already a leader of dst: copying must not demote them
	if _, err := svc.AddMembers(ctx, managerP, dst, modelGroup.AddMembersInput{UserIDs: []string{userA}, MemberRole: modelGroup.MemberRoleLeader}, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.AddMembers(ctx, managerP, dst, modelGroup.AddMembersInput{FromGroupID: &src}, modelAudit.Client{})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if res.Requested != 2 || res.Added != 1 || len(store.members[dst]) != 2 {
		t.Fatalf("copy result = %+v, members = %v", res, store.members[dst])
	}
	if store.members[dst][userA] != modelGroup.MemberRoleLeader || store.members[dst][userB] != modelGroup.MemberRoleMember {
		t.Fatalf("roles after copy = %v, want leader kept and new member", store.members[dst])
	}

	var ve *errr.ValidationError
	for name, in := range map[string]modelGroup.AddMembersInput{
		"copy from itself":  {FromGroupID: &dst},
		"unknown source":    {FromGroupID: ptr(missing)},
		"source not a uuid": {FromGroupID: ptr("abc")},
	} {
		if _, err := svc.AddMembers(ctx, managerP, dst, in, modelAudit.Client{}); !errors.As(err, &ve) || ve.Fields["from_group_id"] == "" {
			t.Fatalf("%s: err = %v, want from_group_id error", name, err)
		}
	}

	out, err := svc.RemoveMembers(ctx, managerP, dst, modelGroup.RemoveMembersInput{UserIDs: []string{userA, userB, missing}}, modelAudit.Client{})
	if err != nil || out.Removed != 2 || len(store.members[dst]) != 0 {
		t.Fatalf("bulk remove: out = %+v, err = %v, members = %v", out, err, store.members[dst])
	}
	if _, err := svc.RemoveMembers(ctx, managerP, dst, modelGroup.RemoveMembersInput{}, modelAudit.Client{}); !errors.As(err, &ve) {
		t.Fatalf("empty remove: err = %v", err)
	}
	if _, err := svc.RemoveMembers(ctx, organizerP, dst, modelGroup.RemoveMembersInput{UserIDs: []string{userA}}, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("no group.manage: err = %v", err)
	}
}
