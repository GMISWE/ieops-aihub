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
	sql, args := memoriesQuery("qwen3-embedding", "doc:m=qwen3-embedding;d=1024;in=16000;s=tei", false)

	if !strings.Contains(sql, "status = 'active'") {
		t.Errorf("default query lost its active-only status predicate:\n%s", sql)
	}
	if strings.Contains(sql, "archived") {
		t.Errorf("default query must not reach archived rows (aihub#637: the flag widens, the default does not move):\n%s", sql)
	}
	if len(args) != 2+len(domain.EmbeddablePrefixes) {
		t.Errorf("args = %d, want model + pipeline + %d type prefixes", len(args), len(domain.EmbeddablePrefixes))
	}
	if args[0] != "qwen3-embedding" {
		t.Errorf("args[0] = %v, want the model (it is $1 in both the emb_model clause and the SQL)", args[0])
	}
	if args[1] != "doc:m=qwen3-embedding;d=1024;in=16000;s=tei" {
		t.Errorf("args[1] = %v, want the pipeline document segment (it is $2 in the emb_pipeline clause)", args[1])
	}
}

func TestMemoriesQueryIncludeArchivedWidensStatus(t *testing.T) {
	sql, _ := memoriesQuery("qwen3-embedding", "doc:m=qwen3-embedding;d=1024;in=16000;s=tei", true)

	if !strings.Contains(sql, "status IN ('active', 'archived')") {
		t.Errorf("-include-archived query does not select active+archived:\n%s", sql)
	}
}

// The flag changes the status predicate and NOTHING else: substituting the
// widened predicate back to the narrow one must reproduce the default query
// byte for byte, so the flag cannot silently drift the type-prefix clauses or
// the embedded_len convergence clause.
func TestMemoriesQueryFlagOnlyMovesTheStatusPredicate(t *testing.T) {
	defSQL, defArgs := memoriesQuery("m", "doc:p", false)
	flagSQL, flagArgs := memoriesQuery("m", "doc:p", true)

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
		sql, _ := memoriesQuery("m", "doc:p", includeArchived)
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
		sql, _ := memoriesQuery("m", "doc:p", includeArchived)
		if !strings.Contains(sql, "embedded_len IS NULL") {
			t.Errorf("includeArchived=%v: convergence clause missing:\n%s", includeArchived, sql)
		}
		if !strings.Contains(sql, "type LIKE $3") {
			t.Errorf("includeArchived=%v: embeddable-type restriction missing:\n%s", includeArchived, sql)
		}
	}
}

// ── aihub#661: a pipeline change must select itself ──────────────────────────
//
// THE criterion of aihub#661, and the one that was paid for in cash: on
// 2026-09-14 converging the corpus after a serving swap required a human to run
// `UPDATE memories SET embedded_len = NULL` over 1671 rows and the same over
// 2525 work_items, because no clause in either selection could name a row from
// a different pipeline. These tests go red if that clause is weakened or
// dropped, on either table.

// The exact expressions are asserted, not just the column name: the whole
// mechanism is `first segment of the stored stamp` vs `first segment of the
// current stamp`, and a predicate that mentions emb_pipeline while comparing
// the wrong part of it would pass a column-name check and select nothing.
const (
	pipelineNullClause  = "emb_pipeline IS NULL"
	pipelineStaleClause = "split_part(emb_pipeline, '|', 1) IS DISTINCT FROM $2"
)

func TestMemoriesQuerySelectsRowsFromAnotherPipeline(t *testing.T) {
	for _, includeArchived := range []bool{false, true} {
		sql, args := memoriesQuery("m", "doc:m=m;d=1024;in=16000;s=tei-1.9.3", includeArchived)

		if !strings.Contains(sql, pipelineStaleClause) {
			t.Errorf("includeArchived=%v: a row from a DIFFERENT pipeline is not selected — this is the manual UPDATE of 2026-09-14 coming back:\n%s",
				includeArchived, sql)
		}
		if !strings.Contains(sql, pipelineNullClause) {
			t.Errorf("includeArchived=%v: a row with NO recorded pipeline identity (written before migration 0041) is not selected:\n%s",
				includeArchived, sql)
		}
		if args[1] != "doc:m=m;d=1024;in=16000;s=tei-1.9.3" {
			t.Errorf("includeArchived=%v: $2 is %v, not the current pipeline document segment", includeArchived, args[1])
		}
	}
}

