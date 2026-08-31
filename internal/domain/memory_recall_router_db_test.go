package domain

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/embedding"
	"github.com/jackc/pgx/v5/pgxpool"
)

// aihub#270 router coverage against a real pgvector Postgres.
//
// The bug these tests exist to prevent: with an embedding provider active, a recall
// carrying a query routed wholly to the vector path, whose WHERE carries
// `emb_vector IS NOT NULL`. methodology.* rows are never embedded, and the text fallback
// fired only when the vector path returned ZERO rows — so any type union that also named
// an embeddable type came back non-empty, the fallback never fired, and every
// methodology.* row the caller asked for vanished silently with a healthy-looking total.
//
// Why these are DB tests and not unit tests: the fix is a routing decision plus a SQL
// complement spliced into a hand-built WHERE with hand-threaded $N placeholders. A
// placeholder that drifts out of step with its args list is a runtime bind error, not a
// compile error, and no amount of pure-function testing reaches it. The helpers' unit
// tests live in memory_recall_hybrid_test.go; these run the actual queries.
//
// Gated on AIHUB_TEST_DB alone — a stub provider stands in for the embedding service, so
// unlike TestRecallWithVector_Live these need no EMBEDDING_BASE_URL and run in CI.

// stubEmbedProvider is a deterministic offline embedding provider. It is not a
// NoopProvider, so isNoopProvider reports false and Recall takes the vector path.
//
// Vectors are derived from a hash of the text and L2-normalized. Directions are
// arbitrary — these tests assert routing and completeness, never ranking quality, which is
// what TestRecallWithVector_Live (against a real model) is for.
type stubEmbedProvider struct{}

const stubEmbedDims = 8

func (s *stubEmbedProvider) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, stubEmbedDims)
	var h uint32 = 2166136261
	for i := 0; i < len(text); i++ {
		h ^= uint32(text[i])
		h *= 16777619
		vec[i%stubEmbedDims] += float32(h%1000) / 1000.0
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		vec[0], norm = 1, 1
	}
	for i := range vec {
		vec[i] = float32(float64(vec[i]) / norm)
	}
	return vec, nil
}

