package server

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

// aihub#224 code_review finding 1: the sort/order feature's only caller-reachable
// surface is handleListWorkItems, and it had no coverage — all the unit tests sat
// on domain helpers. The regression these guard is invisible: drop the two
// `filter.Sort = …` assignments (or move the Normalize call after the query) and
// every other test still passes while `?sort=closed_at` silently reverts to
// created_at ordering, i.e. exactly the bug this wi fixes, reintroduced.

// listWIRequest drives handleListWorkItems with the given query string.
func listWIRequest(t *testing.T, pool *pgxpool.Pool, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/work_items?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, viewerUser())

	if err := handleListWorkItems(pool)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

// A rejected sort/order must 400 *before* the query runs. The nil pool proves
// it: any DB access would panic, so reaching 400 shows validation fires first.
func TestListWorkItems_InvalidSortRejectedBeforeDB(t *testing.T) {
	for _, tc := range []struct {
		name, query, wantIn string
	}{
		{"unknown sort", "project=testproject&sort=priority", "priority"},
		{"unknown order", "project=testproject&order=sideways", "sideways"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := listWIRequest(t, nil, tc.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
			}
			// The message must name the offending value so the caller can fix it.
			if body := rec.Body.String(); !strings.Contains(body, tc.wantIn) {
				t.Errorf("400 body should name the rejected value %q; got %s", tc.wantIn, body)
			}
		})
	}
}

// The core wiring assertion: the parsed query params must actually land on the
// filter that reaches the domain layer.
func TestListWorkItems_SortAndOrderReachTheFilter(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	rec := listWIRequest(t, nil, "project=testproject&sort=closed_at&order=asc")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if captured.Sort != domain.ListWorkItemsSortClosedAt {
		t.Errorf("filter.Sort = %q, want %q — the query param never reached the filter",
			captured.Sort, domain.ListWorkItemsSortClosedAt)
	}
	if captured.Order != domain.ListWorkItemsOrderAsc {
		t.Errorf("filter.Order = %q, want %q", captured.Order, domain.ListWorkItemsOrderAsc)
	}
}

// Omitting both params must produce the pre-aihub#224 filter, normalized — so an
// existing caller keeps created_at DESC.
func TestListWorkItems_DefaultsWhenParamsAbsent(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	if rec := listWIRequest(t, nil, "project=testproject"); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if captured.Sort != domain.ListWorkItemsSortCreatedAt || captured.Order != domain.ListWorkItemsOrderDesc {
		t.Errorf("absent params must default to (created_at, desc); got (%q, %q)",
			captured.Sort, captured.Order)
	}
}

// Case-folding happens server-side, so the domain layer only ever sees the
// canonical lowercase values it matches on.
func TestListWorkItems_SortCaseFoldedBeforeDomain(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})

	if rec := listWIRequest(t, nil, "project=testproject&sort=Closed_At&order=DESC"); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if captured.Sort != domain.ListWorkItemsSortClosedAt || captured.Order != domain.ListWorkItemsOrderDesc {
		t.Errorf("mixed-case params must be folded to (closed_at, desc); got (%q, %q)",
			captured.Sort, captured.Order)
	}
}
