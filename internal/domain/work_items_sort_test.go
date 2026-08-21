package domain

import (
	"strings"
	"testing"
	"time"
)

// ─── ListWorkItems sort/order (aihub#224) ───────────────────────────────────
//
// GET /v1/work_items hardcoded `ORDER BY wi.created_at DESC`, so a wi created
// early but closed today sorted below newer terminal wis — and once a project's
// terminal set exceeded `limit`, recently-closed older wis fell off the page
// entirely. These tests pin the sort/order contract on the pure builders, the
// same technique the cursor tests in work_items_test.go use (no live DB here).

// Defaults must reproduce the pre-aihub#224 behaviour exactly: created_at DESC
// with a strict `<` cursor. This is the "nothing changed for existing callers"
// guard.
func TestListWorkItemsSort_DefaultsToCreatedAtDesc(t *testing.T) {
	col, dir, op := listWorkItemsSort(ListWorkItemsFilter{})
	if col != "wi.created_at" || dir != "DESC" || op != "<" {
		t.Errorf("empty filter must default to (wi.created_at, DESC, <); got (%s, %s, %s)", col, dir, op)
	}
}

// An unrecognised Sort reaching the domain layer (a caller that skipped
// NormalizeListWorkItemsSort) must fall back to the default column rather than
// interpolate the caller's string into ORDER BY.
func TestListWorkItemsSort_UnknownColumnFallsBackToDefault(t *testing.T) {
	col, dir, _ := listWorkItemsSort(ListWorkItemsFilter{Sort: "goal; DROP TABLE work_items", Order: "desc"})
	if col != "wi.created_at" {
		t.Errorf("unknown sort must fall back to wi.created_at, never interpolate caller input; got %q", col)
	}
	if dir != "DESC" {
		t.Errorf("unknown sort must keep the default direction; got %q", dir)
	}
}

// The cursor operator must follow the direction, otherwise ASC pagination walks
// backwards and re-returns page 1 forever.
func TestListWorkItemsSort_AscFlipsCursorOperator(t *testing.T) {
	col, dir, op := listWorkItemsSort(ListWorkItemsFilter{Sort: "closed_at", Order: "asc"})
	if col != "wi.closed_at" || dir != "ASC" || op != ">" {
		t.Errorf("closed_at/asc must yield (wi.closed_at, ASC, >); got (%s, %s, %s)", col, dir, op)
	}
}

// Order is caller-supplied text; accept the documented value case-insensitively
// rather than silently degrading "DESC" to the ASC branch.
func TestListWorkItemsSort_OrderIsCaseInsensitive(t *testing.T) {
	if _, dir, _ := listWorkItemsSort(ListWorkItemsFilter{Order: "ASC"}); dir != "ASC" {
		t.Errorf(`Order "ASC" must resolve to ASC; got %q`, dir)
	}
	if _, dir, _ := listWorkItemsSort(ListWorkItemsFilter{Order: "DESC"}); dir != "DESC" {
		t.Errorf(`Order "DESC" must resolve to DESC; got %q`, dir)
	}
}

// sort=closed_at must add `closed_at IS NOT NULL`. Without it a NULL sort key is
// unreachable by any cursor (`closed_at < $n` is never true for NULL), so open
// wis would silently vanish after page 1. Restricting the set keeps the ordering
// total and the cursor exact — and loses nothing for terminal statuses, which
// the trg_wi_closed_at trigger always stamps.
func TestBuildListWorkItemsWhere_ClosedAtSortExcludesNulls(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{Sort: "closed_at"})
	if !strings.Contains(where, "wi.closed_at IS NOT NULL") {
		t.Errorf("sort=closed_at must restrict to closed rows; got WHERE: %q", where)
	}
	// The predicate binds no argument, so it must not consume a placeholder.
	if len(args) != 1 || args[0] != "proj" {
		t.Errorf("IS NOT NULL must not bind an arg; got args %#v", args)
	}
}

// The default sort must NOT gain the closed_at restriction — that would drop
// every open wi from the plain list.
func TestBuildListWorkItemsWhere_CreatedAtSortKeepsOpenItems(t *testing.T) {
	_, where, _ := buildListWorkItemsWhere("proj", ListWorkItemsFilter{})
	if strings.Contains(where, "closed_at") {
		t.Errorf("default sort must not mention closed_at; got WHERE: %q", where)
	}
}