func TestWorkItemsSelectTakesRowsFromAnotherPipeline(t *testing.T) {
	if !strings.Contains(workItemsSelectSQL, pipelineStaleClause) {
		t.Errorf("work_items from a DIFFERENT pipeline are not selected; the memories pass would converge and this one would not:\n%s", workItemsSelectSQL)
	}
	if !strings.Contains(workItemsSelectSQL, pipelineNullClause) {
		t.Errorf("work_items with no recorded pipeline identity are not selected:\n%s", workItemsSelectSQL)
	}
	// $1/$2 must mean the same thing in both selections: the call site binds
	// (model, pipelineDoc) positionally, and a swap here would compare the model
	// against the stamp and select every row on every run.
	if strings.Contains(workItemsSelectSQL, "$3") {
		t.Errorf("work_items selection binds a third parameter; the call site passes exactly two:\n%s", workItemsSelectSQL)
	}
	if !strings.Contains(workItemsSelectSQL, "emb_model IS DISTINCT FROM $1") ||
		!strings.Contains(workItemsSelectSQL, "split_part(emb_pipeline, '|', 1) IS DISTINCT FROM $2") {
		t.Errorf("work_items selection does not bind $1 to the model and $2 to the pipeline segment:\n%s", workItemsSelectSQL)
	}
}

// Both tables must carry the SAME four convergence clauses. A pipeline change
// that converged one table and not the other would leave half the index in the
// old vector space — the aihub#661 defect at half scale, and harder to notice
// because recall would still return plausible-looking rows.
func TestBothTablesShareEveryConvergenceClause(t *testing.T) {
	memSQL, _ := memoriesQuery("m", "doc:p", false)
	wiSQL := workItemsSelectSQL

	for _, clause := range []string{
		"emb_vector IS NULL",
		"emb_model IS DISTINCT FROM $1",
		"embedded_len IS NULL",
		pipelineNullClause,
		pipelineStaleClause,
	} {
		if !strings.Contains(memSQL, clause) {
			t.Errorf("memories selection lost %q:\n%s", clause, memSQL)
		}
		if !strings.Contains(wiSQL, clause) {
			t.Errorf("work_items selection lost %q:\n%s", clause, wiSQL)
		}
	}
}

// The predicate compares the DOCUMENT segment, never the whole stamp. Comparing
// the whole stamp is the obvious simplification and it is wrong: the second
// segment records the query composition, and Qwen3-Embedding ships
// prompts.document = "" (measured, aihub#660), so editing the instruct prefix
// moves no stored vector. Under a whole-stamp comparison every prefix edit
// would re-embed the entire corpus to write back byte-identical vectors.
func TestBackfillComparesTheDocumentSegmentNotTheWholeStamp(t *testing.T) {
	const model, dims = "Qwen/Qwen3-Embedding-0.6B", 1024

	// Read through currentPipeline, which is what main() calls: asserting
	// against domain.EmbedPipelineDoc directly would prove the two functions
	// differ and say nothing about which one the tool actually passes to the
	// predicate — the only place the choice can go wrong.
	full, doc := currentPipeline(model, dims)

	if doc == full {
		t.Fatalf("the backfill compares the WHOLE stamp (%q): every instruct-prefix edit would re-embed the corpus to write back byte-identical vectors", doc)
	}
	if doc != domain.EmbedPipelineDoc(model, dims) {
		t.Errorf("the compared value %q is not the pipeline document segment %q", doc, domain.EmbedPipelineDoc(model, dims))
	}
	if full != domain.EmbedPipelineID(model, dims) {
		t.Errorf("the written stamp %q is not the full pipeline id %q", full, domain.EmbedPipelineID(model, dims))
	}
	if !strings.HasPrefix(full, doc) {
		t.Fatalf("the document segment %q is not the leading segment of %q, so split_part(...,1) cannot yield it", doc, full)
	}

	_, memArgs := memoriesQuery(model, doc, false)
	if memArgs[1] == full {
		t.Errorf("memories compares the WHOLE stamp: every instruct-prefix edit would re-embed the corpus for byte-identical vectors")
	}
	if memArgs[1] != doc {
		t.Errorf("memories $2 = %v, want the document segment %q", memArgs[1], doc)
	}
}
