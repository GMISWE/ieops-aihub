package main

import (
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// The aihub#637 flag contract, pinned at the SQL-construction layer (no DB
// needed): the default selection is active-only, the flag widens it to
// active+archived, and redacted rows are unreachable under either mode.

func TestMemoriesQueryDefaultIsActiveOnly(t *testing.T) {
	sql, args := memoriesQuery("qwen3-embedding", false)

	if !strings.Contains(sql, "status = 'active'") {
		t.Errorf("default query lost its active-only status predicate:\n%s", sql)
	}
	if strings.Contains(sql, "archived") {
		t.Errorf("default query must not reach archived rows (aihub#637: the flag widens, the default does not move):\n%s", sql)
	}
	if len(args) != 1+len(domain.EmbeddablePrefixes) {
		t.Errorf("args = %d, want model + %d type prefixes", len(args), len(domain.EmbeddablePrefixes))
	}
	if args[0] != "qwen3-embedding" {
		t.Errorf("args[0] = %v, want the model (it is $1 in both the emb_model clause and the SQL)", args[0])
	}
}

func TestMemoriesQueryIncludeArchivedWidensStatus(t *testing.T) {
	sql, _ := memoriesQuery("qwen3-embedding", true)

	if !strings.Contains(sql, "status IN ('active', 'archived')") {
		t.Errorf("-include-archived query does not select active+archived:\n%s", sql)
	}
}

// The flag changes the status predicate and NOTHING else: substituting the
// widened predicate back to the narrow one must reproduce the default query
// byte for byte, so the flag cannot silently drift the type-prefix clauses or
// the embedded_len convergence clause.
func TestMemoriesQueryFlagOnlyMovesTheStatusPredicate(t *testing.T) {
	defSQL, defArgs := memoriesQuery("m", false)
	flagSQL, flagArgs := memoriesQuery("m", true)

	if got := strings.Replace(flagSQL, "status IN ('active', 'archived')", "status = 'active'", 1); got != defSQL {
		t.Errorf("flagged query differs from default beyond the status predicate:\nflagged:\n%s\ndefault:\n%s", flagSQL, defSQL)
	}
	if len(defArgs) != len(flagArgs) {
		t.Errorf("arg count differs between modes: %d vs %d", len(defArgs), len(flagArgs))
	}
	for i := range defArgs {
		if defArgs[i] != flagArgs[i] {
			t.Errorf("args[%d] differs between modes: %v vs %v", i, defArgs[i], flagArgs[i])
		}
	}
}

func TestMemoriesQueryNeverSelectsRedacted(t *testing.T) {
	for _, includeArchived := range []bool{false, true} {
		sql, _ := memoriesQuery("m", includeArchived)
		if strings.Contains(sql, "redacted") {
			t.Errorf("includeArchived=%v: redacted rows must stay unreachable (soft delete, no recall path):\n%s", includeArchived, sql)
		}
	}
}

// Both modes must keep the provenance-convergence clause (aihub#504) and the
// embeddable-type restriction; losing either under the flag would make a
// flagged run a different tool, not a wider one.
func TestMemoriesQueryKeepsSelectionClausesInBothModes(t *testing.T) {
	for _, includeArchived := range []bool{false, true} {
		sql, _ := memoriesQuery("m", includeArchived)
		if !strings.Contains(sql, "embedded_len IS NULL") {
			t.Errorf("includeArchived=%v: convergence clause missing:\n%s", includeArchived, sql)
		}
		if !strings.Contains(sql, "type LIKE $2") {
			t.Errorf("includeArchived=%v: embeddable-type restriction missing:\n%s", includeArchived, sql)
		}
	}
}
