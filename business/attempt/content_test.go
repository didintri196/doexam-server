package attempt

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	modelAttempt "doexam/models/attempt"
	modelExam "doexam/models/exam"
	"doexam/pkg/bytecache"
)

// contentStore fakes the content queries; other storer methods panic.
type contentStore struct {
	AttemptStorer
	mu        sync.Mutex
	items     []modelAttempt.AttemptItemEntity
	versions  map[string]ItemContent
	examIDs   []string
	queried   [][]string // ids asked of ItemContents, per call
	planReads int        // exam structure reads
}

func (f *contentStore) ListAttemptItems(context.Context, string) ([]modelAttempt.AttemptItemEntity, error) {
	return f.items, nil
}

func (f *contentStore) ItemContents(_ context.Context, ids []string) ([]ItemContent, error) {
	f.mu.Lock()
	f.queried = append(f.queried, slices.Clone(ids))
	f.mu.Unlock()
	out := []ItemContent{}
	for _, id := range ids {
		if c, ok := f.versions[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *contentStore) ExamVersionIDs(context.Context, string) ([]string, error) {
	return f.examIDs, nil
}

// the exam structure, for warm-ups (counted in planReads)
func (f *contentStore) FindExam(context.Context, string) (modelExam.ExamEntity, error) {
	at := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	return modelExam.ExamEntity{ID: "e1", Status: modelExam.StatusPublished, PublishedAt: &at}, nil
}

func (f *contentStore) ListSections(context.Context, string) ([]modelExam.SectionEntity, error) {
	f.mu.Lock()
	f.planReads++
	f.mu.Unlock()
	return []modelExam.SectionEntity{{ID: "s1", SortOrder: 1, Selection: modelExam.SelectionFixed}}, nil
}

func (f *contentStore) Candidates(context.Context, string) ([]CandidateRow, error) {
	return []CandidateRow{{SectionID: "s1", ItemID: "i1", VersionID: "v1", Type: "mcq", Content: []byte(`{}`)}}, nil
}

func (f *contentStore) ListRules(context.Context, string) ([]modelExam.RuleEntity, error) {
	return nil, nil
}

func (f *contentStore) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queried)
}

// brokenCache fails every call, like a Redis that went down.
type brokenCache struct{}

func (brokenCache) GetMulti(context.Context, []string) (map[string][]byte, error) {
	return nil, errors.New("connection refused")
}
func (brokenCache) SetMulti(context.Context, map[string][]byte) error {
	return errors.New("connection refused")
}

type passTx struct{}

