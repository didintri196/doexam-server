package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
)

const (
	tenantID = "0192a000-0000-7000-8000-000000000001"
	userID   = "0192a000-0000-7000-8000-000000000002"
)

// fakeStore is an in-memory AuthStorer.
type fakeStore struct {
	tenants  map[string]modelTenant.TenantEntity // by id
	users    map[string]modelAuth.UserEntity     // by id
	roles    map[string][]modelAuth.RoleEntity   // by user id
	sessions map[string]*modelAuth.AuthSessionEntity
	audits   []modelAudit.AuditLogEntity
}

func (f *fakeStore) FindTenantBySlug(_ context.Context, slug string) (modelTenant.TenantEntity, error) {
	for _, t := range f.tenants {
		if t.Slug == slug {
			return t, nil
		}
	}
	return modelTenant.TenantEntity{}, errr.ErrNotFound
}

func (f *fakeStore) ListActiveTenants(_ context.Context, limit int) ([]modelTenant.TenantEntity, error) {
	var out []modelTenant.TenantEntity
	for _, t := range f.tenants {
		if t.IsActive && len(out) < limit {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeStore) FindTenantByID(_ context.Context, id string) (modelTenant.TenantEntity, error) {
	if t, ok := f.tenants[id]; ok {
		return t, nil
	}
	return modelTenant.TenantEntity{}, errr.ErrNotFound
}

func (f *fakeStore) FindUserByUsername(_ context.Context, username string) (modelAuth.UserEntity, error) {
	for _, u := range f.users {
		if strings.EqualFold(u.Username, username) {
			return u, nil
		}
	}
	return modelAuth.UserEntity{}, errr.ErrNotFound
}

func (f *fakeStore) FindUserByID(_ context.Context, id string) (modelAuth.UserEntity, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return modelAuth.UserEntity{}, errr.ErrNotFound
}

func (f *fakeStore) FindUserRoles(_ context.Context, id string) ([]modelAuth.RoleEntity, error) {
	return f.roles[id], nil
}

func (f *fakeStore) UpdateLastLogin(_ context.Context, id string, at time.Time) error {
	u := f.users[id]
	u.LastLoginAt = &at
	f.users[id] = u
	return nil
}

func (f *fakeStore) UpdatePassword(_ context.Context, id, hash string) error {
	u := f.users[id]
	u.PasswordHash = &hash
	u.MustChangePassword = false
	f.users[id] = u
	return nil
}

func (f *fakeStore) CreateSession(_ context.Context, s *modelAuth.AuthSessionEntity) error {
	cp := *s
	f.sessions[s.ID] = &cp
	return nil
}

func (f *fakeStore) FindSessionByHash(_ context.Context, h []byte) (modelAuth.AuthSessionEntity, error) {
	for _, s := range f.sessions {
		if string(s.TokenHash) == string(h) {
			return *s, nil
		}
	}
	return modelAuth.AuthSessionEntity{}, errr.ErrNotFound
}

func (f *fakeStore) TouchSession(_ context.Context, id string, at time.Time) error {
	f.sessions[id].LastUsedAt = at
	return nil
}

func (f *fakeStore) RevokeSession(_ context.Context, id string, at time.Time) error {
	f.sessions[id].RevokedAt = &at
	return nil
}

func (f *fakeStore) RevokeUserSessions(_ context.Context, uid, except string, at time.Time) error {
	for id, s := range f.sessions {
		if s.UserID == uid && id != except && s.RevokedAt == nil {
			s.RevokedAt = &at
		}
	}
	return nil
}

func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, *l)
	return nil
}

func (f *fakeStore) actions() []string {
	var out []string
	for _, a := range f.audits {
		out = append(out, a.Action)
	}
	return out
}

// fakeTx runs fn directly and records the tenant it was scoped to.
type fakeTx struct{ tenants []string }

