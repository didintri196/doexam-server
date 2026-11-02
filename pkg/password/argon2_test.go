package password

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// cheap parameters keep the test fast; production uses DefaultParams.
var testParams = Params{MemoryKiB: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(testParams, 2)

	encoded, err := h.Hash(ctx, "rahasia123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", encoded)
	}

	other, _ := h.Hash(ctx, "rahasia123")
	if other == encoded {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}

	tests := []struct {
		name string
		pw   string
		want bool
	}{
		{name: "correct", pw: "rahasia123", want: true},
		{name: "wrong", pw: "rahasia124", want: false},
		{name: "empty", pw: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := h.Verify(ctx, encoded, tt.pw)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if ok != tt.want {
				t.Fatalf("verify = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestVerifyUsesStoredParams(t *testing.T) {
	ctx := context.Background()
	old := NewHasher(testParams, 1)
	encoded, _ := old.Hash(ctx, "pw-lama")

	stronger := testParams
	stronger.Iterations = 3
	ok, err := NewHasher(stronger, 1).Verify(ctx, encoded, "pw-lama")
	if err != nil || !ok {
		t.Fatalf("verify with new params = %v, %v; want true, nil", ok, err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$a2V5", "$argon2id$v=19$m=x$c2FsdA$a2V5"} {
		if _, err := NewHasher(testParams, 1).Verify(context.Background(), bad, "x"); !errors.Is(err, ErrMalformedHash) {
			t.Fatalf("Verify(%q) err = %v, want ErrMalformedHash", bad, err)
		}
	}
}

func TestHashRespectsContext(t *testing.T) {
	h := NewHasher(testParams, 1)
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
