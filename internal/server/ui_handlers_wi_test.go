package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
			{ID: "1", Status: "queued"},                         // unclaimed
			{ID: "2", Status: "queued"},                         // unclaimed
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

func withFakeListDeps(t *testing.T, fn func(context.Context, *pgxpool.Pool, string, map[string]string, string) (*domain.DependenciesResponse, *domain.AihubError)) {
	t.Helper()
	prev := listDependenciesFn
	listDependenciesFn = fn
	t.Cleanup(func() { listDependenciesFn = prev })
}

func withFakeParentRef(t *testing.T, fn func(context.Context, *pgxpool.Pool, string, map[string]string, string) (*domain.WIRef, *domain.AihubError)) {
	t.Helper()
	prev := getParentRefFn
	getParentRefFn = fn
	t.Cleanup(func() { getParentRefFn = prev })
}

func withFakeListChildren(t *testing.T, fn func(context.Context, *pgxpool.Pool, string, map[string]string, string) ([]domain.WIRef, *domain.AihubError)) {
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
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) ([]domain.WIRef, *domain.AihubError) {
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
	withFakeListDeps(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.DependenciesResponse, *domain.AihubError) {
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
	// The goal is page chrome and stays in the page. The Background markdown is rendered
	// inside a sandboxed iframe (aihub#240), so its elements are asserted on the frame's
	// inner document — in the page body they are attribute-escaped srcdoc bytes.
	if !strings.Contains(body, "do the thing") {
		t.Errorf("goal text missing from body")
	}
	doc := innerDoc(t, body)
	if !strings.Contains(doc, "<h1") {
		t.Errorf("markdown # should render to <h1>; embedded document did not contain <h1>")
	}
	if !strings.Contains(doc, "<ul>") && !strings.Contains(doc, "<ul ") {
		t.Errorf("markdown bullets should render to <ul>; embedded document did not contain <ul>")
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
	withFakeListDeps(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.DependenciesResponse, *domain.AihubError) {
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

// TestUIWIDetail_NoProjectAccessIsIndistinguishableFromMissing asserts that a
// user without access to the wi's project gets the SAME response a nonexistent
// work item gets — and, as always, none of the wi's content.
//
// 🔴 Renamed from TestUIWIDetail_403_NoProjectAccess, and the rename is the
// point of this comment. The old name said 403 while, as of aihub#377, the
// assertion says 404. A test whose name states one contract and whose body
// checks another is precisely the rot this work item spent its whole run digging
// out of neighbouring comments — including one that described an existence
// oracle it had half-closed, and one that justified deferring a leak with a
// reason that was already false. Do not leave a stale name for the next reader
// to trust.
//
// 🔴 Expected status changed 200 -> 404 on 2026-09-06. CONTRACT CHANGE, not a
// red test tuned green — the two are indistinguishable in a diff, so here is the
// check. This handler used to answer a denied caller with HTTP 200 and an
// in-page "no access to project p_other" message, while a work item that does
// not exist answered 404. Different status, different body: one bit, and work
// item slugs are <project>#<seq> counting from 1, so a browser session could
// enumerate other projects. Both cases now render the same body at 404.
//
// 🔴 It keeps all of its discriminating power. If the caller COULD see p_other,
// this handler would answer 200 with the full work item — Title, Status, and
// "secret goal you cannot see" in the body. So asserting 404 AND the absence of
// the goal still goes red the moment authorization stops working; neither
// assertion can be satisfied by a build that leaks. And the goal-absence check
// below is the one assertion that had to hold before this change and after it.
func TestUIWIDetail_NoProjectAccessIsIndistinguishableFromMissing(t *testing.T) {
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

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404 — a non-member must get what a nonexistent "+
			"work item gets (aihub#377). A visible wi would answer 200 here, so this "+
			"assertion cannot be satisfied by a build that leaks.", rec.Code)
	}
	body := rec.Body.String()

	// THE assertion, unchanged across the contract change: no content, ever.
	if strings.Contains(body, "secret goal you cannot see") {
		t.Errorf("body leaked goal of inaccessible wi")
	}
	// Nor the slug, which names the project and is half the enumeration signal.
	if strings.Contains(body, "p_other") {
		t.Errorf("body named the inaccessible project; got: %s", body)
	}
	if !strings.Contains(body, notVisibleMessage) {
		t.Errorf("body should carry the shared not-visible wording verbatim, so it cannot "+
			"be told apart from the missing-wi page; got: %s", body)
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
	withFakeListDeps(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.DependenciesResponse, *domain.AihubError) {
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
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.WIRef, *domain.AihubError) {
		slug := "p1#1"
		return &domain.WIRef{ID: "wi_parent", Slug: &slug, Project: "p1"}, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) ([]domain.WIRef, *domain.AihubError) {
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
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil // no parent
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) ([]domain.WIRef, *domain.AihubError) {
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
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.WIRef, *domain.AihubError) {
		// Cross-project mask: ID="hidden", Slug=nil (domain sentinel).
		return &domain.WIRef{ID: "hidden", Slug: nil, Project: "p_secret"}, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) ([]domain.WIRef, *domain.AihubError) {
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
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) ([]domain.WIRef, *domain.AihubError) {
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
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) ([]domain.WIRef, *domain.AihubError) {
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
	withFakeParentRef(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) (*domain.WIRef, *domain.AihubError) {
		return nil, nil
	})
	withFakeListChildren(t, func(_ context.Context, _ *pgxpool.Pool, _ string, _ map[string]string, _ string) ([]domain.WIRef, *domain.AihubError) {
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

// --- aihub#298: Done segment server-side pagination ---------------------------
//
// The Done segment fetched a single page hardcoded to 200 rows and stopped,
// while its header count came from fetchDoneCount (a real COUNT(*)). Past 200
// terminal items the page therefore printed an exact total above a silently
// truncated list and offered a purely client-side pager over the loaded rows —
// so the archive looked complete, and older items were unreachable by any
// control on the page. These tests pin the behaviours whose regression would
// restore that: the cursor must reach the server, "there are older rows" must
// survive to the markup, and the page must not re-acquire a control that
// implies completeness it does not have.

// doneFakeBase is the newest synthetic archive row's created_at; row i is one
// minute older than row i-1, so an index and a timestamp are interchangeable.
var doneFakeBase = time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

// doneFakeCursor renders the cursor the DOMAIN would mint after serving rows up
// to and including index i: listWorkItemsNextCursor emits that row's sort-column
// value as RFC3339Nano, and buildListWorkItemsWhere then pages with a strict
// `<`, so the next page starts one row later.
//
// 🔴 This fixture used to mint "idx-<n>", a shape no cursor this server issues
// has ever had. That cost nothing while the handler forwarded the token
// untouched, and became load-bearing the moment it started validating it
// (aihub#466): a fixture in a shape the production minter cannot produce cannot
// hold a line about page tokens, and would have made the correct fix look like a
// regression.
func doneFakeCursor(i int) string {
	return doneFakeBase.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
}

// doneFakeStart inverts doneFakeCursor: the index of the first row STRICTLY
// older than the cursor. An unparseable token starts at 0, matching both the
// handler's "show the newest page" fallback and the escaping test, which hands
// this fake a cursor it never minted.
func doneFakeStart(cursor string) int {
	ts, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return 0
	}
	// A cursor NEWER than the newest row would index before the start of the
	// archive. Real paging cannot produce one (the token is always a row's own
	// sort value), but a hand-written fixture can, and a negative start would
	// quietly synthesise rows with future timestamps rather than fail.
	if start := int(doneFakeBase.Sub(ts)/time.Minute) + 1; start > 0 {
		return start
	}
	return 0
}

// doneArchiveFake serves `total` terminal items from a timestamp cursor and
// appends every filter it is called with to *seen, so a test can assert on what
// the handler actually asked the domain layer for (not merely on what came
// back). Rows are numbered newest-first: index i holds seq total-i.
func doneArchiveFake(total int, seen *[]domain.ListWorkItemsFilter) func(context.Context, *pgxpool.Pool, string, domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
	return func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		if seen != nil {
			*seen = append(*seen, f)
		}
		start := 0
		if f.Cursor != nil {
			start = doneFakeStart(*f.Cursor)
		}
		wiType := "fix_bug"
		items := []*domain.WorkItem{}
		for i := start; i < start+f.Limit && i < total; i++ {
			seq := int64(total - i)
			items = append(items, &domain.WorkItem{
				ID:        "wi_" + strconv.FormatInt(seq, 10),
				Seq:       seq,
				Slug:      "arch#" + strconv.FormatInt(seq, 10),
				Project:   "p1",
				Goal:      "archived item",
				Status:    "wrapped",
				WIType:    &wiType,
				Labels:    []string{},
				CreatedAt: doneFakeBase.Add(-time.Duration(i) * time.Minute),
			})
		}
		res := &domain.ListWorkItemsResult{Items: items}
		if start+f.Limit < total {
			c := doneFakeCursor(start + f.Limit - 1)
			res.NextCursor = &c
		}
		return res, nil
	}
}

// renderWIList runs the real list handler against the real template and returns
// the rendered HTML. pool is nil: every DB helper the handler reaches on this
// path either nil-guards or is stubbed by the caller.
func renderWIList(t *testing.T, url string) string {
	t.Helper()
	tmpl := pageTemplate("wi_list.html.tmpl")
	h := handleUIWIList(nil, tmpl)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user", wiTestUser())
	if err := h(c); err != nil {
		t.Fatalf("handler error for %s: %v", url, err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d for %s", rec.Code, url)
	}
	return rec.Body.String()
}

// withDoneCount stubs the archive aggregate count.
func withDoneCount(t *testing.T, n int) {
	t.Helper()
	prev := fetchDoneCountFn
	fetchDoneCountFn = func(_ context.Context, _ *pgxpool.Pool, _ []string, _ string) int { return n }
	t.Cleanup(func() { fetchDoneCountFn = prev })
}

// TestDoneSegment_CursorReachesTheQuery is the core regression: ?done_cursor=
// must be forwarded to the domain filter. If it is dropped, every "older" click
// silently re-serves page 1 — the list still looks fine, which is why this
// asserts on the filter rather than on the row count.
func TestDoneSegment_CursorReachesTheQuery(t *testing.T) {
	var seen []domain.ListWorkItemsFilter
	withFakeListWI(t, doneArchiveFake(417, &seen))
	withDoneCount(t, 417)

	want := doneFakeCursor(49) // the cursor the domain mints after a 50-row page
	renderWIList(t, "/ui/wi?seg=done&project=p1&done_cursor="+url.QueryEscape(want))

	doneFilter := lastDoneFilter(seen)
	if doneFilter == nil {
		t.Fatal("no terminal-status query was issued for the done segment")
	}
	if doneFilter.Cursor == nil {
		t.Fatal("done query carried no cursor: ?done_cursor= was dropped, so paging can never advance")
	}
	// Byte-for-byte, for the reason aihub#435 pinned on the JSON endpoints: the
	// domain casts this string at `$n::timestamptz`, so any re-formatting on the
	// way in is a second conversion upstream of the one that counts.
	if *doneFilter.Cursor != want {
		t.Errorf("done cursor = %q, want %q", *doneFilter.Cursor, want)
	}
}

// lastDoneFilter picks the terminal-status query out of the filters a render
// issued — the Done segment's own, as opposed to the active-status ones the same
// page runs for the count strip.
func lastDoneFilter(seen []domain.ListWorkItemsFilter) *domain.ListWorkItemsFilter {
	var out *domain.ListWorkItemsFilter
	for i := range seen {
		if len(seen[i].Status) > 0 && seen[i].Status[0] == "wrapped" {
			out = &seen[i]
		}
	}
	return out
}

// TestDoneSegment_PageSizeFollowsLimit pins that the page size is the request's
// own limit rather than a hardcoded constant. A reintroduced literal would also
// have to stay under domain.ListWorkItems' 200 cap, above which that function
// silently falls back to 50 (aihub#267) — so a too-large literal degrades
// invisibly, which is exactly what this catches.
func TestDoneSegment_PageSizeFollowsLimit(t *testing.T) {
	for _, limit := range []int{25, 120} {
		var seen []domain.ListWorkItemsFilter
		withFakeListWI(t, doneArchiveFake(417, &seen))
		withDoneCount(t, 417)

		renderWIList(t, "/ui/wi?seg=done&project=p1&limit="+strconv.Itoa(limit))

		found := false
		for _, f := range seen {
			if len(f.Status) > 0 && f.Status[0] == "wrapped" {
				found = true
				if f.Limit != limit {
					t.Errorf("limit=%d: done query used Limit=%d, want %d", limit, f.Limit, limit)
				}
				if f.Limit > 200 {
					t.Errorf("limit=%d: done query Limit=%d exceeds domain's 200 cap and would be clamped to 200, "+
						"returning a short page this handler treats as complete", limit, f.Limit)
				}
			}
		}
		if !found {
			t.Fatalf("limit=%d: no terminal-status query issued", limit)
		}
	}
}

// TestDoneSegment_MoreRowsAreAdvertised: when older rows exist the markup must
// carry a control that reaches them, and when they do not it must not. The
// original defect was precisely that no such control could exist.
func TestDoneSegment_MoreRowsAreAdvertised(t *testing.T) {
	withFakeListWI(t, doneArchiveFake(417, nil))
	withDoneCount(t, 417)

	first := renderWIList(t, "/ui/wi?seg=done&project=p1")
	if !strings.Contains(first, "data-done-older") {
		t.Error("first page of a 417-item archive has no 'older' control: the rest of the archive is unreachable")
	}
	if strings.Contains(first, "data-done-newest") {
		t.Error("first page offers a 'newest' control while already on the newest page")
	}

	// 400 of 417 consumed: the last page is a short one and terminates paging.
	last := renderWIList(t, "/ui/wi?seg=done&project=p1&done_cursor="+url.QueryEscape(doneFakeCursor(399)))
	if strings.Contains(last, "data-done-older") {
		t.Error("final page still offers an 'older' control; paging does not terminate")
	}
	if !strings.Contains(last, "data-done-newest") {
		t.Error("final page offers no way back to the newest page")
	}
}

// TestDoneSegment_HeaderCountIsArchiveTotal guards the pair that made the bug
// legible: the header shows the true archive size while the body shows one
// page. Collapsing them (e.g. counting rendered rows) would hide truncation
// again; that is the state this wi fixed, not a tidier invariant.
func TestDoneSegment_HeaderCountIsArchiveTotal(t *testing.T) {
	withFakeListWI(t, doneArchiveFake(417, nil))
	withDoneCount(t, 417)

	html := renderWIList(t, "/ui/wi?seg=done&project=p1&limit=50")

	if !strings.Contains(html, ">417<") {
		t.Error("header does not show the archive total 417")
	}
	if strings.Count(html, "data-wi-row") != 50 {
		t.Errorf("rendered %d rows, want the 50-row page", strings.Count(html, "data-wi-row"))
	}
}

// TestDoneSegment_NoClientPager: dropdown.js's pager paginates rows already in
// the DOM and prints "N–M of <loaded>". That is honest only when the server
// shipped the segment whole. Rendering it for Done asserts a completeness the
// server-paged list does not have, so it must stay absent there — and present
// everywhere else.
func TestDoneSegment_NoClientPager(t *testing.T) {
	withFakeListWI(t, doneArchiveFake(417, nil))
	withDoneCount(t, 417)

	done := renderWIList(t, "/ui/wi?seg=done&project=p1")
	if strings.Contains(done, "data-grp-pager") {
		t.Error("done renders the client-side pager, which pages only loaded rows and implies the archive is complete")
	}
	if !strings.Contains(done, "data-done-pager") {
		t.Error("done renders no server pager")
	}

	active := renderWIList(t, "/ui/wi?seg=unclaimed&project=p1")
	if !strings.Contains(active, "data-grp-pager") {
		t.Error("active segment lost the client-side pager; the done-only change leaked")
	}
	if strings.Contains(active, "data-done-pager") {
		t.Error("active segment rendered the done server pager")
	}
}

// TestDoneSegment_CursorURLEscapedInBothAttributes pins the escaping asymmetry
// that is invisible in normal fixtures. html/template treats href as a URL
// context and escapes it; hx-get is a custom attribute and gets HTML escaping
// only, so a '+' (a non-UTC RFC3339Nano offset) renders as &#43;, reaches the
// server as a bare '+', and decodes to a space — silently paging from the wrong
// place. UTC cursors end in 'Z' and can never exhibit this, so only an explicit
// '+' fixture can hold the line.
func TestDoneSegment_CursorURLEscapedInBothAttributes(t *testing.T) {
	const plusCursor = "2026-08-30T23:22:54.510849+08:00"

	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		wiType := "fix_bug"
		items := []*domain.WorkItem{{
			ID: "wi_1", Seq: 1, Slug: "arch#1", Project: "p1", Goal: "archived",
			Status: "wrapped", WIType: &wiType, Labels: []string{},
			CreatedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		}}
		c := plusCursor
		return &domain.ListWorkItemsResult{Items: items, NextCursor: &c}, nil
	})
	withDoneCount(t, 417)

	html := renderWIList(t, "/ui/wi?seg=done&project=p1")

	if strings.Contains(html, "done_cursor=2026-08-30T23:22:54.510849&#43;") {
		t.Error("cursor '+' was HTML-escaped but not URL-escaped; it will decode to a space server-side")
	}
	if n := strings.Count(html, "done_cursor=2026-08-30T23%3A22%3A54.510849%2B08%3A00"); n != 2 {
		t.Errorf("URL-escaped cursor appears %d times, want 2 (href and hx-get); raw=%q", n,
			firstMatchAround(html, "done_cursor="))
	}
}

// firstMatchAround returns a short window around needle for failure messages.
func firstMatchAround(s, needle string) string {
	i := strings.Index(s, needle)
	if i < 0 {
		return "<not found>"
	}
	end := i + 90
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}

// --- aihub#466: the silent-empty half of the aihub#435 cursor defect ---------
//
// `?done_cursor=` was read raw and handed to the domain, which casts it at
// `$n::timestamptz` — the same cast that answered 500 on the three JSON
// endpoints until aihub#435 put queryCursor in front of them. Here the failure
// could not even reach the reader: fetchListRowsPaged's error was discarded by
// the condition that tested it (`if ... derr == nil`, no else), so SegRows kept
// its zero value and the page rendered its "Nothing here" empty state — beneath
// a header count from fetchDoneCount, a separate real COUNT(*) that stays exact.
// A non-zero archive total above zero rows, with nothing on the page saying why.
//
// The ruling is aihub#435's validator with /ui's answer: the SAME queryCursor
// decides what a page token is (done_cursor IS a /v1/work_items cursor —
// listWorkItemsNextCursor mints both), and the /ui exemption in queryparam.go
// decides what to do about a bad one, which is the newest page PLUS a notice.
// Never a silent substitution: that is Rule 1's forbidden move, and it is worse
// here than for `limit`, because a page of archive rows looks exactly like the
// page that was asked for.
//
// These tests are the pair that fails on each way of getting it wrong: the first
// on swallowing the bad token, the second on rejecting good ones, the third on
// the half the cursor check cannot cover.

// doneBadCursors are tokens a caller can actually arrive with. `undefined` and
// `null` are what a JS client sends for an unset paging variable; `idx-50` is
// the shape doneArchiveFake itself used to mint, which is why the fixture had to
// start minting real ones; the last is the psql spelling, a space for the T.
var doneBadCursors = []string{
	"undefined",
	"null",
	"idx-50",
	"garbage-not-a-timestamp",
	"0",
	"2026-13-45T99:99:99Z",
	"2026-01-02 03:04:05",
}

// TestDoneSegment_BadCursorRendersTheNewestPageAndSaysSo is the wi's own
// symptom, from both sides at once.
//
// Four assertions per token, and the interesting ones are the last two: an
// implementation that 400s the page would satisfy "no empty archive", and one
// that silently serves page one would satisfy everything except the notice —
// which is the whole difference between this answer and the one Rule 1 forbids.
func TestDoneSegment_BadCursorRendersTheNewestPageAndSaysSo(t *testing.T) {
	for _, bad := range doneBadCursors {
		t.Run(bad, func(t *testing.T) {
			var seen []domain.ListWorkItemsFilter
			withFakeListWI(t, doneArchiveFake(417, &seen))
			withDoneCount(t, 417)

			html := renderWIList(t, "/ui/wi?seg=done&project=p1&limit=50&done_cursor="+url.QueryEscape(bad))

			// 1. The token never reached the query. Not merely "the page did not
			// 500": a cursor this server never minted must not be cast at all,
			// which is the property aihub#435 asserts through `reached` on the
			// JSON side.
			df := lastDoneFilter(seen)
			if df == nil {
				t.Fatal("no terminal-status query was issued for the done segment")
			}
			if df.Cursor != nil {
				t.Errorf("a cursor this server never minted reached the query as %q; it is cast at "+
					"$n::timestamptz there, which is the 500 aihub#435 removed from the JSON endpoints", *df.Cursor)
			}

			// 2. The page is USABLE — the newest page, which is what a stale
			// bookmark wants. An HTML surface cannot answer 400 (queryparam.go's
			// /ui exemption), so failing the page is the wrong half to copy.
			if n := strings.Count(html, "data-wi-row"); n != 50 {
				t.Errorf("rendered %d rows, want the newest 50-row page", n)
			}

			// 3. It is not an empty archive. This is the defect verbatim.
			if strings.Contains(html, "Nothing here") {
				t.Error("the page renders the empty-state for a bad page token: a non-zero archive " +
					"total above zero rows reads as an empty archive")
			}

			// 4. The reader is TOLD, and told enough to act: the parameter named
			// and the value they sent quoted. Silence here is the same class of
			// defect as substituting a default on /v1 — the caller's mistake
			// becomes the server's silence and they never learn.
			notice := elementText(html, "data-done-notice")
			if notice == "" {
				t.Fatal("no notice was rendered: the page fell back to the newest page in silence, " +
					"which is the substitution Rule 1 forbids (queryparam.go)")
			}
			if !strings.Contains(notice, "done_cursor") {
				t.Errorf("the notice does not name the parameter, so the reader cannot find it; got %q", notice)
			}
			if !strings.Contains(notice, htmlEscapeForNotice(bad)) {
				t.Errorf("the notice does not quote the offending value %q; got %q", bad, notice)
			}
		})
	}
}

// elementText returns the text content of the first element carrying attr.
//
// 🔴 Not tidiness — scoping is what gives the assertions above any force.
// `strings.Contains(html, "0")` is true of every render of this page, and "the
// markup mentions done_cursor" is true of any render that emits the archive
// pager, so a whole-page assertion about the notice's CONTENT passes whether or
// not a notice was produced. Two of the checks above were written that way
// first; this is what they were rewritten against.
func elementText(html, attr string) string {
	i := strings.Index(html, attr)
	if i < 0 {
		return ""
	}
	open := strings.Index(html[i:], ">")
	if open < 0 {
		return ""
	}
	start := i + open + 1
	end := strings.Index(html[start:], "</div>")
	if end < 0 {
		return html[start:]
	}
	return html[start : start+end]
}

// htmlEscapeForNotice renders a value the way html/template will inside element
// text, so the assertion above matches the markup rather than the Go string.
// Only the quoting the notice actually exercises is handled — a token arriving
// with '<' or '&' would need more, and pinning that is what
// TestDoneSegment_NoticeIsEscaped below is for.
func htmlEscapeForNotice(s string) string {
	return strings.ReplaceAll(s, `"`, "&#34;")
}

// TestDoneSegment_NoticeIsEscaped: the notice reflects caller text into the
// page, so it has to be inert there. html/template does this automatically for
// element content and the notice is rendered as `{{.DoneNotice}}`, but the whole
// point of a reflected value is that the day someone reaches for a `safeHTML`
// pipeline to "fix the quoting" it stops being automatic — so the property is
// pinned rather than assumed.
func TestDoneSegment_NoticeIsEscaped(t *testing.T) {
	const payload = `<script>alert(1)</script>`
	withFakeListWI(t, doneArchiveFake(417, nil))
	withDoneCount(t, 417)

	html := renderWIList(t, "/ui/wi?seg=done&project=p1&done_cursor="+url.QueryEscape(payload))

	notice := elementText(html, "data-done-notice")
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Error("the notice emitted caller text as live markup")
	}
	if !strings.Contains(notice, "&lt;script&gt;") {
		t.Errorf("the notice does not carry the escaped value at all; got %q", notice)
	}
}

// TestDoneSegment_GoodCursorIsUntouched is the arm that fails if the fix
// over-reaches, and it is the one that matters most: "reject every cursor"
// satisfies the test above completely while breaking every "older →" click on
// the page.
//
// Absent and empty are here too, for aihub#435's reason: `?done_cursor=` is how
// this page's own "↑ 最新" link and a no-JS submit of the filter form spell "the
// newest page", so treating it as malformed would raise a notice on ordinary
// navigation.
func TestDoneSegment_GoodCursorIsUntouched(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantCursor string
	}{
		{"absent", "", ""},
		{"empty", "&done_cursor=", ""},
		{"whitespace", "&done_cursor=%20%20", ""},
		{"minted", "&done_cursor=" + url.QueryEscape(doneFakeCursor(49)), doneFakeCursor(49)},
		// A non-UTC offset: the '+' is the character the escaping test guards on
		// the way OUT, and it must also survive validation on the way back IN.
		{"offset", "&done_cursor=" + url.QueryEscape("2026-08-30T23:22:54.510849+08:00"),
			"2026-08-30T23:22:54.510849+08:00"},
		// Second-resolution, which is what RFC3339Nano emits for a whole second.
		{"whole-second", "&done_cursor=" + url.QueryEscape("2026-08-30T23:11:00Z"), "2026-08-30T23:11:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []domain.ListWorkItemsFilter
			withFakeListWI(t, doneArchiveFake(417, &seen))
			withDoneCount(t, 417)

			html := renderWIList(t, "/ui/wi?seg=done&project=p1&limit=50"+tc.query)

			if strings.Contains(html, "data-done-notice") {
				t.Errorf("a cursor this server mints raised a notice; got %s",
					firstMatchAround(html, "data-done-notice"))
			}
			if strings.Contains(html, "data-seg-err") {
				t.Errorf("a cursor this server mints produced a segment error; got %s",
					firstMatchAround(html, "data-seg-err"))
			}

			df := lastDoneFilter(seen)
			if df == nil {
				t.Fatal("no terminal-status query was issued for the done segment")
			}
			got := ""
			if df.Cursor != nil {
				got = *df.Cursor
			}
			if got != tc.wantCursor {
				t.Errorf("done query cursor = %q, want %q — the token must reach the domain "+
					"byte-for-byte, since the cast at $n::timestamptz is the conversion that counts", got, tc.wantCursor)
			}
		})
	}
}

