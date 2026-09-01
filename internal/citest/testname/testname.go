// Package testname derives short, collision-resistant identifiers from Go test
// names, for tests that need to name a row in the shared test database.
//
// It exists as an ordinary (non-_test.go) package so that test files in
// several packages can share ONE implementation. Before aihub#303 there were
// two byte-identical copies, one in internal/domain and one in internal/server.
//
// Be precise about why that was worth fixing, because the obvious reason is not
// the measured one. `go test ./...` does run those two packages in PARALLEL
// against a single database, so a cross-package collision WOULD be a live race
// — but there are no cross-package collisions today under either the old or the
// new scheme, and none of the old scheme's colliding groups contained a test
// that calls this function at all. So this change is preventive: it removes a
// hazard (two copies free to drift, and a truncation that collides by
// construction) that had not yet produced a failure. The margin between "does
// not collide today" and "cannot collide" is what TestSanitize_RepoCollisions
// keeps, and that test — not a number in this comment — is the thing that goes
// red if it stops holding.
//
// Nothing outside a _test.go file imports this package, so it never reaches a
// production binary.
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
	// hashLen hex digits is 24 bits of the digest, so two names collide only
	// if they share BOTH the 30-character prefix and those 24 bits.
	//
	// No census is quoted here on purpose: an earlier revision of this comment
	// stated a test-function count that was already stale by the end of the
	// same pull request. TestSanitize_RepoCollisions measures it instead and
	// prints the figures, so they are re-derivable rather than remembered:
	//
	//	GOWORK=off go test ./internal/citest/testname/ -run RepoCollisions -v
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
