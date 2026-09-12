package server

// DB-gated probe for the FIFTH instance of the slug-resolution class
// (aihub#362; history: aihub#127 write side, aihub#343 read side, aihub#357
// dependency call sites — and this one, found by aihub#357's independent
// review the same day the fourth was fixed).
//
// POST /v1/work_items/:id/unblock took the raw path parameter into three
// sites: a status probe (`WHERE id=$1`), the UPDATE that requeues, and the
// admin_unblock event's work_item_id (an FK into work_items). A slug — which
// is what every human and every polyforge skill types — matched no row at the
// probe, so this ADMIN endpoint answered 404 "work item not found" for a work
// item that exists and is blocked. Unlike the read-side instances this one is
// at least loud, but it is loud with the WRONG message: it reports absence,
// and an admin trying to unshovel a stuck work item concludes the id is bad,
// not that the endpoint wants the one identifier format nobody types.
//
// The static side of the fix is internal/citest/slugres, which fails on the
// unfixed body of this handler; this file is the behavioural side, pinned on a
// real database.
//
// Run:
//
//	AIHUB_TEST_DB='postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/server/ -run TestUnblockBySlug -count=1 -v

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// unblockAs issues one authenticated POST /v1/work_items/<ref>/unblock and
// returns the HTTP status.
func unblockAs(t *testing.T, pool *pgxpool.Pool, ref string, uc *UserContext) int {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/work_items/"+ref+"/unblock",
		strings.NewReader(`{"reason":"slug probe"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(ref)
	setUser(c, uc)
	if err := handleUnblockWorkItem(pool)(c); err != nil {
		t.Fatalf("handler returned an error for ref %q: %v", ref, err)
	}
	return rec.Code
}

// TestUnblockBySlug_UnblocksAndKeysTheEventCanonically is the acceptance
// probe: addressed by slug, the endpoint must requeue the work item AND file
// the admin_unblock audit event under the CANONICAL id — the event insert was
// the write-side (aihub#127-shaped) half of this instance.
func TestUnblockBySlug_UnblocksAndKeysTheEventCanonically(t *testing.T) {
	pool := setupStepTestDB(t)
	ctx := context.Background()
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	require.NotEmpty(t, wi.Slug, "fixture has no slug to address the work item by")

	_, err := pool.Exec(ctx, `UPDATE work_items SET status='blocked' WHERE id=$1`, wi.ID)
	require.NoError(t, err)

	uc := &UserContext{UserID: uid, DisplayName: uid, Role: "admin"}
	code := unblockAs(t, pool, wi.Slug, uc)
	if code == http.StatusNotFound {
		t.Fatalf("POST /v1/work_items/%s/unblock answered 404 while the same work item exists as %s "+
			"and is blocked. The handler is comparing the caller's raw reference against "+
			"work_items(id) — the aihub#127/#343/#357 class, fifth instance (aihub#362).",
			wi.Slug, wi.ID)
	}
	require.Equal(t, http.StatusOK, code)

	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id=$1`, wi.ID).Scan(&status))
	require.Equal(t, "queued", status, "the work item addressed by slug was not requeued")

	// The audit event must be keyed by the canonical id: filed under the slug
	// it would trip the FK and — because the insert is best-effort — vanish
	// without an error, which is the silent half of this instance.
	var events int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_events WHERE work_item_id=$1 AND event_type='admin_unblock'`,
		wi.ID).Scan(&events))
	require.GreaterOrEqual(t, events, 1,
		"no admin_unblock event under the canonical id: the audit insert lost its work_item_id")
}

// TestUnblockBySlug_StatusGateStillApplies is the control: resolution must not
// widen the endpoint. A work item that is NOT blocked, addressed by slug, must
// answer 409 — the same verdict the canonical id gets — proving the resolved
// id reaches the status check rather than bypassing it.
func TestUnblockBySlug_StatusGateStillApplies(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	require.NotEmpty(t, wi.Slug)

	uc := &UserContext{UserID: uid, DisplayName: uid, Role: "admin"}
	code := unblockAs(t, pool, wi.Slug, uc) // seeded status is 'queued', not 'blocked'
	require.Equal(t, http.StatusConflict, code,
		"a non-blocked work item addressed by slug must 409 exactly like one addressed by id")

	// And a reference that resolves to nothing is still a 404.
	require.Equal(t, http.StatusNotFound, unblockAs(t, pool, "no-such-project#999999", uc))
}
