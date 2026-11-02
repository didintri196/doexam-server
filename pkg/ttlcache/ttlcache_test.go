package ttlcache

import (
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := New[string](time.Hour)
	c.now = func() time.Time { return now }

	c.Put("a", "secret")
	if !c.Has("a") {
		t.Fatal("Has after Put")
	}
	if v, ok := c.Take("a"); !ok || v != "secret" {
		t.Fatalf("Take = %q, %v", v, ok)
	}
	if _, ok := c.Take("a"); ok {
		t.Fatal("Take must be one-time")
	}

	c.Put("b", "x")
	now = now.Add(time.Hour)
	if c.Has("b") {
		t.Fatal("entry must expire after ttl")
	}
}
