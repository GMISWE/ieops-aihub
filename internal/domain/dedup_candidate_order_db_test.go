package domain

// DB-gated tests for aihub#628: checkDedup's candidate query used to be
// `LIMIT 50` with no ORDER BY, so on a project with more than 50 live work
// items the 50 rows that got SCORED were whichever rows the heap returned
// first -- and the CONFLICT_CANDIDATES details array was then emitted in that
// same arbitrary order, so pkg/client's DetailsRenderLimit byte cap (the
// second truncation, aihub#375 / PR #541) also cut arbitrary entries. Both
// truncations must instead drop the LEAST relevant rows, deterministically.
//
// Three subtests, one per ordering key that aihub#628 added:
//
//  1. limit_drops_the_oldest_not_an_arbitrary_row: the unlabeled branch has no
//     relevance signal available in SQL at all (the goal similarity is
//     computed in Go), so its deterministic proxy is recency (seq DESC). With
//     51 live candidates, the row the LIMIT drops must be the single oldest
//     one -- never the newest, which is what heap order yields on a fresh
//     table (rows come back in insertion order, so the mutant that deletes
//     the ORDER BY keeps seqs 1..50 and drops seq 51).
//  2. label_overlap_outranks_recency: the labeled branch ranks label-overlap
//     count ahead of recency, because overlap is a direct proxy for the label
//     component of the composite score. The oldest row is given the largest
//     overlap and must SURVIVE the truncation that eats its same-age peer --
//     red both for the no-ORDER-BY mutant and for a mutant that ranks by
//     recency alone.
//  3. details_orders_most_similar_first: the candidates array in the
//     CONFLICT_CANDIDATES envelope is sorted by the REAL composite score
//     (computed in Go), most similar first, so the client-side render cap
//     truncates the least similar tail. Fixture rows are inserted so that
//     seq order and similarity order disagree; a mutant that deletes the
//     sort.SliceStable emits query order and goes red.
//
// Every fixture goal is checked against candidateScore IN the test before it
// is used, pinning each score inside [0.65, 0.90): at or above 0.90 checkDedup
// returns CONFLICT_DUPLICATE on the first such row and the candidates
// envelope under test is never built; below 0.65 the row never enters it.
//
// Candidates are inserted with direct SQL rather than CreateWorkItem, both to
// control seq exactly and because creating a 51st near-identical goal through
// the API would trip the very dedup being tested.
//
// Follows the AIHUB_TEST_DB gating pattern used across this package: SKIPs
// unless AIHUB_TEST_DB is set, and CI runs it from a dedicated step that
// applies migrations first and scopes itself with -run.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestCheckDedupOrdersCandidatesBeforeTruncation -v -count=1
//
// One test FUNCTION with subtests rather than three functions, following
// aihub#334: internal/citest/dbtestcov ratchets on the number of DB-gated test
// functions. The per-arm coverage claim lives in the CI step's `--- PASS:`
// greps.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertLiveCandidate inserts one queued work item with an explicit seq so the
// test controls both recency order and heap (insertion) order.
func insertLiveCandidate(t *testing.T, pool *pgxpool.Pool, project, userID string, seq int, goal string, labels []string) {
	t.Helper()
	if labels == nil {
		labels = []string{}
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO work_items (id, seq, project, goal, status, labels, reporter_user_id, reporter_display)
		VALUES ($1, $2, $3, $4, 'queued', $5, $6, $6)`,
		fmt.Sprintf("wi_%s%04d", project[len(project)-6:], seq), seq, project, goal, labels, userID)
	require.NoError(t, err, "seeding candidate seq=%d must succeed", seq)
}

// runCheckDedup runs checkDedup in a throwaway transaction and requires it to
// come back as CONFLICT_CANDIDATES, returning the ordered candidates array.
func runCheckDedup(t *testing.T, pool *pgxpool.Pool, req *CreateWorkItemRequest) []map[string]any {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	aerr := checkDedup(ctx, tx, req)
	require.NotNil(t, aerr, "with every fixture scoring in [0.65,0.90), checkDedup must report candidates")
	require.Equal(t, ErrConflictCandidates, aerr.Code, "got %+v", aerr)

	det, ok := aerr.Details.(map[string]any)
	require.True(t, ok, "details must be the map checkDedup builds; got %T", aerr.Details)
	cands, ok := det["candidates"].([]map[string]any)
	require.True(t, ok, "details.candidates must be the candidate array; got %T", det["candidates"])
	return cands
}

func candidateSlugs(cands []map[string]any) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c["slug"].(string))
	}
	return out
}

// requirePartialScore pins a fixture goal (plus labels) inside [0.65, 0.90)
// against the real scoring function, so a later change to the scoring weights
// turns a subtest's silent irrelevance into a loud fixture failure here.
func requirePartialScore(t *testing.T, req *CreateWorkItemRequest, goal string, labels []string) float64 {
	t.Helper()
	score, valid := candidateScore(req, goal, labels, json.RawMessage("[]"))
	require.True(t, valid)
	require.GreaterOrEqual(t, score, 0.65,
		"fixture goal %q must reach the partial-candidate threshold or the row never enters the envelope", goal)
	require.Less(t, score, 0.90,
		"fixture goal %q must stay below the duplicate threshold or checkDedup returns CONFLICT_DUPLICATE instead", goal)
	return score
}

func TestCheckDedupOrdersCandidatesBeforeTruncation(t *testing.T) {
	pool := setupLatestTestDB(t)

	// The query's LIMIT is a literal 50 in checkDedup; the fixtures below seed
	// one row more than that so the truncation is exercised for real.
	const dedupQueryLimit = 50

	t.Run("limit_drops_the_oldest_not_an_arbitrary_row", func(t *testing.T) {
		u := testUser(t, pool)
		project := testProject(t, pool, u)

		req := &CreateWorkItemRequest{
			Project:           project,
			Goal:              "reindex the nightly artifact retention sweep for the archive tier",
			Source:            "human",
			DeclaredResources: json.RawMessage("[]"),
		}
		// Unlabeled on both sides and no resources: the composite score IS the
		// goal similarity (0.7761 for this pair, pinned by the bounds check).
		goal := "reindex the nightly artifact retention sweep for the storage tier"
		requirePartialScore(t, req, goal, nil)

		// Insertion order = seq order, so heap order and recency order agree:
		// a mutant with no ORDER BY returns seqs 1..50 and drops seq 51.
		for seq := 1; seq <= dedupQueryLimit+1; seq++ {
			insertLiveCandidate(t, pool, project, u, seq, goal, nil)
		}

		cands := runCheckDedup(t, pool, req)
		require.Len(t, cands, dedupQueryLimit)

		slugs := candidateSlugs(cands)
		newest := fmt.Sprintf("%s#%d", project, dedupQueryLimit+1)
		oldest := fmt.Sprintf("%s#%d", project, 1)
		assert.Contains(t, slugs, newest,
			"the newest live wi must survive the LIMIT; losing it means the query is not ordering by recency")
		assert.NotContains(t, slugs, oldest,
			"with 51 equal-similarity candidates the one the LIMIT drops must be the single oldest")

		// All 50 scores are identical, so the stable Go sort must preserve the
		// query's seq DESC order end to end: same table state, same envelope.
		expected := make([]string, 0, dedupQueryLimit)
		for seq := dedupQueryLimit + 1; seq >= 2; seq-- {
			expected = append(expected, fmt.Sprintf("%s#%d", project, seq))
		}
		assert.Equal(t, expected, slugs,
			"equal-similarity candidates must keep the query's deterministic recency order")
	})

	t.Run("label_overlap_outranks_recency", func(t *testing.T) {
		u := testUser(t, pool)
		project := testProject(t, pool, u)

		req := &CreateWorkItemRequest{
			Project:           project,
			Goal:              "migrate the queue consumer offsets to the new coordination store",
			Source:            "human",
			Labels:            []string{"dedup-628", "wave-b"},
			DeclaredResources: json.RawMessage("[]"),
		}
		goal := "migrate the queue consumer offsets onto the new coordination db"
		oneLabel := []string{"dedup-628"}
		twoLabels := []string{"dedup-628", "wave-b"}
		lowScore := requirePartialScore(t, req, goal, oneLabel)
		highScore := requirePartialScore(t, req, goal, twoLabels)
		require.Greater(t, highScore, lowScore, "the two-label fixture must outscore the one-label peers")

		// seq 1, the OLDEST row, carries the largest label overlap; every
		// younger row overlaps on one label. Recency alone would drop seq 1.
		insertLiveCandidate(t, pool, project, u, 1, goal, twoLabels)
		for seq := 2; seq <= dedupQueryLimit+1; seq++ {
			insertLiveCandidate(t, pool, project, u, seq, goal, oneLabel)
		}

		cands := runCheckDedup(t, pool, req)
		require.Len(t, cands, dedupQueryLimit)

		slugs := candidateSlugs(cands)
		assert.Contains(t, slugs, fmt.Sprintf("%s#%d", project, 1),
			"the highest-label-overlap candidate must survive the LIMIT even as the oldest row; "+
				"dropping it means the labeled branch ranks by recency alone (or not at all)")
		assert.Contains(t, slugs, fmt.Sprintf("%s#%d", project, dedupQueryLimit+1),
			"the newest one-label candidate must survive; losing it is the no-ORDER-BY heap order")
		assert.NotContains(t, slugs, fmt.Sprintf("%s#%d", project, 2),
			"the row the LIMIT drops must be the oldest of the LOWEST-overlap tier")
		assert.Equal(t, fmt.Sprintf("%s#%d", project, 1), slugs[0],
			"the two-label candidate also carries the highest composite score, so it must sort first in the envelope")
	})

	t.Run("details_orders_most_similar_first", func(t *testing.T) {
		u := testUser(t, pool)
		project := testProject(t, pool, u)

		req := &CreateWorkItemRequest{
			Project:           project,
			Goal:              "rotate the ingest gateway credentials for the telemetry pipeline",
			Source:            "human",
			DeclaredResources: json.RawMessage("[]"),
		}
		// Distinct similarities (0.8889 / 0.7903 / 0.7059 at the time of
		// writing; the bounds are pinned, the exact values are not), inserted
		// so that similarity order disagrees with seq order: the MOST similar
		// row is the oldest. The query returns seq DESC (mid, low, high), so
		// only the Go-side sort can produce high, mid, low.
		goalHigh := "rotate the ingest gateway credentials for every telemetry pipeline"
		goalMid := "rotate the ingest gateway credentials for the telemetry relay"
		goalLow := "rotate the ingest gateway credentials for a telemetry exporter"
		simHigh := requirePartialScore(t, req, goalHigh, nil)
		simMid := requirePartialScore(t, req, goalMid, nil)
		simLow := requirePartialScore(t, req, goalLow, nil)
		require.Greater(t, simHigh, simMid)
		require.Greater(t, simMid, simLow)

		insertLiveCandidate(t, pool, project, u, 1, goalHigh, nil)
		insertLiveCandidate(t, pool, project, u, 2, goalLow, nil)
		insertLiveCandidate(t, pool, project, u, 3, goalMid, nil)

		cands := runCheckDedup(t, pool, req)
		require.Len(t, cands, 3)

		assert.Equal(t, []string{
			fmt.Sprintf("%s#%d", project, 1),
			fmt.Sprintf("%s#%d", project, 3),
			fmt.Sprintf("%s#%d", project, 2),
		}, candidateSlugs(cands),
			"candidates must be ordered by composite similarity DESC, not by the query's seq DESC scan order, "+
				"so the render-cap truncation (client.DetailsRenderLimit, aihub#375) cuts the least similar tail")

		sims := make([]float64, len(cands))
		for i, c := range cands {
			sims[i] = c["similarity"].(float64)
		}
		assert.InDelta(t, simHigh, sims[0], 1e-9)
		assert.InDelta(t, simMid, sims[1], 1e-9)
		assert.InDelta(t, simLow, sims[2], 1e-9)
	})
}
