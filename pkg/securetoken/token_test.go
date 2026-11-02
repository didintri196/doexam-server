package securetoken

import (
	"strings"
	"testing"
)

func TestReadablePassword(t *testing.T) {
	pw, err := ReadablePassword(12)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != 12 {
		t.Fatalf("len = %d, want 12", len(pw))
	}
	for _, r := range pw {
		if !strings.ContainsRune(readableAlphabet, r) {
			t.Fatalf("unexpected char %q in %q", r, pw)
		}
	}
}

func TestGenerateIsRandom(t *testing.T) {
	a, _ := Generate(32)
	b, _ := Generate(32)
	if a == b || len(a) != 43 {
		t.Fatalf("Generate: %q %q", a, b)
	}
}
