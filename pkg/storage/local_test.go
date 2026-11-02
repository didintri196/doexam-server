package storage

import (
	"context"
	"errors"
	"testing"
)

func TestLocalStore(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, "tenant/imports/a.csv", []byte("x,y")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "tenant/imports/a.csv")
	if err != nil || string(got) != "x,y" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if _, err := s.Get(ctx, "tenant/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: err = %v", err)
	}
	for _, bad := range []string{"", "../etc/passwd", "a/../../b"} {
		if err := s.Put(ctx, bad, nil); err == nil {
			t.Fatalf("Put(%q) must fail", bad)
		}
	}
}
