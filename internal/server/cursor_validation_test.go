package server

// aihub#435 — §6.1 T1-6 of the aihub#411 adjudication: a `cursor` the caller
// made up is the CALLER's mistake, and must come back as a 400 naming the
// parameter rather than a 500 carrying the driver's text.
//
// ─── What was actually wrong, per surface ──────────────────────────────────
//
// The wi named ONE read point. There are four, and they were in three
// different states — which is why this file is a per-surface table rather than
// a single assertion:
//
//	GET /v1/work_items   router.go        validated since aihub#382 (queryRFC3339)
//	GET /v1/memories     routes_memory.go RAW -> $n::timestamptz -> 500
//	GET /v1/events       routes_memory.go RAW -> $n::timestamptz -> 500
//	GET /ui/wi           ui_handlers_wi.go RAW, and its error is DISCARDED
//	                                       (both halves fixed by aihub#466)
//
// The last one was a different defect on the same param name (`?done_cursor=`
// fed fetchListRowsPaged inside `if ... derr == nil`, so a bad token rendered an
// empty Done segment with no error at all) and was filed separately, because an
// HTML page cannot answer 400 and needed its own ruling rather than this one
// stretched to cover it. That ruling is aihub#466: the SAME reader decides what
// a page token is — `done_cursor` is listWorkItemsNextCursor's output too, so it
// cannot be legal on one surface and not the other — and the /ui exemption in
// queryparam.go decides what to DO about a bad one, which is render the newest
// page and SAY SO rather than 400 or fall back in silence. Its exemption entry
// is gone from the table below and the /ui/wi row now reads like the first one.
//
// ─── Why the two 500s could not be seen by the Rule-1 gate ─────────────────
//
// queryparam_gate_test.go follows a request value into a CONVERSION or a
// COMPARISON. A cursor is neither: it is a string handed to pgx and cast by
// Postgres. That is the blind spot the gate's own scope note names, and the
// census arm at the bottom of this file is what closes it — not by widening
// the conversion set, which cannot reach here, but by requiring every
// cursor-shaped query param in package server to be either read through
// queryCursor or listed with a reason.
//
// ─── The trap this file exists to keep out ─────────────────────────────────
//
// 🔴 A Recall cursor is NOT a timestamp. domain.formatRecallCursor emits
// `<RFC3339Nano>|<id>` — the id half is the `id DESC` tiebreaker aihub#239
// added, and it is opaque. Validating the whole token as RFC3339 would 400
// every cursor Recall has ever issued, i.e. break pagination completely while
// looking exactly like a correct Rule-1 fix. The verbatim arms below are the
// discriminating ones: they fail on that mistake and pass on the fix, whereas
// the rejection arms pass on both.
//
// DB-free through the recallFn / listEventsFn seams (the same ones the /ui
// handlers use), so this runs on CI's plain unit-test step, which deliberately
// leaves AIHUB_TEST_DB unset.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// cursorTestUser is a plain member with viewer access to the one project these
// requests name. Not an admin: admin short-circuits checkProjectAccess, and a
// guard that only holds for the caller who skips the access check is not the
// guard the callers in the corpus meet.
func cursorTestUser() *UserContext {
	return &UserContext{
		UserID:       "u_cursor_test",
		Role:         "member",
		ProjectRoles: map[string]string{"p_cursor_test": "viewer"},
	}
}

// captureRecallCursor runs one GET /v1/memories and reports the cursor
// handleRecall handed the domain, whether the domain was reached at all, and
// the response.
//
// reached is the load-bearing return: "the response was a 400" and "the query
// never ran" are different claims, and only the second one is the reason this
// change exists. A handler that 400s AFTER the round trip would satisfy the
// status assertion and none of the intent.
func captureRecallCursor(t *testing.T, rawQuery string) (cursor string, reached bool, rec *httptest.ResponseRecorder) {
	t.Helper()
	withFakeRecall(t, func(_ context.Context, _ *pgxpool.Pool, req *domain.RecallRequest) (*domain.RecallResponse, error) {
		reached = true
		cursor = req.Cursor
		return &domain.RecallResponse{}, nil
	})
	c, rec := newRecallRequest(t, rawQuery, cursorTestUser())
	if err := handleRecall(nil)(c); err != nil {
		c.Echo().HTTPErrorHandler(err, c)
	}
	return cursor, reached, rec
}

