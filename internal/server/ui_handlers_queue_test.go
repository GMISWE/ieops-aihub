package server

// Unit tests for the /ui/queue handlers.
//
// Strategy: override the package-level getQueueFn variable with a synthetic
// fixture so we never hit the database. setUser (defined in
// router_auth_test.go) injects a fully-formed UserContext.
//
// Tests exercise:
//   - the /ui/queue full page now 302-redirects to /ui/wi (the queue is
//     embedded there as a collapsible block)
//   - the redirect preserves ?project=
//   - partial endpoint renders all six LCRS sections + omits layout chrome
//   - the /ui/queue route is wired correctly on a real echo group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// fixtureQueue returns a *domain.ReadyQueue with at least one entry in every
// segment so the template's empty-state branch is exercised AND every list
// renders at least one row. The IDs/slugs are deterministic so test bodies
// can grep for them if they want.
func fixtureQueue() *domain.ReadyQueue {
	gotype := "feature"
	return &domain.ReadyQueue{
		Items: []domain.ReadyItem{
			{ID: "wi_item1", Slug: "items-one", WIType: &gotype, Priority: "high", Goal: "ship the thing"},
		},
		Running: []domain.RunningItem{
			{ID: "wi_run1", Slug: "running-one", Goal: "actively cooking", OwnerDisplay: "alice", LastActiveAt: "2026-05-26T10:00:00Z"},
		},
		Stalled: []domain.StalledItem{
			{ID: "wi_stall1", Slug: "stalled-one", StallReason: "blocked on external review", StalledSince: "2026-05-25T10:00:00Z", LastActorDisplay: "bob"},
		},
		Paused: []domain.PausedItem{
			{ID: "wi_pause1", Slug: "paused-one", PausedSince: "2026-05-24T10:00:00Z", LastActorDisplay: "carol"},
		},
		NeedsHumanSession: []domain.ReadyItem{
			{ID: "wi_human1", Slug: "needs-human-one", WIType: &gotype, Priority: "urgent", Goal: "needs a human eye"},
		},
		Unclassified: []domain.ReadyItem{
			{ID: "wi_unc1", Slug: "unc-one", WIType: &gotype, Priority: "low", Goal: "unclassified item"},
		},
	}
}

// withQueueFnOverride swaps getQueueFn for the duration of a test. The fn
// receives the same args as the real one but always returns the supplied
// queue. Returns a cleanup func.
func withQueueFnOverride(q *domain.ReadyQueue) func() {
	prev := getQueueFn
	getQueueFn = func(_ context.Context, _ *pgxpool.Pool, _ string, _ int) (*domain.ReadyQueue, *domain.AihubError) {
		return q, nil
	}
	return func() { getQueueFn = prev }
}

// userWithProjects returns a writer-level UserContext that has viewer access
// to the named projects.
func userWithProjects(projects ...string) *UserContext {
	roles := map[string]string{}
	for _, p := range projects {
		roles[p] = "viewer"
	}
	return &UserContext{
		UserID:       "u_test",
		Email:        "test@example.com",
		DisplayName:  "Test User",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: roles,
		APIKeyID:     "k_test",
	}
}

// userNoProjects is a writer user with zero project memberships — exercises
// the no-projects hint.
func userNoProjects() *UserContext {
	return &UserContext{
		UserID:       "u_lonely",
		Email:        "lonely@example.com",
		DisplayName:  "Lonely User",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: map[string]string{},
		APIKeyID:     "k_lonely",
	}
}

// newQueueRequest builds an echo context aimed at /ui/queue. The handler is
// invoked directly so we don't need the full router; we just need
// setUser(c, ...) to mimic what RequireUISession does.
func newQueueRequest(t *testing.T, target string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, uc)
	return c, rec
}

// TestUIQueue_Redirects_NoProject asserts that GET /ui/queue (no ?project=)
// 302-redirects to the bare /ui/wi list page. No DB call, no user needed for
// the redirect itself.
func TestUIQueue_Redirects_NoProject(t *testing.T) {
	c, rec := newQueueRequest(t, "/ui/queue", userNoProjects())

	if err := handleUIQueue()(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/wi" {
		t.Errorf("Location: got %q, want /ui/wi", loc)
	}
}

// TestUIQueue_Redirects_PreservesProject asserts that ?project= is carried
// through to the /ui/wi redirect target so bookmarks keep working.
func TestUIQueue_Redirects_PreservesProject(t *testing.T) {
	c, rec := newQueueRequest(t, "/ui/queue?project=testproject", userWithProjects("testproject"))

	if err := handleUIQueue()(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/wi?project=testproject" {
		t.Errorf("Location: got %q, want /ui/wi?project=testproject", loc)
	}
}

