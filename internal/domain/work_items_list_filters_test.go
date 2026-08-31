package domain

// Hop 4 of the pf_list_work_items parameter contract (aihub#280).
//
// The contract has four hops — published MCP schema, MCP→HTTP forwarding, query
// param→ListWorkItemsFilter, filter field→SQL — and **a contract with N hops
// needs N assertions**. See internal/mcp/tools_list_wi_schema_test.go for the
// hop table and where the other three live.
//
// This file owns hop 4 only: does a *set* filter field actually reach the WHERE
// clause with its value bound? That is a distinct failure from hop 3. Three
// fields — Source, Milestone, ReadyOnly — used to be set correctly by a caller
// and then ignored by buildListWorkItemsWhere, so every hop-3-style assertion
// was green while the query returned unfiltered rows. `source` in particular was
// mis-triaged as "working" during the first pass on this bug precisely because
// the investigation stopped at hop 3.
//
// Pure-unit (no pool), like the cursor tests above it, so this runs on CI's
// plain "Unit tests" step instead of SKIPping there.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestBuildListWorkItemsWhere_EveryFilterFieldReachesSQL is the hop-4 guard.
//
// For each filter field: set it alone and assert both that the WHERE clause
// grew a predicate naming the right column, and that the value is bound as an
// argument. Asserting only the SQL text would pass for a hard-coded literal;
// asserting only the args would pass for a value bound but never referenced.
func TestBuildListWorkItemsWhere_EveryFilterFieldReachesSQL(t *testing.T) {
	for _, tc := range []struct {
		name string
		// filter is built with project="proj", so project is always $1 and the
		// field under test lands on $2.
		filter   ListWorkItemsFilter
		wantSQL  string
		wantArg  any
		argIndex int
	}{
		{
			name:     "Status",
			filter:   ListWorkItemsFilter{Status: []string{"wrapped"}},
			wantSQL:  "wi.status = ANY($2)",
			wantArg:  []string{"wrapped"},
			argIndex: 1,
		},
		{
			name:     "WIType",
			filter:   ListWorkItemsFilter{WIType: ptrStr("fix_bug")},
			wantSQL:  "wi.wi_type = $2",
			wantArg:  "fix_bug",
			argIndex: 1,
		},
		{
			name:     "Priority",
			filter:   ListWorkItemsFilter{Priority: ptrStr("urgent")},
			wantSQL:  "wi.priority = $2",
			wantArg:  "urgent",
			argIndex: 1,
		},
		{
			// Set by the handler since long before aihub#280, consumed by
			// nothing until it.
			name:     "Milestone",
			filter:   ListWorkItemsFilter{Milestone: ptrStr("v2")},
			wantSQL:  "wi.milestone = $2",
			wantArg:  "v2",
			argIndex: 1,
		},
		{
			// The one the first pass on this bug wrongly cleared: the handler
			// really did read `source` into filter.Source, and the SQL really did
			// ignore it.
			name:     "Source",
			filter:   ListWorkItemsFilter{Source: ptrStr("human")},
			wantSQL:  "wi.source = $2",
			wantArg:  "human",
			argIndex: 1,
		},
		{
			// A legal scenario value on purpose. work_items.scenario is CHECKed
			// to ('coding','writing','data'), so probing with "release" — the
			// value pf-release actually sends — would read as an endorsement of
			// a value no row can hold. See the note in pf-release/SKILL.md.
			name:     "Scenario",
			filter:   ListWorkItemsFilter{Scenario: ptrStr("writing")},
			wantSQL:  "wi.scenario = $2",
			wantArg:  "writing",
			argIndex: 1,
		},
		{
			name:     "Label",
			filter:   ListWorkItemsFilter{Label: ptrStr("mcp")},
			wantSQL:  "$2 = ANY(wi.labels)",
			wantArg:  "mcp",
			argIndex: 1,
		},
		{
			name:     "UserID",
			filter:   ListWorkItemsFilter{UserID: ptrStr("u_abc")},
			wantSQL:  "wi.reporter_user_id = $2",
			wantArg:  "u_abc",
			argIndex: 1,
		},
		{
			name:     "ReporterDisplay",
			filter:   ListWorkItemsFilter{ReporterDisplay: ptrStr("xiaokang")},
			wantSQL:  "wi.reporter_display ILIKE",
			wantArg:  "xiaokang",
			argIndex: 1,
		},
		{
			name:     "IDs",
			filter:   ListWorkItemsFilter{IDs: []string{"wi_a", "wi_b"}},
			wantSQL:  "(wi.id = ANY($2) OR wi.slug = ANY($2))",
			wantArg:  []string{"wi_a", "wi_b"},
			argIndex: 1,
		},
		{
			name:     "Since",
			filter:   ListWorkItemsFilter{Since: ptrTime(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))},
			wantSQL:  "wi.created_at >= $2",
			wantArg:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			argIndex: 1,
		},
		{
			name:     "Query",
			filter:   ListWorkItemsFilter{Query: ptrStr("latency")},
			wantSQL:  "wi.goal ILIKE",
			wantArg:  "latency",
			argIndex: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, where, args := buildListWorkItemsWhere("proj", tc.filter)
			if !strings.Contains(where, tc.wantSQL) {
				t.Errorf("filter.%s is set but the WHERE clause has no %q — the field is consumed by nothing, so the caller's value is silently ignored.\nWHERE: %s",
					tc.name, tc.wantSQL, where)
			}
			if len(args) <= tc.argIndex {
				t.Fatalf("filter.%s is set but only %d arg(s) were bound (want the value at $%d): %#v",
					tc.name, len(args), tc.argIndex+1, args)
			}
			if got := args[tc.argIndex]; !argsEqual(got, tc.wantArg) {
				t.Errorf("filter.%s bound $%d = %#v, want %#v", tc.name, tc.argIndex+1, got, tc.wantArg)
			}
		})
	}
}

