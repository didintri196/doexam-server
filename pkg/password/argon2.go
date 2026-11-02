// Package password hashes and verifies passwords with Argon2id (PHC string format).
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrMalformedHash is returned when a stored hash cannot be parsed.
var ErrMalformedHash = errors.New("malformed password hash")

// Params are Argon2id cost parameters.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultParams follow the OWASP minimum for Argon2id (19 MiB, t=2, p=1).
// Memory is kept modest on purpose: hundreds of participants log in at the
// same moment when an exam starts.
var DefaultParams = Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}

// Hasher hashes passwords while bounding how many hashes run at once,
// so a login burst cannot exhaust memory.
type Hasher struct {
	params Params
	sem    chan struct{}
	dummy  string
}

// NewHasher returns a Hasher. maxConcurrent <= 0 means runtime.NumCPU().
func NewHasher(params Params, maxConcurrent int) *Hasher {
	if maxConcurrent <= 0 {
		maxConcurrent = runtime.NumCPU()
	}
	h := &Hasher{params: params, sem: make(chan struct{}, maxConcurrent)}
	h.dummy = h.hash([]byte("doexam-dummy-password"), make([]byte, params.SaltLen))
	return h
}

// Hash returns the PHC-encoded Argon2id hash of pw.
func (h *Hasher) Hash(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return h.hash([]byte(pw), salt), nil
}

// Verify reports whether pw matches encoded. Parameters are read from encoded,
// so hashes made with older parameters keep verifying.
func (h *Hasher) Verify(ctx context.Context, encoded, pw string) (bool, error) {
	p, salt, key, err := decode(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(pw), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// VerifyDummy spends the same time as a real Verify. Call it when the user does
// not exist so response time does not reveal which usernames are valid.
func (h *Hasher) VerifyDummy(ctx context.Context, pw string) {
	_, _ = h.Verify(ctx, h.dummy, pw) // result is intentionally ignored
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

func (h *Hasher) hash(pw, salt []byte) string {
	p := h.params
	key := argon2.IDKey(pw, salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}
