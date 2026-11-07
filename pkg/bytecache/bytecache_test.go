package bytecache

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryGetSet(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(1 << 20)
	if err := m.SetMulti(ctx, map[string][]byte{"a": []byte("1"), "b": []byte("2")}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetMulti(ctx, []string{"a", "b", "c"})
	if len(got) != 2 || string(got["a"]) != "1" || string(got["b"]) != "2" {
		t.Fatalf("got %v", got)
	}
	// replacing a value keeps one entry
	_ = m.SetMulti(ctx, map[string][]byte{"a": []byte("11")})
	got, _ = m.GetMulti(ctx, []string{"a"})
	if string(got["a"]) != "11" || m.Len() != 2 {
		t.Fatalf("got %v, len %d", got, m.Len())
	}
}

func TestMemoryEvictsLeastRecentlyUsed(t *testing.T) {
	ctx := context.Background()
	val := []byte(strings.Repeat("x", 100))
	m := NewMemory(3 * cost("k1", val)) // room for three entries
	for _, k := range []string{"k1", "k2", "k3"} {
		_ = m.SetMulti(ctx, map[string][]byte{k: val})
	}
	_, _ = m.GetMulti(ctx, []string{"k1"}) // k1 is now the most recent; k2 the oldest
	_ = m.SetMulti(ctx, map[string][]byte{"k4": val})

	got, _ := m.GetMulti(ctx, []string{"k1", "k2", "k3", "k4"})
	if _, ok := got["k2"]; ok {
		t.Fatal("k2 should have been evicted")
	}
	for _, k := range []string{"k1", "k3", "k4"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("%s missing", k)
		}
	}
	if m.size > m.maxBytes {
		t.Fatalf("size %d over bound %d", m.size, m.maxBytes)
	}
}

func TestMemorySkipsOversizedValues(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(200)
	_ = m.SetMulti(ctx, map[string][]byte{"big": make([]byte, 500)})
	if m.Len() != 0 {
		t.Fatal("a value larger than the bound must not be stored")
	}
}
