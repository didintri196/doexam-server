package user

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelUser "doexam/models/user"
	"doexam/pkg/errr"
)

const (
	tenantID = "0192a000-0000-7000-8000-000000000001"
	adminID  = "0192a000-0000-7000-8000-0000000000a1"
	admin2ID = "0192a000-0000-7000-8000-0000000000a2"
	siswaID  = "0192a000-0000-7000-8000-0000000000b1"
)

var roleTable = []modelAuth.RoleEntity{
	{ID: "r-admin", Code: modelAuth.RoleAdmin},
	{ID: "r-author", Code: modelAuth.RoleAuthor},
	{ID: "r-participant", Code: modelAuth.RoleParticipant},
}

// fakeStore is an in-memory UserStorer.
type fakeStore struct {
	users     map[string]modelAuth.UserEntity
	userRoles map[string][]string // user id → role ids
	revoked   []string
	passwords map[string]string
	audits    []modelAudit.AuditLogEntity
}

func newStore() *fakeStore {
	return &fakeStore{
		users: map[string]modelAuth.UserEntity{
			adminID: {ID: adminID, Username: "admin", FullName: "Admin", Status: modelAuth.UserStatusActive},
			siswaID: {ID: siswaID, Username: "siswa1", FullName: "Siswa", Status: modelAuth.UserStatusActive},
		},
		userRoles: map[string][]string{adminID: {"r-admin"}, siswaID: {"r-participant"}},
		passwords: map[string]string{},
	}
}

func (f *fakeStore) addAdmin(id string) {
	f.users[id] = modelAuth.UserEntity{ID: id, Username: "admin2", Status: modelAuth.UserStatusActive}
	f.userRoles[id] = []string{"r-admin"}
}

