package server

// DB-gated probes for the aihub#143 Watching scope and the watch/unwatch write
// path. See the "aihub#143 watching scope DB tests" step in
// .github/workflows/ci.yml.
//
//	AIHUB_TEST_DB='postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/server/ -run 'Watch' -v
//
// ─── Why these are DB tests and not handler unit tests ──────────────────────
//
// The unit tests in ui_handlers_wi_watching_test.go pin that ?watching=1 reaches
// domain.ListWorkItemsFilter.WatcherUserID. That is the WIRING, and it is worth
// pinning, but it cannot answer the question this file exists for:
//
//	does a watch row on a work item in a project the viewer cannot read
//	put that work item on their screen?
//
// Nothing short of a real query can answer that, because the answer lives in how
// two SQL predicates compose — the project allow-list and the wi_watches
// semi-join — and a fake ListWorkItems returns whatever the fake was told to.
// A test whose oracle is "the filter struct had the right fields set" would pass
// against a build where those fields are ANDed wrongly, ORed, or where one is
// dropped downstream.
//
// The negative case is the point. A wi_watches row OUTLIVES the access that
// created it: nothing removes watches when a project membership is revoked. So
// "watched" and "readable" are independent facts about a row, and the fixture
// below constructs exactly the state where they disagree.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// watchFixture is the seeded world every probe in this file reads.
//
// Two projects and four work items, arranged so each of the four
// (watched?, readable?) combinations is present exactly once. Without all four
// the probes are not interpretable: "the hidden one is absent" proves nothing if
// everything is absent, and "the visible one is present" proves nothing if
// everything is present.
type watchFixture struct {
	userID string
	// readable is a project the viewer has a role on; hidden is one they do not.
	readable, hidden string
	// Slugs, by their two facts.
	watchedReadable   string // watched   + readable  -> MUST appear
	unwatchedReadable string // unwatched + readable  -> must NOT appear under ?watching=1
	watchedHidden     string // watched   + NOT readable -> MUST NOT appear, ever  🔴
	unwatchedHidden   string // unwatched + NOT readable -> must NOT appear (control)
	// IDs parallel to the slugs above, for the write-path probes.
	watchedReadableID, unwatchedReadableID, watchedHiddenID string
}

