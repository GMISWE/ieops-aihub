package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// --- shared fixtures ---------------------------------------------------------

// wiTestUser is a writer with access to project "p1".
func wiTestUser() *UserContext {
	return &UserContext{
		UserID:      "u_alice",
		DisplayName: "Alice",
		Role:        "writer",
		ProjectRoles: map[string]string{
			"p1": "writer",
		},
		APIKeyID: "k_alice",
	}
}

// wiInjectUser is a middleware that stuffs a user into echo ctx so handlers
// can call GetUser without spinning up the cookie/session machinery.
func wiInjectUser(u *UserContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(ctxUser), u)
			return next(c)
		}
	}
}

// stringPtr returns a pointer to s. Convenience for *string fields.
func wiStrPtr(s string) *string { return &s }

// TestSegmentFor pins the LCRS segment precedence (aihub#185): terminal -> stalled
// -> running -> unclaimed -> needsyou -> paused -> (fallback) unclaimed.
func TestSegmentFor(t *testing.T) {
	stalled := map[string]bool{"wi_stall": true}
	cases := []struct {
		name   string
		row    *wiListRow
		viewer string
		want   string
	}{
		{"queued unowned -> unclaimed", &wiListRow{ID: "a", Status: "queued"}, "Alice", "unclaimed"},
		{"blocked unowned -> unclaimed", &wiListRow{ID: "b", Status: "blocked"}, "Alice", "unclaimed"},
		{"running alive -> running", &wiListRow{ID: "c", Status: "running", OwnerDisplay: "Alice"}, "Alice", "running"},
		{"running stalled -> stalled", &wiListRow{ID: "wi_stall", Status: "running", OwnerDisplay: "Bob"}, "Alice", "stalled"},
		{"paused mine -> needsyou", &wiListRow{ID: "d", Status: "paused", OwnerDisplay: "Alice"}, "Alice", "needsyou"},
		{"blocked mine -> needsyou", &wiListRow{ID: "e", Status: "blocked", OwnerDisplay: "Alice"}, "Alice", "needsyou"},
		{"paused other -> paused", &wiListRow{ID: "f", Status: "paused", OwnerDisplay: "Bob"}, "Alice", "paused"},
		{"blocked other -> unclaimed (fallback)", &wiListRow{ID: "g", Status: "blocked", OwnerDisplay: "Bob"}, "Alice", "unclaimed"},
		{"wrapped -> done", &wiListRow{ID: "h", Status: "wrapped"}, "Alice", "done"},
		{"cancelled -> done", &wiListRow{ID: "i", Status: "cancelled"}, "Alice", "done"},
		{"failed -> done", &wiListRow{ID: "j", Status: "failed", OwnerDisplay: "Bob"}, "Alice", "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := segmentFor(tc.row, tc.viewer, stalled); got != tc.want {
				t.Errorf("segmentFor(%+v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// TestSegmentListRows pins the segment bucketing + counts (aihub#185), including
// Mine-view owner scoping: others' rows drop from every segment except unclaimed,
// and the needsyou rows get the NeedsYou flag (the .row.hot left bar).
func TestSegmentListRows(t *testing.T) {
	newRows := func() []*wiListRow {
		return []*wiListRow{
			{ID: "1", Status: "queued"},                        // unclaimed
			{ID: "2", Status: "queued"},                        // unclaimed
			{ID: "3", Status: "running", OwnerDisplay: "Alice"}, // running (mine)
			{ID: "4", Status: "running", OwnerDisplay: "Bob"},   // running (other)
			{ID: "5", Status: "paused", OwnerDisplay: "Alice"},  // needsyou
			{ID: "6", Status: "paused", OwnerDisplay: "Bob"},    // paused (other)
		}
	}
	stalled := map[string]bool{}

	// All view: every segment populated.
	cAll, byAll := segmentListRows(newRows(), "Alice", false, stalled)
	for seg, want := range map[string]int{"unclaimed": 2, "running": 2, "needsyou": 1, "paused": 1} {
		if cAll[seg] != want {
			t.Errorf("All view: count[%q] = %d, want %d", seg, cAll[seg], want)
		}
	}
	if len(byAll["needsyou"]) != 1 || !byAll["needsyou"][0].NeedsYou {
		t.Errorf("All view: needsyou row should carry NeedsYou=true")
	}

	// Mine view: Bob's running + paused drop; the unclaimed pool stays.
	cMine, _ := segmentListRows(newRows(), "Alice", true, stalled)
	for seg, want := range map[string]int{"unclaimed": 2, "running": 1, "needsyou": 1, "paused": 0} {
		if cMine[seg] != want {
			t.Errorf("Mine view: count[%q] = %d, want %d", seg, cMine[seg], want)
		}
	}
}

// --- ListWorkItems fakes -----------------------------------------------------

// withFakeListWI swaps the package-level listWorkItemsFn for the duration of
// the test. Defers restore.
func withFakeListWI(t *testing.T, fn func(context.Context, *pgxpool.Pool, string, domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError)) {
	t.Helper()
	prev := listWorkItemsFn
	listWorkItemsFn = fn
	t.Cleanup(func() { listWorkItemsFn = prev })
}

func withFakeGetWI(t *testing.T, fn func(context.Context, *pgxpool.Pool, string) (*domain.WorkItem, *domain.AihubError)) {
	t.Helper()
	prev := getWorkItemFn
	getWorkItemFn = fn
	t.Cleanup(func() { getWorkItemFn = prev })
}

func withFakeListDeps(t *testing.T, fn func(context.Context, *pgxpool.Pool, string, map[string]string) (*domain.DependenciesResponse, *domain.AihubError)) {
	t.Helper()
	prev := listDependenciesFn
	listDependenciesFn = fn
	t.Cleanup(func() { listDependenciesFn = prev })
}

func withFakeParentRef(t *testing.T, fn func(context.Context, *pgxpool.Pool, string, map[string]string) (*domain.WIRef, *domain.AihubError)) {
	t.Helper()
	prev := getParentRefFn
	getParentRefFn = fn
	t.Cleanup(func() { getParentRefFn = prev })
}

func withFakeListChildren(t *testing.T, fn func(context.Context, *pgxpool.Pool, string, map[string]string) ([]domain.WIRef, *domain.AihubError)) {
	t.Helper()
	prev := listChildrenFn
	listChildrenFn = fn
	t.Cleanup(func() { listChildrenFn = prev })
}

// noParentNoChildren wires both parent/children seams to empty results. The
// detail handler's fan-out calls these on every request, so detail tests that
// do not exercise the parent/children paths must still stub them (nil pool
// would otherwise hit the real DB query). Call at the top of such tests.
func noParentNoChildren(t *testing.T) {
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) ([]domain.WIRef, *domain.AihubError) {
		return []domain.WIRef{}, nil
	})
}

func withFakeListEvents(t *testing.T, fn func(context.Context, *pgxpool.Pool, *domain.ListEventsFilter) (*domain.ListEventsResponse, error)) {
	t.Helper()
	prev := listEventsFn
	listEventsFn = fn
	t.Cleanup(func() { listEventsFn = prev })
}

func withFakeRecall(t *testing.T, fn func(context.Context, *pgxpool.Pool, *domain.RecallRequest) (*domain.RecallResponse, error)) {
	t.Helper()
	prev := recallFn
	recallFn = fn
	t.Cleanup(func() { recallFn = prev })
}

// --- list page ---------------------------------------------------------------

// TestUIWIList_FiltersByStatus asserts that the ?status= query param is
// forwarded into the ListWorkItems filter exactly once and that an unknown
// status value is silently dropped (so a hostile param can't break the page).
func TestUIWIList_FiltersByStatus(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1&status=running", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(captured.Status) != 1 || captured.Status[0] != "running" {
		t.Fatalf("filter status: got %v, want [running]", captured.Status)
	}
	if captured.Limit != 50 {
		t.Fatalf("default limit: got %d, want 50", captured.Limit)
	}
}

// TestUIWIList_FiltersByStatus_Empty_DefaultsToActiveSet asserts the default
// (no ?status=) behaviour: queued + running + paused + blocked.
func TestUIWIList_FiltersByStatus_Empty_DefaultsToActiveSet(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	want := map[string]bool{"queued": true, "running": true, "paused": true, "blocked": true}
	if len(captured.Status) != len(want) {
		t.Fatalf("default status set: got %v, want 4 entries", captured.Status)
	}
	for _, s := range captured.Status {
		if !want[s] {
			t.Errorf("unexpected status %q in default set", s)
		}
	}
}

// TestUIWIList_FiltersByKind asserts that ?kind= is forwarded as WIType.
func TestUIWIList_FiltersByKind(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1&kind=fix_bug", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if captured.WIType == nil || *captured.WIType != "fix_bug" {
		t.Fatalf("filter wi_type: got %v, want fix_bug", captured.WIType)
	}
}

