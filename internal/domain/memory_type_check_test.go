package domain

// aihub#445, the pure half (aihub#411 decision table §6.2 T2-6): the CHECK on
// memories.type and the Go check in Remember must name the same set, and this
// test is the only thing that makes that a property rather than an intention.
// It needs no database — it reads the migration as text — so it runs in the
// default `go test ./...` alongside the code it guards.
//
// The direction of a divergence is what it is really about. A CHECK WIDER than
// Go is inert: Go refuses the value first and the column never sees it. A CHECK
// STRICTER than Go is a new defect of exactly the class aihub#433 fixed — Go
// answers 200, the column answers SQLSTATE 23514, and the caller gets a 500
// carrying the driver's constraint text instead of a 400 naming the field. The
// equality asserted below rules out both, which is stronger than the policy
// requires and much easier to state than "no stricter".
//
//	go test ./internal/domain/ -run TestMemoryTypeCheck -v

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const memoryTypeCheckMigration = "../db/migrations/0034_memories_type_check.sql"

// startsWithRE matches starts_with(type, '<prefix>') and captures the prefix.
var startsWithRE = regexp.MustCompile(`starts_with\(\s*type\s*,\s*'([^']*)'\s*\)`)

// migrationUpSQL returns migration 0034's Up section with every comment line
// removed. Stripping comments is load-bearing rather than tidy: that file
// documents the predicate in prose and quotes the SELECT an operator should run
// by hand, so a parse that read comments would find prefixes this migration
// does not enforce and would stay green while the DDL itself drifted.
func migrationUpSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(memoryTypeCheckMigration)
	require.NoError(t, err, "the migration this test is about must exist")

	body := string(raw)
	upAt := strings.Index(body, "-- +goose Up")
	downAt := strings.Index(body, "-- +goose Down")
	require.NotEqual(t, -1, upAt, "missing +goose Up marker")
	require.NotEqual(t, -1, downAt, "missing +goose Down marker")
	require.Greater(t, downAt, upAt, "+goose Down must follow +goose Up")

	var kept []string
	for _, line := range strings.Split(body[upAt+len("-- +goose Up"):downAt], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// addConstraintStmt returns just the ADD CONSTRAINT statement — the predicate
// Postgres actually enforces — separated from the DO block that surveys the
// table with a second copy of it.
func addConstraintStmt(t *testing.T, up string) string {
	t.Helper()
	const head = "ADD CONSTRAINT memories_type_check CHECK ("
	const tail = ") NOT VALID;"
	start := strings.Index(up, head)
	require.NotEqual(t, -1, start,
		"no `%s` in the migration — either the constraint was renamed or it is no longer added NOT VALID, "+
			"and this test can no longer see what is enforced", head)
	end := strings.Index(up[start:], tail)
	require.NotEqual(t, -1, end, "the ADD CONSTRAINT statement does not end in `%s`", tail)
	return up[start : start+end+len(tail)]
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestMemoryTypeCheckMatchesTheGoPrefixes is the anti-drift guard named in
// 0034_memories_type_check.sql and in MemoryTypePrefixes' own comment.
func TestMemoryTypeCheckMatchesTheGoPrefixes(t *testing.T) {
	// Anti-vacuity: an empty Go list would make the comparison satisfiable by a
	// CHECK that names nothing, which is the failure this test exists to catch.
	require.NotEmpty(t, MemoryTypePrefixes, "the enforced vocabulary is empty")

	stmt := addConstraintStmt(t, migrationUpSQL(t))

	var found []string
	for _, m := range startsWithRE.FindAllStringSubmatch(stmt, -1) {
		found = append(found, m[1])
	}
	require.NotEmpty(t, found,
		"no starts_with(type, '…') in the enforced predicate — the parse is broken, or the CHECK "+
			"has been rewritten in a form this test cannot read; either way it is no longer guarding anything")

	require.Equal(t, sortedCopy(MemoryTypePrefixes), sortedCopy(found),
		"memories_type_check and domain.MemoryTypePrefixes name different sets. If the CHECK is the "+
			"narrower one, an off-list type that Go accepts now reaches Postgres and comes back as a 500 "+
			"carrying the constraint text instead of a 400 naming the field (aihub#433's failure mode). "+
			"Fix the migration, or add a new one — never narrow the CHECK to close a gap")

	// The '|' half. Remember rejects any type containing one (aihub#289) because
	// the READ path rejects it too, so such a row could never be recalled by
	// type. Without this clause the CHECK would accept "experience.*|rule.*" —
	// it does start with "experience." — and the column would be strictly wider
	// than Go in the one place the leniency was never meant to reach.
	require.Contains(t, stmt, "strpos(type, '|') = 0",
		"the enforced predicate does not ban '|'; a type containing one starts with a legal prefix, "+
			"passes this CHECK and is refused by every recall that names it")
}

// TestMemoryTypeCheckCensusAgreesWithWhatItEnforces covers the file's SECOND
// copy of the predicate: the DO block counts the rows outside the constraint and
// runs VALIDATE only when it finds none. A census wider than the constraint
// finds too few outliers and makes the migration attempt a VALIDATE that fails
// — a broken deploy; narrower, and it reports rows the constraint would have
// accepted and refuses to validate a table that was clean. Neither is visible
// from the constraint alone, so the two must be compared to each other.
func TestMemoryTypeCheckCensusAgreesWithWhatItEnforces(t *testing.T) {
	up := migrationUpSQL(t)
	stmt := addConstraintStmt(t, up)

	census := strings.Replace(up, stmt, "", 1)
	require.Contains(t, census, "count(*)", "no census query left after removing the ADD CONSTRAINT statement")

	inCensus := map[string]bool{}
	for _, m := range startsWithRE.FindAllStringSubmatch(census, -1) {
		inCensus[m[1]] = true
	}
	var got []string
	for p := range inCensus {
		got = append(got, p)
	}
	require.Equal(t, sortedCopy(MemoryTypePrefixes), sortedCopy(got),
		"the outlier census and the constraint do not test the same prefixes")
	require.Contains(t, census, "strpos(type, '|') > 0",
		"the outlier census does not count piped types, so it would report a table clean that the "+
			"constraint refuses to validate")
}

// TestMemoryTypePrefixGlossNamesEveryPrefix pins the caller-facing rendering.
// The rejection message is the only place a caller who guessed wrong learns the
// vocabulary, and a gloss that dropped a prefix would teach a set narrower than
// the one enforced — the reader would believe methodology.* is illegal
// everywhere, when it is legal for the column and refused only by pf_remember.
func TestMemoryTypePrefixGlossNamesEveryPrefix(t *testing.T) {
	gloss := MemoryTypePrefixGloss()
	for _, p := range MemoryTypePrefixes {
		require.Contains(t, gloss, p+"*", "the gloss omits %q", p)
	}
	// The wording aihub#289's test asserts on, kept verbatim by derivation
	// rather than by a second literal.
	require.True(t, strings.HasPrefix(gloss, "experience.*"),
		"the gloss must still open with experience.* — TestRemember_RejectsPipedType asserts on "+
			"`must be one of experience.*`, and derivation is what keeps that true")
}
