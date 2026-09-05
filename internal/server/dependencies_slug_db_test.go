package server

// DB-gated acceptance test for the READ half of aihub#357, and the last three
// instances of the slug-resolution class aihub#127 opened and aihub#343 carried
// into the read side (see events_slug_db_test.go in this package for that one).
//
// 🔴 The shape of the defect, and why it survived being "already fixed" twice:
//
// All three dependency handlers resolve id-or-slug to a *WorkItem for their
// access check — so a slug 200s the authorization step and looks accepted — and
// then pass the CALLER'S RAW PARAMETER down to a query keyed on
// work_items(id). wi_dependencies.blocked_wi_id / .blocking_wi_id both
// FK-reference that column, so they only ever hold a canonical `wi_...`.
//
// On the READ (handleListDependencies) there is no constraint to trip:
// `WHERE blocked_wi_id = 'aihub#357'` matches nothing and the endpoint answers
// 200 with `{"blocking":[],"blocked_by":[]}` — indistinguishable from a work
// item that genuinely has no dependencies. That is precisely the observation
// aihub#357 was filed on: the reporter passed slugs to pf_list_dependencies,
// read two empty lists, and concluded that pf_create_work_item's blocked_by
// creates no edges. It does create them (guarded in internal/domain,
// create_wi_blocked_by_db_test.go); the read is what lied.
//
// On the two WRITES the failure is loud but still wrong, and each lies in its
// own way. Measured on this fixture against the unfixed tree:
//
//	POST   .../<slug>/dependencies          -> 404 {"code":"NOT_FOUND",
//	                                                "message":"blocked work item <slug> not found"}
//	DELETE .../<slug>/dependencies/<slug>/blocks -> 404 {"code":"NOT_FOUND",
//	                                                "message":"dependency not found"}
//
// Both name the wrong absent thing: the work item exists (the handler just
// resolved it), and so does the dependency. Note the create does NOT surface as
// the foreign-key 500 you would expect from the write-side instances aihub#127
// was filed for — domain.CreateDependency does its own `SELECT project FROM
// work_items WHERE id=$1` with the raw reference and returns NOT_FOUND before
// the INSERT is ever reached. Predicting the 500 and asserting on it would have
// produced a test that goes green for the wrong reason.
//
// Real router, real auth middleware, real echo binder, real Postgres — because
// the whole defect lives in the handler hop. A domain-level test cannot see it:
// domain.ListDependencies is given an id and is correct for every id it is
// given. This is the same reason routes_projects_cas_db_test.go exists beside
// its domain twin.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestDependencyEndpointsResolveSlugs -v -count=1
//
// One test FUNCTION with subtests rather than four functions, following
// aihub#334: internal/citest/dbtestcov ratchets on the number of DB-gated test
// functions. The per-arm coverage claim lives in the CI step's `--- PASS:` greps.
// The subtests share one fixture and run in declaration order — the delete arm
// removes the edge the create arm added, and says so.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const depSlugTestKey = "pfk_dependency_slug_http_test_key"

// depSlugStack stands the real router up against AIHUB_TEST_DB with an admin
// user who owns a project unique to this test.
type depSlugStack struct {
	url     string
	pool    *pgxpool.Pool
	project string
	// uid is captured at setup, not re-derived from t.Name() per call: inside a
	// subtest t.Name() is "Parent/child", which would seed rows against a user
	// that does not exist.
	uid string
}

func newDepSlugStack(t *testing.T) *depSlugStack {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	uid := "u_" + testname.Sanitize(t.Name())
	project := "p_" + testname.Sanitize(t.Name())
	keys, err := json.Marshal([]map[string]any{{"id": "k_depslug", "key_hash": auth.HashKey(depSlugTestKey)}})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users(id,email,display_name,user_type,role,api_keys)
		VALUES($1,$1||'@test.local',$1,'human','admin',$2)
		ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role='admin'`, uid, keys)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`, project, uid)
	require.NoError(t, err)

	// The project name is derived from t.Name(), so it is the same string on
	// every run: clear the previous run's work items or their slugs and seq
	// numbers decide this run's fixture. Order satisfies the FKs that do not
	// cascade from work_items (agent_events, wi_step_completions, run_attempts);
	// wi_dependencies and wi_step_state cascade and are left to the database.
	for _, stmt := range []string{
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		_, err = pool.Exec(ctx, stmt, project)
		require.NoError(t, err, "reset failed on %q", stmt)
	}

	ts := httptest.NewServer(NewRouter(pool, []byte("dependency-slug-test-cookie-secret")))
	t.Cleanup(ts.Close)
	return &depSlugStack{url: ts.URL, pool: pool, project: project, uid: uid}
}

