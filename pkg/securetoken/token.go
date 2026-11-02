// Package securetoken creates opaque random tokens and their storage hashes.
package securetoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
)

// Generate returns a URL-safe random string with n bytes of entropy.
func Generate(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash returns SHA-256 of token. Store only this, never the raw token.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// readableAlphabet leaves out look-alike characters (0/O, 1/l/I) so a temporary
// password can be read aloud or copied from a printed card.
const readableAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

// ReadablePassword returns a random password of n characters from readableAlphabet.
func ReadablePassword(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(readableAlphabet)))
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("read random: %w", err)
		}
		out[i] = readableAlphabet[idx.Int64()]
	}
	return string(out), nil
}
