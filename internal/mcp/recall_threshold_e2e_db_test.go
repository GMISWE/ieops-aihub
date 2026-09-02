package mcp_test

// aihub#148, all four hops at once, through the REAL stack: Postgres with
// pgvector, the real echo router, the real pkg/client, the real MCP handler,
// and the text a model would actually receive.
//
// ─── Why this exists when the other three guards already pass ───────────────
//
// They each cover a hop:
//
//	hops 1+2  internal/mcp/recall_params_wiring_test.go   (schema ⊆ forwarded)
//	hop 2     internal/mcp/recall_wire_query_test.go      (the RawQuery sent)
//	hops 3+4  internal/server/routes_memory_similarity_threshold_db_test.go
//
// and none of them can see a break in the JOIN. That is not a hypothetical
// worry, it is this wi's defect: hop 1 was correct, hop 4 was correct, and the
// parameter was still dead end to end because the two hops between them were
// empty. A per-hop test suite that never composes the hops is precisely the
// suite that was green while similarity_threshold did nothing.
//
// So this asserts the property a caller actually has: pf_recall(…,
// similarity_threshold=0.99) over a corpus whose every row scores 0.5 returns
// an EMPTY items array to the model.
//
// 🔴 n == 0, never n < baseline — with a top_k cap the two are the same
// observation (aihub#280).
//
// DB-gated in the AIHUB_TEST_DB style of internal/domain's integration tests, so
// a plain `go test ./...` skips it. Reuses newE2EStack from
// wi_echo_e2e_db_test.go.
//
//	AIHUB_TEST_DB='postgres://postgres:…@127.0.0.1:5441/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2ERecallSimilarityThreshold -count=1 -v