// captureEventsCursor is captureRecallCursor for GET /v1/events. The empty
// string doubles as "no cursor on the filter", which is exactly the state a
// rejected request must leave behind.
func captureEventsCursor(t *testing.T, rawQuery string) (cursor string, reached bool, rec *httptest.ResponseRecorder) {
	t.Helper()
	withFakeListEvents(t, func(_ context.Context, _ *pgxpool.Pool, f *domain.ListEventsFilter) (*domain.ListEventsResponse, error) {
		reached = true
		if f.Cursor != nil {
			cursor = *f.Cursor
		}
		return &domain.ListEventsResponse{}, nil
	})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/events?"+rawQuery, nil)
	rec = httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, cursorTestUser())
	if err := handleListEvents(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return cursor, reached, rec
}

// ─── arm 1: the rejection ────────────────────────────────────────────────────

// TestCursor_MalformedIsRejectedBeforeTheQuery covers the two surfaces that had
// no check at all. Each row asserts three things, and the third is the one the
// wi is about: 400, the message names the parameter and quotes the value, and
// the domain layer was never entered.
func TestCursor_MalformedIsRejectedBeforeTheQuery(t *testing.T) {
	// Every value here is one a caller could plausibly send. `null` and
	// `undefined` are what a JS client hands over when a paging variable is
	// unset, and they are the reason "the caller would have to be malicious for
	// this to happen" is wrong: they arrive from ordinary client bugs.
	malformed := []string{
		"garbage-not-a-timestamp",
		"null",
		"undefined",
		"0",
		"2026-13-45T99:99:99Z", // right shape, impossible date
		"2026-01-02 03:04:05",  // a space instead of the T — the psql spelling
	}
	for _, bad := range malformed {
		t.Run("recall/"+bad, func(t *testing.T) {
			cursor, reached, rec := captureRecallCursor(t,
				"project=p_cursor_test&cursor="+url.QueryEscape(bad))
			requireCursor400(t, rec, bad)
			require.False(t, reached,
				"a rejected cursor must not reach the query; the domain got %q", cursor)
		})
		t.Run("events/"+bad, func(t *testing.T) {
			cursor, reached, rec := captureEventsCursor(t,
				"project=p_cursor_test&cursor="+url.QueryEscape(bad))
			requireCursor400(t, rec, bad)
			require.False(t, reached,
				"a rejected cursor must not reach the query; the domain got %q", cursor)
		})
	}
}

// requireCursor400 asserts the shape of the refusal.
//
// ⚠️ The parameter name is matched with a LINE-START anchor, not Contains.
// "cursor" appears in nearly every message this file could produce — including
// in the words "next_cursor" — so a Contains check would go green against a
// message that named some other parameter and merely mentioned cursors. The
// helpers in queryparam.go all lead with `<name> must ...`, so the anchor is a
// real property of the contract and not a spelling this test invented.
func requireCursor400(t *testing.T, rec *httptest.ResponseRecorder, offending string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"a cursor this server never issued is the caller's mistake, not a 500. body=%s", rec.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	msg, _ := body["message"].(string)
	require.True(t, strings.HasPrefix(msg, "cursor "),
		"the message must LEAD with the parameter name, the way every other reader in "+
			"queryparam.go does; got %q", msg)
	require.Contains(t, msg, offending,
		"the message must quote the value the caller sent, or they cannot find it; got %q", msg)
	require.NotContains(t, strings.ToLower(msg), "timestamptz",
		"the 400 must be written for the caller, not lifted from the driver; got %q", msg)
}

// ─── arm 2: the control that the rejection arm cannot provide ───────────────

// TestCursor_WellFormedReachesTheDomainVerbatim is the arm that fails if the
// fix over-reaches.
//
// Two properties, both load-bearing:
//
//   - ACCEPTED. "reject every cursor" satisfies arm 1 completely.
//   - VERBATIM. The domain casts the caller's string with `$n::timestamptz`
//     (memory.go, ListEvents / recall paging), so re-formatting it here would
//     be a second conversion upstream of the one that counts, and a Recall
//     cursor would lose its `|<id>` tiebreaker entirely — silently resurrecting
//     the row-skipping bug aihub#239 fixed.
func TestCursor_WellFormedReachesTheDomainVerbatim(t *testing.T) {
	t.Run("recall", func(t *testing.T) {
		for _, cursor := range []string{
			// What formatRecallCursor actually emits: reference time + '|' + id.
			"2026-01-02T03:04:05.123456789Z|mem_abc123",
			"2026-01-02T03:04:05Z|mem_abc123",
			"2026-01-02T12:04:05+09:00|mem_abc123",
			// Pre-aihub#239 cursors carry the timestamp alone and must keep
			// paginating: parseRecallCursor still has a branch for them, so
			// rejecting them here would break in-flight callers this server
			// itself handed a cursor to.
			"2026-01-02T03:04:05.123456789Z",
			"2026-01-02T03:04:05Z",
		} {
			t.Run(cursor, func(t *testing.T) {
				got, reached, rec := captureRecallCursor(t,
					"project=p_cursor_test&cursor="+url.QueryEscape(cursor))
				require.Equal(t, http.StatusOK, rec.Code,
					"a cursor this server minted must be accepted. body=%s", rec.Body.String())
				require.True(t, reached, "the accepted cursor never reached the domain")
				require.Equal(t, cursor, got,
					"the cursor must reach the domain byte-for-byte; re-formatting it "+
						"drops the |id tiebreaker and skips rows (aihub#239)")
			})
		}
	})

	t.Run("events", func(t *testing.T) {
		// ListEvents mints its cursor as CreatedAt.Format(time.RFC3339Nano) and
		// compares `e.created_at < $n::timestamptz` — the whole token is the
		// timestamp, with no tiebreaker half.
		for _, cursor := range []string{
			"2026-01-02T03:04:05.123456789Z",
			"2026-01-02T03:04:05Z",
			"2026-01-02T12:04:05+09:00",
		} {
			t.Run(cursor, func(t *testing.T) {
				got, reached, rec := captureEventsCursor(t,
					"project=p_cursor_test&cursor="+url.QueryEscape(cursor))
				require.Equal(t, http.StatusOK, rec.Code,
					"a cursor this server minted must be accepted. body=%s", rec.Body.String())
				require.True(t, reached, "the accepted cursor never reached the domain")
				require.Equal(t, cursor, got, "the cursor must reach the domain byte-for-byte")
			})
		}
	})
}

