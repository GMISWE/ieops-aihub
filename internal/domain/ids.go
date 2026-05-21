package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewID generates a new prefixed ID in the format "<prefix>_<8 base62 chars>".
// The 8 base62 characters are derived from a ULID-like timestamp+random value.
func NewID(prefix string) string {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to time-based generation
		ts := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(ts >> (uint(i) * 8))
		}
	}

	// Encode 8 bytes into 8 base62 characters
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		sb.WriteByte(base62Chars[b[i]%62])
	}

	return prefix + "_" + sb.String()
}

// NewBase62 generates n base62 characters from cryptographically random bytes.
func NewBase62(n int) string {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(62))
		if err != nil {
			out[i] = base62Chars[0]
		} else {
			out[i] = base62Chars[n.Int64()]
		}
	}
	return string(out)
}

// hashSecretInternal returns the sha256 hex of a session secret.
// This is the internal implementation used by domain code.
// The auth package has the same implementation to avoid import cycles.
func hashSecretInternal(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// FormatIDOrSlug checks whether the input looks like an ID or a slug,
// returns the appropriate WHERE clause condition and value.
func FormatIDOrSlug(idOrSlug string) (column string, value string) {
	if strings.HasPrefix(idOrSlug, "wi_") {
		return "id", idOrSlug
	}
	return "slug", idOrSlug
}

// Unused function to satisfy the import at bottom of run_attempts.go.
// This keeps the code cleaner than duplicating the hash.
var _ = fmt.Sprintf