// TestUIWIList_RejectsUnknownStatus asserts that an unknown ?status= value is
// dropped (defaults to active set), preventing template breakage from arbitrary
// query params.
func TestUIWIList_RejectsUnknownStatus(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1&status=lolwhatever", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	// Unknown status falls through to the active default set, NOT to no-filter.
	if len(captured.Status) != 4 {
		t.Fatalf("unknown status: got filter %v, expected active default", captured.Status)
	}
}

// --- embedded ready queue block ----------------------------------------------

// TestUIWIList_SingleProject_RendersQueueEmbed asserts the collapsible ready
// queue block is present on a single-project list page, wired to poll the
// queue partial endpoint.
func TestUIWIList_SingleProject_RendersQueueEmbed(t *testing.T) {
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// aihub#185: the count strip is gone; status counts moved into the right
	// sidebar (LCRS segments).
	if strings.Contains(body, "pf-queue-embed") || strings.Contains(body, `class="qstrip"`) {
		t.Errorf("aihub#185: the count strip should be removed; body:\n%s", body)
	}
	for _, want := range []string{`class="wi-layout"`, `class="seg-nav"`, "Unclaimed"} {
		if !strings.Contains(body, want) {
			t.Errorf("single-project list should render the LCRS segment sidebar (missing %q); body:\n%s", want, body)
		}
	}
}