// seedWI creates one work item through the real domain path so it gets a real
// slug. Goals must stay mutually dissimilar: CreateWorkItem runs goal-similarity
// dedup against live wis in the same project and would reject a close match
// before this test reached any handler.
func (s *depSlugStack) seedWI(t *testing.T, goal string, blockedBy ...string) *domain.WorkItem {
	t.Helper()
	wi, aerr := domain.CreateWorkItem(context.Background(), s.pool, &domain.CreateWorkItemRequest{
		Project:   s.project,
		Goal:      goal,
		Source:    "human",
		BlockedBy: blockedBy,
	}, s.uid, s.uid)
	require.Nil(t, aerr, "seeding %q failed: %+v", goal, aerr)
	require.NotEmpty(t, wi.Slug, "the fixture needs a slug to exercise slug resolution")
	return wi
}

// req issues one authenticated request. Path segments are escaped by the caller;
// a slug contains '#', which is a fragment delimiter in a raw URL.
func (s *depSlugStack) req(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, s.url+path, rdr)
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+depSlugTestKey)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

// depsRef is the decoded GET /v1/work_items/:id/dependencies body. Only the
// entry ids matter here — direction and cross-project masking have their own
// guards (internal/domain, dependencies_direction_test.go).
type depRefEntry struct {
	ID string `json:"id"`
}

type depsRef struct {
	Blocking  []depRefEntry `json:"blocking"`
	BlockedBy []depRefEntry `json:"blocked_by"`
}

// listDeps reads the dependencies of one work item by whatever reference the
// caller hands it — a canonical id or a slug. The two must agree; that
// agreement IS the assertion in most subtests below.
func (s *depSlugStack) listDeps(t *testing.T, ref string) depsRef {
	t.Helper()
	status, raw := s.req(t, http.MethodGet,
		"/v1/work_items/"+url.PathEscape(ref)+"/dependencies", "")
	require.Equal(t, http.StatusOK, status, "GET dependencies for %q: %s", ref, raw)
	var out depsRef
	require.NoError(t, json.Unmarshal(raw, &out), "body was %q", raw)
	return out
}

// depIDsOf projects entry ids so the by-id and by-slug arms can be compared
// directly instead of element by element.
func depIDsOf(xs []depRefEntry) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}

