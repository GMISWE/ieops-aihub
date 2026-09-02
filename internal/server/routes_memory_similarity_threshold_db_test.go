package server

// aihub#148, hops 3 and 4: the query param handleRecall never parsed, and the
// SQL predicate that was waiting for it.
//
// The defect was not a missing feature. internal/domain/memory_vector.go has
// carried `if req.SimilarityThreshold > 0 { … }` — with comments on how the
// floor interacts with COUNT(*) and with Postgres's bind protocol, and a second
// note in memory.go on why a caller-set threshold must suppress the
// empty-vector text fallback — the whole time. Nothing ever set the field.
// Measured live on 2026-08-29 against production, read-only:
//
//	GET /v1/memories?project=ieops&query=<noise>&top_k=10
//	  no threshold                n=10 total=181 sims=[0.3997 0.3848 0.3853] … 0.3455
//	  &similarity_threshold=0.99  n=10 total=181 sims=[0.3997 0.3848 0.3853] … 0.3455
//
// Byte-identical, where 0.99 should have returned nothing.
//
// 🔴 The criterion below is `n == 0`, NOT `n < baseline`. Under a top_k cap,
// "the filter worked but plenty still match" and "the parameter was discarded"
// are the same observation, and this repo has already published a false
// conclusion from exactly that confusion (aihub#280). Only zero discriminates.
//
// Why a DB test rather than a unit test: the predicate is spliced into a
// hand-built WHERE with hand-threaded $N placeholders, and the count query
// drops a trailing unreferenced bind value when the threshold is off. A
// placeholder that drifts out of step with its args list is a runtime bind
// error, not a compile error, and no pure-function test reaches it.
//
//	AIHUB_TEST_DB='postgres://postgres:…@localhost:5441/aihub_test?sslmode=disable' \
//	  go test ./internal/server/ -run TestHandleRecall_SimilarityThreshold -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/citest/embedprobe"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// thresholdFixture activates the probe provider and seeds nNear rows at cosine
// 1.0 and nFar rows at cosine 0.5 through the real Remember write path, so every
// row really carries an emb_vector (the vector path's WHERE requires one).
func thresholdFixture(t *testing.T, nNear, nFar int) (*pgxpool.Pool, *UserContext, string) {
	t.Helper()
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, project)
	require.NoError(t, err)

	domain.InitEmbeddingProvider(&embedprobe.Provider{})
	t.Cleanup(func() { domain.InitEmbeddingProvider(&embedding.NoopProvider{}) })

	seed := func(marker string, n int) {
		for i := 0; i < n; i++ {
			_, _, rerr := domain.Remember(ctx, pool, &domain.RememberRequest{
				Project:       project,
				Type:          "experience.approach",
				Content:       fmt.Sprintf("%s seeded recall row %d", marker, i),
				Visibility:    "project",
				DedupMode:     "off",
				CallerUserID:  uid,
				CallerDisplay: uid,
			})
			require.NoError(t, rerr, "seed %s row %d", marker, i)
		}
	}
	seed(embedprobe.Near, nNear)
	seed(embedprobe.Far, nFar)

	// Every seeded row must actually have a vector, or a green "0 items" below
	// would be measuring an empty corpus instead of a working filter.
	var embedded int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM memories WHERE project=$1 AND emb_vector IS NOT NULL`, project,
	).Scan(&embedded))
	require.Equal(t, nNear+nFar, embedded, "fixture is not on the vector path: rows without emb_vector cannot be similarity-filtered")

	return pool, &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}, project
}

// recallWithParams issues one real GET /v1/memories and returns the response.
func recallWithParams(t *testing.T, pool *pgxpool.Pool, uc *UserContext, project, params string) domain.RecallResponse {
	t.Helper()
	q := "project=" + project + "&query=probe+for+the+similarity+floor"
	if params != "" {
		q += "&" + params
	}
	c, rec := newRecallRequest(t, q, uc)
	require.NoError(t, handleRecall(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp domain.RecallResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

// TestHandleRecall_SimilarityThresholdIsReachable is the decisive assertion.
//
// Every seeded row sits at cosine 0.5 against the probe query, so a floor of
// 0.99 must leave NOTHING. On the pre-fix build handleRecall never read the
// query param, req.SimilarityThreshold stayed 0, the predicate never appeared in
// the WHERE, and all seven rows came back.
func TestHandleRecall_SimilarityThresholdIsReachable(t *testing.T) {
	const seeded = 7
	pool, uc, project := thresholdFixture(t, 0, seeded)

	// Baseline: the query really does return results without a floor. Without
	// this arm the n==0 assertion below could be satisfied by an empty corpus.
	base := recallWithParams(t, pool, uc, project, "")
	require.Len(t, base.Items, seeded, "the probe query must return results before a floor is applied")
	require.Equal(t, seeded, base.Total)
	for _, it := range base.Items {
		// A nil similarity would mean this row came from the TEXT path, where no
		// cosine exists and no floor can apply — the fixture would then be
		// measuring the wrong path entirely.
		require.NotNil(t, it.Similarity, "row %s has no similarity: it did not come from the vector path", it.ID)
		require.InDelta(t, embedprobe.FarCosine, *it.Similarity, 0.01,
			"fixture drift: rows must sit at cosine 0.5, or 0.99 stops being a discriminating floor")
	}

	// THE ASSERTION. n == 0, not n < baseline: under a top_k cap "the filter
	// worked but many still match" and "the parameter was dropped" are the same
	// observation (aihub#280).
	filtered := recallWithParams(t, pool, uc, project, "similarity_threshold=0.99")
	require.Empty(t, filtered.Items,
		"similarity_threshold=0.99 returned %d items whose top similarity is 0.5. The parameter is "+
			"published in pf_recall's InputSchema and fully implemented in domain; if it returns the "+
			"same rows as no threshold at all, no hop between them is carrying it (aihub#148).",
		len(filtered.Items))

	// An empty page here must be the floor doing its job, not the vector path
	// falling through to text search. domain.Recall's fallback fires only when
	// SimilarityThreshold <= 0, precisely so that "empty is the intended answer"
	// survives — a fallback would have returned all seven rows by recency and
	// made this test green for the wrong reason. `total` is what tells the two
	// apart even if the page were somehow empty for another reason: the count
	// query carries the same floor, so it must read 0 and not 7.
	require.Zero(t, filtered.Total,
		"total=%d with an empty page means the floor reached the row query but not the count "+
			"query, or the text fallback fired despite a caller-set threshold", filtered.Total)

	// RECOVERY, in both spellings of "off". If these did not come back, the
	// filter would be stuck on rather than reachable.
	for _, off := range []string{"", "similarity_threshold=0.0", "similarity_threshold=0"} {
		got := recallWithParams(t, pool, uc, project, off)
		require.Len(t, got.Items, seeded, "%q must return the unfiltered page: 0 means off", off)
		require.Equal(t, seeded, got.Total, "%q must report the unfiltered total", off)
	}

	// aihub#148 acceptance 2: passing 0 must be indistinguishable from passing
	// nothing, item for item and in order — the guard against someone "fixing"
	// this by introducing a non-zero default.
	explicitZero := recallWithParams(t, pool, uc, project, "similarity_threshold=0")
	require.Equal(t, len(base.Items), len(explicitZero.Items))
	for i := range base.Items {
		require.Equal(t, base.Items[i].ID, explicitZero.Items[i].ID,
			"item %d differs between no threshold and threshold=0", i)
	}

	// A malformed value must not become a silent filter. ParseFloat fails, the
	// field stays 0, the floor stays off.
	garbage := recallWithParams(t, pool, uc, project, "similarity_threshold=notanumber")
	require.Len(t, garbage.Items, seeded, "an unparseable threshold must leave the filter off, not empty the page")
}

// TestHandleRecall_SimilarityThresholdFiltersTotal is aihub#148 acceptance 3.
//
// memory_vector.go's comment claims `total` is COUNT(*) over every filter
// INCLUDING the threshold, taken before the LIMIT. That claim is only checkable
// once the threshold can reach the SQL at all, so it is verified here rather
// than assumed.
//
// The fixture makes all three candidate answers distinct: 10 rows exist, 4 clear
// the floor, and the page holds 3. total==4 is the only one that means what the
// comment says. The pre-fix build reports 10.
func TestHandleRecall_SimilarityThresholdFiltersTotal(t *testing.T) {
	const (
		near = 4
		far  = 6
	)
	pool, uc, project := thresholdFixture(t, near, far)

	all := recallWithParams(t, pool, uc, project, "")
	require.Equal(t, near+far, all.Total, "without a floor every row counts")

	// A floor of 0.9 admits the cosine-1.0 rows and excludes the cosine-0.5 ones
	// — a value that discriminates, unlike 0.4 (admits everything) or 1.1
	// (admits nothing), either of which would be green whatever total meant.
	full := recallWithParams(t, pool, uc, project, "similarity_threshold=0.9")
	require.Len(t, full.Items, near, "the floor must admit exactly the %d rows above it", near)
	require.Equal(t, near, full.Total, "total must count the FILTERED set, not the whole corpus")

	// Now shrink the page below the filtered set, so total, len(items) and the
	// corpus size are three different numbers.
	paged := recallWithParams(t, pool, uc, project, "similarity_threshold=0.9&top_k=3")
	require.Len(t, paged.Items, 3, "top_k must still bound the page")
	require.Equal(t, near, paged.Total,
		"total is COUNT(*) over every filter INCLUDING the threshold, taken before the LIMIT: "+
			"want %d (rows above the floor), got %d (%d would mean the floor never reached the count query)",
		near, paged.Total, near+far)
	require.Greater(t, paged.Total, len(paged.Items),
		"the point of this arm is that total, the page size and the corpus size are three "+
			"different numbers; total=%d and page=%d collapses it", paged.Total, len(paged.Items))

	// Every returned row must genuinely be above the floor — the filter must be
	// selecting, not merely truncating.
	for _, it := range full.Items {
		require.NotNil(t, it.Similarity, "row %s has no similarity: it did not come from the vector path", it.ID)
		require.GreaterOrEqual(t, *it.Similarity, 0.9,
			"row %s has similarity %v and is below the requested floor", it.ID, *it.Similarity)
	}
	// And the excluded half must really have been excludable — otherwise a floor
	// that silently matched everything would look the same.
	for _, it := range all.Items {
		require.NotNil(t, it.Similarity, "row %s has no similarity: it did not come from the vector path", it.ID)
	}
}