// TestUIWIList_AllMode_RendersQueueEmbed asserts the count strip IS rendered in
// the cross-project view-all mode (aihub#129 review-round-2 #2): the strip now
// aggregates across accessible projects, so it must be present and poll the
// partial with the __all__ sentinel.
func TestUIWIList_AllMode_RendersQueueEmbed(t *testing.T) {
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=__all__", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// aihub#185: view-all renders the segment sidebar; its links carry the
	// __all__ project sentinel so segment switches preserve the cross-project view.
	if !strings.Contains(body, `class="seg-nav"`) {
		t.Errorf("view-all mode should render the segment sidebar; body:\n%s", body)
	}
	if !strings.Contains(body, "project=__all__") {
		t.Errorf("view-all sidebar links should carry the __all__ sentinel; body:\n%s", body)
	}
}

// TestUIWIList_NoProject_OmitsQueueEmbed asserts the queue embed is hidden
// when no project is resolved (user with zero memberships).
func TestUIWIList_NoProject_OmitsQueueEmbed(t *testing.T) {
	u := &UserContext{
		UserID:       "u_lonely",
		DisplayName:  "Lonely",
		Role:         "writer",
		ProjectRoles: map[string]string{},
		APIKeyID:     "k_lonely",
	}

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(u))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pf-queue-embed") {
		t.Errorf("no-project list should NOT embed the ready queue block")
	}
}

