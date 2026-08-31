// Package testname derives short, collision-resistant identifiers from Go test
// names, for tests that need to name a row in the shared test database.
//
// It exists as an ordinary (non-_test.go) package so that test files in
// several packages can share ONE implementation. Before aihub#303 there were
// two byte-identical copies — one in internal/domain, one in internal/server —
// and `go test ./...` runs those two packages in PARALLEL against a single
// database, so a name collision across the copies was a live race rather than a
// theoretical one. Nothing outside a _test.go file imports this package, so it
// never reaches a production binary.
package testname

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	// maxLen is the longest string Sanitize will return. projects.name is
	// CHECK (^[a-z][a-z0-9_-]{0,39}$) — 40 characters — and every caller
	// splices the result after a 2-character prefix ("p_", "u_", …), so 38
	// would already be one too many.
	maxLen = 37
	// hashLen hex digits is 24 bits of the digest. Two names only collide
	// now if they ALSO share the same 30-character prefix: measured over
	// this repo's 1047 test-function names, 103 of them fall into 38 such
	// prefix groups, the largest holding 8 — so on the order of 1e-5
	// aggregate collision probability, against 10 CERTAIN collisions
	// (21 names) under the plain 37-character truncation this replaced.
	hashLen = 6
	// prefixLen is what is left for the readable prefix once the hash and
	// its '_' separator are subtracted, so the long path is exactly maxLen.
	prefixLen = maxLen - hashLen - 1
)

// Sanitize folds a Go test name (typically t.Name()) into an identifier that is
// safe to splice into a SQL literal or identifier and short enough to be used
// as a project name.
//
// Invariants:
//
//   - Output contains only [a-z0-9_]. A-Z is lowercased; every other rune —
//     including the '/' Go inserts for subtests and any non-ASCII rune —
//     becomes a single '_'. Nothing that could terminate a quoted literal or
//     an identifier survives, which is what lets callers build statements by
//     concatenation.
//   - Output is at most maxLen (37) characters, so "p_"+out fits projects.name's
//     40-character CHECK. Callers also use the result as a user id, a work-item
//     id, a memory id and a Postgres schema name; all of those have looser
//     limits than projects.name, so this bound is the binding one.
//   - Length no longer causes collisions, regardless of caller. A name whose
//     folded form is longer than prefixLen is NOT simply truncated: it becomes
//     folded[:30] + "_" + 6 hex digits of the SHA-256 of the WHOLE folded
//     string. Plain truncation is what this replaces — 10 groups of the repo's
//     1047 test names shared a 37-character prefix, and each such group named
//     the same project row. This is collision-RESISTANT, not injective: 24 bits
//     of digest is a probabilistic bound (see hashLen), not a guarantee.
//   - Output is a pure function of the FOLDED name (both on the short path and
//     on the hashed one), and is deterministic across processes: it is the same
//     string on every run against the same database, which is what makes
//     per-test cleanup, not per-run uniqueness, the isolation mechanism.
//   - Folding is deliberately lossy and stays that way: "TestFoo/bar" and
//     "TestFoo_bar" fold to the same string and therefore still collide. Only
//     LENGTH-induced collisions are what the hash suffix removes.
//   - Names of prefixLen (30) folded characters or fewer are returned folded and
//     otherwise unchanged — no hash is appended — so the short-name output is
//     byte-for-byte what the truncating implementations returned.
func Sanitize(name string) string {
	folded := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			folded = append(folded, byte(r))
		case r >= 'A' && r <= 'Z':
			folded = append(folded, byte(r-'A'+'a'))
		default:
			folded = append(folded, '_')
		}
	}
	if len(folded) <= prefixLen {
		return string(folded)
	}
	// Hash the FULL folded string, not the truncated prefix: hashing the
	// prefix would be a pure function of what was kept and could not
	// distinguish two names that differ only in what was dropped.
	sum := sha256.Sum256(folded)
	return string(folded[:prefixLen]) + "_" + hex.EncodeToString(sum[:])[:hashLen]
}