// ReadyOnly gets its own case: it binds no arguments, so the generic
// value-binding assertion above does not apply to it.
func TestBuildListWorkItemsWhere_ReadyOnlyReachesSQL(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{ReadyOnly: true})

	// The three conditions that define "ready". Naming them individually rather
	// than string-matching the whole predicate means dropping any one of them is
	// caught, not just dropping the clause wholesale.
	for _, want := range []string{
		"wi.status = 'queued'",
		"wi.requires_human_session = false",
		"NOT EXISTS",
		"dep.kind = 'blocks'",
		"blocker.status NOT IN ('wrapped','cancelled','failed')",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("ReadyOnly must contribute %q to the WHERE clause; got: %s", want, where)
		}
	}
	// Binding no args must not disturb the placeholder numbering of anything
	// after it (the aihub#147 defect class).
	if len(args) != 1 || args[0] != "proj" {
		t.Errorf("ReadyOnly must bind no args; got %#v", args)
	}
}

// B7 / aihub#147: ReadyOnly is the first arg-free predicate inserted into the
// MIDDLE of the clause chain, so it is the first one that could desynchronise
// argIdx from len(args). Every other test either omits ReadyOnly or combines it
// only with filters that bind BEFORE it, so an erroneous argIdx++ inside the
// ReadyOnly branch left all of them green while `?ready_only=true&user_id=u`
// became HTTP 500 "could not determine data type of parameter $2".
//
// The assertion therefore has to use a filter that binds AFTER ReadyOnly.
func TestBuildListWorkItemsWhere_ReadyOnlyDoesNotConsumeAPlaceholder(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
		ReadyOnly: true,
		UserID:    ptrStr("u_abc"),
	})
	// $1=project; ReadyOnly binds nothing, so user_id must land on $2, not $3.
	if !strings.Contains(where, "wi.reporter_user_id = $2") {
		t.Errorf("ReadyOnly must not consume a placeholder; the next bound filter must land on $2.\nWHERE: %s", where)
	}
	if len(args) != 2 || args[0] != "proj" || args[1] != "u_abc" {
		t.Errorf("expected args [proj u_abc]; got %#v", args)
	}
	// The invariant that keeps the whole chain safe, asserted directly: the
	// clause text must not reference a placeholder with no bound arg behind it.
	for i := len(args) + 1; i <= len(args)+3; i++ {
		if strings.Contains(where, fmt.Sprintf("$%d", i)) {
			t.Errorf("WHERE references $%d but only %d args are bound — the query fails at runtime.\nWHERE: %s",
				i, len(args), where)
		}
	}
}

