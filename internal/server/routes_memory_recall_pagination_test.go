package server

// Integration tests for aihub#249: GET /v1/memories previously parsed only
// `top_k`, silently ignoring `?limit=N`, and RecallResponse carried no `total`
// — a caller had no way to distinguish "that's everything" from "keep
// paging" short of walking every page. These run handleRecall directly
// against a live DB (gated by AIHUB_TEST_DB), following the seed/echo-context
// pattern from routes_step_test.go.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleRecall_TotalAndLimitAlias' -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// newRecallRequest builds an authenticated GET /v1/memories?<rawQuery> echo.Context.
func newRecallRequest(t *testing.T, rawQuery string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/memories?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, uc)
	return c, rec
}

// seedRecallTestMemories inserts n minimal active, non-decaying (base_strength=5,
// stability_days huge) memories for project, each `n-i` seconds older than the
// last so ordering (and therefore cursor behavior) is deterministic.
func seedRecallTestMemories(t *testing.T, pool *pgxpool.Pool, project, userID string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO memories (id, project, type, content, author_user_id, author_display,
				visibility, status, tags, attrs, base_strength, stability_days, created_at)
			VALUES ($1, $2, 'fact.note', $3, $4, $4, 'project', 'active', '{}', '{}', 5, 36500,
				clock_timestamp() - make_interval(secs => $5))`,
			domain.NewID("mem"), project, fmt.Sprintf("seed content %d", i), userID, n-i,
		)
		require.NoError(t, err)
	}
}

// TestHandleRecall_TotalAndLimitAlias covers all three Decision-1 behaviors:
//   - `total` reports the full matching count, independent of the page size.
//   - `limit` is accepted as an alias for `top_k`.
//   - when both are supplied, `top_k` wins.
func TestHandleRecall_TotalAndLimitAlias(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	_, err := pool.Exec(context.Background(), `DELETE FROM memories WHERE project=$1`, project)
	require.NoError(t, err)
	seedRecallTestMemories(t, pool, project, uid, 7)

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}

	// `limit` alone (no top_k) must be honored — this is the exact bug: it was
	// silently ignored and the server-side default (20) applied instead.
	c, rec := newRecallRequest(t, "project="+project+"&limit=3", uc)
	require.NoError(t, handleRecall(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp domain.RecallResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Items, 3, "limit=3 must page down to 3 items, not the top_k default of 20")
	require.Equal(t, 7, resp.Total, "total must report the full matching set regardless of page size")
	require.NotNil(t, resp.NextCursor, "more rows remain past this page")

	// Both supplied: top_k wins over limit (2, not 5).
	c2, rec2 := newRecallRequest(t, "project="+project+"&top_k=2&limit=5", uc)
	require.NoError(t, handleRecall(pool)(c2))
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
	var resp2 domain.RecallResponse
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &resp2))
	require.Len(t, resp2.Items, 2, "top_k must win over limit when both are supplied")
	require.Equal(t, 7, resp2.Total)

	// A page large enough to cover every row: total still reports 7 and
	// next_cursor is nil (nothing left to page).
	c3, rec3 := newRecallRequest(t, "project="+project+"&limit=50", uc)
	require.NoError(t, handleRecall(pool)(c3))
	require.Equal(t, http.StatusOK, rec3.Code, rec3.Body.String())
	var resp3 domain.RecallResponse
	require.NoError(t, json.Unmarshal(rec3.Body.Bytes(), &resp3))
	require.Len(t, resp3.Items, 7)
	require.Equal(t, 7, resp3.Total)
	require.Nil(t, resp3.NextCursor)

	// review_fix (code_review minor finding a): a MALFORMED `top_k` must fall
	// through to a valid `limit` rather than discarding it. Silently dropping a
	// caller-supplied page size is the precise failure mode this wi removes, so
	// the alias must not reintroduce it on the bad-input path.
	c4, rec4 := newRecallRequest(t, "project="+project+"&top_k=abc&limit=3", uc)
	require.NoError(t, handleRecall(pool)(c4))
	require.Equal(t, http.StatusOK, rec4.Code, rec4.Body.String())
	var resp4 domain.RecallResponse
	require.NoError(t, json.Unmarshal(rec4.Body.Bytes(), &resp4))
	require.Len(t, resp4.Items, 3, "a malformed top_k must fall through to limit=3, not silently drop it and use the default 20")
	require.Equal(t, 7, resp4.Total)

	// Both malformed: fall all the way back to Recall()'s default page size.
	c5, rec5 := newRecallRequest(t, "project="+project+"&top_k=abc&limit=xyz", uc)
	require.NoError(t, handleRecall(pool)(c5))
	require.Equal(t, http.StatusOK, rec5.Code, rec5.Body.String())
	var resp5 domain.RecallResponse
	require.NoError(t, json.Unmarshal(rec5.Body.Bytes(), &resp5))
	require.Len(t, resp5.Items, 7, "both params malformed -> default page size (20) still returns all 7 seeded rows")
	require.Equal(t, 7, resp5.Total)
}

// TestHandleRecall_BiggerPageIsNotASmallerPage is aihub#309 at the hop that was
// actually broken.
//
// The unit tests in internal/domain/memory_topk_test.go pin the page-size
// resolution itself, including the negative control that shows the measurement
// goes red on the pre-change build. They cannot see the defect, though: it lived
// in the COMPOSITION — handleRecall's own `if req.TopK > 10 { req.TopK = 10 }`
// three lines above its only call to domain.Recall, whose default page size is 20.
// A cap in the handler is invisible to a domain test, so this test exists to
// assert the contract at the hop that carries it, over real rows.
//
// Measured against production (10.146.0.34) on 2026-09-01, one filter, total=220
// throughout: top_k=30 -> 10 items, top_k unset -> 20 items, top_k=20 -> 10,
// top_k=300 -> 10. This test reproduces those four arms against 30 seeded rows,
// where the pre-change build measures 10 / 20 / 10 / 10 and fails on the first
// assertion below.
//
// It seeds 30 rows rather than the 7 above deliberately: the existing test cannot
// detect this bug at any page size, because with only 7 matching rows every page
// size >= 7 returns 7 and the `limit=50` case passed throughout the inversion.
func TestHandleRecall_BiggerPageIsNotASmallerPage(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	_, err := pool.Exec(context.Background(), `DELETE FROM memories WHERE project=$1`, project)
	require.NoError(t, err)
	const seeded = 30
	seedRecallTestMemories(t, pool, project, uid, seeded)

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}

	// pageSize issues one real GET /v1/memories and returns how many items came
	// back, plus whether the response said more rows remain.
	pageSize := func(t *testing.T, params string) (int, bool) {
		t.Helper()
		q := "project=" + project
		if params != "" {
			q += "&" + params
		}
		c, rec := newRecallRequest(t, q, uc)
		require.NoError(t, handleRecall(pool)(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp domain.RecallResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, seeded, resp.Total, "total must report the full matching set for %q", params)
		return len(resp.Items), resp.NextCursor != nil
	}

	unset, unsetMore := pageSize(t, "")
	asked30, asked30More := pageSize(t, "top_k=30")

	// THE DECISIVE ASSERTION. The inversion is this inequality being backwards, and
	// it is stated relationally on purpose: both sides are measured in the same run
	// against the same rows, so no single-point expectation can stand in for it and
	// no change to either default can make it vacuous.
	require.GreaterOrEqual(t, asked30, unset,
		"top_k=30 returned %d items but top_k unset returned %d: asking for a bigger page "+
			"returns a smaller one (aihub#309). A caller reacting to a short page by asking "+
			"for more gets less, and nothing in the response says so.", asked30, unset)

	// The two arms in absolute terms, so a regression that moves BOTH sides together
	// cannot satisfy the inequality above by collapsing it.
	require.Equal(t, 20, unset, "no page size named -> Recall's default page of 20 (aihub#249)")
	require.True(t, unsetMore, "20 of 30 rows returned, so more must remain")
	require.Equal(t, 30, asked30, "top_k=30 must be honoured verbatim: 30 <= the 200 ceiling")
	require.False(t, asked30More, "all 30 rows fit in the requested page, so nothing remains")

	// `limit` is aihub#249's alias for top_k and reaches the same normalization, so
	// it must not be capped either — the deleted cap sat downstream of the alias and
	// silently shrank both spellings.
	viaLimit, _ := pageSize(t, "limit=30")
	require.Equal(t, 30, viaLimit, "limit=30 is the aihub#249 alias for top_k=30 and must page the same")

	// REVERSE DIRECTION — aihub#249's contract, which this fix must not break: bad
	// input falls back to the DEFAULT, never to a smaller page.
	for _, params := range []string{"top_k=0", "top_k=-5", "top_k=abc&limit=xyz"} {
		got, more := pageSize(t, params)
		require.Equal(t, 20, got,
			"%s must fall back to the default page of 20, not to a smaller page", params)
		require.True(t, more, "%s: 20 of 30 rows returned, so more must remain", params)
	}

	// Above the ceiling the caller still gets everything available here (30 rows),
	// where the pre-change build returned 10. That the ceiling is 200 rather than
	// unbounded is asserted in internal/domain/memory_topk_test.go — seeding 201
	// rows to observe a constant would buy nothing this test does not already show.
	huge, hugeMore := pageSize(t, "top_k=300")
	require.Equal(t, 30, huge, "top_k=300 is bounded by Recall's 200 ceiling, so all 30 rows fit")
	require.False(t, hugeMore, "nothing remains past a 200-row page over 30 rows")

	// The value that made the inversion absurd rather than merely surprising:
	// asking for EXACTLY the default used to return half of it.
	exactlyDefault, _ := pageSize(t, "top_k=20")
	require.Equal(t, 20, exactlyDefault, "top_k=20 must equal what naming no page size returns")
}
