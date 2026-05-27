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
	if !strings.Contains(body, "pf-queue-embed") {
		t.Errorf("single-project list should embed the ready queue block; body:\n%s", body)
	}
	if !strings.Contains(body, `hx-get="/ui/queue/partial`) {
		t.Errorf("queue embed should poll /ui/queue/partial; body:\n%s", body)
	}
}

// TestUIWIList_AllMode_OmitsQueueEmbed asserts the queue embed is hidden in
// the cross-project view-all mode (the queue is per-project).
func TestUIWIList_AllMode_OmitsQueueEmbed(t *testing.T) {
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
	if strings.Contains(rec.Body.String(), "pf-queue-embed") {
		t.Errorf("view-all mode should NOT embed the ready queue block")
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
			Goal: "secret goal you cannot see",
			Status: "running",
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

// Verify wiStrPtr is referenced to keep helper used in case test fixtures grow.
var _ = wiStrPtr