// The cursor predicate must be on the column actually ordered by. Keying it on
// created_at while ordering by closed_at is the failure this wi exists to fix.
func TestBuildListWorkItemsWhere_CursorUsesSortColumn(t *testing.T) {
	cursor := time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
		Sort:   "closed_at",
		Cursor: ptrStr(cursor),
	})
	// $1=project; IS NOT NULL binds nothing; so the cursor is $2.
	if !strings.Contains(where, "wi.closed_at < $2::timestamptz") {
		t.Errorf("cursor must compare the sort column; got WHERE: %q", where)
	}
	if strings.Contains(where, "wi.created_at <") {
		t.Errorf("cursor must not also bound created_at; got WHERE: %q", where)
	}
	if len(args) != 2 || args[1] != cursor {
		t.Errorf("expected cursor bound as $2; got args %#v", args)
	}
}

// ASC pagination must use `>`, so next_cursor moves forward through the set.
func TestBuildListWorkItemsWhere_CursorOperatorFollowsAscOrder(t *testing.T) {
	cursor := time.Now().UTC().Format(time.RFC3339Nano)
	_, where, _ := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
		Sort:   "closed_at",
		Order:  "asc",
		Cursor: ptrStr(cursor),
	})
	if !strings.Contains(where, "wi.closed_at > $2::timestamptz") {
		t.Errorf("asc cursor must use a strict `>`; got WHERE: %q", where)
	}
}

// Placeholder numbering must survive the new non-binding predicate sitting
// between the arg-bearing filters (cf. aihub#147).
func TestBuildListWorkItemsWhere_ClosedAtSortKeepsPlaceholderNumbering(t *testing.T) {
	cursor := time.Now().UTC().Format(time.RFC3339Nano)
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
		Status: []string{"wrapped", "cancelled", "failed"},
		Sort:   "closed_at",
		Cursor: ptrStr(cursor),
	})
	// $1=project, $2=status ANY, $3=cursor.
	if !strings.Contains(where, "wi.closed_at < $3::timestamptz") {
		t.Errorf("placeholder numbering broken by the IS NOT NULL predicate; got WHERE: %q", where)
	}
	if len(args) != 3 || args[2] != cursor {
		t.Errorf("expected cursor as $3; got args %#v", args)
	}
}

// ─── next_cursor emission ───────────────────────────────────────────────────

// next_cursor must carry the sort column's value. Emitting created_at while the
// page was ordered by closed_at makes page 2 an arbitrary slice of the table.
func TestListWorkItemsNextCursor_UsesSortColumn(t *testing.T) {
	created := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	closed := time.Date(2026, 7, 20, 17, 30, 0, 0, time.UTC)
	wi := &WorkItem{CreatedAt: created, ClosedAt: &closed}

	got := listWorkItemsNextCursor(wi, "wi.created_at")
	if got == nil || *got != created.Format(time.RFC3339Nano) {
		t.Errorf("created_at sort must emit created_at as the cursor; got %v", got)
	}

	got = listWorkItemsNextCursor(wi, "wi.closed_at")
	if got == nil || *got != closed.Format(time.RFC3339Nano) {
		t.Errorf("closed_at sort must emit closed_at as the cursor; got %v", got)
	}
}

// A NULL sort value cannot be encoded as a cursor. The WHERE clause makes this
// unreachable, but if it ever were reached, ending pagination is correct and
// emitting a cursor from a different column is not.
func TestListWorkItemsNextCursor_NilClosedAtEndsPagination(t *testing.T) {
	wi := &WorkItem{CreatedAt: time.Now().UTC()}
	if got := listWorkItemsNextCursor(wi, "wi.closed_at"); got != nil {
		t.Errorf("a NULL closed_at must not produce a cursor; got %q", *got)
	}
}

// ─── caller-input validation ────────────────────────────────────────────────

// Empty input means "caller did not ask", so it must default rather than 400 —
// every existing caller sends neither param.
func TestNormalizeListWorkItemsSort_EmptyDefaults(t *testing.T) {
	sort, order, err := NormalizeListWorkItemsSort("", "")
	if err != nil {
		t.Fatalf("empty sort/order must be accepted; got error %v", err)
	}
	if sort != ListWorkItemsSortCreatedAt || order != ListWorkItemsOrderDesc {
		t.Errorf("expected (created_at, desc); got (%s, %s)", sort, order)
	}
}

