package sessiontoken

import (
	"strings"
	"testing"
	"time"
)

func TestCodeRotatesAndVerifies(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	rot := 15 * time.Minute
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC) // start of a rotation window

	c0 := Code(secret, t0, rot)
	if len(c0) != Length || strings.ContainsAny(c0, "0O1IL") {
		t.Fatalf("code %q has wrong length or look-alike characters", c0)
	}
	if Code(secret, t0.Add(14*time.Minute), rot) != c0 {
		t.Fatal("code must be stable within a rotation")
	}
	c1 := Code(secret, t0.Add(15*time.Minute), rot)
	if c1 == c0 {
		t.Fatal("code must change after a rotation")
	}
	if !ExpiresAt(t0.Add(3*time.Minute), rot).Equal(t0.Add(rot)) {
		t.Fatalf("expires = %v", ExpiresAt(t0, rot))
	}

	tests := []struct {
		name string
		code string
		at   time.Time
		want bool
	}{
		{"current", c0, t0.Add(time.Minute), true},
		{"lower case and spaces", " " + strings.ToLower(c0) + " ", t0, true},
		{"previous rotation still accepted", c0, t0.Add(16 * time.Minute), true},
		{"two rotations old rejected", c0, t0.Add(31 * time.Minute), false},
		{"future code rejected", c1, t0, false},
		{"wrong length", c0[:5], t0, false},
		{"other secret", Code([]byte("other"), t0, rot), t0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify(secret, tt.code, tt.at, rot); got != tt.want {
				t.Fatalf("Verify = %v, want %v", got, tt.want)
			}
		})
	}
}