func (f *fakeTx) WithTenant(ctx context.Context, id string, fn func(context.Context) error) error {
	f.tenants = append(f.tenants, id)
	return fn(ctx)
}

// fakeHasher stores "hash:" + password.
type fakeHasher struct{ dummyCalls int }

func (h *fakeHasher) Hash(_ context.Context, pw string) (string, error) { return "hash:" + pw, nil }
func (h *fakeHasher) Verify(_ context.Context, enc, pw string) (bool, error) {
	return enc == "hash:"+pw, nil
}
func (h *fakeHasher) VerifyDummy(context.Context, string) { h.dummyCalls++ }

type fixture struct {
	svc    *AuthService
	store  *fakeStore
	tx     *fakeTx
	hasher *fakeHasher
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	hash := "hash:rahasia123"
	store := &fakeStore{
		tenants: map[string]modelTenant.TenantEntity{
			tenantID: {ID: tenantID, Slug: "sman1", Name: "SMAN 1", Preset: modelTenant.PresetSchool, IsActive: true},
		},
		users: map[string]modelAuth.UserEntity{
			userID: {ID: userID, TenantID: tenantID, Username: "admin", FullName: "Admin", PasswordHash: &hash, Status: modelAuth.UserStatusActive},
		},
		roles: map[string][]modelAuth.RoleEntity{
			userID: {{Code: "admin", Permissions: modelAuth.TextArray{"user.manage", "exam.read"}}},
		},
		sessions: map[string]*modelAuth.AuthSessionEntity{},
	}
	f := &fixture{store: store, tx: &fakeTx{}, hasher: &fakeHasher{}, now: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)}
	f.svc = NewAuthService(store, f.tx, f.hasher, 12*time.Hour, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.svc.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) login(t *testing.T) modelAuth.LoginDTO {
	t.Helper()
	dto, err := f.svc.Login(context.Background(),
		modelAuth.LoginInput{Tenant: "sman1", Username: "admin", Password: "rahasia123"}, modelAudit.Client{IP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return dto
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name      string
		in        modelAuth.LoginInput
		mutate    func(*fakeStore)
		wantErr   error
		wantDummy bool
		wantAudit string
	}{
		{name: "success", in: modelAuth.LoginInput{Tenant: " SMAN1 ", Username: "Admin", Password: "rahasia123"}, wantAudit: modelAudit.ActionLogin},
		{name: "missing fields", in: modelAuth.LoginInput{}, wantErr: errr.ErrInvalidInput},
		{name: "unknown tenant", in: modelAuth.LoginInput{Tenant: "nope", Username: "admin", Password: "rahasia123"}, wantErr: errr.ErrUnauthorized, wantDummy: true},
		{name: "inactive tenant", in: modelAuth.LoginInput{Tenant: "sman1", Username: "admin", Password: "rahasia123"},
			mutate:  func(s *fakeStore) { t := s.tenants[tenantID]; t.IsActive = false; s.tenants[tenantID] = t },
			wantErr: errr.ErrUnauthorized, wantDummy: true},
		{name: "tenant omitted, single institution", in: modelAuth.LoginInput{Username: "admin", Password: "rahasia123"}, wantAudit: modelAudit.ActionLogin},
		{name: "tenant omitted, several institutions", in: modelAuth.LoginInput{Username: "admin", Password: "rahasia123"},
			mutate: func(s *fakeStore) {
				s.tenants["0192a000-0000-7000-8000-000000000009"] = modelTenant.TenantEntity{ID: "0192a000-0000-7000-8000-000000000009", Slug: "sman2", IsActive: true}
			},
			wantErr: errr.ErrInvalidInput},
		{name: "tenant omitted, none active", in: modelAuth.LoginInput{Username: "admin", Password: "rahasia123"},
			mutate:  func(s *fakeStore) { t := s.tenants[tenantID]; t.IsActive = false; s.tenants[tenantID] = t },
			wantErr: errr.ErrUnauthorized, wantDummy: true},
		{name: "unknown user", in: modelAuth.LoginInput{Tenant: "sman1", Username: "ghost", Password: "rahasia123"}, wantErr: errr.ErrUnauthorized, wantDummy: true},
		{name: "wrong password", in: modelAuth.LoginInput{Tenant: "sman1", Username: "admin", Password: "salah12345"}, wantErr: errr.ErrUnauthorized, wantAudit: modelAudit.ActionLoginFailed},
		{name: "suspended user", in: modelAuth.LoginInput{Tenant: "sman1", Username: "admin", Password: "rahasia123"},
			mutate: func(s *fakeStore) {
				u := s.users[userID]
				u.Status = modelAuth.UserStatusSuspended
				s.users[userID] = u
			},
			wantErr: errr.ErrUnauthorized, wantAudit: modelAudit.ActionLoginFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.mutate != nil {
				tt.mutate(f.store)
			}
			dto, err := f.svc.Login(context.Background(), tt.in, modelAudit.Client{IP: "10.0.0.1", UserAgent: "test"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if errors.Is(err, errr.ErrUnauthorized) && err.Error() != "invalid credentials" {
				t.Fatalf("auth failures must share one message, got %q", err.Error())
			}
			if got := f.hasher.dummyCalls > 0; got != tt.wantDummy {
				t.Fatalf("dummy verify called = %v, want %v", got, tt.wantDummy)
			}
			if tt.wantAudit != "" && (len(f.store.audits) != 1 || f.store.audits[0].Action != tt.wantAudit) {
				t.Fatalf("audits = %v, want [%s]", f.store.actions(), tt.wantAudit)
			}
			if tt.wantErr != nil {
				if len(f.store.sessions) != 0 {
					t.Fatal("failed login must not create a session")
				}
				return
			}
			if !strings.HasPrefix(dto.Token, tenantID+".") {
				t.Fatalf("token %q must start with tenant id", dto.Token)
			}
			if !dto.ExpiresAt.Equal(f.now.Add(12 * time.Hour)) {
				t.Fatalf("expires_at = %v", dto.ExpiresAt)
			}
			if dto.User.Tenant.Slug != "sman1" || len(dto.User.Permissions) != 2 {
				t.Fatalf("unexpected user dto %+v", dto.User)
			}
			for _, s := range f.store.sessions {
				if strings.Contains(string(s.TokenHash), dto.Token) {
					t.Fatal("raw token must not be stored")
				}
			}
			for _, id := range f.tx.tenants {
				if id != tenantID {
					t.Fatalf("transaction scoped to %s, want %s", id, tenantID)
				}
			}
		})
	}
}

func TestAuthenticate(t *testing.T) {
	tests := []struct {
		name    string
		token   func(valid string) string
		mutate  func(*fixture)
		wantErr error
	}{
		{name: "valid", token: func(v string) string { return v }},
		{name: "garbage", token: func(string) string { return "garbage" }, wantErr: errr.ErrUnauthorized},
		{name: "bad tenant prefix", token: func(v string) string { return "not-a-uuid." + strings.SplitN(v, ".", 2)[1] }, wantErr: errr.ErrUnauthorized},
		{name: "unknown token", token: func(string) string { return tenantID + ".xxxx" }, wantErr: errr.ErrUnauthorized},
		{name: "expired", token: func(v string) string { return v }, mutate: func(f *fixture) { f.now = f.now.Add(12 * time.Hour) }, wantErr: errr.ErrUnauthorized},
		{name: "revoked", token: func(v string) string { return v }, mutate: func(f *fixture) {
			for _, s := range f.store.sessions {
				at := f.now
				s.RevokedAt = &at
			}
		}, wantErr: errr.ErrUnauthorized},
		{name: "suspended after login", token: func(v string) string { return v }, mutate: func(f *fixture) {
			u := f.store.users[userID]
			u.Status = modelAuth.UserStatusSuspended
			f.store.users[userID] = u
		}, wantErr: errr.ErrUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			dto := f.login(t)
			if tt.mutate != nil {
				tt.mutate(f)
			}
			p, err := f.svc.Authenticate(context.Background(), tt.token(dto.Token))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (p.TenantID != tenantID || p.UserID != userID || !p.Has("user.manage") || p.Has("tenant.manage")) {
				t.Fatalf("unexpected principal %+v", p)
			}
		})
	}
}

func TestAuthenticateTouchesSessionAtMostOncePerInterval(t *testing.T) {
	f := newFixture(t)
	dto := f.login(t)
	var sess *modelAuth.AuthSessionEntity
	for _, s := range f.store.sessions {
		sess = s
	}
	loginAt := sess.LastUsedAt

	f.now = f.now.Add(30 * time.Second)
	if _, err := f.svc.Authenticate(context.Background(), dto.Token); err != nil {
		t.Fatal(err)
	}
	if !sess.LastUsedAt.Equal(loginAt) {
		t.Fatal("last_used_at must not be written within touchInterval")
	}

	f.now = f.now.Add(time.Minute)
	if _, err := f.svc.Authenticate(context.Background(), dto.Token); err != nil {
		t.Fatal(err)
	}
	if !sess.LastUsedAt.Equal(f.now) {
		t.Fatalf("last_used_at = %v, want %v", sess.LastUsedAt, f.now)
	}
}

func TestLogout(t *testing.T) {
	f := newFixture(t)
	dto := f.login(t)
	p, _ := f.svc.Authenticate(context.Background(), dto.Token)

	if err := f.svc.Logout(context.Background(), p, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(context.Background(), dto.Token); !errors.Is(err, errr.ErrUnauthorized) {
		t.Fatalf("token after logout: err = %v, want ErrUnauthorized", err)
	}
	if got := f.store.actions(); got[len(got)-1] != modelAudit.ActionLogout {
		t.Fatalf("audits = %v", got)
	}
}

func TestChangePassword(t *testing.T) {
	tests := []struct {
		name      string
		in        modelAuth.ChangePasswordInput
		wantErr   error
		wantField string
	}{
		{name: "success", in: modelAuth.ChangePasswordInput{CurrentPassword: "rahasia123", NewPassword: "barubanget1"}},
		{name: "wrong current", in: modelAuth.ChangePasswordInput{CurrentPassword: "salah12345", NewPassword: "barubanget1"}, wantErr: errr.ErrInvalidInput, wantField: "current_password"},
		{name: "too short", in: modelAuth.ChangePasswordInput{CurrentPassword: "rahasia123", NewPassword: "pendek"}, wantErr: errr.ErrInvalidInput, wantField: "new_password"},
		{name: "same as current", in: modelAuth.ChangePasswordInput{CurrentPassword: "rahasia123", NewPassword: "rahasia123"}, wantErr: errr.ErrInvalidInput, wantField: "new_password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			current := f.login(t)
			other := f.login(t) // second device
			p, _ := f.svc.Authenticate(context.Background(), current.Token)

			err := f.svc.ChangePassword(context.Background(), p, tt.in, modelAudit.Client{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				var ve *errr.ValidationError
				if !errors.As(err, &ve) || ve.Fields[tt.wantField] == "" {
					t.Fatalf("want validation error on %s, got %v", tt.wantField, err)
				}
				return
			}
			if *f.store.users[userID].PasswordHash != "hash:barubanget1" {
				t.Fatal("password not updated")
			}
			if _, err := f.svc.Authenticate(context.Background(), current.Token); err != nil {
				t.Fatalf("current session must stay valid: %v", err)
			}
			if _, err := f.svc.Authenticate(context.Background(), other.Token); !errors.Is(err, errr.ErrUnauthorized) {
				t.Fatalf("other sessions must be revoked, err = %v", err)
			}
		})
	}
}
