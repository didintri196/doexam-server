package tenant

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelTenant "doexam/models/tenant"
	"doexam/pkg/errr"
)

const profileTenantID = "0192a000-0000-7000-8000-0000000000aa"

var (
	pngLogo  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	adminP   = modelAuth.Principal{TenantID: profileTenantID, UserID: "0192a000-0000-7000-8000-0000000000bb", Permissions: []string{modelAuth.PermTenantManage}}
	authorP  = modelAuth.Principal{TenantID: profileTenantID, UserID: "0192a000-0000-7000-8000-0000000000cc", Permissions: []string{modelAuth.PermItemWrite}}
	noClient = modelAudit.Client{}
)

func profileFixture() (*TenantService, *fakeStore, fakeFiles) {
	store := &fakeStore{tenants: []modelTenant.TenantEntity{{
		ID: profileTenantID, Slug: "sman1", Name: "SMAN 1", Preset: modelTenant.PresetSchool,
		Settings: []byte("{}"), IsActive: true,
	}}}
	files := fakeFiles{}
	svc := newService(store, &fakeTx{})
	svc.files = files
	return svc, store, files
}

func ptr[T any](v T) *T { return &v }

func TestUpdate(t *testing.T) {
	svc, store, _ := profileFixture()
	ctx := context.Background()

	dto, err := svc.Update(ctx, adminP, modelTenant.UpdateInput{
		Name: ptr(" SMA Negeri 1 "), Timezone: ptr("Asia/Makassar"), Locale: ptr("en"),
		PrimaryColor: ptr("#1D4ED8"), Tagline: ptr(" Ujian Online "), Letterhead: ptr("Jl. Merdeka 1"),
	}, noClient)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	s := dto.Settings
	if dto.Name != "SMA Negeri 1" || s.Timezone != "Asia/Makassar" || s.Locale != "en" ||
		s.PrimaryColor == nil || *s.PrimaryColor != "#1d4ed8" || s.Tagline != "Ujian Online" || s.Letterhead != "Jl. Merdeka 1" {
		t.Fatalf("dto = %+v", dto)
	}
	if len(store.audits) != 1 || store.audits[0].Action != modelAudit.ActionTenantUpdate {
		t.Fatalf("audits = %+v", store.audits)
	}

	// "" resets to the default, nil leaves a field alone
	dto, err = svc.Update(ctx, adminP, modelTenant.UpdateInput{Timezone: ptr(""), PrimaryColor: ptr("")}, noClient)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if dto.Settings.Timezone != modelTenant.DefaultTimezone || dto.Settings.PrimaryColor != nil || dto.Settings.Locale != "en" {
		t.Fatalf("after reset = %+v", dto.Settings)
	}

	// a no-op writes nothing
	updates, audits := store.updates, len(store.audits)
	if _, err := svc.Update(ctx, adminP, modelTenant.UpdateInput{Locale: ptr("en")}, noClient); err != nil {
		t.Fatalf("no-op: %v", err)
	}
	if store.updates != updates || len(store.audits) != audits {
		t.Fatal("an unchanged update must not write or audit")
	}
}

func TestUpdateRejected(t *testing.T) {
	tests := []struct {
		name      string
		p         modelAuth.Principal
		in        modelTenant.UpdateInput
		wantErr   error
		wantField string
	}{
		{name: "no permission", p: authorP, in: modelTenant.UpdateInput{Name: ptr("X")}, wantErr: errr.ErrForbidden},
		{name: "empty name", p: adminP, in: modelTenant.UpdateInput{Name: ptr("  ")}, wantErr: errr.ErrInvalidInput, wantField: "name"},
		{name: "long name", p: adminP, in: modelTenant.UpdateInput{Name: ptr(strings.Repeat("a", 201))}, wantErr: errr.ErrInvalidInput, wantField: "name"},
		{name: "bad timezone", p: adminP, in: modelTenant.UpdateInput{Timezone: ptr("Mars/Olympus")}, wantErr: errr.ErrInvalidInput, wantField: "timezone"},
		{name: "local timezone", p: adminP, in: modelTenant.UpdateInput{Timezone: ptr("Local")}, wantErr: errr.ErrInvalidInput, wantField: "timezone"},
		{name: "bad locale", p: adminP, in: modelTenant.UpdateInput{Locale: ptr("fr")}, wantErr: errr.ErrInvalidInput, wantField: "locale"},
		{name: "bad color", p: adminP, in: modelTenant.UpdateInput{PrimaryColor: ptr("blue")}, wantErr: errr.ErrInvalidInput, wantField: "primary_color"},
		{name: "long tagline", p: adminP, in: modelTenant.UpdateInput{Tagline: ptr(strings.Repeat("é", 101))}, wantErr: errr.ErrInvalidInput, wantField: "tagline"},
		{name: "long letterhead", p: adminP, in: modelTenant.UpdateInput{Letterhead: ptr(strings.Repeat("é", 501))}, wantErr: errr.ErrInvalidInput, wantField: "letterhead"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store, _ := profileFixture()
			_, err := svc.Update(context.Background(), tt.p, tt.in, noClient)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			var ve *errr.ValidationError
			if tt.wantField != "" && (!errors.As(err, &ve) || ve.Fields[tt.wantField] == "") {
				t.Fatalf("want error on %s, got %v", tt.wantField, err)
			}
			if store.updates != 0 || len(store.audits) != 0 {
				t.Fatal("nothing may be written")
			}
		})
	}
}