// seedWatchFixture builds the world described on watchFixture.
//
// Work items are inserted with raw SQL rather than domain.CreateWorkItem for the
// reason seedListParamsFixture gives: CreateWorkItem runs goal-similarity dedup,
// and four items in two projects would have to be mutually dissimilar for
// reasons that have nothing to do with what is under test.
func seedWatchFixture(t *testing.T, pool *pgxpool.Pool) watchFixture {
	t.Helper()
	ctx := context.Background()

	// Two-character prefixes, not readable ones: projects.name is
	// CHECK (^[a-z][a-z0-9_-]{0,39}$) and testname.Sanitize is sized for exactly
	// a 2-char prefix (maxLen 37 + 2 = 39). "pw_ok_" overruns it and Postgres
	// rejects the row — which is how this comment came to exist.
	base := testname.Sanitize(t.Name())
	f := watchFixture{
		userID:   "u_" + base,
		readable: "po" + base, // project the viewer CAN read
		hidden:   "pn" + base, // project the viewer canNot read
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO users(id,email,display_name) VALUES($1,$1||'@test.local',$1)
		 ON CONFLICT (id) DO NOTHING`, f.userID)
	require.NoError(t, err)
	for _, p := range []string{f.readable, f.hidden} {
		_, err = pool.Exec(ctx,
			`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`,
			p, f.userID)
		require.NoError(t, err)
	}

	// Child-to-parent, so a rerun starts from a known-empty pair of projects.
	for _, p := range []string{f.readable, f.hidden} {
		for _, q := range []string{
			`DELETE FROM wi_watches WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
			`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
			`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
			`DELETE FROM wi_step_state WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
			`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
			`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
			`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
			`DELETE FROM work_items WHERE project=$1`,
		} {
			_, derr := pool.Exec(ctx, q, p)
			require.NoError(t, derr)
		}
	}

	seq := int64(9100)
	insert := func(project, goal string, watched bool) (id, slug string) {
		id = domain.NewID("wi")
		seq++
		// status queued + no current attempt puts the row in the "unclaimed"
		// segment, which is what /ui/wi selects when no ?seg= is given. Every
		// probe below therefore reads the default view.
		_, ierr := pool.Exec(ctx, `
			INSERT INTO work_items (
				id, seq, project, scenario, goal, source, wi_type, priority,
				requires_human_session, labels, status,
				declared_resources, reporter_user_id, reporter_display,
				parent_work_item_id, attrs, content
			) VALUES ($1,$2,$3,'coding',$4,'human','feature','normal',
				false,'{}','queued',
				'[]',$5,$5,
				NULL,'{}',NULL)`, id, seq, project, goal, f.userID)
		require.NoError(t, ierr)
		if watched {
			_, werr := pool.Exec(ctx,
				`INSERT INTO wi_watches (user_id, work_item_id) VALUES ($1,$2)`, f.userID, id)
			require.NoError(t, werr)
		}
		return id, project + "#" + itoa64(seq)
	}

	f.watchedReadableID, f.watchedReadable = insert(f.readable, "watched and readable", true)
	f.unwatchedReadableID, f.unwatchedReadable = insert(f.readable, "unwatched but readable", false)
	// 🔴 The row the whole file is about: a live watch on a work item in a
	// project this viewer has no role on. Reachable in production by watching
	// something and then being removed from the project.
	f.watchedHiddenID, f.watchedHidden = insert(f.hidden, "watched but NOT readable", true)
	_, f.unwatchedHidden = insert(f.hidden, "neither watched nor readable", false)

	return f
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// watchViewer is the fixture's user as the UI sees them: a plain writer holding
// a role on the readable project ONLY. Not an admin — checkProjectAccessSoft and
// availableProjectsForUI both short-circuit for admins, so an admin viewer would
// make every probe here pass for the wrong reason.
func watchViewer(f watchFixture) *UserContext {
	return &UserContext{
		UserID:       f.userID,
		DisplayName:  f.userID,
		Role:         "writer",
		ProjectRoles: map[string]string{f.readable: "writer"},
	}
}

// wiListSlugs renders /ui/wi with the given query and returns the slugs of the
// rows on screen, sorted.
//
// It reads the RENDERED PAGE rather than an intermediate struct on purpose: the
// claim being tested is "this work item is not on the user's screen", and every
// layer between the query and the screen (segment bucketing, the Mine in-memory
// pass, the template's own conditionals) is a place a row could reappear.
func wiListSlugs(t *testing.T, pool *pgxpool.Pool, rawQuery string, uc *UserContext) []string {
	t.Helper()
	body := renderWIListWithPool(t, pool, "/ui/wi?"+rawQuery, uc)
	out := wiRowIDRe.FindAllStringSubmatch(body, -1)
	slugs := make([]string, 0, len(out))
	for _, m := range out {
		slugs = append(slugs, m[1])
	}
	sort.Strings(slugs)
	return slugs
}

// wiRowIDRe matches the per-row id cell the list template emits.
var wiRowIDRe = regexp.MustCompile(`<span class="id">([^<]*)</span>`)

// renderWIListWithPool is renderWIList's live-database sibling.
func renderWIListWithPool(t *testing.T, pool *pgxpool.Pool, url string, uc *UserContext) string {
	t.Helper()
	tmpl := pageTemplate("wi_list.html.tmpl")
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user", uc)
	require.NoError(t, handleUIWIList(pool, tmpl)(c), "handler error for %s", url)
	require.Equal(t, http.StatusOK, rec.Code, "status for %s", url)
	return rec.Body.String()
}