// TestUIWIList_NoProject_DefaultsToAllProjects asserts that hitting /ui/wi with no
// ?project= param defaults to the cross-project "All projects" view, so the top-nav
// Work Items link always lands on every accessible project rather than silently
// selecting the first one. A user with at least one project must enter all-mode.
func TestUIWIList_NoProject_DefaultsToAllProjects(t *testing.T) {
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser())) // wiTestUser can see project p1
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi", nil) // no ?project=
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// aihub#185: all-mode carries the __all__ project sentinel (form hidden input +
	// sidebar segment links); single-project mode would carry project=p1. This is
	// the discriminator for the default view.
	if !strings.Contains(rec.Body.String(), "project=__all__") {
		t.Errorf("no-param /ui/wi should default to All projects (all-mode); body:\n%s", rec.Body.String())
	}
}

// TestUIWIList_RendersGroupWrapWithTotalCount asserts the per-section markup:
// each section renders as a .grp-wrap with a .grp-n total-count pill and a
// per-block pager container, so the client can paginate each block independently.
func TestUIWIList_RendersGroupWrapWithTotalCount(t *testing.T) {
	now := time.Now()
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{
			{ID: "wi_a", Slug: "p1#1", Project: "p1", Status: "queued", Goal: "first", CreatedAt: now},
			{ID: "wi_b", Slug: "p1#2", Project: "p1", Status: "queued", Goal: "second", CreatedAt: now.Add(-time.Hour)},
		}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	// All view so we exercise the full grouping path.
	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1&all=1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`class="wi-layout"`, `class="seg-nav"`, `class="seg-item`, "data-grp-rows", `class="grp-n"`} {
		if !strings.Contains(body, want) {
			t.Errorf("list body missing %q; body:\n%s", want, body)
		}
	}
	// Both queued items are ownerless+queued → Unclaimed=2: the sidebar segment
	// count and the selected-segment header count (default seg = unclaimed) read 2.
	if !strings.Contains(body, `Unclaimed<span class="cnt">2</span>`) {
		t.Errorf("expected Unclaimed sidebar count of 2; body:\n%s", body)
	}
	if !strings.Contains(body, `<span class="grp-n">2</span>`) {
		t.Errorf("expected selected-segment header count of 2; body:\n%s", body)
	}
}

// --- in-place HTMX filtering (aihub#129) -------------------------------------

