package tenant

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
)

type fakeStore struct {
	existing  int64
	locked    bool
	tenants   []modelTenant.TenantEntity
	roles     []modelAuth.RoleEntity
	users     []modelAuth.UserEntity
	userRoles []modelAuth.UserRoleEntity
	audits    []modelAudit.AuditLogEntity
}

func (f *fakeStore) LockSetup(context.Context) error { f.locked = true; return nil }
func (f *fakeStore) CountTenants(context.Context) (int64, error) {
	return f.existing + int64(len(f.tenants)), nil
}
func (f *fakeStore) CreateTenant(_ context.Context, t *modelTenant.TenantEntity) error {
	f.tenants = append(f.tenants, *t)
	return nil
}
func (f *fakeStore) CreateRoles(_ context.Context, r []modelAuth.RoleEntity) error {
	f.roles = append(f.roles, r...)
	return nil
}
func (f *fakeStore) CreateUser(_ context.Context, u *modelAuth.UserEntity) error {
	f.users = append(f.users, *u)
	return nil
}
func (f *fakeStore) AssignRoles(_ context.Context, l []modelAuth.UserRoleEntity) error {
	f.userRoles = append(f.userRoles, l...)
	return nil
}
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, *l)
	return nil
}
func (f *fakeStore) FindByID(_ context.Context, id string) (modelTenant.TenantEntity, error) {
	for _, t := range f.tenants {
		if t.ID == id {
			return t, nil
		}
	}
	return modelTenant.TenantEntity{}, errr.ErrNotFound
}

type fakeTx struct{ tenantID string }

func (f *fakeTx) WithTenant(ctx context.Context, id string, fn func(context.Context) error) error {
	f.tenantID = id
	return fn(ctx)
}

type fakeHasher struct{}

func (fakeHasher) Hash(_ context.Context, pw string) (string, error) { return "hash:" + pw, nil }

func validInput() modelTenant.SetupInput {
	return modelTenant.SetupInput{
		TenantSlug: " SMAN1-Kota ", TenantName: "SMAN 1 Kota", Preset: modelTenant.PresetSchool,
		AdminUsername: "admin", AdminFullName: "Admin Sekolah", AdminPassword: "rahasia123",
	}
}

func newService(store *fakeStore, tx *fakeTx) *TenantService {
	return NewTenantService(store, tx, fakeHasher{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSetup(t *testing.T) {
	store, tx := &fakeStore{}, &fakeTx{}
	dto, err := newService(store, tx).Setup(context.Background(), validInput())
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if dto.Tenant.Slug != "sman1-kota" || !dto.Tenant.IsActive {
		t.Fatalf("tenant dto = %+v", dto.Tenant)
	}
	if !store.locked {
		t.Fatal("setup must take the advisory lock")
	}
	if tx.tenantID != dto.Tenant.ID {
		t.Fatalf("tx scoped to %s, want %s", tx.tenantID, dto.Tenant.ID)
	}
	if len(store.roles) != 5 {
		t.Fatalf("want 5 system roles, got %d", len(store.roles))
	}
	names := map[string]string{}
	for _, r := range store.roles {
		if !r.IsSystem || r.TenantID != dto.Tenant.ID {
			t.Fatalf("bad role %+v", r)
		}
		names[r.Code] = r.Name
	}
	if names[modelAuth.RoleParticipant] != "Siswa" {
		t.Fatalf("school preset participant name = %q, want Siswa", names[modelAuth.RoleParticipant])
	}
	if *store.users[0].PasswordHash != "hash:rahasia123" {
		t.Fatal("admin password must be hashed")
	}
	if len(store.userRoles) != 1 || store.userRoles[0].RoleID != roleID(store.roles, modelAuth.RoleAdmin) {
		t.Fatalf("admin must get the admin role, got %+v", store.userRoles)
	}
	if len(store.audits) != 1 || store.audits[0].Action != modelAudit.ActionTenantSetup {
		t.Fatalf("audits = %+v", store.audits)
	}
}

func TestSetupRejected(t *testing.T) {
	tests := []struct {
		name      string
		existing  int64
		mutate    func(*modelTenant.SetupInput)
		wantErr   error
		wantField string
	}{
		{name: "already set up", existing: 1, wantErr: errr.ErrAlreadyExists},
		{name: "bad slug", mutate: func(in *modelTenant.SetupInput) { in.TenantSlug = "no spaces!" }, wantErr: errr.ErrInvalidInput, wantField: "tenant_slug"},
		{name: "bad preset", mutate: func(in *modelTenant.SetupInput) { in.Preset = "kampus" }, wantErr: errr.ErrInvalidInput, wantField: "preset"},
		{name: "weak password", mutate: func(in *modelTenant.SetupInput) { in.AdminPassword = "123" }, wantErr: errr.ErrInvalidInput, wantField: "admin_password"},
		{name: "missing admin name", mutate: func(in *modelTenant.SetupInput) { in.AdminFullName = "  " }, wantErr: errr.ErrInvalidInput, wantField: "admin_full_name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{existing: tt.existing}
			in := validInput()
			if tt.mutate != nil {
				tt.mutate(&in)
			}
			_, err := newService(store, &fakeTx{}).Setup(context.Background(), in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantField != "" {
				var ve *errr.ValidationError
				if !errors.As(err, &ve) || ve.Fields[tt.wantField] == "" {
					t.Fatalf("want validation error on %s, got %v", tt.wantField, err)
				}
			}
			if len(store.tenants) != 0 {
				t.Fatal("nothing may be created")
			}
		})
	}
}

func roleID(roles []modelAuth.RoleEntity, code string) string {
	for _, r := range roles {
		if r.Code == code {
			return r.ID
		}
	}
	return ""
}