func TestDependencyEndpointsResolveSlugs(t *testing.T) {
	s := newDepSlugStack(t)

	blocker := s.seedWI(t, "rotate expired TLS certificates on the edge proxies")
	blocked := s.seedWI(t, "backfill missing avatar thumbnails for legacy users", blocker.ID)
	spare := s.seedWI(t, "archive stale feature flags older than one year")

	require.Equal(t, "blocked", blocked.Status, "fixture: blocked_by must derive status=blocked")

	// ── The reporter's exact call. Red before the fix. ────────────────────────
	// By id and by slug must return the same answer. Asserting only "the slug
	// call returns one entry" would be enough to go red here, but comparing the
	// two arms is what makes the test say the right thing when it fails: the
	// data is there, the reference form is what changes the answer.
	t.Run("list_by_slug_returns_the_same_edges_as_by_id", func(t *testing.T) {
		byID := s.listDeps(t, blocked.ID)
		require.Len(t, byID.BlockedBy, 1,
			"fixture check: reading by canonical id must already show the blocker")

		bySlug := s.listDeps(t, blocked.Slug)
		require.Len(t, bySlug.BlockedBy, 1,
			"GET dependencies by slug (%s) returned an empty blocked_by while the SAME work item read by id "+
				"(%s) returns %d — the handler resolved the slug for its access check and then queried with the "+
				"raw parameter, so a work item with dependencies is indistinguishable from one without",
			blocked.Slug, blocked.ID, len(byID.BlockedBy))
		assert.Equal(t, depIDsOf(byID.BlockedBy), depIDsOf(bySlug.BlockedBy),
			"the slug and the id must name the same blockers")
		assert.Equal(t, blocker.ID, bySlug.BlockedBy[0].ID)
	})

	// The blocking end was the reporter's second query and fails for the same
	// reason, through the other SQL branch of ListDependencies.
	t.Run("list_by_slug_reports_the_blocking_end_too", func(t *testing.T) {
		bySlug := s.listDeps(t, blocker.Slug)
		require.Len(t, bySlug.Blocking, 1,
			"reading the BLOCKER by slug (%s) shows nothing blocked by it, so 'what does finishing this "+
				"unblock' is unanswerable from the blocker's side as well", blocker.Slug)
		assert.Equal(t, blocked.ID, bySlug.Blocking[0].ID)
		assert.Empty(t, bySlug.BlockedBy, "nothing blocks the blocker")
	})

	// The write side of the same class: loud rather than silent, but no more
	// usable. Pre-fix this is a 404 naming a work item that demonstrably exists
	// (the fixture created it and the handler resolved it one line earlier).
	t.Run("create_by_slug_creates_a_readable_edge", func(t *testing.T) {
		status, raw := s.req(t, http.MethodPost,
			"/v1/work_items/"+url.PathEscape(spare.Slug)+"/dependencies",
			fmt.Sprintf(`{"blocking_wi_id":%q,"kind":"blocks"}`, blocker.Slug))
		require.Equal(t, http.StatusCreated, status,
			"POST dependencies with slugs on both ends answered %d: %s", status, raw)

		deps := s.listDeps(t, spare.ID)
		require.Len(t, deps.BlockedBy, 1, "the edge created by slug must be readable by id")
		assert.Equal(t, blocker.ID, deps.BlockedBy[0].ID,
			"the stored edge must reference the canonical id, not the slug it was created with")
	})

	// Depends on the subtest above having created the edge.
	t.Run("delete_by_slug_removes_the_edge_created_by_slug", func(t *testing.T) {
		status, raw := s.req(t, http.MethodDelete,
			"/v1/work_items/"+url.PathEscape(spare.Slug)+
				"/dependencies/"+url.PathEscape(blocker.Slug)+"/blocks", "")
		require.Equal(t, http.StatusOK, status,
			"DELETE dependencies with slugs answered %d — a 404 here means the DELETE matched no row "+
				"because it compared a slug against a column holding canonical ids, and the caller is told "+
				"the dependency does not exist while it does: %s", status, raw)

		deps := s.listDeps(t, spare.ID)
		assert.Empty(t, deps.BlockedBy, "the edge must actually be gone, not merely reported gone")
	})

	// The fourth instance, and one aihub#357 names in its own impact list:
	// PredictConflicts' will_unlock query compares dep.blocking_wi_id — an id
	// column — against the caller's raw work_item_id. The project lookup ten
	// lines above it in the same function already wrote `id=$1 OR slug=$1`, so
	// half the function accepted a slug and the other half silently answered
	// `"will_unlock": []` for it. Same silent shape as the list endpoint: an
	// empty array is what a wi that unblocks nothing also returns.
	//
	// This arm leans on `blocked` still being blocked by `blocker` — the subtest
	// above only removed the `spare` edge.
	t.Run("predict_conflicts_will_unlock_by_slug_matches_by_id", func(t *testing.T) {
		unlockIDs := func(ref string) []string {
			t.Helper()
			status, raw := s.req(t, http.MethodPost, "/v1/conflicts/predict",
				fmt.Sprintf(`{"work_item_id":%q,"declared_resources":[]}`, ref))
			require.Equal(t, http.StatusOK, status, "predict for %q: %s", ref, raw)
			var out struct {
				WillUnlock []struct {
					ID string `json:"id"`
				} `json:"will_unlock"`
			}
			require.NoError(t, json.Unmarshal(raw, &out), "body was %q", raw)
			ids := make([]string, 0, len(out.WillUnlock))
			for _, w := range out.WillUnlock {
				ids = append(ids, w.ID)
			}
			return ids
		}

		byID := unlockIDs(blocker.ID)
		require.Contains(t, byID, blocked.ID,
			"fixture check: finishing the blocker must be predicted to unblock %s", blocked.Slug)

		bySlug := unlockIDs(blocker.Slug)
		assert.Equal(t, byID, bySlug,
			"predicting by slug (%s) returned a different will_unlock than the same work item by id (%s) — "+
				"an empty list here is indistinguishable from a claim that unblocks nothing",
			blocker.Slug, blocker.ID)
	})
}