// TestCursor_AbsentAndEmptyAreNotRejections separates "no cursor" from "a bad
// cursor". They are the same empty string on the wire and must not become the
// same answer: `?cursor=` is how a client spells "first page" when its paging
// variable is unset, and a 400 there would break first-page reads that work
// today.
func TestCursor_AbsentAndEmptyAreNotRejections(t *testing.T) {
	// The third spelling is whitespace-only. queryCursor trims before testing for
	// empty — the same order queryRFC3339 uses for `since` — so it lands here
	// rather than in the rejection table. Pinned because it is the one input
	// whose answer this change MOVES: `?cursor=%20` used to reach the cast and
	// come back 500, and "first page" is the better of the two answers to a
	// token with no content in it.
	for _, q := range []string{
		"project=p_cursor_test",
		"project=p_cursor_test&cursor=",
		"project=p_cursor_test&cursor=%20%20",
	} {
		t.Run("recall/"+q, func(t *testing.T) {
			got, reached, rec := captureRecallCursor(t, q)
			require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
			require.True(t, reached)
			require.Empty(t, got, "an absent cursor must not become a filter")
		})
		t.Run("events/"+q, func(t *testing.T) {
			got, reached, rec := captureEventsCursor(t, q)
			require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
			require.True(t, reached)
			require.Empty(t, got, "an absent cursor must not become a filter")
		})
	}
}

// TestCursor_RecallIdHalfIsNotConstrained pins the half of the token this
// server must NOT have an opinion about.
//
// The id half is opaque to the handler on purpose. A validator that also
// checked it — say, `mem_` + base62 — would be a second, private copy of the id
// format, and the day NewID's alphabet changes it becomes a 400 on cursors the
// server is still issuing. The timestamp half is validated because the server
// knows its format; the id half is forwarded because it does not need to.
func TestCursor_RecallIdHalfIsNotConstrained(t *testing.T) {
	for _, id := range []string{"mem_abc123", "not-an-id", "", "wi_xyz", "0"} {
		cursor := "2026-01-02T03:04:05.123456789Z|" + id
		t.Run(cursor, func(t *testing.T) {
			got, reached, rec := captureRecallCursor(t,
				"project=p_cursor_test&cursor="+url.QueryEscape(cursor))
			require.Equal(t, http.StatusOK, rec.Code,
				"only the timestamp half is this server's to validate. body=%s", rec.Body.String())
			require.True(t, reached)
			require.Equal(t, cursor, got)
		})
	}
}

// ─── arm 3: the census ───────────────────────────────────────────────────────

// cursorReaderFunc is the one reader a cursor-shaped query param may go
// through. Named once so the census message can point at it.
const cursorReaderFunc = "queryCursor"

// rawCursorReadExemptions lists every place in package server that reads a
// cursor-shaped query param WITHOUT queryCursor, keyed by file and parameter
// name, each with the reason it is not a defect.
//
// Keyed by (file, param) rather than by line, because a line number rots on the
// next edit above it and a rotted key fails open — the entry stops matching,
// the site becomes "new", and the only thing the gate then proves is that
// somebody renumbered it.
var rawCursorReadExemptions = map[string]string{
	"router.go|cursor": "the semantic-search branch tests NON-EMPTINESS only " +
		"(`c.QueryParam(\"cursor\") != \"\"`) to reject cursor+query as a combination; " +
		"it never reads the value, and the same handler validates it through " +
		cursorReaderFunc + " a few lines above",
}