func (s *stubEmbedProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := s.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (s *stubEmbedProvider) ModelID() string              { return "stub-embed-v1" }
func (s *stubEmbedProvider) Dims() int                    { return stubEmbedDims }
func (s *stubEmbedProvider) Ping(_ context.Context) error { return nil }

// recallRouterFixture seeds a project with nEmb embeddable rows (embedded through the stub
// provider by the normal Remember write path) and nMeth methodology.spec rows (inserted
// directly, the way pf_save_artifact writes them — never embedded). It activates the stub
// provider for the duration of the test.
func recallRouterFixture(t *testing.T, nEmb, nMeth int) (*pgxpool.Pool, string, string) {
	t.Helper()
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	ctx := context.Background()

	mustExec(t, pool, fmt.Sprintf(`DELETE FROM memories WHERE project='%s'`, proj))

	InitEmbeddingProvider(&stubEmbedProvider{})
	t.Cleanup(func() { InitEmbeddingProvider(&embedding.NoopProvider{}) })

	for i := 0; i < nEmb; i++ {
		if _, _, err := Remember(ctx, pool, &RememberRequest{
			Project:       proj,
			Type:          "experience.approach",
			Content:       fmt.Sprintf("embeddable recall row %d about vector routing", i),
			Visibility:    "project",
			DedupMode:     "off",
			CallerUserID:  uid,
			CallerDisplay: uid,
		}); err != nil {
			t.Fatalf("seed embeddable row %d: %v", i, err)
		}
	}

	// author_display is set explicitly: the column is nullable in the schema but
	// scanMemoryLite reads it into a non-pointer string, so a row seeded without it fails
	// the scan. Every real write path (Remember, SaveArtifact) always populates it.
	for i := 0; i < nMeth; i++ {
		mustExec(t, pool, fmt.Sprintf(
			`INSERT INTO memories(id,project,author_user_id,author_display,type,content,visibility)
			 VALUES ('mem_%srt%d','%s','%s','%s','methodology.spec','spec doc %d about vector routing','project')`,
			testname.Sanitize(t.Name()), i, proj, uid, uid, i))
	}

	// Guard the fixture's own premise: exactly the embeddable rows carry a vector. If this
	// ever stops holding, every assertion below is testing something else.
	var embedded, methodology int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE emb_vector IS NOT NULL),
		       count(*) FILTER (WHERE type LIKE 'methodology.%')
		FROM memories WHERE project=$1`, proj).Scan(&embedded, &methodology); err != nil {
		t.Fatalf("fixture verify: %v", err)
	}
	if embedded != nEmb {
		t.Fatalf("fixture: %d rows have emb_vector, want %d", embedded, nEmb)
	}
	if methodology != nMeth {
		t.Fatalf("fixture: %d methodology rows, want %d", methodology, nMeth)
	}

	return pool, proj, uid
}

func typeHistogram(items []MemoryWithStrength) map[string]int {
	h := map[string]int{}
	for _, it := range items {
		h[strings.SplitN(it.Type, ".", 2)[0]]++
	}
	return h
}

// TestRecallRouterMixedUnionReturnsBothHalves is the regression test. It reproduces the
// exact type union polyforge 1.1.8's pf-spec/pf-plan Step 1 sends, with enough embeddable
// rows to fill top_k on their own — the precise condition under which the pre-fix code
// returned zero methodology rows. Against pre-fix code this test FAILS.
func TestRecallRouterMixedUnionReturnsBothHalves(t *testing.T) {
	pool, proj, uid := recallRouterFixture(t, 8, 4)

	resp, err := Recall(context.Background(), pool, &RecallRequest{
		Project:      proj,
		Query:        "vector routing",
		Types:        []string{"methodology.spec", "methodology.plan", "fact.*", "rule.*", "experience.*"},
		TopK:         8,
		CallerUserID: uid,
		CallerRole:   "writer",
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	hist := typeHistogram(resp.Items)
	t.Logf("items=%d total=%d hist=%v", len(resp.Items), resp.Total, hist)

	if hist["methodology"] == 0 {
		t.Errorf("aihub#270 regression: 0 methodology.* rows in a %d-item union recall (hist=%v)",
			len(resp.Items), hist)
	}
	if hist["experience"] == 0 {
		t.Errorf("the vector half was lost (hist=%v)", hist)
	}
	if len(resp.Items) > 8 {
		t.Errorf("returned %d items, exceeds TopK 8", len(resp.Items))
	}
	// 8 embeddable + 4 methodology, all matching the filter and above min_strength.
	if resp.Total != 12 {
		t.Errorf("Total = %d, want 12", resp.Total)
	}
	// Round-robin: with both halves non-empty and a budget of 8, neither may be starved.
	if hist["methodology"] != 4 {
		t.Errorf("methodology rows = %d, want all 4 within the 8-item budget (hist=%v)",
			hist["methodology"], hist)
	}
}

// TestRecallRouterPureEmbeddableStaysOnVectorPath pins the no-regression case: a filter
// naming only embeddable types must behave exactly as it did before aihub#270 — vector
// path, cosine scores populated, no text complement, no methodology rows.
func TestRecallRouterPureEmbeddableStaysOnVectorPath(t *testing.T) {
	pool, proj, uid := recallRouterFixture(t, 8, 4)

	resp, err := Recall(context.Background(), pool, &RecallRequest{
		Project:      proj,
		Query:        "vector routing",
		Types:        []string{"experience.*", "rule.*"},
		TopK:         8,
		CallerUserID: uid,
		CallerRole:   "writer",
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Items) == 0 {
		t.Fatal("no items")
	}
	if h := typeHistogram(resp.Items); h["methodology"] != 0 {
		t.Errorf("methodology rows leaked into a purely embeddable filter: %v", h)
	}
	for _, it := range resp.Items {
		if it.Similarity == nil {
			t.Fatalf("item %s has nil Similarity — this left the vector path", it.ID)
		}
	}
	if resp.Total != 8 {
		t.Errorf("Total = %d, want 8 (embeddable rows only)", resp.Total)
	}
}

// TestRecallRouterPureNonEmbeddableSkipsVectorPath covers a filter naming only
// non-embeddable types: there is nothing for the vector path to match, so the request goes
// straight to text and must return every methodology row.
func TestRecallRouterPureNonEmbeddableSkipsVectorPath(t *testing.T) {
	pool, proj, uid := recallRouterFixture(t, 8, 4)

	resp, err := Recall(context.Background(), pool, &RecallRequest{
		Project:      proj,
		Query:        "vector routing",
		Types:        []string{"methodology.spec", "methodology.plan"},
		TopK:         8,
		CallerUserID: uid,
		CallerRole:   "writer",
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Items) != 4 {
		t.Errorf("got %d items, want 4 methodology rows", len(resp.Items))
	}
	for _, it := range resp.Items {
		if it.Similarity != nil {
			t.Errorf("item %s carries a Similarity — a non-embeddable filter cannot be scored", it.ID)
		}
	}
}

// TestRecallRouterEmptyTypeFilterStaysSemantic pins the deliberate scope limit from the
// aihub#270 review: an UNFILTERED semantic recall is not topped up with non-embeddable
// rows. The text path cannot score relevance — it orders by reference time — so topping up
// here would hand half the budget to whichever methodology.* rows happen to be newest,
// regardless of the query. Naming methodology.spec explicitly is what opts into the
// complement; see TestRecallRouterMixedUnionReturnsBothHalves.
func TestRecallRouterEmptyTypeFilterStaysSemantic(t *testing.T) {
	pool, proj, uid := recallRouterFixture(t, 8, 4)

	resp, err := Recall(context.Background(), pool, &RecallRequest{
		Project:      proj,
		Query:        "vector routing",
		TopK:         8,
		CallerUserID: uid,
		CallerRole:   "writer",
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if h := typeHistogram(resp.Items); h["methodology"] != 0 {
		t.Errorf("unfiltered semantic recall was topped up with %d methodology rows (%v); "+
			"that budget belongs to the query's semantic matches", h["methodology"], h)
	}
}

// TestRecallRouterThresholdGatesEmbeddableHalfOnly pins the SimilarityThreshold rule the
// aihub#270 review asked to have made explicit: a caller-set threshold gates the half a
// cosine score exists for. It still suppresses the "vector empty, retry everything as
// text" fallback, but it does not suppress rows of a non-embeddable type the caller named,
// which were never candidates for similarity filtering.
func TestRecallRouterThresholdGatesEmbeddableHalfOnly(t *testing.T) {
	pool, proj, uid := recallRouterFixture(t, 8, 4)

	// A threshold above 1.0 admits no cosine score at all, so the embeddable half is
	// definitively empty and only the named non-embeddable half can answer.
	resp, err := Recall(context.Background(), pool, &RecallRequest{
		Project:             proj,
		Query:               "vector routing",
		Types:               []string{"methodology.spec", "experience.*"},
		TopK:                8,
		SimilarityThreshold: 1.5,
		CallerUserID:        uid,
		CallerRole:          "writer",
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	hist := typeHistogram(resp.Items)
	if hist["experience"] != 0 {
		t.Errorf("threshold 1.5 admitted %d embeddable rows (%v) — it must gate that half",
			hist["experience"], hist)
	}
	if hist["methodology"] != 4 {
		t.Errorf("got %d methodology rows, want 4 — the threshold must not gate a half that "+
			"has no similarity score to gate on (%v)", hist["methodology"], hist)
	}
}

// TestRecallRouterWorkItemScopedBypassesVectorPath pins the pre-existing aihub#192
// guarantee the bug report singled out as still-healthy: a wi-scoped recall skips the
// vector path entirely, so methodology.* stays reachable there.
func TestRecallRouterWorkItemScopedBypassesVectorPath(t *testing.T) {
	pool, proj, uid := recallRouterFixture(t, 8, 4)
	ctx := context.Background()

	wiID := "wi_" + testname.Sanitize(t.Name())
	mustExec(t, pool, fmt.Sprintf(
		`INSERT INTO work_items(id,seq,project,goal,wi_type,reporter_user_id,reporter_display)
		 VALUES ('%s',1,'%s','router test','fix_bug','%s','%s') ON CONFLICT (id) DO NOTHING`,
		wiID, proj, uid, uid))
	mustExec(t, pool, fmt.Sprintf(
		`UPDATE memories SET work_item_id='%s' WHERE project='%s' AND type='methodology.spec'`,
		wiID, proj))

	resp, err := Recall(ctx, pool, &RecallRequest{
		Project:      proj,
		Query:        "vector routing",
		Types:        []string{"methodology.spec"},
		WorkItemID:   &wiID,
		TopK:         8,
		CallerUserID: uid,
		CallerRole:   "writer",
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Items) != 4 {
		t.Errorf("wi-scoped recall returned %d items, want 4", len(resp.Items))
	}
}

// TestRecallRouterComplementPlaceholdersBindAcrossOptions exercises the hand-threaded $N
// placeholders in the complement query under every combination of the optional clauses
// that sit either side of the injected type-complement predicate. A misnumbered
// placeholder surfaces here as a Postgres bind error, which is the failure mode no
// pure-function test can reach.
func TestRecallRouterComplementPlaceholdersBindAcrossOptions(t *testing.T) {
	pool, proj, uid := recallRouterFixture(t, 4, 4)
	ctx := context.Background()

	base := RecallRequest{
		Project:      proj,
		Query:        "vector routing",
		Types:        []string{"methodology.spec", "experience.*"},
		TopK:         4,
		CallerUserID: uid,
		CallerRole:   "writer",
	}

	cases := []struct {
		name  string
		mutha func(r *RecallRequest)
	}{
		{"defaults", func(*RecallRequest) {}},
		{"admin caller drops the visibility params", func(r *RecallRequest) { r.CallerRole = "admin" }},
		{"include archived", func(r *RecallRequest) { r.IncludeArchived = true }},
		{"min strength set", func(r *RecallRequest) { r.MinStrength = 0.9 }},
		{"lexical algo takes the other SELECT branch", func(r *RecallRequest) { r.RecallAlgo = "lexical" }},
		{"similarity threshold set", func(r *RecallRequest) { r.SimilarityThreshold = 0.01 }},
		{"no type filter", func(r *RecallRequest) { r.Types = nil }},
		{"single non-embeddable type", func(r *RecallRequest) { r.Types = []string{"methodology.spec"} }},
		{"wildcard non-embeddable type", func(r *RecallRequest) { r.Types = []string{"methodology.*", "fact.*"} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			req.Types = append([]string(nil), base.Types...)
			tc.mutha(&req)
			if _, err := Recall(ctx, pool, &req); err != nil {
				t.Fatalf("Recall: %v", err)
			}
		})
	}
}
