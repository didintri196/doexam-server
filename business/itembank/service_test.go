package itembank

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelBank "doexam/models/itembank"
	"doexam/pkg/errr"
)

const (
	tenantID = "0192a000-0000-7000-8000-000000000001"
	bankID   = "0192a000-0000-7000-8000-0000000000b1"
	itemID   = "0192a000-0000-7000-8000-0000000000c1"
	verID    = "0192a000-0000-7000-8000-0000000000d1"
	guruID   = "0192a000-0000-7000-8000-0000000000e1"
)

// fakeStore implements only what the tests touch; anything else panics via the nil interface.
type fakeStore struct {
	ItemBankStorer
	access   modelBank.Access
	item     modelBank.ItemEntity
	versions map[string]modelBank.ItemVersionEntity
	created  []modelBank.ItemVersionEntity
	updated  []modelBank.ItemVersionEntity
	audits   []string
	media    []string // media files of the tenant
}

func (f *fakeStore) ExistingMediaIDs(_ context.Context, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if slices.Contains(f.media, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeStore) FindBank(_ context.Context, id string) (modelBank.BankEntity, error) {
	if id != bankID {
		return modelBank.BankEntity{}, errBankNotFound
	}
	return modelBank.BankEntity{ID: bankID, SubjectID: "s1"}, nil
}
func (f *fakeStore) MemberAccess(context.Context, string, string) (modelBank.Access, error) {
	return f.access, nil
}
func (f *fakeStore) FindItem(_ context.Context, id string, _ bool) (modelBank.ItemEntity, error) {
	if id != f.item.ID {
		return modelBank.ItemEntity{}, errItemNotFound
	}
	return f.item, nil
}
func (f *fakeStore) FindVersion(_ context.Context, id string) (modelBank.ItemVersionEntity, error) {
	return f.versions[id], nil
}
func (f *fakeStore) MaxVersionNo(context.Context, string) (int, error) { return len(f.versions), nil }
func (f *fakeStore) CreateVersion(_ context.Context, v *modelBank.ItemVersionEntity) error {
	f.created = append(f.created, *v)
	f.versions[v.ID] = *v
	return nil
}
func (f *fakeStore) UpdateVersion(_ context.Context, v *modelBank.ItemVersionEntity) error {
	if old := f.versions[v.ID]; old.LockedAt != nil && (string(old.Content) != string(v.Content) || string(old.AnswerKey) != string(v.AnswerKey)) {
		return errors.New("trigger: locked version content changed") // mirrors item_versions_immutable
	}
	f.updated = append(f.updated, *v)
	f.versions[v.ID] = *v
	return nil
}
func (f *fakeStore) UpdateItem(_ context.Context, it *modelBank.ItemEntity) error {
	f.item = *it
	return nil
}
func (f *fakeStore) HasLockedVersion(context.Context, string) (bool, error) {
	for _, v := range f.versions {
		if v.LockedAt != nil {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, l.Action)
	return nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

var (
	authorP = modelAuth.Principal{TenantID: tenantID, UserID: guruID, Permissions: []string{modelAuth.PermItemRead, modelAuth.PermItemWrite}}
	adminP  = modelAuth.Principal{TenantID: tenantID, UserID: "admin", Permissions: []string{modelAuth.PermTenantManage, modelAuth.PermItemRead, modelAuth.PermItemWrite}}
	graderP = modelAuth.Principal{TenantID: tenantID, UserID: guruID, Permissions: []string{modelAuth.PermExamRead}}
)

// newFixture: one true/false item whose current version is optionally locked.
func newFixture(t *testing.T, access modelBank.Access, locked bool) (*ItemBankService, *fakeStore) {
	t.Helper()
	body := normalizeBody(modelBank.TypeTrueFalse, raw(`{"stem":"Bumi bulat"}`), raw(`{"value":true}`), nil, errr.Validation{})
	v := modelBank.ItemVersionEntity{ID: verID, ItemID: itemID, VersionNo: 1, DefaultPoints: 1, Tags: []string{}}
	applyBody(&v, body)
	if locked {
		at := time.Now()
		v.LockedAt = &at
	}
	store := &fakeStore{
		access:   access,
		item:     modelBank.ItemEntity{ID: itemID, BankID: bankID, Status: modelBank.StatusActive, CurrentVersionID: ptr(verID)},
		versions: map[string]modelBank.ItemVersionEntity{verID: v},
	}
	return NewItemBankService(store, fakeTx{}, slog.New(slog.NewTextHandler(io.Discard, nil))), store
}

func ptr[T any](v T) *T { return &v }

func TestBankAccess(t *testing.T) {
	tests := []struct {
		name    string
		p       modelAuth.Principal
		access  modelBank.Access
		lvl     level
		wantErr error
	}{
		{"viewer reads", authorP, modelBank.AccessViewer, levelRead, nil},
		{"viewer cannot write", authorP, modelBank.AccessViewer, levelWrite, errr.ErrForbidden},
		{"editor writes", authorP, modelBank.AccessEditor, levelWrite, nil},
		{"editor cannot manage", authorP, modelBank.AccessEditor, levelManage, errr.ErrForbidden},
		{"owner manages", authorP, modelBank.AccessOwner, levelManage, nil},
		{"non-member sees nothing", authorP, "", levelRead, errr.ErrNotFound},
		{"admin is owner everywhere", adminP, "", levelManage, nil},
		{"member without item perms sees nothing", graderP, modelBank.AccessOwner, levelRead, errr.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newFixture(t, tt.access, false)
			_, _, err := svc.bankAccess(context.Background(), tt.p, bankID, tt.lvl)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateItemVersioning(t *testing.T) {
	newKey := raw(`{"value":false}`)
	tests := []struct {
		name        string
		locked      bool
		in          modelBank.ItemUpdateInput
		wantNew     bool
		wantVersion int
	}{
		{name: "unlocked content edit updates in place", in: modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{AnswerKey: newKey}}, wantVersion: 1},
		{name: "locked content edit creates version 2", locked: true, in: modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{AnswerKey: newKey}}, wantNew: true, wantVersion: 2},
		{name: "locked metadata edit stays in place", locked: true, in: modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{Tags: &[]string{"fisika"}}}, wantVersion: 1},
		{name: "locked identical content resent stays in place", locked: true,
			in: modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{Type: modelBank.TypeTrueFalse, Content: raw(`{"stem":"Bumi bulat"}`), AnswerKey: raw(`{"value":true}`)}}, wantVersion: 1},
		{name: "locked status change stays in place", locked: true, in: modelBank.ItemUpdateInput{Status: ptr(modelBank.StatusRetired)}, wantVersion: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newFixture(t, modelBank.AccessEditor, tt.locked)
			dto, err := svc.UpdateItem(context.Background(), authorP, itemID, tt.in, modelAudit.Client{})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if got := len(store.created) == 1; got != tt.wantNew {
				t.Fatalf("new version created = %v, want %v", got, tt.wantNew)
			}
			if dto.Version.VersionNo != tt.wantVersion || *store.item.CurrentVersionID != dto.Version.ID {
				t.Fatalf("current version = %d (%s), want %d", dto.Version.VersionNo, dto.Version.ID, tt.wantVersion)
			}
			if tt.wantNew {
				if dto.Version.Locked || string(store.versions[verID].AnswerKey) != `{"value":true}` {
					t.Fatal("the locked version must stay untouched and the new one unlocked")
				}
			}
			if len(store.audits) != 1 {
				t.Fatalf("audits = %v", store.audits)
			}
		})
	}
}

func TestUpdateItemRejectsInvalid(t *testing.T) {
	svc, store := newFixture(t, modelBank.AccessEditor, false)
	// switching type without a matching key must fail validation, nothing written
	_, err := svc.UpdateItem(context.Background(), authorP, itemID,
		modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{Type: modelBank.TypeMCQSingle}}, modelAudit.Client{})
	var ve *errr.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want validation error", err)
	}
	if len(store.updated)+len(store.created) != 0 {
		t.Fatal("nothing may be written")
	}
	if _, err := svc.UpdateItem(context.Background(), authorP, itemID, modelBank.ItemUpdateInput{}, modelAudit.Client{}); err != nil {
		t.Fatalf("empty patch must be fine: %v", err)
	}
}

