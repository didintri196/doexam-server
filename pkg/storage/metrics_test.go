package storage

import (
	"context"
	"errors"
	"testing"

	"doexam/pkg/metrics/metricstest"
)

func TestInstrumented(t *testing.T) {
	r := metricstest.Install(t)
	local, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := Instrument(local)
	ctx := context.Background()
	if err := s.Put(ctx, "t1/media/a.png", []byte("12345")); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Get(ctx, "t1/media/a.png"); err != nil || string(b) != "12345" {
		t.Fatalf("get = %q, %v", b, err)
	}
	if _, err := s.Get(ctx, "t1/media/missing.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := s.Put(ctx, "../escape", []byte("x")); err == nil {
		t.Fatal("expected an invalid key to fail")
	}
	if got := r.Value("doexam.storage.transferred", map[string]string{"op": "put"}); got != 5 {
		t.Errorf("bytes put = %v, want 5", got)
	}
	if got := r.Value("doexam.storage.transferred", map[string]string{"op": "get"}); got != 5 {
		t.Errorf("bytes read = %v, want 5", got)
	}
	if got := r.Value("doexam.storage.errors", nil); got != 1 {
		t.Errorf("errors = %v, want 1 (not found is not an error)", got)
	}
	if got := r.Value("doexam.storage.operation.duration", nil); got != 4 {
		t.Errorf("operations = %v, want 4", got)
	}
}
