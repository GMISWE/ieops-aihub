package server

// Integration tests for aihub#273: GET /v1/work_items?query=<text>.
//
// With no embedding provider configured (the test default is NoopProvider) the
// query parameter must take the ILIKE text path — never a silent empty set —
// and combining query with sort/order/cursor must be rejected up front rather
// than silently ignored (the aihub#267/#271 family: no silently dropped
// parameters). DB-gated like the other handler suites:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleListWorkItems_Query' -v -count=1

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

func newListWIRequest(t *testing.T, rawQuery string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/work_items?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, uc)
	return c, rec
}

func seedQueryTestWorkItem(t *testing.T, pool *pgxpool.Pool, project, uid, goal string, seq int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO work_items (
			id, seq, project, scenario, goal, source, wi_type, priority,
			requires_human_session, milestone, labels, status,
			declared_resources, reporter_user_id, reporter_display,
			parent_work_item_id, attrs, content
		) VALUES (
			$1, $2, $3, 'coding', $4, 'human', 'fix_bug', 'normal',
			FALSE, NULL, '{}', 'queued',
			'[]', $5, $5,
			NULL, '{}', NULL
		)`, domain.NewID("wi"), seq, project, goal, uid)
	require.NoError(t, err)
}

func TestHandleListWorkItems_Query(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	_, err := pool.Exec(context.Background(), `DELETE FROM work_items WHERE project=$1`, project)
	require.NoError(t, err)
	seedQueryTestWorkItem(t, pool, project, uid, "embedding provider latency spike on TEI", 9001)
	seedQueryTestWorkItem(t, pool, project, uid, "unrelated cleanup chore", 9002)

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}

	// No query: both rows, behaviour unchanged (regression guard).
	c0, rec0 := newListWIRequest(t, "project="+project, uc)
	require.NoError(t, handleListWorkItems(pool)(c0))
	require.Equal(t, http.StatusOK, rec0.Code, rec0.Body.String())
	var resp0 struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec0.Body.Bytes(), &resp0))
	require.Len(t, resp0.Items, 2)
	for _, it := range resp0.Items {
		_, has := it["similarity"]
		require.False(t, has, "similarity must be omitted outside the semantic path")
	}

	// query with NoopProvider (test default): ILIKE fallback matches on goal —
	// the aihub#270 failure mode (structurally unreachable rows) must not exist
	// on this endpoint.
	c1, rec1 := newListWIRequest(t, "project="+project+"&query=embedding", uc)
	require.NoError(t, handleListWorkItems(pool)(c1))
	require.Equal(t, http.StatusOK, rec1.Code, rec1.Body.String())
	var resp1 struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec1.Body.Bytes(), &resp1))
	require.Len(t, resp1.Items, 1, "ILIKE fallback must match the embedding wi")
	require.Contains(t, resp1.Items[0]["goal"], "embedding provider")

	// query that matches nothing: empty list, not an error.
	c2, rec2 := newListWIRequest(t, "project="+project+"&query=zzz_no_such_thing", uc)
	require.NoError(t, handleListWorkItems(pool)(c2))
	require.Equal(t, http.StatusOK, rec2.Code)
	var resp2 struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &resp2))
	require.Len(t, resp2.Items, 0)

	// query + sort/order/cursor: rejected loudly, never silently ignored.
	for _, combo := range []string{"sort=created_at", "order=asc", "cursor=2026-01-01T00:00:00Z"} {
		c3, rec3 := newListWIRequest(t, fmt.Sprintf("project=%s&query=x&%s", project, combo), uc)
		require.NoError(t, handleListWorkItems(pool)(c3))
		require.Equal(t, http.StatusBadRequest, rec3.Code,
			"query+%s must 400, got %d: %s", combo, rec3.Code, rec3.Body.String())
	}
}