// Caller-supplied input: an unrecognised value is a hard reject that names the
// offending value and enumerates the legal ones (mem_X8JDSC96).
func TestNormalizeListWorkItemsSort_RejectsUnknownSort(t *testing.T) {
	_, _, err := NormalizeListWorkItemsSort("priority", "")
	if err == nil {
		t.Fatal("unknown sort must be rejected")
	}
	if err.Code != ErrBadRequest {
		t.Errorf("expected ErrBadRequest; got %v", err.Code)
	}
	if !strings.Contains(err.Message, "priority") {
		t.Errorf("error must name the offending value; got %q", err.Message)
	}
	for _, legal := range ListWorkItemsSortValues() {
		if !strings.Contains(err.Message, legal) {
			t.Errorf("error must enumerate legal value %q; got %q", legal, err.Message)
		}
	}
}

func TestNormalizeListWorkItemsSort_RejectsUnknownOrder(t *testing.T) {
	_, _, err := NormalizeListWorkItemsSort("closed_at", "sideways")
	if err == nil {
		t.Fatal("unknown order must be rejected")
	}
	if !strings.Contains(err.Message, "sideways") {
		t.Errorf("error must name the offending value; got %q", err.Message)
	}
	for _, legal := range ListWorkItemsOrderValues() {
		if !strings.Contains(err.Message, legal) {
			t.Errorf("error must enumerate legal value %q; got %q", legal, err.Message)
		}
	}
}

// Normalize returns lowercased values so the filter carries exactly what
// listWorkItemsSort matches on.
func TestNormalizeListWorkItemsSort_NormalizesCase(t *testing.T) {
	sort, order, err := NormalizeListWorkItemsSort("Closed_At", "ASC")
	if err != nil {
		t.Fatalf("mixed-case input must be accepted; got %v", err)
	}
	if sort != ListWorkItemsSortClosedAt || order != ListWorkItemsOrderAsc {
		t.Errorf("expected (closed_at, asc); got (%s, %s)", sort, order)
	}
}

// The published enum must come from the enforced set, so a contract consumer
// (HTTP 400 text, MCP schema, OpenAPI) cannot drift from the validator.
func TestListWorkItemsSortValues_MatchesEnforcedSet(t *testing.T) {
	values := ListWorkItemsSortValues()
	if len(values) != len(listWorkItemsSortColumns) {
		t.Fatalf("published sort enum (%v) and enforced set (%d entries) disagree", values, len(listWorkItemsSortColumns))
	}
	for _, v := range values {
		if _, ok := listWorkItemsSortColumns[v]; !ok {
			t.Errorf("published sort value %q is not enforced", v)
		}
		if _, _, err := NormalizeListWorkItemsSort(v, ""); err != nil {
			t.Errorf("published sort value %q is rejected by the validator: %v", v, err)
		}
	}
	for _, v := range ListWorkItemsOrderValues() {
		if _, _, err := NormalizeListWorkItemsSort("", v); err != nil {
			t.Errorf("published order value %q is rejected by the validator: %v", v, err)
		}
	}
}

// ─── assembled statement ────────────────────────────────────────────────────
//
// The domain suite has no live pool, so the generated SQL never executes here.
// These pin the assembled statement instead: clause order, the ORDER BY built
// from two interpolated fragments, and the LIMIT (which must be limit+1, the
// extra row being how ListWorkItems detects "there is a next page").

func TestBuildListWorkItemsQuery_DefaultOrderByAndLimit(t *testing.T) {
	query, args, sortCol := buildListWorkItemsQuery("proj", ListWorkItemsFilter{Limit: 20})

	if !strings.Contains(query, "ORDER BY wi.created_at DESC") {
		t.Errorf("default ORDER BY missing/garbled; got:\n%s", query)
	}
	// limit+1: the sentinel row that signals a further page exists.
	if !strings.Contains(query, "LIMIT 21") {
		t.Errorf("expected LIMIT 21 (limit+1); got:\n%s", query)
	}
	// Clause order must be FROM → WHERE → ORDER BY → LIMIT, or Postgres rejects it.
	iFrom := strings.Index(query, "FROM work_items wi")
	iWhere := strings.Index(query, "WHERE ")
	iOrder := strings.Index(query, "ORDER BY ")
	iLimit := strings.Index(query, "LIMIT ")
	if iFrom >= iWhere || iWhere >= iOrder || iOrder >= iLimit {
		t.Errorf("clause order is not FROM/WHERE/ORDER BY/LIMIT (%d/%d/%d/%d); got:\n%s",
			iFrom, iWhere, iOrder, iLimit, query)
	}
	if sortCol != "wi.created_at" {
		t.Errorf("sortCol = %q, want wi.created_at", sortCol)
	}
	if len(args) != 1 || args[0] != "proj" {
		t.Errorf("args = %#v, want [proj]", args)
	}
}