// TestDoneSegment_NoticeOnlyWhenDoneIsOnScreen: a bad done_cursor beside a
// different ?seg= consumed nothing and fell back to nothing, so a notice saying
// the page went back to the newest one would be a true-sounding sentence about
// an event that did not happen.
func TestDoneSegment_NoticeOnlyWhenDoneIsOnScreen(t *testing.T) {
	withFakeListWI(t, doneArchiveFake(417, nil))
	withDoneCount(t, 417)

	other := renderWIList(t, "/ui/wi?seg=unclaimed&project=p1&done_cursor=undefined")
	if strings.Contains(other, "data-done-notice") {
		t.Error("a bad done_cursor raised the Done notice on a segment that never read it")
	}

	// Positive control, so the negative above cannot pass because the notice is
	// broken everywhere.
	done := renderWIList(t, "/ui/wi?seg=done&project=p1&done_cursor=undefined")
	if !strings.Contains(done, "data-done-notice") {
		t.Error("the notice is not rendered on the Done segment either — the check above is vacuous")
	}
}

// TestDoneSegment_FetchFailureIsNotAnEmptyArchive covers the second half of the
// wi's goal, the one the cursor check cannot reach.
//
// queryCursor refuses a token Postgres would refuse; a token it ACCEPTS can
// still fail the query for reasons that have nothing to do with paging, and
// before this every one of those rendered as an empty archive as well. So the
// error is surfaced on the SEGMENT — not as data.Err, which replaces the whole
// two-column layout and would take the sidebar and every other segment's count
// down with it for the failure of one query.
func TestDoneSegment_FetchFailureIsNotAnEmptyArchive(t *testing.T) {
	// Only the TERMINAL-status query fails. Failing every list query would trip
	// fetchListGroups first, which sets data.Err and returns before the segment
	// branch runs — so the test would pass on a page that never exercised the
	// code under test.
	ok := doneArchiveFake(417, nil)
	withFakeListWI(t, func(ctx context.Context, pool *pgxpool.Pool, project string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		if len(f.Status) > 0 && f.Status[0] == "wrapped" {
			return nil, domain.NewErr(domain.ErrInternalError, "archive query exploded")
		}
		return ok(ctx, pool, project, f)
	})
	withDoneCount(t, 417)

	html := renderWIList(t, "/ui/wi?seg=done&project=p1")

	segErr := elementText(html, "data-seg-err")
	if segErr == "" {
		t.Fatal("a failed archive query renders no error state; its error is still being discarded")
	}
	if !strings.Contains(segErr, "archive query exploded") {
		t.Errorf("the error state does not carry the reason, so the page says only that "+
			"something is wrong; got %q", segErr)
	}
	if strings.Contains(html, "Nothing here") {
		t.Error(`a failed query renders the empty state: "empty" and "failed to load" are different facts`)
	}
	if n := strings.Count(html, "data-wi-row"); n != 0 {
		t.Errorf("a failed query rendered %d rows", n)
	}

	// The header count is what made the old behaviour a lie rather than merely
	// an omission: it comes from a separate COUNT(*) and is still exact. It must
	// stay — it is the evidence that the archive is not empty — and the error
	// box beside it is what stops it reading as truncation.
	if !strings.Contains(html, ">417<") {
		t.Error("the archive total is gone; the page can no longer show that rows exist but did not load")
	}

	// The whole page must survive: this is a segment-level state, not a page one.
	if !strings.Contains(html, "seg-nav") {
		t.Error("the segment sidebar was taken down by one segment's query failure")
	}
}