import (
	"context"
	"fmt"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/citest/embedprobe"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// seedThresholdCorpus writes n rows through the real Remember path with the
// probe provider active, so each carries an emb_vector pointing in the direction
// `marker` selects. Returns once every row is verified embedded — an unembedded
// corpus would make the n==0 assertion below true for the wrong reason.
func seedThresholdCorpus(t *testing.T, s *e2eStack, marker string, n int) {
	t.Helper()
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, s.project); err != nil {
		t.Fatalf("clear memories: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, _, err := domain.Remember(ctx, s.pool, &domain.RememberRequest{
			Project:       s.project,
			Type:          "experience.approach",
			Content:       fmt.Sprintf("%s end-to-end recall row %d about the similarity floor", marker, i),
			Visibility:    "project",
			DedupMode:     "off",
			CallerUserID:  "u_echo_e2e",
			CallerDisplay: "u_echo_e2e",
		}); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	var embedded int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM memories WHERE project=$1 AND emb_vector IS NOT NULL`, s.project,
	).Scan(&embedded); err != nil {
		t.Fatalf("verify embeddings: %v", err)
	}
	if embedded != n {
		t.Fatalf("only %d/%d seeded rows carry an emb_vector; the vector path (the only path with a "+
			"cosine floor) would not see the rest, and an empty result would prove nothing", embedded, n)
	}
}

// recallItems calls pf_recall and returns the item count the MODEL sees.
func recallItems(t *testing.T, s *e2eStack, args map[string]any) int {
	t.Helper()
	_, decoded := s.call(t, "pf_recall", args)
	items, ok := decoded["items"].([]any)
	if !ok {
		if decoded["items"] == nil {
			return 0
		}
		t.Fatalf("pf_recall returned items of type %T, want an array: %v", decoded["items"], decoded)
	}
	return len(items)
}

// TestE2ERecallSimilarityThresholdReachesSQL is aihub#148's acceptance criterion
// 1, on the MCP surface.
func TestE2ERecallSimilarityThresholdReachesSQL(t *testing.T) {
	s := newE2EStack(t)

	domain.InitEmbeddingProvider(&embedprobe.Provider{})
	t.Cleanup(func() { domain.InitEmbeddingProvider(&embedding.NoopProvider{}) })

	const seeded = 7
	seedThresholdCorpus(t, s, embedprobe.Far, seeded)

	base := map[string]any{"project": s.project, "query": "the similarity floor"}

	// The known-good arm: this query returns results. Without it, "0 items" below
	// would be consistent with an empty corpus, a broken query, or a 400 — the
	// reference side of a differential measurement lying.
	if n := recallItems(t, s, base); n != seeded {
		t.Fatalf("baseline pf_recall returned %d items, want %d — the n==0 arm below would prove nothing", n, seeded)
	}

	// THE ASSERTION.
	withFloor := map[string]any{"project": s.project, "query": "the similarity floor", "similarity_threshold": 0.99}
	if n := recallItems(t, s, withFloor); n != 0 {
		t.Fatalf("pf_recall(similarity_threshold=0.99) returned %d items over a corpus whose every row "+
			"scores 0.5. The parameter is published in the InputSchema and implemented in domain; "+
			"returning the same rows as no threshold at all is aihub#148's exact signature — some hop "+
			"between the tool call and the SQL is dropping it.", n)
	}

	// Recovery: 0 is off, and off must return the corpus. A filter that cannot be
	// turned back off is a worse bug than one that never turned on.
	for _, off := range []any{0.0, "0"} {
		args := map[string]any{"project": s.project, "query": "the similarity floor", "similarity_threshold": off}
		if n := recallItems(t, s, args); n != seeded {
			t.Errorf("pf_recall(similarity_threshold=%#v) returned %d items, want %d: 0 means the "+
				"filter is off and must be indistinguishable from not sending it", off, n, seeded)
		}
	}
	if n := recallItems(t, s, base); n != seeded {
		t.Errorf("pf_recall with no threshold returned %d items after a filtered call, want %d", n, seeded)
	}
}

// TestE2ERecallSimilarityThresholdSelectsRatherThanTruncates: a floor that
// admitted everything or nothing would satisfy the test above at either end
// without ever selecting. Here 4 rows sit above the floor and 6 below, so the
// only way to return 4 is to have applied the predicate.
func TestE2ERecallSimilarityThresholdSelectsRatherThanTruncates(t *testing.T) {
	s := newE2EStack(t)

	domain.InitEmbeddingProvider(&embedprobe.Provider{})
	t.Cleanup(func() { domain.InitEmbeddingProvider(&embedding.NoopProvider{}) })

	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, s.project); err != nil {
		t.Fatalf("clear memories: %v", err)
	}
	const (
		near = 4
		far  = 6
	)
	for _, spec := range []struct {
		marker string
		n      int
	}{{embedprobe.Near, near}, {embedprobe.Far, far}} {
		for i := 0; i < spec.n; i++ {
			if _, _, err := domain.Remember(ctx, s.pool, &domain.RememberRequest{
				Project:       s.project,
				Type:          "experience.approach",
				Content:       fmt.Sprintf("%s mixed corpus row %d", spec.marker, i),
				Visibility:    "project",
				DedupMode:     "off",
				CallerUserID:  "u_echo_e2e",
				CallerDisplay: "u_echo_e2e",
			}); err != nil {
				t.Fatalf("seed %s row %d: %v", spec.marker, i, err)
			}
		}
	}

	base := map[string]any{"project": s.project, "query": "mixed corpus"}
	if n := recallItems(t, s, base); n != near+far {
		t.Fatalf("baseline returned %d items, want %d", n, near+far)
	}
	// 0.9 admits cosine 1.0 and excludes cosine 0.5 — three distinguishable
	// outcomes (10 = dropped, 4 = applied, 0 = over-applied).
	args := map[string]any{"project": s.project, "query": "mixed corpus", "similarity_threshold": 0.9}
	if n := recallItems(t, s, args); n != near {
		t.Fatalf("pf_recall(similarity_threshold=0.9) returned %d items, want %d "+
			"(%d would mean the parameter was dropped, 0 that it excluded everything)", n, near, near+far)
	}
}

// TestE2ERecallTopKAcceptsAJSONNumber is aihub#148 defect 2 end to end.
//
// The discriminating value is 3 against 7 seeded rows and a server default page
// size of 20: took effect -> 3, fell back -> 7. Sending 20 or 7 would be green
// either way, which is the trap this criterion exists to avoid.
func TestE2ERecallTopKAcceptsAJSONNumber(t *testing.T) {
	s := newE2EStack(t)

	domain.InitEmbeddingProvider(&embedprobe.Provider{})
	t.Cleanup(func() { domain.InitEmbeddingProvider(&embedding.NoopProvider{}) })

	const seeded = 7
	seedThresholdCorpus(t, s, embedprobe.Far, seeded)

	base := map[string]any{"project": s.project, "query": "the similarity floor"}
	if n := recallItems(t, s, base); n != seeded {
		t.Fatalf("baseline returned %d items, want %d (the default page of 20 covers all of them)", n, seeded)
	}
	// Both spellings a real caller uses. A JSON number is the natural way to
	// write "max results: 3", and it is what strArg silently discarded.
	for _, shape := range []any{3.0, "3"} {
		args := map[string]any{"project": s.project, "query": "the similarity floor", "top_k": shape}
		if n := recallItems(t, s, args); n != 3 {
			t.Errorf("pf_recall(top_k=%#v) returned %d items, want 3 — %d means the page size was "+
				"dropped and the server's default of 20 applied, with no error at any hop",
				shape, n, seeded)
		}
	}
}