// A slug is a legal `ids` value: the MCP schema publishes "IDs or slugs" and
// GetWorkItem has always accepted either spelling. Before aihub#280 the list
// predicate matched only wi.id, so ids=["aihub#280"] returned an empty 200 —
// a published capability the SQL did not implement.
func TestBuildListWorkItemsWhere_IDsMatchSlugsToo(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{IDs: []string{"aihub#280"}})
	for _, want := range []string{"wi.id = ANY($2)", "wi.slug = ANY($2)"} {
		if !strings.Contains(where, want) {
			t.Errorf("ids must match slugs as well as ids; missing %q.\nWHERE: %s", want, where)
		}
	}
	// One bound arg referenced twice — not two args, which would desynchronise
	// every placeholder after it.
	if len(args) != 2 {
		t.Fatalf("expected 2 bound args (project + the id list); got %#v", args)
	}
	if !argsEqual(args[1], []string{"aihub#280"}) {
		t.Errorf("id list must be bound once as $2; got %#v", args[1])
	}
}

// `ready_only=true` and the ready queue's items[] segment must be the SAME
// question. This asserts the mechanism that makes them so — both are built from
// readyOnlyPredicate — on BOTH sides, which is the part the previous version of
// this test only claimed in a comment.
//
// An identical inlined copy would not be a defect; a divergent one is, and any
// divergence breaks the Contains below. That is the property worth guarding.
func TestReadyQueueItemsQueryUsesTheSharedReadyPredicate(t *testing.T) {
	// Side 1: the list filter.
	_, where, _ := buildListWorkItemsWhere("proj", ListWorkItemsFilter{ReadyOnly: true})
	if !strings.Contains(where, readyOnlyPredicate) {
		t.Errorf("the ready_only list filter must embed readyOnlyPredicate verbatim, not a paraphrase.\nWHERE: %s", where)
	}
	// Side 2: the ready queue. This is the assertion that did not exist before —
	// without it, inlining a divergent copy into GetReadyQueue left every test
	// green while the two surfaces silently disagreed about the same work item.
	items := buildReadyQueueItemsQuery()
	if !strings.Contains(items, readyOnlyPredicate) {
		t.Errorf("the ready queue's items[] query must embed readyOnlyPredicate verbatim.\nQUERY: %s", items)
	}
	// $1=project and $2=max, unchanged: this query is called with exactly those
	// two args, so a third placeholder would panic at runtime.
	for _, want := range []string{"wi.project = $1", "LIMIT $2"} {
		if !strings.Contains(items, want) {
			t.Errorf("ready queue items[] query must still contain %q; got: %s", want, items)
		}
	}
	if strings.Contains(items, "$3") {
		t.Errorf("ready queue items[] query is called with 2 args; it must not reference $3: %s", items)
	}
	// The predicate must be a self-contained parenthesised expression so it can
	// be ANDed into any WHERE without changing meaning.
	if !strings.HasPrefix(readyOnlyPredicate, "(") || !strings.HasSuffix(readyOnlyPredicate, ")") {
		t.Errorf("readyOnlyPredicate must be parenthesised so it composes safely into any WHERE; got %q", readyOnlyPredicate)
	}
	// And "no live blocker" must itself be one definition: it is ANDed into
	// readyOnlyPredicate and into two other ready-queue segments.
	if !strings.Contains(readyOnlyPredicate, noLiveBlockerPredicate) {
		t.Errorf("readyOnlyPredicate must be built from noLiveBlockerPredicate, not a copy of it")
	}
}