// TestUIWIList_FullPage_WiresHTMXFilterBar asserts the full page renders the
// filter form wired for in-place HTMX requests (hx-get + #wi-list-body target)
// and wraps the grouped list in the #wi-list-body container the controls swap.
func TestUIWIList_FullPage_WiresHTMXFilterBar(t *testing.T) {
	now := time.Now()
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{
			{ID: "wi_a", Slug: "p1#1", Project: "p1", Status: "queued", Goal: "g", CreatedAt: now},
		}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="wi-list-body"`,
		`hx-get="/ui/wi"`,
		`hx-target="#wi-list-body"`,
		`hx-include="this"`,
		`hx-push-url="true"`,
		`class="seg-nav"`, // aihub#185: status is now the sidebar, not data-status-params
	} {
		if !strings.Contains(body, want) {
			t.Errorf("full page missing %q; body:\n%s", want, body)
		}
	}
}

// TestUIWIList_HXRequest_ReturnsFragmentOnly asserts that an HX-Request
// targeting wi-list-body returns ONLY the list-body fragment (the grouped
// sections), not the full page chrome or the filter bar — so a filter toggle
// swaps just the rows in place.
func TestUIWIList_HXRequest_ReturnsFragmentOnly(t *testing.T) {
	now := time.Now()
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{
			{ID: "wi_a", Slug: "p1#1", Project: "p1", Status: "queued", Goal: "g", CreatedAt: now},
		}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1&all=1", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "wi-list-body")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Fragment: no layout chrome, no filter bar, no #wi-list-body wrapper.
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("HX fragment must not include layout chrome; got DOCTYPE:\n%s", body)
	}
	if strings.Contains(body, "data-wi-filters") {
		t.Errorf("HX fragment must not re-emit the filter bar; body:\n%s", body)
	}
	if strings.Contains(body, `id="wi-list-body"`) {
		t.Errorf("HX fragment is the inner content; it must not re-emit the #wi-list-body wrapper; body:\n%s", body)
	}
	// But it MUST carry the two-column layout (sidebar + the segment's rows) it is
	// meant to swap in — so the sidebar highlight + middle update together.
	if !strings.Contains(body, `class="wi-layout"`) || !strings.Contains(body, `class="seg-nav"`) || !strings.Contains(body, "data-grp-rows") {
		t.Errorf("HX fragment should contain the two-column layout + rows; body:\n%s", body)
	}
}

// TestUIWIList_HXRequest_BoostedNavReturnsFullPage asserts that a boosted
// full-page navigation (HX-Request set but HX-Target is NOT wi-list-body) still
// gets the whole document, not the bare fragment.
func TestUIWIList_HXRequest_BoostedNavReturnsFullPage(t *testing.T) {
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1", nil)
	req.Header.Set("HX-Request", "true") // boosted nav, no #wi-list-body target
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
		t.Errorf("boosted nav (no wi-list-body target) should return the full page")
	}
}

// TestUIWIList_MultiStatus_AllForwarded asserts that repeated ?status= params
// are ALL forwarded into the ListWorkItems filter — the mechanism that lets the
// multi-status selection travel with every in-place request (and survive a
// project switch).
func TestUIWIList_MultiStatus_AllForwarded(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi?project=p1&status=running&status=wrapped&status=running", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Both distinct statuses forwarded, duplicate de-duped.
	want := map[string]bool{"running": true, "wrapped": true}
	if len(captured.Status) != len(want) {
		t.Fatalf("multi-status filter: got %v, want running+wrapped", captured.Status)
	}
	for _, s := range captured.Status {
		if !want[s] {
			t.Errorf("unexpected status %q forwarded", s)
		}
	}
}

// --- detail page -------------------------------------------------------------

// TestUIWIDetail_404_UnknownSlug asserts that a missing wi yields a 404
// response with a body page (layout chrome stays intact).
func TestUIWIDetail_404_UnknownSlug(t *testing.T) {
	withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
		return nil, domain.NewErr(domain.ErrNotFound, "work item \"nope\" not found")
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi/nope", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("body should include layout chrome; got: %s", body)
	}
	if !strings.Contains(body, "not found") {
		t.Fatalf("body should mention error message; got: %s", body)
	}
}

// TestUIWIDetail_200_RendersMarkdown asserts that the Background card renders
// the wi.Content field through the md template func (markdown → HTML).
func TestUIWIDetail_200_RendersMarkdown(t *testing.T) {
	now := time.Now()
	content := "# hello\n\n- one\n- two"
	noParentNoChildren(t)
	withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
		return &domain.WorkItem{
			ID:        "wi_test",
			Slug:      "test-1",
			Project:   "p1",
			Goal:      "do the thing",
			Status:    "running",
			Priority:  "normal",
			Source:    "human",
			Content:   &content,
			CreatedAt: now,
			UpdatedAt: now,
		}, nil
	})
	withFakeListDeps(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.DependenciesResponse, *domain.AihubError) {
		return &domain.DependenciesResponse{
			Blocking:  []domain.DependencyListEntry{},
			BlockedBy: []domain.DependencyListEntry{},
		}, nil
	})
	withFakeListEvents(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.ListEventsFilter) (*domain.ListEventsResponse, error) {
		return &domain.ListEventsResponse{Events: []domain.EventRow{}}, nil
	})
	withFakeRecall(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.RecallRequest) (*domain.RecallResponse, error) {
		return &domain.RecallResponse{Items: []domain.MemoryWithStrength{}}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi/test-1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<h1") {
		t.Errorf("markdown # should render to <h1>; body did not contain <h1>")
	}
	if !strings.Contains(body, "<ul>") && !strings.Contains(body, "<ul ") {
		t.Errorf("markdown bullets should render to <ul>; body did not contain <ul>")
	}
	if !strings.Contains(body, "do the thing") {
		t.Errorf("goal text missing from body")
	}
}

// TestUIWIDetail_RendersArtifactLinks asserts that methodology artifacts are
// surfaced with hrefs that target /ui/artifacts/<id>/html (cookie-authed
// mirror of /v1/artifacts/<id>/html).
func TestUIWIDetail_RendersArtifactLinks(t *testing.T) {
	now := time.Now()
	noParentNoChildren(t)
	withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
		return &domain.WorkItem{
			ID:        "wi_a",
			Slug:      "a-1",
			Project:   "p1",
			Goal:      "g",
			Status:    "running",
			CreatedAt: now,
			UpdatedAt: now,
		}, nil
	})
	withFakeListDeps(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.DependenciesResponse, *domain.AihubError) {
		return &domain.DependenciesResponse{}, nil
	})
	withFakeListEvents(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.ListEventsFilter) (*domain.ListEventsResponse, error) {
		return &domain.ListEventsResponse{Events: []domain.EventRow{}}, nil
	})
	withFakeRecall(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.RecallRequest) (*domain.RecallResponse, error) {
		return &domain.RecallResponse{
			Items: []domain.MemoryWithStrength{
				{Memory: domain.Memory{
					ID:         "mem_spec1",
					Type:       "methodology.spec",
					Content:    "spec body",
					Visibility: "project",
					Project:    "p1",
				}},
				{Memory: domain.Memory{
					ID:         "mem_plan1",
					Type:       "methodology.plan",
					Content:    "plan body",
					Visibility: "project",
					Project:    "p1",
				}},
			},
		}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi/a-1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/ui/artifacts/mem_spec1/html"`) {
		t.Errorf("expected spec artifact href, got body fragment:\n%s", body)
	}
	if !strings.Contains(body, `href="/ui/artifacts/mem_plan1/html"`) {
		t.Errorf("expected plan artifact href, got body fragment:\n%s", body)
	}
	if !strings.Contains(body, "methodology.spec") {
		t.Errorf("expected artifact type label methodology.spec")
	}
}