func TestItemAccessHidesOtherBanks(t *testing.T) {
	svc, _ := newFixture(t, "", false)
	if _, err := svc.GetItem(context.Background(), authorP, itemID); !errors.Is(err, errr.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	svc, _ = newFixture(t, modelBank.AccessViewer, false)
	_, err := svc.UpdateItem(context.Background(), authorP, itemID, modelBank.ItemUpdateInput{}, modelAudit.Client{})
	if !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("viewer update: err = %v, want ErrForbidden", err)
	}
}

func TestDeleteUsedItemRefused(t *testing.T) {
	svc, _ := newFixture(t, modelBank.AccessEditor, true)
	if err := svc.DeleteItem(context.Background(), authorP, itemID, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestItemDTOHasKeyAndExplanation(t *testing.T) {
	svc, store := newFixture(t, modelBank.AccessViewer, false)
	v := store.versions[verID]
	v.Explanation = mustJSON(modelBank.Explanation{Body: "<p>karena</p>"})
	store.versions[verID] = v
	dto, err := svc.GetItem(context.Background(), authorP, itemID)
	if err != nil {
		t.Fatal(err)
	}
	var key modelBank.KeyTrueFalse
	if json.Unmarshal(dto.Version.AnswerKey, &key) != nil || !key.Value || dto.Version.Explanation == nil {
		t.Fatalf("dto = %+v", dto.Version)
	}
}

func TestItemMediaMustExist(t *testing.T) {
	known, unknown := "0192a000-0000-7000-8000-0000000000f1", "0192a000-0000-7000-8000-0000000000f2"
	stem := func(id string) json.RawMessage {
		return raw(`{"stem":"<p>Perhatikan gambar</p><img data-media-id=\"` + id + `\" alt=\"grafik\">"}`)
	}
	svc, store := newFixture(t, modelBank.AccessEditor, false)
	store.media = []string{known}

	_, err := svc.UpdateItem(context.Background(), authorP, itemID, modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{Content: stem(unknown)}}, modelAudit.Client{})
	var ve *errr.ValidationError
	if !errors.As(err, &ve) || ve.Fields["content"] == "" {
		t.Fatalf("unknown media: err = %v, want content error", err)
	}
	if len(store.updated) != 0 {
		t.Fatal("nothing may be written")
	}
	dto, err := svc.UpdateItem(context.Background(), authorP, itemID, modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{Content: stem(known)}}, modelAudit.Client{})
	if err != nil {
		t.Fatalf("known media: %v", err)
	}
	if !strings.Contains(string(dto.Version.Content), known) {
		t.Fatalf("content lost the image: %s", dto.Version.Content)
	}
	bad := "<p>x</p>"
	if _, err := svc.UpdateItem(context.Background(), authorP, itemID, modelBank.ItemUpdateInput{VersionInput: modelBank.VersionInput{Explanation: ptr(bad + `<img data-media-id="` + unknown + `">`)}}, modelAudit.Client{}); !errors.As(err, &ve) || ve.Fields["explanation"] == "" {
		t.Fatalf("unknown media in explanation: err = %v", err)
	}
}
