package domain

// aihub#360 — the memory-side acceptance anchor, against a real pgvector
// Postgres: the lexical section retrieves a target the vector section cannot.
//
// The scenario is aihub#367's canonical failure sample, reconstructed exactly
// ("用摘录查母文" — query a stored document with a verbatim excerpt of itself):
// production measured that the query `already_held empty` could not retrieve
// the document whose FIRST LINE is `already_held: []`, because a single
// unchunked vector per row gives an excerpt no lexical foothold. Here the
// embedding provider is a deterministic fake that reproduces that geometry on
// purpose — the excerpt query and the decoy rows share a direction while the
// target document sits orthogonal to both — so the vector page fills with
// decoys and the target is structurally outside it, exactly the measured
// production shape (recall@1 0/42; 11 of 12 misses retrievable by an unrelated
// query, i.e. present and embedded, just not semantically reachable).
//
// Against a tree without the lexical section this test cannot pass: the
// before/after contrast is items[] (miss, full plausible page) vs lexical
// (hit, total=1), in one response.
//
//	AIHUB_TEST_DB=postgres://... go test ./internal/domain/ \
//	  -run '^TestRecallLexicalSectionRetrievesWhatTheVectorPathCannot$' -v -count=1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// anchorEmbedProvider reproduces the aihub#367 miss geometry deterministically:
// any text carrying the target-document marker embeds to e2, everything else
// (the excerpt query, the decoy rows) to e1. Cosine(query, decoy) = 1,
// cosine(query, target) = 0 — the target can never enter a vector page the
// decoys can fill.
type anchorEmbedProvider struct{}

const anchorTargetMarker = "ANCHOR-360-TARGET-BODY"

func (p *anchorEmbedProvider) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(text, anchorTargetMarker) {
		return []float32{0, 1, 0, 0}, nil
	}
	return []float32{1, 0, 0, 0}, nil
}

