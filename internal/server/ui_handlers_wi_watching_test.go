package server

// Pure-unit probes for the aihub#143 Watching scope wiring on /ui/wi. No
// database: the domain call is swapped for a fake and the assertions read the
// filter it was handed, plus the rendered HTML.
//
// These cover what the DB suite deliberately does not — the plumbing between
// the query string and domain.ListWorkItemsFilter, and the two places that
// plumbing can be right in one branch and wrong in another (the active
// segments' query vs the Done segment's separate COUNT). They run in
// `go test ./...` with no AIHUB_TEST_DB, so they are the half of this feature's
// coverage that CI executes unconditionally.
//
// The authorization property is NOT here. A fake ListWorkItems returns whatever
// it was told to, so nothing in this file can distinguish a query that is bounded
// by the project allow-list from one that is not; that lives in
// ui_handlers_wi_watching_db_test.go against a real Postgres.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// captureListFilters swaps in a fake that records every filter the handler
// issues and returns no rows.
func captureListFilters(t *testing.T, seen *[]domain.ListWorkItemsFilter) {
	t.Helper()
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		*seen = append(*seen, f)
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})
}

// TestWatchingParam_ReachesTheDomainFilter is the wiring probe, with its own
// negative control. `?watching=1` must set WatcherUserID to the SESSION's user
// id — and the request without it must leave the field nil, or "the scope is
// wired" would be indistinguishable from "every list is watch-filtered".
func TestWatchingParam_ReachesTheDomainFilter(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		var seen []domain.ListWorkItemsFilter
		captureListFilters(t, &seen)
		withDoneCount(t, 0)

		renderWIList(t, "/ui/wi?project=p1&watching=1")

		if len(seen) == 0 {
			t.Fatal("no list query was issued")
		}
		f := seen[len(seen)-1]
		if f.WatcherUserID == nil {
			t.Fatal("?watching=1 did not reach domain.ListWorkItemsFilter.WatcherUserID")
		}
		// The session's UserID, never anything from the query string: there is
		// no request shape that can ask for somebody else's watch list, so there
		// is no such request to authorize.
		if got := *f.WatcherUserID; got != wiTestUser().UserID {
			t.Errorf("WatcherUserID = %q, want the session user %q", got, wiTestUser().UserID)
		}
	})

	t.Run("off (negative control)", func(t *testing.T) {
		var seen []domain.ListWorkItemsFilter
		captureListFilters(t, &seen)
		withDoneCount(t, 0)

		renderWIList(t, "/ui/wi?project=p1")

		if len(seen) == 0 {
			t.Fatal("no list query was issued")
		}
		if f := seen[len(seen)-1]; f.WatcherUserID != nil {
			t.Errorf("without ?watching= the filter must be nil; got %q", *f.WatcherUserID)
		}
	})

	t.Run("garbled value is off, not on", func(t *testing.T) {
		// queryBoolLenientUI's direction of failure, asserted rather than
		// assumed: an unparseable toggle must degrade to the WIDER view. A
		// narrower one would hide work items while still looking like a complete
		// list, which is the failure mode nobody notices.
		var seen []domain.ListWorkItemsFilter
		captureListFilters(t, &seen)
		withDoneCount(t, 0)

		renderWIList(t, "/ui/wi?project=p1&watching=sure")

		if f := seen[len(seen)-1]; f.WatcherUserID != nil {
			t.Errorf("watching=sure must read as off; got %q", *f.WatcherUserID)
		}
	})
}

// TestWatchingParam_WinsOverTheOwnerFilter pins the exclusivity. Mine and
// Watching are two view scopes over one list; a request carrying both (a stale
// hidden field, a hand-built URL, a double-lit pair of pills) must resolve to
// ONE of them rather than to their intersection, which no control on the page
// can express and no control can clear.
func TestWatchingParam_WinsOverTheOwnerFilter(t *testing.T) {
	var seen []domain.ListWorkItemsFilter
	captureListFilters(t, &seen)
	withDoneCount(t, 0)

	body := renderWIList(t, "/ui/wi?project=p1&watching=1&owner=Alice")

	f := seen[len(seen)-1]
	if f.WatcherUserID == nil {
		t.Fatal("watching must still be applied when owner is also present")
	}
	if f.OwnerDisplay != nil {
		t.Errorf("owner must be cleared when watching wins; got %q", *f.OwnerDisplay)
	}
	// And the page must SAY so: a "me" pill left lit while doing nothing is the
	// same bug from the user's side.
	if strings.Contains(body, `class="grp-me on" data-me-toggle`) {
		t.Error("the me pill must not render as on while the watching scope is active")
	}
	if !strings.Contains(body, `data-watch-toggle`) {
		t.Error("the watching pill must be rendered")
	}
	if !strings.Contains(body, `class="grp-me wch on" data-watch-toggle`) {
		t.Error("the watching pill must render as on under ?watching=1")
	}
}

