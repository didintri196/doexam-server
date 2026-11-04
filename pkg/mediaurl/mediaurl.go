// Package mediaurl signs and verifies media URLs. Browsers load <img>/<audio> without the
// Authorization header, so access is granted by an HMAC over tenant, file and expiry that
// the server hands out only to callers allowed to see the content.
package mediaurl

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// KeyLen is the size of a generated signing key.
const KeyLen = 32

// ErrInvalid means the signature is wrong or has expired.
var ErrInvalid = errors.New("invalid or expired media link")

// NewKey returns a random signing key.
func NewKey() ([]byte, error) {
	k := make([]byte, KeyLen)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("media signing key: %w", err)
	}
	return k, nil
}

// Signer signs links that expire. Expiry is rounded up to Bucket so links handed out
// within one bucket are identical (cache friendly).
type Signer struct {
	key    []byte
	TTL    time.Duration
	Bucket time.Duration
}

// NewSigner returns a Signer; links live at least ttl.
func NewSigner(key []byte, ttl, bucket time.Duration) *Signer {
	return &Signer{key: key, TTL: ttl, Bucket: bucket}
}

// Params are the query parameters of a signed link.
type Params struct {
	Tenant  string
	Expires int64
	Sig     string
}

// Sign returns the parameters for fileID of tenantID, valid from now for at least TTL.
func (s *Signer) Sign(tenantID, fileID string, now time.Time) Params {
	exp := now.Add(s.TTL)
	if s.Bucket > 0 {
		exp = exp.Truncate(s.Bucket).Add(s.Bucket)
	}
	e := exp.Unix()
	return Params{Tenant: tenantID, Expires: e, Sig: s.mac(tenantID, fileID, e)}
}

// Path returns the URL path with query for fileID under prefix (e.g. "/api/v1/media/").
func (s *Signer) Path(prefix, tenantID, fileID string, now time.Time) string {
	p := s.Sign(tenantID, fileID, now)
	return fmt.Sprintf("%s%s?t=%s&e=%d&s=%s", prefix, fileID, p.Tenant, p.Expires, p.Sig)
}

// Verify checks the signature and expiry.
func (s *Signer) Verify(tenantID, fileID, expires, sig string, now time.Time) error {
	e, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || now.Unix() > e {
		return ErrInvalid
	}
	want := s.mac(tenantID, fileID, e)
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return ErrInvalid
	}
	return nil
}

func (s *Signer) mac(tenantID, fileID string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	fmt.Fprintf(m, "%s\n%s\n%d", tenantID, fileID, exp)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

// DecodeKey parses a hex or base64 key from configuration; it must be at least 16 bytes.
func DecodeKey(s string) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil && len(b) >= 16 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) >= 16 {
			return b, nil
		}
	}
	return nil, errors.New("media signing key must be hex or base64 of at least 16 bytes")
}
