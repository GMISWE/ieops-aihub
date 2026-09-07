package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewID generates a new prefixed ID in the format "<prefix>_<8 base62 chars>".
// Uses crypto/rand with big.Int rejection sampling to eliminate modulo bias.
func NewID(prefix string) string {
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(62))
		if err != nil {
			// crypto/rand failure is fatal: do not fall back to weaker sources.
			panic("crypto/rand unavailable: " + err.Error())
		}
		b[i] = base62Chars[n.Int64()]
	}
	return prefix + "_" + string(b)
}

// NewBase62 generates n base62 characters from cryptographically random bytes.
// A crypto/rand failure is fatal (matches NewID's behavior); falling back to a
// constant character would silently bias the output.
func NewBase62(n int) string {
	out := make([]byte, n)
	for i := range n {
		idx, err := rand.Int(rand.Reader, big.NewInt(62))
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		out[i] = base62Chars[idx.Int64()]
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

// FormatIDOrSlug is deliberately absent (aihub#402).
//
// It returned ("id"|"slug", value) by testing a `wi_` prefix — the single-column
// dispatch this repo no longer uses anywhere. It could not be repaired into the
// `id = $1 OR slug = $1` union because its whole signature is the choice of ONE
// column, and it had zero production callers: the only references were its own
// test and a comment in internal/cli/doctor.go that misattributed GetWorkItem's
// behaviour to it. So it was removed rather than fixed.
//
// Do not reintroduce it. The rule is one SQL predicate, `id = $1 OR slug = $1`,
// scoped to the caller's visible projects where the caller has any; a helper
// that returns a column name cannot express it, and
// TestNoWorkItemPrefixDispatch fails on a new one.