func TestLogo(t *testing.T) {
	svc, store, files := profileFixture()
	ctx := context.Background()

	dto, err := svc.SetLogo(ctx, adminP, "logo.png", pngLogo, noClient)
	if err != nil {
		t.Fatalf("set logo: %v", err)
	}
	if len(store.media) != 1 {
		t.Fatalf("media = %+v", store.media)
	}
	m := store.media[0]
	if m.MimeType != "image/png" || !strings.HasPrefix(m.StorageKey, profileTenantID+"/branding/") || !strings.HasSuffix(m.StorageKey, ".png") {
		t.Fatalf("media file = %+v", m)
	}
	if !bytes.Equal(files[m.StorageKey], pngLogo) {
		t.Fatal("logo bytes not stored")
	}
	want := "/api/v1/public/tenant/logo?tenant=sman1&v=" + m.ID
	if dto.Settings.LogoURL == nil || *dto.Settings.LogoURL != want {
		t.Fatalf("logo_url = %v, want %s", dto.Settings.LogoURL, want)
	}

	// served without login, with or without the slug
	for _, slug := range []string{"", "sman1"} {
		logo, err := svc.Logo(ctx, slug)
		if err != nil || !bytes.Equal(logo.Data, pngLogo) || logo.MimeType != "image/png" || len(logo.SHA256) != 32 {
			t.Fatalf("logo(%q) = %+v, %v", slug, logo.MimeType, err)
		}
	}

	dto, err = svc.RemoveLogo(ctx, adminP, noClient)
	if err != nil || dto.Settings.LogoURL != nil {
		t.Fatalf("remove: %+v, %v", dto.Settings, err)
	}
	if _, err := svc.Logo(ctx, ""); !errr.IsNotFound(err) {
		t.Fatalf("logo after removal: err = %v, want not found", err)
	}
	if len(store.audits) != 2 || store.audits[0].Action != modelAudit.ActionTenantLogo {
		t.Fatalf("audits = %+v", store.audits)
	}
	// removing again is a no-op
	if _, err := svc.RemoveLogo(ctx, adminP, noClient); err != nil || len(store.audits) != 2 {
		t.Fatalf("second removal: %v, audits %d", err, len(store.audits))
	}
}

func TestSetLogoRejected(t *testing.T) {
	tests := []struct {
		name    string
		p       modelAuth.Principal
		data    []byte
		wantErr error
	}{
		{name: "no permission", p: authorP, data: pngLogo, wantErr: errr.ErrForbidden},
		{name: "empty", p: adminP, data: nil, wantErr: errr.ErrInvalidInput},
		{name: "too large", p: adminP, data: append(append([]byte{}, pngLogo...), make([]byte, modelTenant.MaxLogoBytes)...), wantErr: errr.ErrInvalidInput},
		{name: "svg", p: adminP, data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), wantErr: errr.ErrInvalidInput},
		{name: "pdf renamed to png", p: adminP, data: []byte("%PDF-1.7\n..."), wantErr: errr.ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store, files := profileFixture()
			_, err := svc.SetLogo(context.Background(), tt.p, "logo.png", tt.data, noClient)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if len(files) != 0 || len(store.media) != 0 || len(store.audits) != 0 {
				t.Fatal("nothing may be stored")
			}
		})
	}
}

func TestPublic(t *testing.T) {
	ctx := context.Background()

	svc, store, _ := profileFixture()
	store.tenants[0].Settings = modelTenant.Settings{Locale: "en", PrimaryColor: "#112233", Tagline: "Ujian Online", Letterhead: "internal"}.Encode()
	dto, err := svc.Public(ctx, "")
	if err != nil {
		t.Fatalf("public: %v", err)
	}
	if dto.Slug != "sman1" || dto.Name != "SMAN 1" || dto.Locale != "en" || *dto.PrimaryColor != "#112233" || dto.Tagline != "Ujian Online" || dto.LogoURL != nil {
		t.Fatalf("dto = %+v", dto)
	}
	if _, err := svc.Public(ctx, "nope"); !errr.IsNotFound(err) {
		t.Fatalf("unknown slug: err = %v", err)
	}

	store.tenants[0].IsActive = false
	if _, err := svc.Public(ctx, "sman1"); !errr.IsNotFound(err) {
		t.Fatalf("inactive tenant by slug: err = %v", err)
	}
	if _, err := svc.Public(ctx, ""); err == nil || err.Error() != "tenant not found" {
		t.Fatalf("no active tenant: err = %v", err)
	}

	empty := newService(&fakeStore{}, &fakeTx{})
	if _, err := empty.Public(ctx, ""); err == nil || err.Error() != "setup not completed" {
		t.Fatalf("before setup: err = %v", err)
	}

	two, store2, _ := profileFixture()
	store2.tenants = append(store2.tenants, modelTenant.TenantEntity{ID: "0192a000-0000-7000-8000-0000000000dd", Slug: "sman2", IsActive: true})
	var ve *errr.ValidationError
	if _, err := two.Public(ctx, ""); !errors.As(err, &ve) || ve.Fields["tenant"] == "" {
		t.Fatalf("several tenants without slug: err = %v", err)
	}
	if dto, err := two.Public(ctx, "sman2"); err != nil || dto.Slug != "sman2" {
		t.Fatalf("by slug: %+v, %v", dto, err)
	}
}