func (p *anchorEmbedProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, s := range texts {
		v, err := p.Embed(ctx, s)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (p *anchorEmbedProvider) ModelID() string              { return "anchor-embed-360" }
func (p *anchorEmbedProvider) Dims() int                    { return 4 }
func (p *anchorEmbedProvider) Ping(_ context.Context) error { return nil }

func TestRecallLexicalSectionRetrievesWhatTheVectorPathCannot(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	ctx := context.Background()

	InitEmbeddingProvider(&anchorEmbedProvider{})
	t.Cleanup(func() { InitEmbeddingProvider(&embedding.NoopProvider{}) })

	// The parent document: first line is the excerpt the query will carry,
	// body holds the marker that steers its vector away from the query's.
	targetContent := "already_held: []\nlock scan transcript, " + anchorTargetMarker + " — the apply loop saw no held locks"
	target, _, err := Remember(ctx, pool, &RememberRequest{
		Project: proj, Type: "experience.debug", Content: targetContent,
		Visibility: "project", DedupMode: "off", CallerUserID: uid, CallerDisplay: uid,
	})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := Remember(ctx, pool, &RememberRequest{
			Project: proj, Type: "experience.approach",
			Content:    "decoy row " + strings.Repeat("x", i+1) + " about gateway routing",
			Visibility: "project", DedupMode: "off", CallerUserID: uid, CallerDisplay: uid,
		}); err != nil {
			t.Fatalf("seed decoy %d: %v", i, err)
		}
	}
	// A private row by ANOTHER author whose content also matches the excerpt:
	// the lexical section must apply the same visibility scoping as recall
	// itself, so this row may never surface for uid.
	otherUID := uid + "b"
	mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('`+otherUID+`','`+otherUID+`@test.local','`+otherUID+`') ON CONFLICT (id) DO NOTHING`)
	mustExec(t, pool, `INSERT INTO memories(id,project,author_user_id,author_display,type,content,visibility)
		VALUES ('mem_360priv','`+proj+`','`+otherUID+`','`+otherUID+`','experience.debug','private copy: already_held: []','private')`)

	// ── The anchor: query = verbatim excerpt (the document's first line) ──
	excerpt := "already_held: []"
	resp, rerr := Recall(ctx, pool, &RecallRequest{
		Project: proj, Query: excerpt, TopK: 3,
		CallerUserID: uid, CallerRole: "writer",
	})
	if rerr != nil {
		t.Fatalf("Recall: %v", rerr)
	}

	// Vector section: served by the vector path (similarities present), filled
	// by decoys, target ABSENT — the aihub#367 miss, reproduced.
	if len(resp.Items) == 0 {
		t.Fatal("vector section is empty — the fixture was supposed to fill it with decoys")
	}
	for _, it := range resp.Items {
		if it.Similarity == nil {
			t.Fatalf("item %s carries no similarity — the vector path did not serve items[], so this test is not measuring the miss it claims to", it.ID)
		}
		if it.ID == target.ID {
			t.Fatalf("the vector page contains the target — the anchor premise (excerpt cannot retrieve parent semantically) did not hold, fix the fixture")
		}
	}

	// Lexical section: present, and it holds exactly the parent document.
	lex := resp.Lexical
	if lex == nil {
		t.Fatal("lexical section absent on a recall that carried a query")
	}
	if lex.Match != LexicalMatchAllTokensSubstring {
		t.Errorf("lexical.match = %q, want %q", lex.Match, LexicalMatchAllTokensSubstring)
	}
	if lex.Total != 1 || len(lex.Items) != 1 {
		t.Fatalf("lexical total/items = %d/%d, want 1/1 (the private other-author row must be scoped out)", lex.Total, len(lex.Items))
	}
	if lex.Items[0].ID != target.ID {
		t.Fatalf("lexical hit = %s, want the parent document %s", lex.Items[0].ID, target.ID)
	}
	if !strings.Contains(lex.Items[0].Snippet, excerpt) {
		t.Errorf("snippet %q does not show the matched line %q", lex.Items[0].Snippet, excerpt)
	}

	// ── Explicit zero: a query matching nothing still gets the section ──
	resp0, rerr := Recall(ctx, pool, &RecallRequest{
		Project: proj, Query: "zzz_definitely_not_in_any_row_360", TopK: 3,
		CallerUserID: uid, CallerRole: "writer",
	})
	if rerr != nil {
		t.Fatalf("Recall zero-hit: %v", rerr)
	}
	if resp0.Lexical == nil {
		t.Fatal("lexical section absent on a zero-hit query — absence must keep meaning 'no query was sent'")
	}
	if resp0.Lexical.Total != 0 {
		t.Fatalf("zero-hit lexical.total = %d, want 0", resp0.Lexical.Total)
	}
	raw, jerr := json.Marshal(resp0.Lexical)
	if jerr != nil {
		t.Fatalf("marshal: %v", jerr)
	}
	for _, want := range []string{`"total":0`, `"items":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("serialized zero-hit section %s lacks %s — the explicit-presence contract of aihub#360 requirement 1", raw, want)
		}
	}
	if strings.Contains(string(raw), "similarity") {
		t.Errorf("lexical section serialization mentions similarity: %s — no cosine exists here and none may be invented", raw)
	}

	// ── wi-scoped recall: query + work_item_id now has a lexical answer ──
	// (The router deliberately skips the vector path under a work_item_id
	// filter, and the text path ignores the query entirely, so before
	// aihub#360 the query text did nothing at all on this shape.)
	wiType := "fix_bug"
	wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: proj, Goal: "anchor wi for the scoped-recall arm", Scenario: "coding", WIType: &wiType,
		Source: "human", ForceCreate: true, ForceReason: "aihub#360 anchor",
	}, uid, uid, nil, "")
	if aerr != nil {
		t.Fatalf("CreateWorkItem: %v", aerr)
	}
	if _, _, err := Remember(ctx, pool, &RememberRequest{
		Project: proj, Type: "experience.debug", Content: "scoped: already_held: [] inside " + wi.Slug,
		Visibility: "project", DedupMode: "off", WorkItemID: &wi.ID,
		CallerUserID: uid, CallerDisplay: uid,
	}); err != nil {
		t.Fatalf("seed scoped memory: %v", err)
	}
	respWI, rerr := Recall(ctx, pool, &RecallRequest{
		Project: proj, Query: excerpt, TopK: 5, WorkItemID: &wi.ID,
		CallerUserID: uid, CallerRole: "writer",
	})
	if rerr != nil {
		t.Fatalf("Recall scoped: %v", rerr)
	}
	if respWI.Lexical == nil || respWI.Lexical.Total != 1 {
		t.Fatalf("wi-scoped lexical section = %+v, want exactly the one memory attached to %s (the project-wide match must be filtered out)", respWI.Lexical, wi.Slug)
	}
	if respWI.Lexical.Items[0].WorkItemID == nil || *respWI.Lexical.Items[0].WorkItemID != wi.ID {
		t.Errorf("scoped lexical hit carries work_item_id %v, want %s", respWI.Lexical.Items[0].WorkItemID, wi.ID)
	}
}
