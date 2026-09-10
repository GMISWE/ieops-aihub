package server

// aihub#543 probe wave 2 — the three pf_get_step claims that are about a
// DECLARATION rather than about rows: what `completed_steps` is read from, what
// the response echoes, and what the two sibling tools the description names do
// with a slug.
//
//	"`completed_steps` is the step-history table, not a derived view … keyed on
//	 `step_attempt_id`"
//	    -> TestCompletedStepsQueryReadsTheHistoryTableItself
//	"A `failed` entry is not a finished step, and the query does not hide one …
//	 with NO status filter, and that table's `status` domain is
//	 {completed, failed}"
//	    -> TestCompletedStepsQueryHasNoStatusFilter
//	"The echoed `work_item_id` is the canonical id even when a slug was passed"
//	    -> TestGetStepEchoesTheResolvedWorkItemID
//	"… and that is the value `pf_recall` and `pf_read_events` need"
//	    -> TestListEventsFiltersOnTheResolvedWorkItemID (the events half; the
//	       recall half is internal/domain's
//	       TestRecallResolvesTheWorkItemFilterBeforeComparingIt)
//
// 🔴 The last two exist because a card sentence was MEASURED FALSE. It said
// pf_recall and pf_read_events "return nothing for a slug, silently" — the
// pre-aihub#343 / pre-aihub#363 behaviour. Both resolve a slug today, the
// pf_get_step TOOL DESCRIPTION carried the same stale advice, and aihub#422's own
// test header had already recorded it as stale without anything going red. The
// arms below hold the resolution that makes the corrected sentence true.
//
// All four read source or a migration rather than driving a database, per
// aihub#543 spec §3.3: a query's text, a struct assignment and a CHECK
// constraint are not rows. The DB-gated arms that hold the same subjects from
// the other side are named in each doc comment.
//
//	GOWORK=off go test ./internal/server/ -run 'TestCompletedStepsQuery(Reads|HasNo)|TestGetStepEchoes|TestListEventsFilters' -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stepMigrationText returns every migration's SQL, joined.
func stepMigrationText(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "db", "migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files under ../db/migrations; the test is running from the wrong " +
			"directory and every pattern below would report a missing constraint")
	}
	var b strings.Builder
	for _, f := range files {
		src, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatal(readErr)
		}
		b.Write(src)
		b.WriteString("\n")
	}
	return b.String()
}

// TestCompletedStepsQueryReadsTheHistoryTableItself holds "the step-history
// table, not a derived view" and "keyed on `step_attempt_id`".
//
// "Not a derived view" is the half with no other home: every DB-gated arm in
// this package reads rows through the same query, so a view interposed between
// them and the table would be invisible to all of them. What that phrase buys a
// caller is that nothing filters or aggregates on the way out, which is the
// premise the failed-entry bullet below depends on.
//
// The keying is asserted as the UNIQUE index, which is what makes a second write
// under one step_attempt_id impossible; the refusal a caller sees is held
// end-to-end by routes_step_history_row_db_test.go
// (TestHandleUpdateStep_DuplicateStepAttemptIsAConflictNotASilentDrop).
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M15 enforcement: point completedStepsQuery at
//	    `wi_step_completions_recent_v`      GREEN, then RED — the first version of
//	                                        the pattern matched the table name as
//	                                        a PREFIX and this mutant is why it now
//	                                        ends in `\b`
//	M15b enforcement: point it at wi_step_state
//	                                        RED  the_query_names_the_table
//	M16 enforcement: change the CREATE TABLE to CREATE VIEW in 0005
//	                                        RED  the_query_names_the_table (and
//	                                             the status-domain arm below,
//	                                             which reads the table's body)
//	M17 enforcement: drop the UNIQUE from idx_wsc_attempt in 0005
//	                                        RED  the_history_is_keyed_on_the_step
//	                                             _attempt
//	P4  publication: make every citation in the pf_get_step card unresolvable
//	                                        RED  K12
func TestCompletedStepsQueryReadsTheHistoryTableItself(t *testing.T) {
	const table = "wi_step_completions"
	sql := stepMigrationText(t)

	t.Run("the_query_names_the_table", func(t *testing.T) {
		// The trailing `\b` is measured, not decorative: without it a mutant pointing
		// the query at `wi_step_completions_recent_v` matched this pattern as a PREFIX
		// and this arm stayed GREEN — a view interposed under a longer name is exactly
		// the shape the card's sentence refuses.
		if !regexp.MustCompile(`(?i)from\s+` + table + `\b`).MatchString(completedStepsQuery) {
			t.Errorf("completedStepsQuery does not read FROM %s:\n%s\nThe card says this response "+
				"is the history table itself; a name that is not the table is either a view or "+
				"another table, and both change what a caller is reading without changing the "+
				"response's shape.", table, completedStepsQuery)
		}
		if !regexp.MustCompile(`(?i)create\s+table\s+` + table + `\b`).MatchString(sql) {
			t.Errorf("no migration declares CREATE TABLE %s. \"not a derived view\" is the claim, "+
				"and a view with the same name and the same columns satisfies every row-level "+
				"assertion in this package.", table)
		}
		if regexp.MustCompile(`(?i)create\s+(or\s+replace\s+)?view\s+` + table + `\b`).MatchString(sql) {
			t.Errorf("a migration declares a VIEW named %s, which the card says this is not", table)
		}
	})

	t.Run("the_history_is_keyed_on_the_step_attempt", func(t *testing.T) {
		keyed := regexp.MustCompile(`(?is)create\s+unique\s+index\s+\w+\s+on\s+` + table +
			`\s*\(\s*step_attempt_id\s*\)`)
		if !keyed.MatchString(sql) {
			t.Error("no UNIQUE index on " + table + "(step_attempt_id). The card says the rows are " +
				"KEYED on it, and that is what refuses a second history row for one step attempt — " +
				"without the index the duplicate is stored and pf_get_step reports one step twice, " +
				"which reads to a resuming agent as a retry that never happened.")
		}
	})
}

