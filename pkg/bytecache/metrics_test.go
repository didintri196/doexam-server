package bytecache

import (
	"context"
	"errors"
	"testing"

	"doexam/pkg/metrics/metricstest"
)

type failingStore struct{}

func (failingStore) GetMulti(context.Context, []string) (map[string][]byte, error) {
	return nil, errors.New("down")
}
func (failingStore) SetMulti(context.Context, map[string][]byte) error { return errors.New("down") }

func TestInstrumented(t *testing.T) {
	r := metricstest.Install(t)
	ctx := context.Background()
	c := Instrument(NewMemory(1<<20), "memory")
	if err := c.SetMulti(ctx, map[string][]byte{"item-content:v1:a": []byte("x"), "exam-plan:v1:b": []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetMulti(ctx, []string{"item-content:v1:a", "item-content:v1:z", "exam-plan:v1:b", "nocolon"}); err != nil {
		t.Fatal(err)
	}
	const req = "doexam.cache.requests"
	for _, tt := range []struct {
		cache, result string
		want          float64
	}{
		{"item-content", "hit", 1}, {"item-content", "miss", 1}, {"exam-plan", "hit", 1}, {"other", "miss", 1},
	} {
		if got := r.Value(req, map[string]string{"cache": tt.cache, "result": tt.result, "backend": "memory"}); got != tt.want {
			t.Errorf("%s %s = %v, want %v", tt.cache, tt.result, got, tt.want)
		}
	}
	if got := r.Value("doexam.cache.entries", map[string]string{"backend": "memory"}); got != 2 {
		t.Errorf("entries = %v, want 2", got)
	}

	bad := Instrument(failingStore{}, "redis")
	_, _ = bad.GetMulti(ctx, []string{"item-content:v1:a"})
	_ = bad.SetMulti(ctx, map[string][]byte{"k": nil})
	if got := r.Value("doexam.cache.errors", map[string]string{"backend": "redis"}); got != 2 {
		t.Errorf("errors = %v, want 2", got)
	}
	if got := r.Value(req, map[string]string{"backend": "redis", "result": "miss"}); got != 1 {
		t.Errorf("a failed read counts as a miss: got %v", got)
	}
}