// TestWatchingScope_ListsOnlyWatchedItems is the positive half, with its own
// control: the same request WITHOUT ?watching=1 must show more, or the probe
// below cannot distinguish "the scope filtered" from "the page was empty".
func TestWatchingScope_ListsOnlyWatchedItems(t *testing.T) {
	pool := setupStepTestDB(t)
	f := seedWatchFixture(t, pool)
	uc := watchViewer(f)

	base := "project=" + f.readable + "&limit=100"

	all := wiListSlugs(t, pool, base, uc)
	require.ElementsMatch(t, []string{f.watchedReadable, f.unwatchedReadable}, all,
		"control: without the watching scope the readable project shows both of its items")

	watched := wiListSlugs(t, pool, base+"&watching=1", uc)
	require.Equal(t, []string{f.watchedReadable}, watched,
		"?watching=1 must show the watched item and drop the unwatched one")
}

// TestWatchingScope_ProjectAccessStillBounds is the authorization negative case.
//
// 🔴 A wi_watches row must not be a read grant. The fixture holds a live watch on
// a work item in a project this viewer has no role on; it must not appear in the
// cross-project Watching view, and the assertion is the ABSOLUTE "this slug is
// not on the page", not a relative row count that a smaller page would also
// satisfy.
//
// The positive control in the same request is what makes the absence mean
// something: the readable watched item MUST be there. If the query were broken
// in the other direction — returning nothing at all — a bare "the hidden slug is
// absent" assertion would pass while the feature was dead.
func TestWatchingScope_ProjectAccessStillBounds(t *testing.T) {
	pool := setupStepTestDB(t)
	f := seedWatchFixture(t, pool)
	uc := watchViewer(f)

	// __all__ is the widest scope the UI can ask for; if the bound holds here it
	// holds in single-project mode, which additionally runs checkProjectAccessSoft.
	got := wiListSlugs(t, pool, "project=__all__&watching=1&limit=100", uc)

	require.Contains(t, got, f.watchedReadable,
		"positive control: the watched item in a readable project must be listed, "+
			"otherwise the absence assertions below are vacuous")
	require.NotContains(t, got, f.watchedHidden,
		"🔴 a watch row on a work item in a project the viewer cannot read must NOT "+
			"put it on their screen — the wi_watches predicate has to be ANDed with the "+
			"project allow-list, never substituted for it")
	require.NotContains(t, got, f.unwatchedHidden, "control: unwatched + unreadable")
	require.NotContains(t, got, f.unwatchedReadable, "control: unwatched + readable")
}

// TestWatchingScope_ViewAllWithNoAccessibleProjectsListsNothing covers the hole
// found while wiring the scope: an EMPTY project allow-list produced a query
// with no project predicate at all — every work item in the database — instead
// of an empty page.
//
// It is reachable without the Watching scope (hence the first sub-case), but it
// is fatal WITH it, because the whole safety argument for wi_watches is that it
// is intersected with a project predicate that is always there.
func TestWatchingScope_ViewAllWithNoAccessibleProjectsListsNothing(t *testing.T) {
	pool := setupStepTestDB(t)
	f := seedWatchFixture(t, pool)

	// A real, authenticated user who is a member of nothing. Not an admin.
	stranger := &UserContext{
		UserID:       f.userID,
		DisplayName:  f.userID,
		Role:         "writer",
		ProjectRoles: map[string]string{},
	}

	t.Run("plain view-all", func(t *testing.T) {
		got := wiListSlugs(t, pool, "project=__all__&limit=100", stranger)
		require.Empty(t, got,
			"a caller with access to no project asked for every project and must get an "+
				"empty page; an empty allow-list is 'bound to nothing', not 'unbounded'")
	})

	t.Run("watching view-all", func(t *testing.T) {
		// This user genuinely watches two work items — so an unbounded query
		// would return them, and "empty" here is a real assertion rather than a
		// tautology about a user who watches nothing.
		got := wiListSlugs(t, pool, "project=__all__&watching=1&limit=100", stranger)
		require.Empty(t, got,
			"watching two work items in projects they cannot read must not make either visible")
	})
}