func (passTx) WithTenant(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func version(id, html string) ItemContent {
	return ItemContent{VersionID: id, Type: "mcq", Content: []byte(`{"html":"` + html + `"}`), AnswerKey: []byte(`{"option":"a"}`)}
}

func newContentStore() *contentStore {
	return &contentStore{
		items: []modelAttempt.AttemptItemEntity{
			{ID: "ai1", Position: 1, ItemVersionID: "v2"},
			{ID: "ai2", Position: 2, ItemVersionID: "v1"},
		},
		versions: map[string]ItemContent{"v1": version("v1", "one"), "v2": version("v2", "two"), "v3": version("v3", "three")},
		examIDs:  []string{"v1", "v2", "v3"},
	}
}

func contentService(store AttemptStorer, cache ContentCache) *AttemptService {
	return NewAttemptService(store, passTx{}, nil, nil, cache, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestItemsReadCacheAfterFirstLoad(t *testing.T) {
	store := newContentStore()
	cache := bytecache.NewMemory(1 << 20)
	svc := contentService(store, cache)
	a := modelAttempt.AttemptEntity{ID: "a1", TenantID: "t1"}

	for range 3 {
		items, err := svc.items(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		// position order of attempt_items, each joined with its own version
		if len(items) != 2 || items[0].ID != "ai1" || string(items[0].Content) != `{"html":"two"}` ||
			items[1].ID != "ai2" || string(items[1].AnswerKey) != `{"option":"a"}` || items[1].Type != "mcq" {
			t.Fatalf("items = %+v", items)
		}
	}
	if store.calls() != 1 {
		t.Fatalf("database read %d times, want 1 (then the cache)", store.calls())
	}
}

func TestItemsKeyedByTenant(t *testing.T) {
	store := newContentStore()
	svc := contentService(store, bytecache.NewMemory(1<<20))
	if _, err := svc.items(context.Background(), modelAttempt.AttemptEntity{ID: "a1", TenantID: "t1"}); err != nil {
		t.Fatal(err)
	}
	// another tenant never reads the first tenant's entries
	if _, err := svc.items(context.Background(), modelAttempt.AttemptEntity{ID: "a2", TenantID: "t2"}); err != nil {
		t.Fatal(err)
	}
	if store.calls() != 2 {
		t.Fatalf("database read %d times, want 2 (one per tenant)", store.calls())
	}
}

func TestItemsSurviveBrokenCache(t *testing.T) {
	store := newContentStore()
	svc := contentService(store, brokenCache{})
	items, err := svc.items(context.Background(), modelAttempt.AttemptEntity{ID: "a1", TenantID: "t1"})
	if err != nil {
		t.Fatalf("a failing cache must fall back to the database: %v", err)
	}
	if len(items) != 2 || string(items[0].Content) != `{"html":"two"}` {
		t.Fatalf("items = %+v", items)
	}
}

func TestItemsWithoutCache(t *testing.T) {
	store := newContentStore()
	svc := contentService(store, nil)
	for range 2 {
		if _, err := svc.items(context.Background(), modelAttempt.AttemptEntity{ID: "a1", TenantID: "t1"}); err != nil {
			t.Fatal(err)
		}
	}
	if store.calls() != 2 {
		t.Fatalf("database read %d times, want 2", store.calls())
	}
}

func TestItemsMissingVersion(t *testing.T) {
	store := newContentStore()
	delete(store.versions, "v1")
	svc := contentService(store, bytecache.NewMemory(1<<20))
	if _, err := svc.items(context.Background(), modelAttempt.AttemptEntity{ID: "a1", TenantID: "t1"}); err == nil {
		t.Fatal("an attempt item whose version is missing must fail, not be dropped")
	}
}

func TestWarmExamFillsCache(t *testing.T) {
	store := newContentStore()
	svc := contentService(store, bytecache.NewMemory(1<<20))
	svc.WarmExam(context.Background(), "t1", "e1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if store.calls() != 1 || !slices.Equal(store.queried[0], []string{"v1", "v2", "v3"}) {
		t.Fatalf("warm-up queried %v, want the whole exam once", store.queried)
	}
	// starting afterwards reads nothing from the database
	if _, err := svc.items(context.Background(), modelAttempt.AttemptEntity{ID: "a1", TenantID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if store.calls() != 1 {
		t.Fatalf("database read %d times after warm-up, want 1", store.calls())
	}
	// the exam plan was warmed too: starting reads no structure from the database
	exam, _ := store.FindExam(context.Background(), "e1")
	if _, err := svc.plan(context.Background(), "t1", exam); err != nil {
		t.Fatal(err)
	}
	if store.planReads != 1 {
		t.Fatalf("exam structure read %d times, want 1 (by the warm-up)", store.planReads)
	}
}

func TestConcurrentMissesShareOneQuery(t *testing.T) {
	store := &slowStore{contentStore: newContentStore(), release: make(chan struct{})}
	svc := contentService(store, bytecache.NewMemory(1<<20))
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := svc.items(context.Background(), modelAttempt.AttemptEntity{ID: "a1", TenantID: "t1"})
			errs <- err
		})
	}
	time.Sleep(50 * time.Millisecond) // let every request reach the shared query
	close(store.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if store.calls() != 1 {
		t.Fatalf("database read %d times, want 1 shared query", store.calls())
	}
}

// slowStore holds ItemContents until released, so concurrent misses overlap.
type slowStore struct {
	*contentStore
	release chan struct{}
}

func (s *slowStore) ItemContents(ctx context.Context, ids []string) ([]ItemContent, error) {
	<-s.release
	return s.contentStore.ItemContents(ctx, ids)
}