// TestCompletedStepsQueryHasNoStatusFilter holds the failed-entry bullet's two
// verifiable halves: the query filters on the work item alone, and the column's
// domain is exactly the two values the description tells a caller to distinguish.
//
// The behavioural half — that a failed row really comes back — is held with a
// database by routes_step_dbgated_test.go
// (TestHandleGetStep_SummaryAndErrorTypeLandInTheirOwnFields, which seeds one
// completed and one failed row and reads both back).
//
// MUTANTS:
//
//	M18 enforcement: add `AND status = 'completed'` to completedStepsQuery
//	                                        RED  no_status_filter
//	M19 enforcement: add a third value to the status CHECK in 0005
//	                                        RED  the_status_domain_is_two_values
//	P9  publication: rename this arm        RED  K12 ARM_CITATION_UNRESOLVED
func TestCompletedStepsQueryHasNoStatusFilter(t *testing.T) {
	t.Run("no_status_filter", func(t *testing.T) {
		// Any comparison of the status column inside a WHERE, however spelled.
		filter := regexp.MustCompile(`(?i)where[^)]*\bstatus\b\s*(=|<>|!=|\bin\b|\blike\b)`)
		if filter.MatchString(completedStepsQuery) {
			t.Errorf("completedStepsQuery filters on status:\n%s\nThe card says it does not, and "+
				"that the prose moved rather than the query when aihub#450 corrected the advice: "+
				"filtering here would delete the retry history the same sentence promises and "+
				"break the aihub#390 invariant that completed_steps equals the attempt's "+
				"step_completed/step_failed events.", completedStepsQuery)
		}
		if !strings.Contains(completedStepsQuery, "work_item_id = $1") {
			t.Errorf("completedStepsQuery no longer filters by work item:\n%s\nThe assertion above "+
				"is that ONE predicate is present and the status is not; a query with no WHERE at "+
				"all would satisfy it while returning every work item's history",
				completedStepsQuery)
		}
	})

	t.Run("the_status_domain_is_two_values", func(t *testing.T) {
		// Scoped to the history table's own CREATE TABLE body: several tables in
		// this schema carry a `status` CHECK, and a domain read off whichever one
		// the joined text happened to match first would be a different claim
		// wearing this one's failure message.
		body := regexp.MustCompile(`(?is)create\s+table\s+wi_step_completions\s*\((.*?)\n\);`).
			FindStringSubmatch(stepMigrationText(t))
		if body == nil {
			t.Fatal("no CREATE TABLE wi_step_completions body found in any migration, so the domain " +
				"below would be read from another table's status column")
		}
		m := regexp.MustCompile(`(?is)status\s+text\s+not\s+null\s+check\s*\(\s*status\s+in\s*\(([^)]*)\)`).
			FindStringSubmatch(body[1])
		if m == nil {
			t.Fatal("no CHECK (status IN (…)) on wi_step_completions.status — the card " +
				"names the domain {completed, failed} as the reason a `failed` entry is a real " +
				"answer rather than a corrupt row, and without the constraint that domain is a " +
				"convention")
		}
		var values []string
		for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
			values = append(values, q[1])
		}
		if strings.Join(values, ",") != "completed,failed" {
			t.Errorf("the status domain is %v; the card publishes {completed, failed}. A third value "+
				"would arrive in completed_steps as an entry a caller has no rule for — the "+
				"description tells it to count `completed` and redo everything else.", values)
		}
	})
}

// stepSourceFunc returns the AST of one top-level function in this package.
func stepSourceFunc(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range parsed.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("%s declares no top-level %s; the card names it, so a rename is a review decision "+
		"rather than something a scan should absorb by finding nothing", file, name)
	return nil
}