func (f *fakeStore) ListRoles(context.Context) ([]modelAuth.RoleEntity, error) { return roleTable, nil }
func (f *fakeStore) FindRolesByCodes(_ context.Context, codes []string) ([]modelAuth.RoleEntity, error) {
	var out []modelAuth.RoleEntity
	for _, r := range roleTable {
		if slices.Contains(codes, r.Code) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) RolesOfUsers(_ context.Context, ids []string) (map[string][]modelAuth.RoleEntity, error) {
	out := map[string][]modelAuth.RoleEntity{}
	for _, id := range ids {
		for _, rid := range f.userRoles[id] {
			for _, r := range roleTable {
				if r.ID == rid {
					out[id] = append(out[id], r)
				}
			}
		}
	}
	return out, nil
}
func (f *fakeStore) GroupsOfUser(context.Context, string) ([]modelUser.UserGroupDTO, error) {
	return []modelUser.UserGroupDTO{}, nil
}
func (f *fakeStore) List(_ context.Context, fl modelUser.ListFilter) ([]modelAuth.UserEntity, int64, error) {
	var out []modelAuth.UserEntity
	for _, u := range f.users {
		out = append(out, u)
	}
	return out, int64(len(out)), nil
}
func (f *fakeStore) FindByID(_ context.Context, id string) (modelAuth.UserEntity, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return modelAuth.UserEntity{}, errUserNotFound
}
func (f *fakeStore) FindByIdentifiers(_ context.Context, keys []string) ([]modelAuth.UserEntity, error) {
	var out []modelAuth.UserEntity
	for _, u := range f.users {
		for _, k := range keys {
			if strings.ToLower(u.Username) == k || (u.ExternalID != nil && strings.ToLower(*u.ExternalID) == k) ||
				(u.Email != nil && strings.ToLower(*u.Email) == k) {
				out = append(out, u)
				break
			}
		}
	}
	return out, nil
}
func (f *fakeStore) Create(_ context.Context, u *modelAuth.UserEntity) error {
	for _, e := range f.users {
		if e.Username == u.Username {
			return &errr.ValidationError{Fields: map[string]string{"username": "already in use"}}
		}
	}
	f.users[u.ID] = *u
	return nil
}
func (f *fakeStore) Update(_ context.Context, u *modelAuth.UserEntity) error {
	f.users[u.ID] = *u
	return nil
}
func (f *fakeStore) SoftDelete(_ context.Context, id string) error { delete(f.users, id); return nil }
func (f *fakeStore) SetPassword(_ context.Context, id, hash string, _ bool) error {
	f.passwords[id] = hash
	return nil
}
func (f *fakeStore) ReplaceRoles(_ context.Context, _, uid string, ids []string) error {
	f.userRoles[uid] = ids
	return nil
}
func (f *fakeStore) RevokeSessions(_ context.Context, uid string, _ time.Time) error {
	f.revoked = append(f.revoked, uid)
	return nil
}
func (f *fakeStore) LockAdminRole(context.Context) error { return nil }
func (f *fakeStore) CountActiveAdmins(_ context.Context, exclude string) (int64, error) {
	var n int64
	for id, rids := range f.userRoles {
		u, ok := f.users[id]
		if ok && id != exclude && u.Status == modelAuth.UserStatusActive && slices.Contains(rids, "r-admin") {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, *l)
	return nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

type fakeHasher struct{}

func (fakeHasher) Hash(_ context.Context, pw string) (string, error) { return "hash:" + pw, nil }

var (
	adminP   = modelAuth.Principal{TenantID: tenantID, UserID: adminID, Permissions: []string{modelAuth.PermUserManage}}
	noPermsP = modelAuth.Principal{TenantID: tenantID, UserID: siswaID, Permissions: []string{modelAuth.PermAttemptTake}}
)

// fakeForgetter records which users' cached logins were dropped.
type fakeForgetter struct{ forgotten []string }

func (f *fakeForgetter) ForgetUser(tenantID, userID string) {
	f.forgotten = append(f.forgotten, tenantID+"/"+userID)
}

func newService(store *fakeStore) *UserService {
	return NewUserService(store, fakeTx{}, fakeHasher{}, &fakeForgetter{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// forgotten reports whether the service dropped the cached logins of userID.
func forgotten(svc *UserService, userID string) bool {
	return slices.Contains(svc.sessions.(*fakeForgetter).forgotten, tenantID+"/"+userID)
}

func ptr[T any](v T) *T { return &v }

func TestForbiddenWithoutPermission(t *testing.T) {
	svc, ctx := newService(newStore()), context.Background()
	checks := map[string]error{}
	_, checks["list"] = func() (any, error) {
		_, _, err := svc.List(ctx, noPermsP, modelUser.ListFilter{Page: 1, PerPage: 20})
		return nil, err
	}()
	_, checks["get"] = svc.Get(ctx, noPermsP, siswaID)
	_, checks["create"] = svc.Create(ctx, noPermsP, modelUser.CreateInput{Username: "x"}, modelAudit.Client{})
	_, checks["update"] = svc.Update(ctx, noPermsP, siswaID, modelUser.UpdateInput{}, modelAudit.Client{})
	checks["delete"] = svc.Delete(ctx, noPermsP, adminID, modelAudit.Client{})
	_, checks["reset"] = svc.ResetPassword(ctx, noPermsP, adminID, modelAudit.Client{})
	_, checks["roles"] = svc.ListRoles(ctx, noPermsP)
	for name, err := range checks {
		if !errors.Is(err, errr.ErrForbidden) {
			t.Errorf("%s: err = %v, want ErrForbidden", name, err)
		}
	}
}

func TestCreate(t *testing.T) {
	tests := []struct {
		name         string
		in           modelUser.CreateInput
		wantErr      error
		wantField    string
		wantTemp     bool
		wantRoles    []string
		wantMustChng bool
	}{
		{name: "defaults", in: modelUser.CreateInput{Username: " 12345 ", FullName: "Budi"},
			wantTemp: true, wantRoles: []string{"participant"}, wantMustChng: true},
		{name: "explicit password and roles", in: modelUser.CreateInput{Username: "guru1", FullName: "Bu Guru",
			Password: "rahasia123", RoleCodes: []string{"Author", "author"}, MustChangePassword: ptr(false)},
			wantRoles: []string{"author"}},
		{name: "unknown role", in: modelUser.CreateInput{Username: "x123", FullName: "X", RoleCodes: []string{"superuser"}},
			wantErr: errr.ErrInvalidInput, wantField: "role_codes"},
		{name: "bad username", in: modelUser.CreateInput{Username: "a b", FullName: "X"}, wantErr: errr.ErrInvalidInput, wantField: "username"},
		{name: "bad email", in: modelUser.CreateInput{Username: "abc", FullName: "X", Email: ptr("Budi <b@x.id>")}, wantErr: errr.ErrInvalidInput, wantField: "email"},
		{name: "short password", in: modelUser.CreateInput{Username: "abc", FullName: "X", Password: "123"}, wantErr: errr.ErrInvalidInput, wantField: "password"},
		{name: "duplicate username", in: modelUser.CreateInput{Username: "siswa1", FullName: "X"}, wantErr: errr.ErrInvalidInput, wantField: "username"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()
			dto, err := newService(store).Create(context.Background(), adminP, tt.in, modelAudit.Client{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				var ve *errr.ValidationError
				if !errors.As(err, &ve) || ve.Fields[tt.wantField] == "" {
					t.Fatalf("want field error %s, got %v", tt.wantField, err)
				}
				return
			}
			if got := dto.TemporaryPassword != ""; got != tt.wantTemp {
				t.Fatalf("temporary password returned = %v, want %v", got, tt.wantTemp)
			}
			stored := store.users[dto.User.ID]
			if tt.wantTemp && *stored.PasswordHash != "hash:"+dto.TemporaryPassword {
				t.Fatal("stored hash must match the returned temporary password")
			}
			if !slices.Equal(dto.User.Roles, tt.wantRoles) || stored.MustChangePassword != tt.wantMustChng {
				t.Fatalf("roles = %v, must_change = %v", dto.User.Roles, stored.MustChangePassword)
			}
			if len(store.audits) != 1 || store.audits[0].Action != modelAudit.ActionUserCreate {
				t.Fatalf("audits = %+v", store.audits)
			}
		})
	}
}

func TestUpdateGuards(t *testing.T) {
	tests := []struct {
		name       string
		withAdmin2 bool
		target     string
		in         modelUser.UpdateInput
		wantErr    error
		wantRevoke bool
	}{
		{name: "suspend participant revokes sessions", target: siswaID,
			in: modelUser.UpdateInput{Status: ptr(modelAuth.UserStatusSuspended)}, wantRevoke: true},
		{name: "cannot suspend yourself", withAdmin2: true, target: adminID,
			in: modelUser.UpdateInput{Status: ptr(modelAuth.UserStatusSuspended)}, wantErr: errr.ErrForbidden},
		{name: "cannot demote last admin", target: adminID,
			in: modelUser.UpdateInput{RoleCodes: ptr([]string{"author"})}, wantErr: errr.ErrForbidden},
		{name: "can demote when another admin exists", withAdmin2: true, target: adminID,
			in: modelUser.UpdateInput{RoleCodes: ptr([]string{"author"})}},
		{name: "can suspend another admin while you remain", target: admin2ID, withAdmin2: true,
			in: modelUser.UpdateInput{Status: ptr(modelAuth.UserStatusSuspended)}, wantRevoke: true},
		{name: "empty role list rejected", target: siswaID,
			in: modelUser.UpdateInput{RoleCodes: ptr([]string{})}, wantErr: errr.ErrInvalidInput},
		{name: "clear email", target: siswaID, in: modelUser.UpdateInput{Email: ptr("")}},
		{name: "unknown user", target: "0192a000-0000-7000-8000-0000000000ff", in: modelUser.UpdateInput{FullName: ptr("X")}, wantErr: errr.ErrNotFound},
		{name: "invalid id", target: "nope", in: modelUser.UpdateInput{}, wantErr: errr.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()
			if tt.withAdmin2 {
				store.addAdmin(admin2ID)
			}
			svc := newService(store)
			_, err := svc.Update(context.Background(), adminP, tt.target, tt.in, modelAudit.Client{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			// a saved change (status, roles) must drop the user's cached logins; a refused one must not
			if got := forgotten(svc, tt.target); got != (tt.wantErr == nil) {
				t.Fatalf("cached logins dropped = %v, want %v", got, tt.wantErr == nil)
			}
			if got := slices.Contains(store.revoked, tt.target); got != tt.wantRevoke {
				t.Fatalf("sessions revoked = %v, want %v", got, tt.wantRevoke)
			}
			if tt.wantErr == nil && (len(store.audits) != 1 || store.audits[0].Before == nil || store.audits[0].After == nil) {
				t.Fatalf("update must be audited with before/after, got %+v", store.audits)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name       string
		withAdmin2 bool
		target     string
		wantErr    error
	}{
		{name: "participant", target: siswaID},
		{name: "yourself", target: adminID, withAdmin2: true, wantErr: errr.ErrForbidden},
		{name: "other admin when you remain", target: admin2ID, withAdmin2: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()
			if tt.withAdmin2 {
				store.addAdmin(admin2ID)
			}
			svc := newService(store)
			err := svc.Delete(context.Background(), adminP, tt.target, modelAudit.Client{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := forgotten(svc, tt.target); got != (tt.wantErr == nil) {
				t.Fatalf("cached logins dropped = %v, want %v", got, tt.wantErr == nil)
			}
			_, exists := store.users[tt.target]
			if exists != (tt.wantErr != nil) {
				t.Fatalf("user exists after delete = %v", exists)
			}
		})
	}
}

func TestDeleteLastAdminByAnotherManager(t *testing.T) {
	// A non-admin user manager must not be able to delete the only admin.
	store := newStore()
	manager := modelAuth.Principal{TenantID: tenantID, UserID: siswaID, Permissions: []string{modelAuth.PermUserManage}}
	err := newService(store).Delete(context.Background(), manager, adminID, modelAudit.Client{})
	if !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestResetPassword(t *testing.T) {
	store := newStore()
	svc := newService(store)

	dto, err := svc.ResetPassword(context.Background(), adminP, siswaID, modelAudit.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dto.TemporaryPassword) != temporaryPasswordLen || store.passwords[siswaID] != "hash:"+dto.TemporaryPassword {
		t.Fatalf("temporary password %q not stored", dto.TemporaryPassword)
	}
	if !slices.Contains(store.revoked, siswaID) {
		t.Fatal("reset must revoke sessions")
	}
	if !forgotten(svc, siswaID) {
		t.Fatal("reset must drop the user's cached logins")
	}
	if _, err := svc.ResetPassword(context.Background(), adminP, adminID, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("reset yourself: err = %v, want ErrForbidden", err)
	}
}

func TestListValidatesFilter(t *testing.T) {
	for _, f := range []modelUser.ListFilter{
		{Page: 1, PerPage: 20, Status: "deleted"},
		{Page: 1, PerPage: 20, Kind: "teacher"},
	} {
		_, _, err := newService(newStore()).List(context.Background(), adminP, f)
		if !errors.Is(err, errr.ErrInvalidInput) {
			t.Fatalf("%+v: err = %v, want ErrInvalidInput", f, err)
		}
	}
}