// TestUIWIEventsPartial_NoLayout asserts the partial endpoint returns ONLY
// the fragment, no layout chrome (no <!DOCTYPE html>).
func TestUIWIEventsPartial_NoLayout(t *testing.T) {
	now := time.Now()
	withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
		return &domain.WorkItem{
			ID: "wi_p", Slug: "p-1", Project: "p1", Goal: "g", Status: "running",
			CreatedAt: now, UpdatedAt: now,
		}, nil
	})
	withFakeListEvents(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.ListEventsFilter) (*domain.ListEventsResponse, error) {
		actor := "Alice"
		return &domain.ListEventsResponse{
			Events: []domain.EventRow{
				{
					ID:           "evt_1",
					EventType:    "step_started",
					ActorDisplay: &actor,
					Payload:      json.RawMessage(`{"step":"spec"}`),
					CreatedAt:    now,
				},
			},
		}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser()))
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi/p-1/events/partial", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("partial response must not include layout chrome; got DOCTYPE in body:\n%s", body)
	}
	if !strings.Contains(body, "step_started") {
		t.Errorf("expected event type in body; got:\n%s", body)
	}
	if !strings.Contains(body, "Alice") {
		t.Errorf("expected actor display in body; got:\n%s", body)
	}
}

// TestUIWIDetail_403_NoProjectAccess asserts a user without access to the wi's
// project sees an access-denied body, not the wi content.
func TestUIWIDetail_403_NoProjectAccess(t *testing.T) {
	now := time.Now()
	withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
		return &domain.WorkItem{
			ID: "wi_secret", Slug: "secret-1", Project: "p_other",
			Goal:      "secret goal you cannot see",
			Status:    "running",
			CreatedAt: now, UpdatedAt: now,
		}, nil
	})

	e := echo.New()
	g := e.Group("/ui", wiInjectUser(wiTestUser())) // u only has p1
	registerUIWIHandlers(g, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/wi/secret-1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (in-page error)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret goal you cannot see") {
		t.Errorf("body leaked goal of inaccessible wi")
	}
	if !strings.Contains(body, "no access") {
		t.Errorf("body should explain no-access; got: %s", body)
	}
}