// TestWatchToggle_RoundTripsAndIsIdempotent exercises the write path end to end
// through the HTTP handlers, and asserts on the DB row rather than on the button
// text: the button is rendered from what the handler intended to write, so
// reading it back would confirm the intent and not the effect.
func TestWatchToggle_RoundTripsAndIsIdempotent(t *testing.T) {
	pool := setupStepTestDB(t)
	f := seedWatchFixture(t, pool)
	uc := watchViewer(f)

	isWatched := func() bool {
		var found bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM wi_watches WHERE user_id=$1 AND work_item_id=$2)`,
			f.userID, f.unwatchedReadableID).Scan(&found))
		return found
	}

	require.False(t, isWatched(), "fixture precondition")

	require.Equal(t, http.StatusOK, watchRequest(t, pool, http.MethodPost, f.unwatchedReadableID, uc))
	require.True(t, isWatched(), "POST must create the watch")

	// Twice. A double click, or a retry after a dropped response, must converge
	// rather than error or toggle back off.
	require.Equal(t, http.StatusOK, watchRequest(t, pool, http.MethodPost, f.unwatchedReadableID, uc))
	require.True(t, isWatched(), "a second POST is a no-op, not an error and not a toggle")

	require.Equal(t, http.StatusOK, watchRequest(t, pool, http.MethodDelete, f.unwatchedReadableID, uc))
	require.False(t, isWatched(), "DELETE must remove the watch")

	require.Equal(t, http.StatusOK, watchRequest(t, pool, http.MethodDelete, f.unwatchedReadableID, uc))
	require.False(t, isWatched(), "a second DELETE is a no-op")
}

// TestWatchToggle_RefusesAnUnreadableWorkItem pins the write-side authorization.
//
// An unauthorized watch could never make a work item visible (the read scope
// intersects with the project allow-list — the test above), but it would still
// let any session write a row naming any work item id, which both confirms the
// id exists and accumulates state on another team's item. So the write is
// authorized on its own terms, and this asserts BOTH halves: the status code AND
// that no row was written. The status alone would pass against a handler that
// answers 403 after committing.
func TestWatchToggle_RefusesAnUnreadableWorkItem(t *testing.T) {
	pool := setupStepTestDB(t)
	f := seedWatchFixture(t, pool)
	uc := watchViewer(f)

	// Start from a clean slate on the hidden item for THIS assertion: the
	// fixture deliberately pre-watches it, and a DELETE that is refused would
	// otherwise be indistinguishable from one that worked.
	_, err := pool.Exec(context.Background(),
		`DELETE FROM wi_watches WHERE user_id=$1 AND work_item_id=$2`, f.userID, f.watchedHiddenID)
	require.NoError(t, err)

	code := watchRequest(t, pool, http.MethodPost, f.watchedHiddenID, uc)
	require.Equal(t, http.StatusForbidden, code,
		"watching a work item in a project the caller has no role on must be refused")

	var found bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM wi_watches WHERE user_id=$1 AND work_item_id=$2)`,
		f.userID, f.watchedHiddenID).Scan(&found))
	require.False(t, found, "the refused request must not have written a row")
}

// watchRequest drives POST/DELETE /ui/wi/:id/watch and returns the status code.
func watchRequest(t *testing.T, pool *pgxpool.Pool, method, idOrSlug string, uc *UserContext) int {
	t.Helper()
	tmpl := partialTemplate("wi_watch_toggle.html.tmpl")
	h := handleUIWIWatchToggle(pool, tmpl, method == http.MethodPost)

	e := echo.New()
	req := httptest.NewRequest(method, "/ui/wi/"+idOrSlug+"/watch", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(idOrSlug)
	c.Set("user", uc)
	require.NoError(t, h(c))
	return rec.Code
}
