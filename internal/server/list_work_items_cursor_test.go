package server

// Hop 3 of the cursor contract (aihub#382), companion to the domain-side
// rows.Err() guard in internal/domain/list_work_items_rows_err_db_test.go.
//
// The two are NOT alternatives. The domain check fixes the CLASS: an
// execute-time failure must never be reported as an empty page, whatever
// caused it. This file is about WHOSE mistake an unparseable cursor is. It is
// the caller's — next_cursor is always RFC3339Nano (listWorkItemsNextCursor),
// so a value that does not parse is one this server never emitted — and
// queryparam.go Rule 1 says 400 naming the parameter and the value, before any
// query runs. Without this hop the caller gets the domain's 500 for a typo only
// they can fix; before aihub#382 they got a 200 with an empty page.
//
// DB-free through the listWorkItemsFn seam, so it runs on CI's plain "Unit
// tests" step. The fake seam is also what makes the rejection arm FAIL CLEANLY
// on the unfixed handler instead of panicking on a nil pool: unfixed, the raw
// string reaches the seam and the response is 200 with the cursor captured.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestListWorkItems_UnparseableCursorRejectedBeforeDB(t *testing.T) {
	const cursor = "garbage-not-a-timestamp"
	f, _, rec := captureListWIFilter(t, "project=testproject&cursor="+cursor)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unparseable cursor, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, cursor) || !strings.Contains(body, "cursor") {
		t.Errorf("the 400 must name the parameter and the offending value so the caller can fix it; got %s", body)
	}
	if f.Cursor != nil {
		t.Errorf("the rejected cursor still reached the domain layer as %q", *f.Cursor)
	}
}

// CONTROL. A cursor of the shape next_cursor emits must pass through VERBATIM:
// the domain casts the string with `::timestamptz`, so re-formatting it here
// would be a second, invisible conversion upstream of the one that counts, and
// rejecting it would break every paginating caller. Green before and after the
// fix, on purpose.
func TestListWorkItems_WellFormedCursorReachesTheFilterVerbatim(t *testing.T) {
	for _, cursor := range []string{
		"2026-01-02T03:04:05.123456789Z", // RFC3339Nano, exactly what next_cursor emits
		"2026-01-02T03:04:05Z",           // no fraction
		"2026-01-02T12:04:05+09:00",      // a zone offset
	} {
		t.Run(cursor, func(t *testing.T) {
			f, _, rec := captureListWIFilter(t, "project=testproject&cursor="+url.QueryEscape(cursor))
			if rec.Code != http.StatusOK {
				t.Fatalf("a well-formed cursor must be accepted; got %d (body: %s)", rec.Code, rec.Body.String())
			}
			if f.Cursor == nil {
				t.Fatal("the cursor never reached the filter")
			}
			if *f.Cursor != cursor {
				t.Fatalf("the cursor must reach the domain unchanged; sent %q, filter carries %q", cursor, *f.Cursor)
			}
		})
	}
}