// The tether Work-view query (aihub#224's motivating caller), end to end through
// the builder: terminal statuses ordered by close time, newest first.
func TestBuildListWorkItemsQuery_ClosedAtDescMatchesTetherRecentQuery(t *testing.T) {
	query, args, sortCol := buildListWorkItemsQuery("aihub", ListWorkItemsFilter{
		Status: []string{"wrapped", "cancelled", "failed"},
		Sort:   ListWorkItemsSortClosedAt,
		Order:  ListWorkItemsOrderDesc,
		Limit:  20,
	})

	for _, want := range []string{
		"wi.status = ANY($2)",
		"wi.closed_at IS NOT NULL",
		"ORDER BY wi.closed_at DESC",
		"LIMIT 21",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("assembled query missing %q; got:\n%s", want, query)
		}
	}
	// The old hardcoded ordering must be gone, not merely supplemented.
	if strings.Contains(query, "ORDER BY wi.created_at") {
		t.Errorf("closed_at sort still emits the created_at ORDER BY; got:\n%s", query)
	}
	if sortCol != "wi.closed_at" {
		t.Errorf("sortCol = %q, want wi.closed_at (next_cursor keys off this)", sortCol)
	}
	if len(args) != 2 {
		t.Errorf("args = %#v, want [aihub, [wrapped cancelled failed]]", args)
	}
}

// ASC must reach ORDER BY as ASC — a direction dropped here would silently keep
// the DESC page while the cursor walked forward with `>`, returning nothing.
func TestBuildListWorkItemsQuery_AscReachesOrderBy(t *testing.T) {
	query, _, _ := buildListWorkItemsQuery("proj", ListWorkItemsFilter{
		Sort:  ListWorkItemsSortClosedAt,
		Order: ListWorkItemsOrderAsc,
		Limit: 5,
	})
	if !strings.Contains(query, "ORDER BY wi.closed_at ASC") {
		t.Errorf("asc order did not reach ORDER BY; got:\n%s", query)
	}
}

// code_review finding 5: Sort was ToLower'd but not TrimSpace'd while Order was
// both, so a direct-filter caller passing " closed_at " silently got created_at
// ordering. Both are now trimmed symmetrically.
func TestListWorkItemsSort_TrimsWhitespaceSymmetrically(t *testing.T) {
	col, dir, op := listWorkItemsSort(ListWorkItemsFilter{Sort: " closed_at ", Order: " asc "})
	if col != "wi.closed_at" || dir != "ASC" || op != ">" {
		t.Errorf("padded Sort/Order must be trimmed alike; got (%s, %s, %s)", col, dir, op)
	}
}

// code_review test-coverage gap: sort=closed_at combined with a NON-terminal
// status is a legal request that necessarily returns nothing, because no queued
// or running wi has a closed_at. Pin it as intended rather than surprising: both
// predicates are ANDed, so the empty result is a property of the query, not a
// bug to be "fixed" later by dropping the IS NOT NULL.
func TestBuildListWorkItemsQuery_ClosedAtSortWithOpenStatusIsEmptyByConstruction(t *testing.T) {
	query, _, _ := buildListWorkItemsQuery("proj", ListWorkItemsFilter{
		Status: []string{"queued", "running"},
		Sort:   ListWorkItemsSortClosedAt,
		Limit:  20,
	})
	for _, want := range []string{"wi.status = ANY($2)", "wi.closed_at IS NOT NULL"} {
		if !strings.Contains(query, want) {
			t.Errorf("expected %q to be ANDed into the query; got:\n%s", want, query)
		}
	}
	// Neither predicate may be dropped in favour of the other.
	if strings.Count(query, " AND ") < 2 {
		t.Errorf("project, status and closed_at predicates must all be ANDed; got:\n%s", query)
	}
}
