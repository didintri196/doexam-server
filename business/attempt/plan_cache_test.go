package attempt

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	modelExam "doexam/models/exam"
	"doexam/pkg/bytecache"
)

// planStore fakes the exam structure reads; other storer methods panic.
type planStore struct {
	AttemptStorer
	mu    sync.Mutex
	reads int // Candidates calls (the heavy query)
}

func (f *planStore) ListSections(context.Context, string) ([]modelExam.SectionEntity, error) {
	minutes := 30
	return []modelExam.SectionEntity{
		{ID: "s1", SortOrder: 1, Title: "Fixed", Instructions: []byte(`{"html":"<p>Kerjakan</p>"}`), Selection: modelExam.SelectionFixed, AllowRevisit: true},
		{ID: "s2", SortOrder: 2, Title: "Random", DurationMinutes: &minutes, Selection: modelExam.SelectionRandom},
	}, nil
}

func (f *planStore) Candidates(context.Context, string) ([]CandidateRow, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	pts := 2.5
	return []CandidateRow{
		{SectionID: "s1", ItemID: "i1", VersionID: "v1", Type: "mcq", Content: []byte(`{"options":[{"id":"a"},{"id":"b"},{"id":"c"}]}`), DefaultPoints: 1, Points: &pts, SortOrder: 1},
		{SectionID: "s1", ItemID: "i2", VersionID: "v2", Type: "essay", Content: []byte(`{}`), DefaultPoints: 5, SortOrder: 2},
		{RuleID: "r1", ItemID: "i3", VersionID: "v3", Type: "mcq", Content: []byte(`{"options":[{"id":"x"},{"id":"y"}]}`), DefaultPoints: 1},
		{RuleID: "r1", ItemID: "i4", VersionID: "v4", Type: "mcq", Content: []byte(`{"options":[{"id":"p"},{"id":"q"}]}`), DefaultPoints: 1},
		{RuleID: "r1", ItemID: "i5", VersionID: "v5", Type: "short_text", Content: []byte(`{}`), DefaultPoints: 1},
	}, nil
}

func (f *planStore) ListRules(context.Context, string) ([]modelExam.RuleEntity, error) {
	return []modelExam.RuleEntity{{ID: "r1", SectionID: "s2", ItemCount: 2}}, nil
}

func (f *planStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func publishedExam(at time.Time) modelExam.ExamEntity {
	return modelExam.ExamEntity{ID: "e1", Status: modelExam.StatusPublished, PublishedAt: &at}
}

func TestPlanCachedPerPublication(t *testing.T) {
	store := &planStore{}
	svc := contentService(store, bytecache.NewMemory(1<<20))
	ctx := context.Background()
	at := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)

	for range 3 {
		if _, err := svc.plan(ctx, "t1", publishedExam(at)); err != nil {
			t.Fatal(err)
		}
	}
	if store.count() != 1 {
		t.Fatalf("structure read %d times, want 1", store.count())
	}
	// republished after an unpublish/edit: a new publication is a new plan
	if _, err := svc.plan(ctx, "t1", publishedExam(at.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	// another tenant never shares the entry
	if _, err := svc.plan(ctx, "t2", publishedExam(at)); err != nil {
		t.Fatal(err)
	}
	if store.count() != 3 {
		t.Fatalf("structure read %d times, want 3", store.count())
	}
	// an exam that is not published has no version to pin: never cached
	draft := modelExam.ExamEntity{ID: "e1", Status: modelExam.StatusDraft}
	for range 2 {
		if _, err := svc.plan(ctx, "t1", draft); err != nil {
			t.Fatal(err)
		}
	}
	if store.count() != 5 {
		t.Fatalf("structure read %d times, want 5", store.count())
	}
}

func TestCachedPlanDrawsTheSameAttempt(t *testing.T) {
	ctx := context.Background()
	exam := publishedExam(time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC))
	direct, err := contentService(&planStore{}, nil).plan(ctx, "t1", exam)
	if err != nil {
		t.Fatal(err)
	}
	svc := contentService(&planStore{}, bytecache.NewMemory(1<<20))
	if _, err := svc.plan(ctx, "t1", exam); err != nil { // fills the cache
		t.Fatal(err)
	}
	cached, err := svc.plan(ctx, "t1", exam) // decoded from the cache
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(direct.Sections, cached.Sections) {
		t.Fatalf("sections differ after the cache:\n%+v\n%+v", direct.Sections, cached.Sections)
	}
	// same seed → the same drawn items, points and option order
	for seed := range int64(20) {
		a, errA := buildPlan(direct.Specs, seed, true, true)
		b, errB := buildPlan(cached.Specs, seed, true, true)
		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("seed %d: plan differs after the cache:\n%+v\n%+v", seed, a, b)
		}
	}
}

func TestPlanSurvivesBrokenCache(t *testing.T) {
	store := &planStore{}
	svc := contentService(store, brokenCache{})
	pl, err := svc.plan(context.Background(), "t1", publishedExam(time.Now()))
	if err != nil {
		t.Fatalf("a failing cache must fall back to the database: %v", err)
	}
	if len(pl.Sections) != 2 || len(pl.Specs) != 2 || len(pl.Specs[1].Rules[0].Pool) != 3 {
		t.Fatalf("plan = %+v", pl)
	}
}

func TestConcurrentStartsShareOnePlanRead(t *testing.T) {
	store := &slowPlanStore{planStore: &planStore{}, release: make(chan struct{})}
	svc := contentService(store, bytecache.NewMemory(1<<20))
	exam := publishedExam(time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC))
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := svc.plan(context.Background(), "t1", exam)
			errs <- err
		})
	}
	time.Sleep(50 * time.Millisecond) // let every start reach the shared read
	close(store.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if store.count() != 1 {
		t.Fatalf("structure read %d times, want 1 shared read", store.count())
	}
}

// slowPlanStore holds Candidates until released, so concurrent misses overlap.
type slowPlanStore struct {
	*planStore
	release chan struct{}
}

func (s *slowPlanStore) Candidates(ctx context.Context, examID string) ([]CandidateRow, error) {
	<-s.release
	return s.planStore.Candidates(ctx, examID)
}
