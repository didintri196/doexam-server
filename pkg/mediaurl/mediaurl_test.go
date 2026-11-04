package mediaurl

import (
	"strconv"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"), 12*time.Hour, time.Hour)
	now := time.Date(2026, 11, 4, 9, 15, 0, 0, time.UTC)
	p := s.Sign("tenant-a", "file-1", now)

	if got := time.Unix(p.Expires, 0).UTC(); !got.Equal(time.Date(2026, 11, 4, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry = %v, want rounded up to the hour after now+12h", got)
	}
	// links issued within the same hour are identical
	if q := s.Sign("tenant-a", "file-1", now.Add(30*time.Minute)); q != p {
		t.Fatalf("same bucket gave %v and %v", p, q)
	}
	e := strconv.FormatInt(p.Expires, 10)
	if err := s.Verify("tenant-a", "file-1", e, p.Sig, now); err != nil {
		t.Fatalf("valid link rejected: %v", err)
	}
	for name, tc := range map[string][4]string{
		"other file":   {"tenant-a", "file-2", e, p.Sig},
		"other tenant": {"tenant-b", "file-1", e, p.Sig},
		"moved expiry": {"tenant-a", "file-1", strconv.FormatInt(p.Expires+3600, 10), p.Sig},
		"bad expiry":   {"tenant-a", "file-1", "x", p.Sig},
		"bad sig":      {"tenant-a", "file-1", e, "AAAA"},
	} {
		if err := s.Verify(tc[0], tc[1], tc[2], tc[3], now); err != ErrInvalid {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if err := s.Verify("tenant-a", "file-1", e, p.Sig, time.Unix(p.Expires+1, 0)); err != ErrInvalid {
		t.Fatalf("expired link accepted")
	}
	other := NewSigner([]byte("another-key-another-key-another!!"), 12*time.Hour, time.Hour)
	if err := other.Verify("tenant-a", "file-1", e, p.Sig, now); err != ErrInvalid {
		t.Fatal("link must not verify under another key")
	}
}

func TestDecodeKey(t *testing.T) {
	if _, err := DecodeKey("00112233445566778899aabbccddeeff"); err != nil {
		t.Fatalf("hex: %v", err)
	}
	if _, err := DecodeKey("ABEiM0RVZneImaq7zN3u/w=="); err != nil {
		t.Fatalf("base64: %v", err)
	}
	if _, err := DecodeKey("short"); err == nil {
		t.Fatal("short key accepted")
	}
}
