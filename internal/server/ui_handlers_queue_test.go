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

// TestUIQueuePartial_RendersSixSections_NoLayout asserts the partial endpoint
// renders all six LCRS sections (the coverage that used to live on the full
// page) and omits layout chrome so htmx can innerHTML-swap it.
func TestUIQueuePartial_RendersSixSections_NoLayout(t *testing.T) {
	defer withQueueFnOverride(fixtureQueue())()

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

	wantSections := []string{"Running", "Items", "Needs you", "Stalled", "Paused", "Unclassified"}
	for _, s := range wantSections {
		if !strings.Contains(body, s) {
			t.Errorf("partial missing section heading %q", s)
		}
	}

	// Spot check that fixture entries actually landed.
	wantSlugs := []string{"running-one", "items-one", "needs-human-one", "stalled-one", "paused-one", "unc-one"}
	for _, s := range wantSlugs {
		if !strings.Contains(body, s) {
			t.Errorf("partial missing fixture slug %q", s)
		}
	}
}

func TestUIQueueRoute_Mounted(t *testing.T) {
	defer withQueueFnOverride(fixtureQueue())()

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
