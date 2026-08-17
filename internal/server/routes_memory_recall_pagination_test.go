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
