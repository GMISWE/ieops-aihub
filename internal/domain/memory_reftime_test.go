package domain

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A memory that was never activated has no last_activated_at; its reference
// time is simply when it was created.
func TestMemoryRefTime_NilActivationFallsBackToCreated(t *testing.T) {
	created := time.Now().Add(-2 * time.Hour)
	if got := memoryRefTime(nil, created); !got.Equal(created) {
		t.Fatalf("nil activation: got %v, want %v", got, created)
	}
}

// A memory activated after it was created is as fresh as its activation.
func TestMemoryRefTime_PrefersLaterActivation(t *testing.T) {
	created := time.Now().Add(-48 * time.Hour)
	act := time.Now().Add(-1 * time.Hour)
	if got := memoryRefTime(&act, created); !got.Equal(act) {
		t.Fatalf("later activation: got %v, want %v", got, act)
	}
}

// The case aihub#236 turns on: UpdateMemory carries a STALE last_activated_at
// onto a brand-new head. Reference time must be the new created_at, or the
// fresh head is treated as heavily decayed.
func TestMemoryRefTime_StaleActivationLosesToFreshCreated(t *testing.T) {
	act := time.Now().Add(-200 * 24 * time.Hour)
	created := time.Now()
	if got := memoryRefTime(&act, created); !got.Equal(created) {
		t.Fatalf("stale activation: got %v, want %v", got, created)
	}
}

// Behavioural consequence of the above for the decay curve: a fact.* memory
// (stability 180d) freshly re-created while carrying a 200-day-old activation
// must still read at ~full strength. Under the old "activation wins if set"
// rule this returned 3*exp(-200/180) ~= 0.99 and could fall below the default
// min_strength of 0.3 after further decay.
func TestMemoryStrength_StaleActivationDoesNotDecayFreshHead(t *testing.T) {
	act := time.Now().Add(-200 * 24 * time.Hour)
	got := MemoryStrength(3, 180, &act, time.Now())
	if got < 2.99 {
		t.Fatalf("fresh head carrying stale activation: got %v, want ~3.0", got)
	}
}

// Guard the existing contract: stability_days <= 0 yields 0, not NaN/Inf.
func TestMemoryStrength_ZeroStabilityIsZero(t *testing.T) {
	if got := MemoryStrength(3, 0, nil, time.Now()); got != 0 {
		t.Fatalf("zero stability: got %v, want 0", got)
	}
}

// memRefTimeSQL must stay the total GREATEST expression. A COALESCE rewrite
// still compiles and still returns a timestamp, so nothing else in the build
// would catch the swap — but it silently restores aihub#236.
func TestMemRefTimeSQL_IsTotalGreatestExpression(t *testing.T) {
	if memRefTimeSQL != `GREATEST(last_activated_at, created_at)` {
		t.Fatalf("memRefTimeSQL = %q; the Go mirror memoryRefTime takes the later of the "+
			"two, so the SQL must be GREATEST, not a fallback", memRefTimeSQL)
	}
}

// The DB-backed ordering assertions in memory_ranking_test.go only run when
// AIHUB_TEST_DB is set, which is neither the default locally nor in CI. This
// test needs no database: it parses every non-test .go file in the package and
// rejects the SQL spellings that caused aihub#236, so a regression fails the
// ordinary `go test ./...` rather than waiting for someone to provision Postgres.
//
// Scope is the WHOLE PACKAGE deliberately. The first version of this test parsed
// only memory.go, and that is exactly why two live COALESCE sites shipped past
// it — gc.go's archive sweep and memory_vector.go's three expressions. Reference
// time is a package-wide invariant, so the guard must be package-wide too.
//
// Matching is done on a lowercased, whitespace-collapsed form so `nulls  last`,
// a line break mid-expression, or `coalesce(m.last_activated_at` cannot slip by.
// It still cannot see through a query assembled from several literals — that is
// a known limit, not a claim of completeness.
//
// Only string literals are inspected — the AST carries no comments, so the
// "Do NOT rewrite this as COALESCE(...)" warning above memRefTimeSQL cannot
// trip it.
func TestRecallSQL_HasNoTierOrderingOrStaleReferenceTime(t *testing.T) {
	forbidden := []struct {
		re  *regexp.Regexp
		why string
	}{
		{regexp.MustCompile(`coalesce\(\s*(?:\w+\.)?last_activated_at`),
			"prefers a stale activation timestamp over a fresher created_at (aihub#236); " +
				"use memRefTimeSQL instead"},
		{regexp.MustCompile(`nulls\s+(?:last|first)`),
			"makes never-activated a sorting tier instead of a value (aihub#236); " +
				"order by memRefTimeSQL, which is total and needs no NULL ordering"},
		// Ordering by the bare column re-creates the tier even with no explicit
		// NULLS clause: PostgreSQL defaults DESC to NULLS FIRST, which inverts
		// the original bug rather than removing it.
		{regexp.MustCompile(`order\s+by\s+(?:\w+\.)?last_activated_at`),
			"ordering by the bare column re-tiers rows — PostgreSQL defaults DESC to " +
				"NULLS FIRST (aihub#236); order by memRefTimeSQL instead"},
	}

	// Normalise: unquote-ish (strip backticks/quotes is unnecessary since we
	// only substring-match), lowercase, collapse all runs of whitespace to one
	// space so newlines and indentation inside raw string literals do not hide
	// a match.
	normalise := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(s)), " ")
	}

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	checked := 0
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue // test files legitimately carry these patterns as data
		}
		checked++
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, src, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", src, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			norm := normalise(lit.Value)
			for _, fb := range forbidden {
				if fb.re.MatchString(norm) {
					t.Errorf("%s: SQL literal matches %q — %s",
						fset.Position(lit.Pos()), fb.re, fb.why)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no non-test .go files were scanned — the guard is not actually running")
	}
	t.Logf("scanned %d non-test source files in package domain", checked)
}

// Activation state is server-derived and MUST NOT be settable by a client.
// handleRemember binds the HTTP body straight into domain.RememberRequest
// (internal/server/routes_memory.go:60) with no intermediate DTO, so an
// exported field with a JSON name would let any project writer pin a memory to
// the top of every recall. Regression guard: this fails if the json:"-" tags
// are ever dropped.
//
// Note this asserts encoding/json behaviour, which is what echo's Bind uses for
// JSON request bodies; it does not exercise echo itself.
func TestRememberRequest_ActivationFieldsAreNotBindable(t *testing.T) {
	body := `{
		"project": "p", "type": "fact.note", "content": "x",
		"activation_count": 9999,
		"last_activated_at": "2030-01-01T00:00:00Z",
		"last_activated_by": "u_attacker",
		"LastActivatedAt": "2030-01-01T00:00:00Z",
		"ActivationCount": 4242
	}`
	var req RememberRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ActivationCount != 0 {
		t.Errorf("ActivationCount settable from body: got %d, want 0", req.ActivationCount)
	}
	if req.LastActivatedAt != nil {
		t.Errorf("LastActivatedAt settable from body: got %v, want nil", req.LastActivatedAt)
	}
	if req.LastActivatedBy != nil {
		t.Errorf("LastActivatedBy settable from body: got %v, want nil", req.LastActivatedBy)
	}
	// Sanity: normal fields still bind, so the test is not vacuous.
	if req.Project != "p" || req.Type != "fact.note" {
		t.Errorf("ordinary fields must still bind: project=%q type=%q", req.Project, req.Type)
	}
}