// TestWatchingScope_DoneCountCarriesTheWatcher covers the one segment whose
// count and rows come from different statements.
//
// Done's rows go through fetchListRowsPaged, which inherits filter.WatcherUserID
// for free; its count is a standalone COUNT(*) that does not. Left unwired, the
// archive footer would read "1 / 417" — one watched row under the whole-archive
// total — which is exactly the "the list looks truncated" illusion aihub#298
// was filed to remove.
func TestWatchingScope_DoneCountCarriesTheWatcher(t *testing.T) {
	var gotWatcher string
	var calls int
	prev := fetchDoneCountFn
	fetchDoneCountFn = func(_ context.Context, _ *pgxpool.Pool, _ []string, watcher string) int {
		calls++
		gotWatcher = watcher
		return 0
	}
	t.Cleanup(func() { fetchDoneCountFn = prev })

	var seen []domain.ListWorkItemsFilter
	captureListFilters(t, &seen)

	renderWIList(t, "/ui/wi?project=p1&seg=done&watching=1")

	if calls == 0 {
		t.Fatal("the done count was never fetched")
	}
	if gotWatcher != wiTestUser().UserID {
		t.Errorf("done count watcher = %q, want the session user %q", gotWatcher, wiTestUser().UserID)
	}

	// The rows half, so the two are pinned together rather than one at a time.
	var doneFilter *domain.ListWorkItemsFilter
	for i := range seen {
		if len(seen[i].Status) > 0 && seen[i].Status[0] == "wrapped" {
			doneFilter = &seen[i]
		}
	}
	if doneFilter == nil {
		t.Fatal("no terminal-status query was issued for the done segment")
	}
	if doneFilter.WatcherUserID == nil {
		t.Error("the Done ROW query must carry the watcher too, or the rows and the count disagree")
	}

	// Negative control: without the scope the count must be asked for the whole
	// archive, not for an empty watcher that happens to look the same.
	gotWatcher = "sentinel"
	renderWIList(t, "/ui/wi?project=p1&seg=done")
	if gotWatcher != "" {
		t.Errorf("without ?watching= the done count must be unscoped; got watcher %q", gotWatcher)
	}
}

// TestWatchingScope_SurvivesASegmentClickWithoutJS pins the no-JS / bookmark
// path.
//
// With JS, dropdown.js re-injects the live pill state into every request that
// swaps #wi-list-body. Without it, the ONLY thing carrying the scope across a
// segment click or an archive page is the href the server wrote — so if those
// hrefs drop `watching`, the scope silently resets to All the moment the user
// clicks anything, and the page still looks entirely normal.
//
// This is also the only automated coverage the client wiring has: the repo has
// no JS test harness, so dropdown.js's half is verified by review, and this test
// pins the half that a server can be held to.
func TestWatchingScope_SurvivesASegmentClickWithoutJS(t *testing.T) {
	withFakeListWI(t, doneArchiveFake(417, nil))
	withDoneCount(t, 417)

	on := renderWIList(t, "/ui/wi?project=p1&seg=done&watching=1")
	for _, want := range []string{
		// every sidebar segment link
		`href="/ui/wi?seg=running&project=p1&owner=&watching=1"`,
		`href="/ui/wi?seg=done&project=p1&owner=&watching=1"`,
	} {
		if !strings.Contains(on, want) {
			t.Errorf("segment link does not carry the watching scope; missing %s", want)
		}
	}
	if !strings.Contains(on, "watching=1&limit=50&done_cursor=") {
		t.Error("the Done archive pager does not carry the watching scope")
	}
	if !strings.Contains(on, `<input type="hidden" name="watching" value="1">`) {
		t.Error("the filter form's hidden watching field is not primed for a no-JS submit")
	}

	// Negative control: with the scope off the hrefs must carry an EMPTY
	// watching, never a stale 1 — otherwise turning the scope off would be
	// undone by the next click.
	off := renderWIList(t, "/ui/wi?project=p1&seg=done")
	if strings.Contains(off, "watching=1") {
		t.Error("with the scope off, no link may carry watching=1")
	}
}