// TestGetStepEchoesTheResolvedWorkItemID holds "The echoed `work_item_id` is the
// canonical id even when a slug was passed".
//
// The resolution itself — that GetWorkItem answers to either spelling — is held
// with a database by internal/domain/work_item_ref_db_test.go
// (TestGetWorkItemResolvesIDOrSlug). What no arm held is the hop between them:
// this handler could resolve the work item for its access check and then echo
// the caller's own parameter, which is precisely the defect aihub#343 fixed one
// endpoint over, where the resolved id was used for the check and the raw value
// for the query.
//
// So the assertion is about the SOURCE of the echoed value, not about its value
// in one fixture: the response's WorkItemID must be assigned from a variable
// that the function has re-assigned from the resolved row.
//
// MUTANTS:
//
//	M20 enforcement: `s.WorkItemID = c.Param("id")`      RED
//	M21 enforcement: replace the `wiID = wi.ID` line with `_ = wi.ID`  RED
//	P4  publication: make every citation in the pf_get_step card unresolvable
//	                                                      RED  K12
func TestGetStepEchoesTheResolvedWorkItemID(t *testing.T) {
	fn := stepSourceFunc(t, "routes_step.go", "handleGetStep")

	// Which identifiers hold a value assigned from a `.ID` selector — i.e. from
	// the resolved row rather than from the request.
	resolved := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		sel, ok := assign.Rhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ID" {
			return true
		}
		if id, ok := assign.Lhs[0].(*ast.Ident); ok {
			resolved[id.Name] = true
		}
		return true
	})
	if len(resolved) == 0 {
		t.Fatal("handleGetStep assigns nothing from a resolved row's .ID. The echo the card promises " +
			"is the CANONICAL id, and this handler is where a slug becomes one — see the aihub#127 " +
			"comment on the line that does it")
	}

	echoed := ""
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WorkItemID" {
			return true
		}
		if id, ok := assign.Rhs[0].(*ast.Ident); ok {
			echoed = id.Name
			return true
		}
		if inner, ok := assign.Rhs[0].(*ast.SelectorExpr); ok && inner.Sel.Name == "ID" {
			// Assigned straight from the row, which is the same guarantee.
			echoed = "wi.ID"
		}
		return true
	})
	switch {
	case echoed == "":
		t.Fatal("handleGetStep never assigns StepState.WorkItemID; the echo the card promises would " +
			"then be whatever the zero value marshals to")
	case echoed == "wi.ID":
		// Direct, and correct by construction.
	case !resolved[echoed]:
		t.Errorf("handleGetStep echoes %q, which it never assigned from the resolved row. The card "+
			"says the echoed work_item_id is the canonical id EVEN WHEN A SLUG WAS PASSED, and a "+
			"handler that echoes its own path parameter answers a slug with that slug — the exact "+
			"shape aihub#343 fixed on GET /v1/events, where the resolved id served the access "+
			"check and the raw one served the query.", echoed)
	}
}

// TestListEventsFiltersOnTheResolvedWorkItemID is the pf_read_events half of the
// corrected sentence: it takes either spelling, because the filter is built from
// the resolved row.
//
// The behavioural arm is events_slug_db_test.go
// (TestListEventsBySlug_ReturnsTheSameStreamAsByID, which asserts the two
// spellings return the same stream) and its visibility control
// (TestListEventsBySlug_StillScopesToTheWorkItem). This one is the wiring, and
// it is the half that regressed once: aihub#343's whole defect was a handler
// that resolved the reference for its access check and then filtered on the raw
// parameter, which no response shape can show.
//
// MUTANTS:
//
//	M22 enforcement: build the filter from the query parameter instead of wi.ID
//	    — the aihub#343 defect, restored    RED
//	P4  publication: make every citation in the pf_get_step card unresolvable
//	                                        RED  K12
func TestListEventsFiltersOnTheResolvedWorkItemID(t *testing.T) {
	fn := stepSourceFunc(t, "routes_memory.go", "handleListEvents")

	filtered, raw := false, false
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok || lhs.Sel.Name != "WorkItemID" {
			return true
		}
		unary, ok := assign.Rhs[0].(*ast.UnaryExpr)
		if !ok || unary.Op != token.AND {
			// `f.WorkItemID = &something` is the only shape this filter takes;
			// anything else is a rewrite that has to be read rather than matched.
			raw = true
			return true
		}
		if sel, ok := unary.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "ID" {
			filtered = true
			return true
		}
		raw = true
		return true
	})

	if !filtered || raw {
		t.Errorf("handleListEvents does not build its work-item filter from the resolved row's .ID "+
			"(resolved=%t, other-source=%t). The pf_get_step card now tells callers that "+
			"pf_read_events takes either spelling; on the raw parameter a slug matches nothing in "+
			"agent_events.work_item_id, which FK-references work_items(id), and the endpoint "+
			"answers 200 with an empty stream — indistinguishable from a work item with no "+
			"events. That is aihub#343, and it outlived aihub#127 by eight months for exactly "+
			"that reason.", filtered, raw)
	}
}
