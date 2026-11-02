package exam

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
	modelExam "doexam/models/exam"
	"doexam/pkg/errr"
)

func cands(ids ...string) []Candidate {
	out := make([]Candidate, len(ids))
	for i, id := range ids {
		out[i] = Candidate{ItemID: id, VersionID: "v-" + id}
	}
	return out
}

func TestUnmetRules(t *testing.T) {
	tests := []struct {
		name      string
		rules     []modelExam.RuleEntity
		pools     map[string][]Candidate
		wantShort map[string]int // rule id → assigned
	}{
		{
			name:  "enough distinct items",
			rules: []modelExam.RuleEntity{{ID: "r1", ItemCount: 2}, {ID: "r2", ItemCount: 1}},
			pools: map[string][]Candidate{"r1": cands("a", "b"), "r2": cands("c")},
		},
		{
			// Greedy fails here: r1 takes "a" first, then r2 (only "a") is stuck.
			// A valid assignment is r1←b, r2←a, which augmenting paths find.
			name:  "overlap solvable only by re-assigning",
			rules: []modelExam.RuleEntity{{ID: "r1", ItemCount: 1}, {ID: "r2", ItemCount: 1}},
			pools: map[string][]Candidate{"r1": cands("a", "b"), "r2": cands("a")},
		},
		{
			name:      "shared pool too small",
			rules:     []modelExam.RuleEntity{{ID: "r1", ItemCount: 2}, {ID: "r2", ItemCount: 2}},
			pools:     map[string][]Candidate{"r1": cands("a", "b", "c"), "r2": cands("a", "b", "c")},
			wantShort: map[string]int{"r2": 1},
		},
		{
			name:      "empty pool",
			rules:     []modelExam.RuleEntity{{ID: "r1", ItemCount: 3}},
			pools:     map[string][]Candidate{"r1": nil},
			wantShort: map[string]int{"r1": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]int{}
			for _, s := range unmetRules(tt.rules, tt.pools) {
				got[s.rule.ID] = s.assigned
			}
			total := 0
			for _, n := range tt.wantShort {
				total += n
			}
			if len(got) != len(tt.wantShort) {
				t.Fatalf("short rules = %v, want %v", got, tt.wantShort)
			}
			// with a shared pool, which rule ends up short is arbitrary; the deficit is not
			gotTotal := 0
			for _, n := range got {
				gotTotal += n
			}
			if gotTotal != total {
				t.Fatalf("assigned in short rules = %v, want %v", got, tt.wantShort)
			}
		})
	}
}

func TestApplyExamInput(t *testing.T) {
	ptr := func(s string) *string { return &s }
	var in modelExam.ExamInput
	_ = json.Unmarshal([]byte(`{"title":"  PTS Ganjil ","mode":"random_blueprint","duration_minutes":90,
		"passing_score":75.555,"settings":{"calculator":true},"security":{"profile":"standard"},
		"instructions":"<p>Kerjakan <b>sendiri</b></p><script>x</script>"}`), &in)
	e := modelExam.ExamEntity{}
	v := errr.Validation{}
	applyExamInput(&e, in, v)
	if v.Err() != nil {
		t.Fatal(v.Err())
	}
	if e.Title != "PTS Ganjil" || e.Mode != modelExam.ModeRandomBlueprint || *e.DurationMinutes != 90 || *e.PassingScore != 75.56 {
		t.Fatalf("exam = %+v", e)
	}
	if string(e.Instructions) != `{"body":"<p>Kerjakan sendiri</p>"}` || string(e.Settings) != `{"calculator":true}` {
		t.Fatalf("instructions=%s settings=%s", e.Instructions, e.Settings)
	}

	// null clears, bad values fail
	_ = json.Unmarshal([]byte(`{"duration_minutes":null,"passing_score":null}`), &in)
	applyExamInput(&e, in, errr.Validation{})
	if e.DurationMinutes != nil || e.PassingScore != nil {
		t.Fatal("null must clear duration and passing score")
	}
	bad := modelExam.ExamInput{Title: ptr(""), Settings: json.RawMessage(`[1]`), MaxAttempts: new(int)}
	v = errr.Validation{}
	applyExamInput(&e, bad, v)
	for _, f := range []string{"title", "settings", "max_attempts"} {
		if v[f] == "" {
			t.Errorf("want error on %s, got %v", f, v)
		}
	}
}

// fakeStore implements only what the tests touch; other methods panic via the nil interface.
type fakeStore struct {
	ExamStorer
	exam     modelExam.ExamEntity
	sections []modelExam.SectionEntity
	items    []SectionItemRow
	rules    []modelExam.RuleEntity
	pool     map[string][]Candidate // rule → candidates
	sessions int64
	pinned   bool
	locked   bool
	inserted []modelExam.RulePoolEntity
	cleared  bool
	audits   []string
}

