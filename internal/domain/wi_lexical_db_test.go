package domain

// aihub#360 — the work-item-side acceptance anchor, against a real pgvector
// Postgres. The wi side is the one aihub#367 measured WORST — 0/6 at every N —
// so the anchor here demonstrates both halves of the published contract:
//
//   - excerpt query: the vector page fills with decoys, the parent work item
//     is structurally outside it, and the lexical section retrieves it;
//   - garbage query: the vector page is STILL full (the aihub#276 shape —
//     ranking, not matching), and lexical.total is an explicit 0 — the signal
//     a caller is told to trust.
//
// The provider is the same deterministic fake as recall_lexical_db_test.go
// (anchorEmbedProvider): target's goal+content carries the marker and embeds
// orthogonal to everything else.
//
//	AIHUB_TEST_DB=postgres://... go test ./internal/domain/ \
//	  -run '^TestListWorkItemsLexicalSectionRetrievesWhatTheVectorPathCannot$' -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

func TestListWorkItemsLexicalSectionRetrievesWhatTheVectorPathCannot(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	ctx := context.Background()

	InitEmbeddingProvider(&anchorEmbedProvider{})
	t.Cleanup(func() { InitEmbeddingProvider(&embedding.NoopProvider{}) })

	wiType := "fix_bug"
	// The parent work item: the excerpt the query will carry is a verbatim
	// line of its content; the marker in another line steers its vector away.
	excerpt := "gateway SCAN burns 2.1s per cross-region request"
	targetContent := "observed in production:\n" + excerpt + "\n" + anchorTargetMarker + " profiling attached"
	// The goal deliberately shares no token with the excerpt, so the snippet
	// assertion below pins the CONTENT line as the evidence line.
	target, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: proj, Goal: "anchor parent work item", Scenario: "coding", WIType: &wiType,
		Content: &targetContent, Source: "human", ForceCreate: true, ForceReason: "aihub#360 anchor",
	}, uid, uid, nil, "")
	if aerr != nil {
		t.Fatalf("CreateWorkItem target: %v", aerr)
	}
	for i := 0; i < 3; i++ {
		if _, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project: proj, Goal: fmt.Sprintf("decoy work item %d about unrelated scheduling", i),
			Scenario: "coding", WIType: &wiType, Source: "human",
			ForceCreate: true, ForceReason: "aihub#360 anchor decoy",
		}, uid, uid, nil, ""); aerr != nil {
			t.Fatalf("CreateWorkItem decoy %d: %v", i, aerr)
		}
	}

	// Fixture premise: every row got a vector from the fake provider. Without
	// this the "vector page misses the target" arm could pass for the wrong
	// reason (an unembedded corpus falls through to ILIKE).
	var embedded int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM work_items WHERE project=$1 AND emb_vector IS NOT NULL`, proj).Scan(&embedded); err != nil {
		t.Fatalf("fixture verify: %v", err)
	}
	if embedded != 4 {
		t.Fatalf("fixture: %d work items carry a vector, want 4", embedded)
	}

	// ── The anchor: query = verbatim excerpt of the target's content ──
	q := excerpt
	res, lerr := ListWorkItems(ctx, pool, proj, ListWorkItemsFilter{Query: &q, Limit: 3})
	if lerr != nil {
		t.Fatalf("ListWorkItems: %v", lerr)
	}
	if res.Semantic == nil {
		t.Fatal("no semantic block — the vector path did not serve items[], so this is not measuring the aihub#367 miss")
	}
	for _, it := range res.Items {
		if it.ID == target.ID {
			t.Fatalf("the vector page contains the target — the anchor premise did not hold, fix the fixture")
		}
	}
	lex := res.Lexical
	if lex == nil {
		t.Fatal("lexical section absent on a query= list")
	}
	if lex.Match != LexicalMatchAllTokensSubstring {
		t.Errorf("lexical.match = %q, want %q", lex.Match, LexicalMatchAllTokensSubstring)
	}
	if lex.Total != 1 || len(lex.Items) != 1 {
		t.Fatalf("lexical total/items = %d/%d, want 1/1", lex.Total, len(lex.Items))
	}
	if lex.Items[0].ID != target.ID || lex.Items[0].Slug != target.Slug {
		t.Fatalf("lexical hit = %s/%s, want the parent %s/%s", lex.Items[0].ID, lex.Items[0].Slug, target.ID, target.Slug)
	}
	if !strings.Contains(lex.Items[0].Snippet, "SCAN burns 2.1s") {
		t.Errorf("snippet %q does not show the matched line", lex.Items[0].Snippet)
	}

	// ── The garbage-query contrast: full semantic page, explicit lexical 0 ──
	garbage := "f3a9c1d0b7e28456aa19fd3c"
	res0, lerr := ListWorkItems(ctx, pool, proj, ListWorkItemsFilter{Query: &garbage, Limit: 3})
	if lerr != nil {
		t.Fatalf("ListWorkItems garbage: %v", lerr)
	}
	if res0.Semantic == nil || len(res0.Items) == 0 {
		t.Fatalf("the garbage query was supposed to return a full semantic page (ranking, not matching — aihub#276); got semantic=%v items=%d",
			res0.Semantic, len(res0.Items))
	}
	if res0.Lexical == nil || res0.Lexical.Total != 0 {
		t.Fatalf("garbage-query lexical = %+v, want an explicit total of 0", res0.Lexical)
	}
	raw, jerr := json.Marshal(res0.Lexical)
	if jerr != nil {
		t.Fatalf("marshal: %v", jerr)
	}
	for _, want := range []string{`"total":0`, `"items":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("serialized zero-hit section %s lacks %s", raw, want)
		}
	}
	if strings.Contains(string(raw), "similarity") {
		t.Errorf("lexical section serialization mentions similarity: %s", raw)
	}

	// ── Filters still scope the section: a status filter that excludes the
	// target must empty it, not leak past it. ──
	mustExec(t, pool, `UPDATE work_items SET status='cancelled', closed_at=clock_timestamp() WHERE id='`+target.ID+`'`)
	resF, lerr := ListWorkItems(ctx, pool, proj, ListWorkItemsFilter{Query: &q, Limit: 3, Status: []string{"queued"}})
	if lerr != nil {
		t.Fatalf("ListWorkItems filtered: %v", lerr)
	}
	if resF.Lexical == nil || resF.Lexical.Total != 0 {
		t.Fatalf("status-filtered lexical = %+v, want 0 — the section must AND the caller's filters, not replace them", resF.Lexical)
	}
}