// withListFnForStrip overrides listWorkItemsFn with a fixed item set so the
// count strip can be asserted deterministically without a DB. Returns a
// cleanup func. (The queue partial now derives Running / Needs you / Unclaimed
// from the same grouping the wi list uses — aihub#129 review-round-2 #2.)
func withListFnForStrip(items []*domain.WorkItem) func() {
	prev := listWorkItemsFn
	listWorkItemsFn = func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: items}, nil
	}
	return func() { listWorkItemsFn = prev }
}

// TestUIQueuePartial_CountStripOnly asserts the partial endpoint renders ONLY
// the four-cell count strip (Running / Needs you / Unclaimed / Stalled) and
// omits layout chrome so htmx can innerHTML-swap it.
//
// aihub#129 review-round-1 #7: the queue partial used to also render the full
// grouped row lists, which duplicated the wi list below it. The rows now live
// ONLY in the wi list — so this test asserts the partial has the count cells
// but does NOT re-render the individual fixture rows.
//
// review-round-2 #2: the strip counts are now sourced from groupListRows (the
// same grouping the list uses), so the partial calls listWorkItemsFn — we fake
// it here so the strip is deterministic and never hits the DB.
func TestUIQueuePartial_CountStripOnly(t *testing.T) {
	defer withQueueFnOverride(fixtureQueue())()
	// A small ownerless item set — enough to exercise the strip without a DB.
	// (With nil pool the current-attempt owner lookup is empty, so the queued
	// item lands in Unclaimed; the structural assertions below don't depend on
	// the exact per-cell numbers.)
	defer withListFnForStrip([]*domain.WorkItem{
		{ID: "wi_run", Slug: "row-run", Project: "testproject", Status: "running"},
		{ID: "wi_pause", Slug: "row-pause", Project: "testproject", Status: "paused"},
		{ID: "wi_pool", Slug: "row-pool", Project: "testproject", Status: "queued"},
	})()

	tmpl := partialTemplate("queue_section.html.tmpl")
	c, rec := newQueueRequest(t, "/ui/queue/partial?project=testproject", userWithProjects("testproject"))

	if err := handleUIQueuePartial(nil, tmpl)(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("partial should NOT include <!DOCTYPE html>; got:\n%s", body)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("partial should NOT include <html>; got:\n%s", body)
	}

	// The four count-strip cells must be present.
	wantCells := []string{"Running", "Needs you", "Unclaimed", "Stalled"}
	for _, s := range wantCells {
		if !strings.Contains(body, s) {
			t.Errorf("count strip missing cell label %q", s)
		}
	}

	// The strip container is the only structural element — no grouped row list.
	if !strings.Contains(body, `class="qstrip"`) {
		t.Errorf("partial missing the .qstrip count strip")
	}
	if strings.Contains(body, `class="list"`) {
		t.Errorf("partial should NOT render the grouped .list rows anymore; got:\n%s", body)
	}
	// No cell carries the old highlighted-background class anymore (#5).
	if strings.Contains(body, `class="qcell hot"`) {
		t.Errorf("Needs you cell must not be grey-highlighted (qcell hot); got:\n%s", body)
	}

	// The fixture row slugs must NOT be re-rendered here — they belong to the
	// wi list below the strip.
	notSlugs := []string{"row-run", "row-pause", "row-pool"}
	for _, s := range notSlugs {
		if strings.Contains(body, s) {
			t.Errorf("count strip should NOT contain fixture row slug %q", s)
		}
	}
}

func TestUIQueueRoute_Mounted(t *testing.T) {
	defer withQueueFnOverride(fixtureQueue())()
	// The partial now derives strip counts from the wi list grouping, so it
	// calls listWorkItemsFn — fake it to avoid a nil-pool DB hit.
	defer withListFnForStrip([]*domain.WorkItem{})()

	// Build a minimal echo with a /ui group that injects a user before our
	// handler — same shape as RegisterUIRoutes but without the real session
	// middleware.
	e := echo.New()
	g := e.Group("/ui", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			setUser(c, userWithProjects("testproject"))
			return next(c)
		}
	})
	// Reset queueTmpl so the register call rebuilds for this test (defensive
	// — package-level cache could already be populated by another test).
	queueTmpl = nil
	registerUIQueueHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/queue?project=testproject", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("GET /ui/queue: expected 302, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/wi?project=testproject" {
		t.Errorf("expected redirect to /ui/wi?project=testproject, got %q", loc)
	}

	// Partial endpoint should also be wired.
	req2 := httptest.NewRequest(http.MethodGet, "/ui/queue/partial?project=testproject", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /ui/queue/partial: expected 200, got %d", rec2.Code)
	}
}