func (f *fakeStore) FindExam(_ context.Context, id string, _ bool) (modelExam.ExamEntity, error) {
	if id != f.exam.ID {
		return modelExam.ExamEntity{}, errExamNotFound
	}
	return f.exam, nil
}
func (f *fakeStore) UpdateExam(_ context.Context, e *modelExam.ExamEntity) error {
	f.exam = *e
	return nil
}
func (f *fakeStore) ListSections(context.Context, string) ([]modelExam.SectionEntity, error) {
	return f.sections, nil
}
func (f *fakeStore) ListSectionItems(context.Context, string) ([]SectionItemRow, error) {
	return f.items, nil
}
func (f *fakeStore) ListRules(context.Context, string) ([]modelExam.RuleEntity, error) {
	return f.rules, nil
}
func (f *fakeStore) RuleCandidates(_ context.Context, r modelExam.RuleEntity, exclude []string) ([]Candidate, error) {
	var out []Candidate
	for _, c := range f.pool[r.ID] {
		if !slices.Contains(exclude, c.ItemID) {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeStore) PoolSize(_ context.Context, ruleID string) (int64, error) {
	var n int64
	for _, p := range f.inserted {
		if p.RuleID == ruleID {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) PinSectionItems(context.Context, string) error { f.pinned = true; return nil }
func (f *fakeStore) InsertPool(_ context.Context, rows []modelExam.RulePoolEntity) error {
	f.inserted = append(f.inserted, rows...)
	return nil
}
func (f *fakeStore) LockVersions(context.Context, string, time.Time) error {
	f.locked = true
	return nil
}
func (f *fakeStore) ClearPins(context.Context, string) error { f.cleared = true; return nil }
func (f *fakeStore) CountLiveSessions(context.Context, string) (int64, error) {
	return f.sessions, nil
}
func (f *fakeStore) CreateAuditLog(_ context.Context, l *modelAudit.AuditLogEntity) error {
	f.audits = append(f.audits, l.Action)
	return nil
}

type fakeTx struct{}

func (fakeTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

const (
	tenantID = "0192a000-0000-7000-8000-000000000001"
	examID   = "0192a000-0000-7000-8000-0000000000e1"
	guruID   = "0192a000-0000-7000-8000-0000000000a1"
)

var (
	guruP    = modelAuth.Principal{TenantID: tenantID, UserID: guruID, Permissions: []string{modelAuth.PermExamRead, modelAuth.PermExamWrite, modelAuth.PermExamPublish}}
	otherP   = modelAuth.Principal{TenantID: tenantID, UserID: "someone-else", Permissions: guruP.Permissions}
	authorP  = modelAuth.Principal{TenantID: tenantID, UserID: guruID, Permissions: []string{modelAuth.PermExamRead, modelAuth.PermExamWrite}}
	proctorP = modelAuth.Principal{TenantID: tenantID, UserID: "p", Permissions: []string{modelAuth.PermExamRead}}
)

// readyStore: a blueprint exam with one fixed and one random section that can be published.
func readyStore() *fakeStore {
	dur := 60
	return &fakeStore{
		exam: modelExam.ExamEntity{ID: examID, Title: "PTS", Mode: modelExam.ModeRandomBlueprint, Status: modelExam.StatusDraft,
			DurationMinutes: &dur, CreatedBy: ptrS(guruID)},
		sections: []modelExam.SectionEntity{
			{ID: "s1", Title: "Wajib", Selection: modelExam.SelectionFixed},
			{ID: "s2", Title: "Acak", Selection: modelExam.SelectionRandom},
		},
		items: []SectionItemRow{
			{SectionItemEntity: modelExam.SectionItemEntity{ID: "si1", SectionID: "s1", ItemID: "a"}, ItemStatus: "active", DefaultPoints: 2},
		},
		rules: []modelExam.RuleEntity{{ID: "r1", SectionID: "s2", ItemCount: 2}},
		pool:  map[string][]Candidate{"r1": cands("a", "b", "c")}, // "a" is hand-picked → excluded
	}
}

func ptrS(s string) *string { return &s }

func newService(store *fakeStore) *ExamService {
	return NewExamService(store, fakeTx{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPublish(t *testing.T) {
	store := readyStore()
	dto, err := newService(store).Publish(context.Background(), guruP, examID, modelAudit.Client{})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if dto.Status != modelExam.StatusPublished || dto.PublishedAt == nil || !store.pinned || !store.locked {
		t.Fatalf("status=%s pinned=%v locked=%v", dto.Status, store.pinned, store.locked)
	}
	if len(store.inserted) != 2 || slices.ContainsFunc(store.inserted, func(p modelExam.RulePoolEntity) bool { return p.ItemVersionID == "v-a" }) {
		t.Fatalf("pool must hold b and c, not the hand-picked a: %+v", store.inserted)
	}
	if dto.ItemCount != 3 || dto.Sections[1].Rules[0].Available != 2 {
		t.Fatalf("item_count=%d available=%d", dto.ItemCount, dto.Sections[1].Rules[0].Available)
	}
	if dto.TotalPoints != nil {
		t.Fatal("total points unknown while a rule uses item defaults")
	}
	if !slices.Contains(store.audits, modelAudit.ActionExamPublish) {
		t.Fatalf("audits = %v", store.audits)
	}
}

func TestPublishRefusesWithProblems(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeStore)
		want   string
	}{
		{"no sections", func(f *fakeStore) { f.sections, f.items, f.rules = nil, nil, nil }, "no sections"},
		{"draft item", func(f *fakeStore) { f.items[0].ItemStatus = "draft" }, "only active items"},
		{"no duration", func(f *fakeStore) { f.exam.DurationMinutes = nil }, "needs a duration"},
		{"empty random section", func(f *fakeStore) { f.rules = nil }, "has no rules"},
		{"pool too small", func(f *fakeStore) { f.rules[0].ItemCount = 3 }, "rule needs 3 items but only 2"},
		{"linear with random section", func(f *fakeStore) { f.exam.Mode = modelExam.ModeLinear }, "is random but the exam is linear"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := readyStore()
			tt.mutate(store)
			svc := newService(store)
			check, err := svc.Check(context.Background(), proctorP, examID)
			if err != nil || check.Ready || !slices.ContainsFunc(check.Problems, func(p modelExam.Problem) bool { return strings.Contains(p.Message, tt.want) }) {
				t.Fatalf("check = %+v, %v; want problem %q", check, err, tt.want)
			}
			_, err = svc.Publish(context.Background(), guruP, examID, modelAudit.Client{})
			if !errors.Is(err, errr.ErrInvalidInput) || store.pinned || store.exam.Status != modelExam.StatusDraft {
				t.Fatalf("publish err = %v pinned=%v status=%s", err, store.pinned, store.exam.Status)
			}
		})
	}
}

func TestPublishPermissions(t *testing.T) {
	if _, err := newService(readyStore()).Publish(context.Background(), authorP, examID, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("without exam.publish: err = %v", err)
	}
	if _, err := newService(readyStore()).Publish(context.Background(), otherP, examID, modelAudit.Client{}); !errors.Is(err, errr.ErrForbidden) {
		t.Fatalf("not the creator: err = %v", err)
	}
	admin := otherP
	admin.Permissions = append(slices.Clone(admin.Permissions), modelAuth.PermTenantManage)
	if _, err := newService(readyStore()).Publish(context.Background(), admin, examID, modelAudit.Client{}); err != nil {
		t.Fatalf("tenant.manage may publish any exam: %v", err)
	}
}

func TestPublishedExamIsFrozen(t *testing.T) {
	store := readyStore()
	svc := newService(store)
	if _, err := svc.Publish(context.Background(), guruP, examID, modelAudit.Client{}); err != nil {
		t.Fatal(err)
	}
	title := "PTS Ganjil 2026"
	if _, err := svc.Update(context.Background(), guruP, examID, modelExam.ExamInput{Title: &title}, modelAudit.Client{}); err != nil {
		t.Fatalf("title must stay editable: %v", err)
	}
	mode := modelExam.ModeLinear
	if _, err := svc.Update(context.Background(), guruP, examID, modelExam.ExamInput{Mode: &mode}, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("mode change on published: err = %v, want ErrConflict", err)
	}
	if _, err := svc.structure(context.Background(), guruP, examID, "test", modelAudit.Client{},
		func(context.Context, modelExam.ExamEntity) error { return nil }); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("structure change on published: err = %v, want ErrConflict", err)
	}
	if err := svc.Delete(context.Background(), guruP, examID, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("delete published: err = %v, want ErrConflict", err)
	}

	store.sessions = 1
	if _, err := svc.Unpublish(context.Background(), guruP, examID, modelAudit.Client{}); !errors.Is(err, errr.ErrConflict) {
		t.Fatalf("unpublish with sessions: err = %v, want ErrConflict", err)
	}
	store.sessions = 0
	dto, err := svc.Unpublish(context.Background(), guruP, examID, modelAudit.Client{})
	if err != nil || dto.Status != modelExam.StatusDraft || !store.cleared {
		t.Fatalf("unpublish = %v, %v, cleared=%v", dto.Status, err, store.cleared)
	}
}

func TestSamePermutation(t *testing.T) {
	have := []string{"a", "b", "c"}
	if samePermutation([]string{"c", "a", "b"}, have) != nil {
		t.Fatal("valid permutation rejected")
	}
	for _, bad := range [][]string{{"a", "b"}, {"a", "a", "b"}, {"a", "b", "x"}} {
		if samePermutation(bad, have) == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}
