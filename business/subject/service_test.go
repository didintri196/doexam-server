package subject

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	modelAudit "doexam/models/audit"
	modelAuth "doexam/models/auth"
	modelSubject "doexam/models/subject"
	"doexam/pkg/errr"
)

const (
	tenantID = "0192a000-0000-7000-8000-000000000001"
	mtk      = "0192a000-0000-7000-8000-0000000000a1"
	ipa      = "0192a000-0000-7000-8000-0000000000a2"
	cRoot    = "0192a000-0000-7000-8000-0000000000c1"
	cChild   = "0192a000-0000-7000-8000-0000000000c2"
	cGrand   = "0192a000-0000-7000-8000-0000000000c3"
	cIPA     = "0192a000-0000-7000-8000-0000000000c9"
)

// fakeStore implements only what the tests touch.
type fakeStore struct {
	SubjectStorer
	comps  map[string]modelSubject.CompetencyEntity
	banks  int64
	used   int64
	audits int
}

func newStore() *fakeStore {
	return &fakeStore{comps: map[string]modelSubject.CompetencyEntity{
		cRoot:  {ID: cRoot, SubjectID: mtk, Code: "1", Name: "Bilangan"},
		cChild: {ID: cChild, SubjectID: mtk, ParentID: ptr(cRoot), Code: "1.1", Name: "Pecahan"},
		cGrand: {ID: cGrand, SubjectID: mtk, ParentID: ptr(cChild), Code: "1.1.1", Name: "Desimal"},
		cIPA:   {ID: cIPA, SubjectID: ipa, Code: "1", Name: "Energi"},
	}}
}

func ptr[T any](v T) *T { return &v }

func (f *fakeStore) FindSubject(_ context.Context, id string) (modelSubject.SubjectEntity, error) {
	if id != mtk && id != ipa {
		return modelSubject.SubjectEntity{}, errSubjectNotFound
	}
	return modelSubject.SubjectEntity{ID: id, Code: "MTK", Name: "Matematika"}, nil
}
func (f *fakeStore) CountBanks(context.Context, string) (int64, error) { return f.banks, nil }
func (f *fakeStore) SoftDeleteSubject(context.Context, string) error   { return nil }
func (f *fakeStore) FindCompetency(_ context.Context, id string) (modelSubject.CompetencyEntity, error) {
	c, ok := f.comps[id]
	if !ok {
		return c, errCompetencyNotFound
	}
	return c, nil
}
func (f *fakeStore) CreateCompetency(_ context.Context, c *modelSubject.CompetencyEntity) error {
	f.comps[c.ID] = *c
	return nil
}
func (f *fakeStore) UpdateCompetency(_ context.Context, c *modelSubject.CompetencyEntity) error {
	f.comps[c.ID] = *c
	return nil
}
func (f *fakeStore) CountChildCompetencies(_ context.Context, id string) (int64, error) {
	var n int64
	for _, c := range f.comps {
		if c.ParentID != nil && *c.ParentID == id {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) CountItemVersionsUsing(context.Context, string) (int64, error) {
	return f.used, nil
}
func (f *fakeStore) SoftDeleteCompetency(_ context.Context, id string) error {
	delete(f.comps, id)
	return nil
}
func (f *fakeStore) CompetencyAncestorIDs(_ context.Context, id string) ([]string, error) {
	var out []string
	for c := f.comps[id]; c.ParentID != nil; c = f.comps[*c.ParentID] {
		out = append(out, *c.ParentID)
	}
	return out, nil
}
func (f *fakeStore) CreateAuditLog(context.Context, *modelAudit.AuditLogEntity) error {
	f.audits++
	return nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

var (
	authorP = modelAuth.Principal{TenantID: tenantID, Permissions: []string{modelAuth.PermItemRead, modelAuth.PermItemWrite}}
	proctor = modelAuth.Principal{TenantID: tenantID, Permissions: []string{modelAuth.PermExamRead}}
)

func newService(store *fakeStore) *SubjectService {
	return NewSubjectService(store, fakeTx{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCompetencyTree(t *testing.T) {
	store := newStore()
	svc, ctx, c := newService(store), context.Background(), modelAudit.Client{}

	for name, parent := range map[string]string{"self": cRoot, "child": cChild, "grandchild": cGrand} {
		_, err := svc.UpdateCompetency(ctx, authorP, cRoot, modelSubject.CompetencyUpdateInput{ParentID: ptr(parent)}, c)
		var ve *errr.ValidationError
		if !errors.As(err, &ve) || ve.Fields["parent_id"] == "" {
			t.Errorf("move under %s: err = %v, want parent_id error", name, err)
		}
	}
	if _, err := svc.UpdateCompetency(ctx, authorP, cGrand, modelSubject.CompetencyUpdateInput{ParentID: ptr(cIPA)}, c); err == nil {
		t.Error("parent from another subject must be rejected")
	}
	if _, err := svc.CreateCompetency(ctx, authorP, mtk, modelSubject.CompetencyInput{ParentID: ptr(cIPA), Code: "x", Name: "x"}, c); err == nil {
		t.Error("create under another subject's competency must be rejected")
	}
	if _, err := svc.UpdateCompetency(ctx, authorP, cGrand, modelSubject.CompetencyUpdateInput{ParentID: ptr("")}, c); err != nil {
		t.Fatalf("move to root: %v", err)
	}
	if store.comps[cGrand].ParentID != nil {
		t.Fatal("parent must be cleared")
	}

	if err := svc.DeleteCompetency(ctx, authorP, cRoot, c); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("delete with children: err = %v, want ErrConflict", err)
	}
	store.used = 3
	if err := svc.DeleteCompetency(ctx, authorP, cGrand, c); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("delete used: err = %v, want ErrConflict", err)
	}
	store.used = 0
	if err := svc.DeleteCompetency(ctx, authorP, cGrand, c); err != nil {
		t.Fatalf("delete leaf: %v", err)
	}
}

func TestSubjectPermissionsAndDelete(t *testing.T) {
	store := newStore()
	svc, ctx := newService(store), context.Background()

	if _, err := svc.Get(ctx, proctor, mtk); err != nil {
		t.Fatalf("exam.read may read subjects: %v", err)
	}
	if _, err := svc.Create(ctx, proctor, modelSubject.SubjectInput{Code: "X", Name: "X"}, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("proctor create: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.Create(ctx, authorP, modelSubject.SubjectInput{Code: " ", Name: "X"}, modelAudit.Client{}); !errors.Is(err, errr.ErrInvalidInput) {
		t.Fatalf("blank code: err = %v, want ErrInvalidInput", err)
	}
	store.banks = 1
	if err := svc.Delete(ctx, authorP, mtk, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("delete with banks: err = %v, want ErrConflict", err)
	}
}