// --- parent / children navigation (aihub#142) --------------------------------

// detailFixtureWI stubs getWI + the always-on side-loads (deps/events/recall)
// with empty results so a detail test only has to wire the parent/children
// seams it cares about. Returns the wi the handler will render.
func detailFixtureWI(t *testing.T, wiID, slug, project string) {
	t.Helper()
	now := time.Now()
	withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
		return &domain.WorkItem{
			ID: wiID, Slug: slug, Project: project, Goal: "g", Status: "running",
			Priority: "normal", CreatedAt: now, UpdatedAt: now,
		}, nil
	})
	withFakeListDeps(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.DependenciesResponse, *domain.AihubError) {
		return &domain.DependenciesResponse{Blocking: []domain.DependencyListEntry{}, BlockedBy: []domain.DependencyListEntry{}}, nil
	})
	withFakeListEvents(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.ListEventsFilter) (*domain.ListEventsResponse, error) {
		return &domain.ListEventsResponse{Events: []domain.EventRow{}}, nil
	})
	withFakeRecall(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.RecallRequest) (*domain.RecallResponse, error) {
		return &domain.RecallResponse{Items: []domain.MemoryWithStrength{}}, nil
	})
}

func getDetailBody(t *testing.T, u *UserContext, slug string) (int, string) {
	t.Helper()
	e := echo.New()
	g := e.Group("/ui", wiInjectUser(u))
	registerUIWIHandlers(g, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/ui/wi/"+slug, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestUIWIDetail_RendersParentLink asserts a wi with a parent renders the
// Parent meta row linking to the parent's slug.
func TestUIWIDetail_RendersParentLink(t *testing.T) {
	detailFixtureWI(t, "wi_child", "p1#9", "p1")
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.WIRef, *domain.AihubError) {
		slug := "p1#1"
		return &domain.WIRef{ID: "wi_parent", Slug: &slug, Project: "p1"}, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) ([]domain.WIRef, *domain.AihubError) {
		return []domain.WIRef{}, nil
	})

	code, body := getDetailBody(t, wiTestUser(), "p1#9")
	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", code, body)
	}
	if !strings.Contains(body, "Parent") {
		t.Errorf("expected a Parent meta row; body:\n%s", body)
	}
	// wiref path-escapes '#' as %23, so the href is /ui/wi/p1%231 while the
	// visible link text stays the raw slug.
	if !strings.Contains(body, `href="/ui/wi/p1%231"`) {
		t.Errorf("expected parent slug link to p1%%231; body:\n%s", body)
	}
	if !strings.Contains(body, ">p1#1</a>") {
		t.Errorf("expected parent slug text p1#1; body:\n%s", body)
	}
}

// TestUIWIDetail_NoParent_OmitsParentRow asserts a wi with no parent does NOT
// render the Parent meta row.
func TestUIWIDetail_NoParent_OmitsParentRow(t *testing.T) {
	detailFixtureWI(t, "wi_orphan", "p1#5", "p1")
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil // no parent
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) ([]domain.WIRef, *domain.AihubError) {
		return []domain.WIRef{}, nil
	})

	code, body := getDetailBody(t, wiTestUser(), "p1#5")
	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", code)
	}
	if strings.Contains(body, `<span class="k">Parent</span>`) {
		t.Errorf("a parentless wi must not render the Parent meta row; body:\n%s", body)
	}
}

// TestUIWIDetail_HiddenParent_Masked asserts a cross-project parent the caller
// cannot see renders the hidden placeholder and never leaks the slug.
func TestUIWIDetail_HiddenParent_Masked(t *testing.T) {
	detailFixtureWI(t, "wi_child", "p1#9", "p1")
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.WIRef, *domain.AihubError) {
		// Cross-project mask: ID="hidden", Slug=nil (domain sentinel).
		return &domain.WIRef{ID: "hidden", Slug: nil, Project: "p_secret"}, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) ([]domain.WIRef, *domain.AihubError) {
		return []domain.WIRef{}, nil
	})

	code, body := getDetailBody(t, wiTestUser(), "p1#9")
	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", code)
	}
	if !strings.Contains(body, "Parent") {
		t.Errorf("hidden parent should still show the Parent row with a placeholder; body:\n%s", body)
	}
	if strings.Contains(body, "p_secret") {
		t.Errorf("hidden parent must not leak the project; body:\n%s", body)
	}
}

