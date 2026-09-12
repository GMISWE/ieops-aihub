package domain

// aihub#379: the caller-scoped visibility predicate must exist in exactly ONE
// SQL copy — memoryVisibilityScopeSQL in memory_visibility.go — and both SQL
// readers (Recall's text path in memory.go, the vector path in
// memory_vector.go) must obtain it by calling that function.
//
// This is a structural gate, not a behavioural one: the behavioural parity
// with the Go predicate is TestMemoryVisibilityParity_SQLAgreesWithGoPredicate
// (internal/server, DB-gated). This one exists because the parity test drives
// the TEXT path only — a re-inlined, drifted copy in memory_vector.go would
// stay green there. Here it goes red the moment the clause literal reappears
// outside the builder, or a known reader stops calling it.

import (
	"os"
	"strings"
	"testing"
)

const (
	visPrivateClause = `visibility != 'private'`
	visAdminClause   = `visibility != 'admin'`
	builderFile      = "memory_visibility.go"
	builderCall      = "memoryVisibilityScopeSQL("
)

func TestMemoryVisibilitySQLPredicateHasOneCopy(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	privWhere := map[string]int{}
	adminWhere := map[string]int{}
	callers := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(name)
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		src := string(b)
		if n := strings.Count(src, visPrivateClause); n > 0 {
			privWhere[name] = n
		}
		if n := strings.Count(src, visAdminClause); n > 0 {
			adminWhere[name] = n
		}
		if n := strings.Count(src, builderCall); n > 0 {
			callers[name] = n
		}
	}

	for clause, found := range map[string]map[string]int{
		visPrivateClause: privWhere,
		visAdminClause:   adminWhere,
	} {
		total := 0
		for _, n := range found {
			total += n
		}
		if total != 1 || found[builderFile] != 1 {
			t.Errorf("the SQL literal %q must appear exactly once in this package, inside %s "+
				"(memoryVisibilityScopeSQL). Found: %v.\n"+
				"If you are adding a memory reader, call memoryVisibilityScopeSQL instead of "+
				"inlining the clause — aihub#379 exists because an inlined copy drifted.",
				clause, builderFile, found)
		}
	}

	// Every known SQL reader must still call the builder (the definition file
	// itself also matches, which is fine — the assertions below are per-file).
	// memory.go holds two readers (Recall's text path and loadForwardRelations);
	// per-file counting cannot tell them apart, but the literal-count arm above
	// already refuses any re-inlined copy wherever it appears.
	//
	// memory_lexical.go is on the list because it proved the point while this
	// gate was in flight: aihub#360 landed recallLexical with its own inline
	// "mirrors recallText's predicate exactly" copy — the fifth — during the
	// very wi that was collapsing the first four.
	for _, reader := range []string{"memory.go", "memory_vector.go", "memory_unmatched.go", "memory_lexical.go"} {
		if callers[reader] == 0 {
			t.Errorf("%s no longer calls memoryVisibilityScopeSQL — if its memory query dropped "+
				"visibility scoping on purpose, update this gate with the reason; if it grew its "+
				"own WHERE clause, that is the aihub#379 defect returning", reader)
		}
	}
}