// The project scope clause that the whole ids=-without-project relaxation rests
// on. handleListWorkItems answers an ids= lookup with no project by setting
// AccessibleProjects instead of naming a project; if that field did not reach
// the SQL, the query would carry NO project clause at all and the relaxation
// would be an unbounded read across every project (aihub#280).
func TestBuildListWorkItemsWhere_AccessibleProjectsBoundsAnUnscopedQuery(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("", ListWorkItemsFilter{
		AccessibleProjects: []string{"a", "b"},
		IDs:                []string{"wi_x"},
	})
	if !strings.Contains(where, "wi.project = ANY($1)") {
		t.Fatalf("AccessibleProjects must bound the query with a project clause; got WHERE: %s", where)
	}
	if !argsEqual(args[0], []string{"a", "b"}) {
		t.Errorf("AccessibleProjects must be bound as $1; got %#v", args[0])
	}
	// The admin case: no project and no allow-list is deliberately unscoped, and
	// the handler only reaches it for u.Role=="admin". Pinned so that contract
	// stays a decision rather than becoming an accident.
	_, adminWhere, adminArgs := buildListWorkItemsWhere("", ListWorkItemsFilter{IDs: []string{"wi_x"}})
	if strings.Contains(adminWhere, "wi.project") {
		t.Errorf("no project + no AccessibleProjects must add no project clause (admin view-all); got: %s", adminWhere)
	}
	if len(adminArgs) != 1 {
		t.Errorf("expected only the IDs arg bound; got %#v", adminArgs)
	}
}

// Placeholder numbering with several of the newly-wired filters at once. Each
// added condition bumps argIdx, so a missed bump collides two values onto one
// placeholder — silently returning the wrong rows rather than erroring
// (aihub#147).
func TestBuildListWorkItemsWhere_NewFiltersKeepPlaceholderNumbering(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
		Status:    []string{"wrapped"},
		Milestone: ptrStr("v2"),
		Source:    ptrStr("human"),
		Scenario:  ptrStr("writing"),
		Label:     ptrStr("alpha"),
	})
	// $1=project, $2=status, $3=milestone, $4=source, $5=scenario, $6=label.
	for _, want := range []string{
		"wi.project = $1",
		"wi.status = ANY($2)",
		"wi.milestone = $3",
		"wi.source = $4",
		"wi.scenario = $5",
		"$6 = ANY(wi.labels)",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("expected %q in WHERE; got: %s", want, where)
		}
	}
	wantArgs := []any{"proj", []string{"wrapped"}, "v2", "human", "writing", "alpha"}
	if len(args) != len(wantArgs) {
		t.Fatalf("expected %d bound args, got %d: %#v", len(wantArgs), len(args), args)
	}
	for i := range wantArgs {
		if !argsEqual(args[i], wantArgs[i]) {
			t.Errorf("arg $%d = %#v, want %#v", i+1, args[i], wantArgs[i])
		}
	}
}

// The negative control, mirrored from the end-to-end suite: an empty filter must
// add no conditions beyond the project scope. Without this, "narrow everything
// by default" would make every n==0 probe pass for the wrong reason.
func TestBuildListWorkItemsWhere_EmptyFilterAddsNothing(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{})
	if where != "WHERE wi.project = $1" {
		t.Errorf("an empty filter must produce only the project scope; got %q", where)
	}
	if len(args) != 1 {
		t.Errorf("an empty filter must bind only the project; got %#v", args)
	}
}

func ptrTime(ts time.Time) *time.Time { return &ts }

// argsEqual compares bound args, handling the []string values that ANY($n)
// predicates bind (which are not comparable with ==).
func argsEqual(got, want any) bool {
	gs, gok := got.([]string)
	ws, wok := want.([]string)
	if gok || wok {
		if !gok || !wok || len(gs) != len(ws) {
			return false
		}
		for i := range gs {
			if gs[i] != ws[i] {
				return false
			}
		}
		return true
	}
	gt, gok := got.(time.Time)
	wt, wok := want.(time.Time)
	if gok && wok {
		return gt.Equal(wt)
	}
	return got == want
}
