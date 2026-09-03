package server

// DB-gated probe for the READ side of the slug-resolution defect aihub#127
// fixed on the write side (aihub#343).
//
// GET /v1/events resolved id-or-slug to a *WorkItem for its access check and
// then put the CALLER'S raw parameter into the query, while ListEvents compares
// it to agent_events.work_item_id — a column that FK-references work_items(id)
// and therefore only ever holds a canonical `wi_...`.
//
// 🔴 Why this one outlived aihub#127 by eight months: every instance that fix
// covered was a WRITE, where a slug reaching a work_item_id column trips the
// foreign key and surfaces as a 500. A READ has no constraint to trip.
// `WHERE work_item_id = 'aihub#343'` matches nothing, and the handler answers
// 200 with an empty list — indistinguishable from a work item that genuinely
// has no events. Measured against production before the fix, both arms on the
// same work item:
//
//	pf_read_events(work_item_id="aihub#343")   -> {"events":null}
//	pf_read_events(work_item_id="wi_V7ph7bYu") -> 2 events
//
// It is load-bearing for aihub#343 rather than a drive-by: that work item's
// acceptance criterion is "decide from pf_read_events' return ALONE whether
// this lock should currently be held", and a slug — which is what every human
// and every polyforge skill types — answered every such question with "nothing
// was ever recorded". Emitting the events would not have been enough while this
// stood, and it is the same failure mode the work item exists to prevent,
// arriving one layer down.
//
// Run:
//
//	AIHUB_TEST_DB='postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/server/ -run TestListEventsBySlug -count=1 -v

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// newListEventsRequest issues one authenticated GET /v1/events.
func newListEventsRequest(t *testing.T, rawQuery string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/events?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, uc)
	return c, rec
}

// listEventsFor returns the decoded events[] and the HTTP status.
func listEventsFor(t *testing.T, pool *pgxpool.Pool, rawQuery string, uc *UserContext) ([]map[string]any, int) {
	t.Helper()
	c, rec := newListEventsRequest(t, rawQuery, uc)
	if err := handleListEvents(pool)(c); err != nil {
		t.Fatalf("handler returned an error for %q: %v", rawQuery, err)
	}
	if rec.Code != http.StatusOK {
		return nil, rec.Code
	}
	var decoded struct {
		Events []map[string]any `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded), "body: %s", rec.Body.String())
	return decoded.Events, rec.Code
}

// TestListEventsBySlug_ReturnsTheSameStreamAsByID is the acceptance probe.
//
// The two identifiers name ONE work item, so the two streams must be equal. The
// assertion is equality against the canonical arm rather than "the slug arm is
// non-empty", because a non-emptiness check would also pass a handler that
// ignored work_item_id altogether and returned the whole project.
func TestListEventsBySlug_ReturnsTheSameStreamAsByID(t *testing.T) {
	pool := setupStepTestDB(t)
	ctx := context.Background()
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)

	// Two events, not one: with a single event "the slug arm returned 1 row" and
	// "the handler ignored the filter and this project has 1 event" agree.
	for i, typ := range []string{"note", "decision"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, event_type, payload, project)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			domain.NewID("evt"), wi.ID, uid, typ,
			[]byte(`{"probe":`+string(rune('0'+i))+`}`), project)
		require.NoError(t, err)
	}

	uc := &UserContext{
		UserID: uid, DisplayName: uid, Role: "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}

	byID, codeID := listEventsFor(t, pool, "work_item_id="+wi.ID, uc)
	require.Equal(t, http.StatusOK, codeID)
	require.GreaterOrEqual(t, len(byID), 2,
		"the canonical-id arm returned %d events; the fixture is not discriminating", len(byID))

	require.NotEmpty(t, wi.Slug, "fixture has no slug to address the work item by")
	bySlug, codeSlug := listEventsFor(t, pool, "work_item_id="+wi.Slug, uc)
	require.Equal(t, http.StatusOK, codeSlug)

	if len(bySlug) == 0 {
		t.Fatalf("GET /v1/events?work_item_id=%s returned ZERO events while the same work item "+
			"addressed as %s returned %d. A slug is what every human and every polyforge skill "+
			"types, and the handler answers 200 with an empty list — indistinguishable from "+
			"'nothing was ever recorded' (aihub#343, the read side of aihub#127).",
			wi.Slug, wi.ID, len(byID))
	}

	idsOf := func(evs []map[string]any) []string {
		out := make([]string, 0, len(evs))
		for _, e := range evs {
			s, _ := e["id"].(string)
			out = append(out, s)
		}
		return out
	}
	require.Equal(t, idsOf(byID), idsOf(bySlug),
		"the slug and the canonical id name ONE work item, so the two streams must be identical")
}

// TestListEventsBySlug_StillScopesToTheWorkItem is the control.
//
// Resolving the slug must not degrade into "return everything": a second work
// item in the same project must NOT appear in either arm. Without this, the
// probe above would be satisfied by a handler that dropped the filter.
func TestListEventsBySlug_StillScopesToTheWorkItem(t *testing.T) {
	pool := setupStepTestDB(t)
	ctx := context.Background()
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)

	// A second work item in the same project, with its own event. Inserted with
	// raw SQL: CreateWorkItem would dedup against the first one's goal.
	otherID := domain.NewID("wi")
	_, err := pool.Exec(ctx, `
		INSERT INTO work_items (
			id, seq, project, scenario, goal, source, wi_type, priority,
			requires_human_session, milestone, labels, status,
			declared_resources, reporter_user_id, reporter_display,
			parent_work_item_id, attrs, content
		) VALUES (
			$1, $2, $3, 'coding', 'a neighbour with its own timeline', 'human', 'fix_bug', 'normal',
			FALSE, NULL, '{}', 'queued',
			'[]', $4, $4,
			NULL, '{}', NULL
		)`, otherID, 8700, project, uid)
	require.NoError(t, err)

	mine := domain.NewID("evt")
	_, err = pool.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'note', '{"whose":"mine"}', $4)`, mine, wi.ID, uid, project)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'note', '{"whose":"neighbour"}', $4)`,
		domain.NewID("evt"), otherID, uid, project)
	require.NoError(t, err)

	uc := &UserContext{
		UserID: uid, DisplayName: uid, Role: "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}
	bySlug, code := listEventsFor(t, pool, "work_item_id="+wi.Slug, uc)
	require.Equal(t, http.StatusOK, code)
	for _, e := range bySlug {
		gotWI, _ := e["work_item_id"].(string)
		require.Equal(t, wi.ID, gotWI,
			"the slug arm returned an event belonging to %s; resolving the slug must not widen "+
				"the filter to the whole project", gotWI)
	}

	// And the project-scoped call, which never took a slug, still sees both —
	// otherwise "scoped correctly" and "the fixture never inserted the
	// neighbour's event" would look the same.
	byProject, code := listEventsFor(t, pool, "project="+project, uc)
	require.Equal(t, http.StatusOK, code)
	require.GreaterOrEqual(t, len(byProject), 2,
		"the project-scoped arm sees %d events; the neighbour fixture did not land, so the "+
			"scoping assertion above proves nothing", len(byProject))
}