// TestCursor_EveryCursorParamGoesThroughOneReader is the structural arm.
//
// The three surfaces above are fixed by the arms above them. This one is about
// the FOURTH handler, the one nobody has written yet: package server has a
// standing rule that a caller-supplied value is read through queryparam.go, and
// for every OTHER kind of value queryparam_gate_test.go enforces it by
// following the value into a conversion. A cursor never reaches a conversion,
// so that gate is structurally blind to it and a new endpoint could bind a raw
// cursor tomorrow with nothing going red.
//
// So this arm counts read points instead of tracing values: every
// `c.QueryParam("...cursor...")` in a non-test file of package server must be
// exempted by name, and the exemption must carry a reason.
func TestCursor_EveryCursorParamGoesThroughOneReader(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var rawSites []string  // "file|param" for c.QueryParam("...cursor...")
	var validated []string // "file|param" for queryCursor(c, "...")
	filesWalked := 0

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, perr, "parsing %s", name)
		filesWalked++

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "QueryParam" {
				if p, ok := stringLit(call.Args[0]); ok && strings.Contains(p, "cursor") {
					rawSites = append(rawSites, name+"|"+p)
				}
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == cursorReaderFunc && len(call.Args) >= 2 {
				if p, ok := stringLit(call.Args[1]); ok {
					validated = append(validated, name+"|"+p)
				}
			}
			return true
		})
	}

	// FLOOR. A walk that finds nothing reports green, and "the directory moved"
	// looks identical to "every surface is clean" (aihub#458 K8). These numbers
	// are the state this change leaves behind, so they fail loudly if a later
	// edit deletes a validated surface instead of adding one.
	require.GreaterOrEqual(t, filesWalked, 15,
		"the walk found %d source files in package server; at that count it is measuring "+
			"nothing and a green here means the directory moved, not that the surfaces are clean",
		filesWalked)
	require.GreaterOrEqual(t, len(validated), 4,
		"expected at least the three JSON list endpoints plus /ui/wi's done_cursor to read "+
			"cursor through %s, found %v", cursorReaderFunc, validated)

	sort.Strings(rawSites)
	for _, site := range rawSites {
		reason, exempt := rawCursorReadExemptions[site]
		require.True(t, exempt,
			"%s reads a cursor-shaped query param raw. Route it through %s, or add\n\n\t%q: \"<why this one is different>\",\n\nto rawCursorReadExemptions with the reason.",
			site, cursorReaderFunc, site)
		require.NotEmpty(t, reason, "the exemption for %s carries no reason", site)
	}

	// The mirror: an exemption for a site that no longer exists is a comment
	// pretending to be a check, and it is how the list grows past what anyone
	// has read.
	for site := range rawCursorReadExemptions {
		require.Contains(t, rawSites, site,
			"rawCursorReadExemptions still exempts %s, which no longer reads a raw cursor — delete the entry", site)
	}
}

// stringLit returns the value of a basic string literal expression.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// cursorCardPath is here so the anchor below does not hard-code a path twice.
func cursorCardPath(tool string) string {
	return filepath.Join("..", "..", "docs", "mcp-cards", tool+".md")
}

// TestCursor_CardsRecordTheRuling keeps the three contract cards honest about
// this change.
//
// Not decoration: pf_read_events.md carried "§6.1 T1-6 is filed, not landed
// (aihub#435): today a malformed cursor is not guaranteed to come back as a
// 400" in its Open section. That sentence is now false, and a card whose Open
// section lists a closed item is worse than one that lists nothing — the next
// reader budgets work for it. The machine block is untouched by this change
// (no schema or description edit), so the contract-card gate's K3 arm has
// nothing to say here; this is the prose half.
func TestCursor_CardsRecordTheRuling(t *testing.T) {
	for _, tool := range []string{"pf_list_work_items", "pf_recall", "pf_read_events"} {
		t.Run(tool, func(t *testing.T) {
			b, err := os.ReadFile(cursorCardPath(tool))
			require.NoError(t, err)
			card := string(b)
			// Asserted with Truef rather than Contains/NotContains on purpose: a
			// failing Contains prints the whole haystack, and these haystacks are
			// 8-12 kB cards. A failure report nobody scrolls to the end of is a
			// failure report that gets skimmed.
			require.Truef(t, strings.Contains(card, "aihub#435"),
				"%s: the card must name the wi that settled its cursor behaviour (aihub#435)", tool)
			require.Falsef(t, strings.Contains(card, "T1-6 is filed, not landed"),
				"%s: the Open section still reports T1-6 as outstanding", tool)
		})
	}
}