// TestWatchToggle_RoutesAreRegisteredAndAuthed covers the one hop every other
// test in this feature skips: the route table.
//
// The DB suite calls handleUIWIWatchToggle directly, so an unregistered — or
// wrongly-pathed, or wrongly-methoded — route would leave every one of those
// tests green while the button 404s in the browser. This builds the REAL route
// table via RegisterUIRoutes and checks both halves:
//
//   - the two methods resolve to a handler at all (not echo's 404/405), and
//   - they sit inside the authed group, so an anonymous request is turned away
//     rather than reaching the handler with a nil user.
//
// No database and no cookie: RequireUISession short-circuits first, which is
// exactly the property the second half asserts.
//
// 🔴 The registration half reads echo's ROUTE TABLE, not a status code, and that
// is not a stylistic choice. The first version of this test deleted the DELETE
// registration as a mutant and still passed: an echo group installs its
// middleware on a catch-all, so an UNREGISTERED path under /ui is answered by
// RequireUISession's 302 — indistinguishable from a registered one. Any probe
// built on the response of an anonymous request is structurally blind here.
func TestWatchToggle_RoutesAreRegisteredAndAuthed(t *testing.T) {
	e := echo.New()
	RegisterUIRoutes(e, nil, []byte(testCookieSecret))

	const path = "/ui/wi/:id/watch"
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		if r.Path == path {
			registered[r.Method] = true
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if !registered[method] {
			t.Errorf("%s %s is not in the route table; the button would 404 in a browser "+
				"while every handler-level test in this feature stays green", method, path)
		}
	}
	// Negative control: a verb nobody registered must NOT be there, or the
	// assertions above would pass against a route table that contains everything.
	if registered[http.MethodPut] {
		t.Errorf("PUT %s is registered but was never wired — the lookup above is not discriminating", path)
	}

	// Second half: the routes sit inside the authed group, so an anonymous
	// request is turned away rather than reaching the handler with a nil user.
	do := registeredUI(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if rec := do(method, "/ui/wi/aihub%23143/watch"); rec.Code == http.StatusOK {
			t.Errorf("%s answered 200 with no session — the route escaped the authed group", method)
		}
	}
}

// TestWatchToggle_RendersBothStatesFromOneDefinition pins that the detail page
// and the POST/DELETE endpoint render the SAME partial, so "the button after a
// click" and "the button after a reload" cannot drift.
//
// It also pins the method per state, which is the part a reader is most likely
// to get backwards: watching -> DELETE (remove), not watching -> POST (add).
func TestWatchToggle_RendersBothStatesFromOneDefinition(t *testing.T) {
	tmpl := partialTemplate("wi_watch_toggle.html.tmpl")

	for _, tc := range []struct {
		name       string
		state      watchToggle
		wantSubstr []string
		notSubstr  []string
	}{
		{
			name:       "not watching offers to add",
			state:      watchToggle{Ref: "aihub#143", Watching: false},
			wantSubstr: []string{`hx-post="/ui/wi/aihub%23143/watch"`, `aria-pressed="false"`, "Watch"},
			notSubstr:  []string{"hx-delete"},
		},
		{
			name:       "watching offers to remove",
			state:      watchToggle{Ref: "aihub#143", Watching: true},
			wantSubstr: []string{`hx-delete="/ui/wi/aihub%23143/watch"`, `aria-pressed="true"`, "Watching"},
			notSubstr:  []string{"hx-post"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			if err := tmpl.ExecuteTemplate(&sb, "wi_watch_toggle.html.tmpl", tc.state); err != nil {
				t.Fatalf("render: %v", err)
			}
			out := sb.String()
			for _, want := range tc.wantSubstr {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in:\n%s", want, out)
				}
			}
			for _, no := range tc.notSubstr {
				if strings.Contains(out, no) {
					t.Errorf("unexpected %q in:\n%s", no, out)
				}
			}
		})
	}
}