// TestUIWIDetail_RendersChildren_InSeqOrder asserts the Children card lists the
// child slugs and preserves the order the domain layer returns (seq ASC).
func TestUIWIDetail_RendersChildren_InSeqOrder(t *testing.T) {
	detailFixtureWI(t, "wi_parent", "p1#1", "p1")
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) ([]domain.WIRef, *domain.AihubError) {
		s2, s3, s4 := "p1#2", "p1#3", "p1#4"
		return []domain.WIRef{
			{ID: "wi_c2", Slug: &s2, Project: "p1"},
			{ID: "wi_c3", Slug: &s3, Project: "p1"},
			{ID: "wi_c4", Slug: &s4, Project: "p1"},
		}, nil
	})

	code, body := getDetailBody(t, wiTestUser(), "p1#1")
	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", code, body)
	}
	if !strings.Contains(body, "<h3>Children</h3>") {
		t.Errorf("expected a Children card; body:\n%s", body)
	}
	// Count pill is a plain integer, not a progress ratio.
	if !strings.Contains(body, `<span class="grp-n">3</span>`) {
		t.Errorf("expected Children count pill of 3; body:\n%s", body)
	}
	// Order preserved: p1#2 before p1#3 before p1#4.
	i2 := strings.Index(body, "p1#2")
	i3 := strings.Index(body, "p1#3")
	i4 := strings.Index(body, "p1#4")
	if i2 < 0 || i3 < 0 || i4 < 0 || i2 >= i3 || i3 >= i4 {
		t.Errorf("children must render in seq order p1#2 < p1#3 < p1#4; got idx %d,%d,%d", i2, i3, i4)
	}
}

// TestUIWIDetail_NoChildren_OmitsCard asserts a leaf wi (no children) does NOT
// render the Children card at all.
func TestUIWIDetail_NoChildren_OmitsCard(t *testing.T) {
	detailFixtureWI(t, "wi_leaf", "p1#7", "p1")
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) ([]domain.WIRef, *domain.AihubError) {
		return []domain.WIRef{}, nil
	})

	code, body := getDetailBody(t, wiTestUser(), "p1#7")
	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", code)
	}
	if strings.Contains(body, "<h3>Children</h3>") {
		t.Errorf("a leaf wi must not render the Children card; body:\n%s", body)
	}
}

// TestUIWIDetail_HiddenChild_Masked asserts a cross-project child renders the
// hidden placeholder without leaking its slug/project.
func TestUIWIDetail_HiddenChild_Masked(t *testing.T) {
	detailFixtureWI(t, "wi_parent", "p1#1", "p1")
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string) ([]domain.WIRef, *domain.AihubError) {
		visible := "p1#2"
		return []domain.WIRef{
			{ID: "wi_c2", Slug: &visible, Project: "p1"},
			{ID: "hidden", Slug: nil, Project: "p_secret"}, // cross-project mask
		}, nil
	})

	code, body := getDetailBody(t, wiTestUser(), "p1#1")
	if code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", code)
	}
	if !strings.Contains(body, `<span class="grp-n">2</span>`) {
		t.Errorf("Children count includes the hidden child; want pill 2; body:\n%s", body)
	}
	if strings.Contains(body, "p_secret") {
		t.Errorf("hidden child must not leak the project; body:\n%s", body)
	}
	if !strings.Contains(body, "p1#2") {
		t.Errorf("the visible child slug should render; body:\n%s", body)
	}
}

// Verify wiStrPtr is referenced to keep helper used in case test fixtures grow.
var _ = wiStrPtr
