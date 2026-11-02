// Package sessiontoken derives the rotating exam-room token from a session secret,
// TOTP-style: code = f(HMAC-SHA256(secret, floor(unix / rotation))). Nothing per
// rotation is stored; the server recomputes the code to verify it.
package sessiontoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// Length is the number of characters in a code.
const Length = 6

// alphabet has no look-alikes (0/O, 1/I/L) so codes can be read aloud and copied from a board.
const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// SecretLen is the size of a generated secret.
const SecretLen = 32

// NewSecret returns a random secret.
func NewSecret() ([]byte, error) {
	b := make([]byte, SecretLen)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("read random: %w", err)
	}
	return b, nil
}

// Code returns the code valid at t.
func Code(secret []byte, t time.Time, rotation time.Duration) string {
	return codeAt(secret, counter(t, rotation))
}

// ExpiresAt returns when the code valid at t stops being the current one.
func ExpiresAt(t time.Time, rotation time.Duration) time.Time {
	step := int64(rotation / time.Second)
	return time.Unix((counter(t, rotation)+1)*step, 0).UTC()
}

// Verify reports whether code matches the current or the previous rotation at t
// (the previous one covers participants typing while the code changes).
// Comparison is case-insensitive and constant-time.
func Verify(secret []byte, code string, t time.Time, rotation time.Duration) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != Length {
		return false
	}
	c := counter(t, rotation)
	ok := 0
	for _, n := range []int64{c, c - 1} {
		ok |= subtle.ConstantTimeCompare([]byte(codeAt(secret, n)), []byte(code))
	}
	return ok == 1
}

func counter(t time.Time, rotation time.Duration) int64 {
	step := int64(rotation / time.Second)
	if step <= 0 {
		step = 1
	}
	return t.Unix() / step
}

func codeAt(secret []byte, n int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(n))
	mac := hmac.New(sha256.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	out := make([]byte, Length)
	// six base-31 digits of the first 64 bits (≈ 29.7 bits); the tiny modulo bias is irrelevant here
	v := binary.BigEndian.Uint64(sum[:8])
	for i := range out {
		out[i] = alphabet[v%uint64(len(alphabet))]
		v /= uint64(len(alphabet))
	}
	return string(out)
}
