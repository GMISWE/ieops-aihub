package server

// aihub#265 — DB-free gates for GET /v1/work_items/:id/step's step history.
//
// Why these exist in this shape. The behaviour they guard is assembled inside a
// handler that needs a database, and the DB-gated test that would cover it
// end-to-end cannot land while .github/workflows/ci.yml is held by another work
// item (dbtestcov fails the build for a DB-gated test that no CI step names).
// So the two parts that CAN be reached without a database are pulled out and
// pinned here: the truncation arithmetic, and the SQL-to-struct column order.
//
// Be honest about what is left over. Neither test proves that handleGetStep
// passes the real query result into truncateCompletedSteps — an edit that hands
// it a nil slice keeps both of these green. That gap is stated in the PR and is
// what the deferred DB test is for. What these DO close is every mutation that
// does not touch the handler: the arithmetic, the nil/empty distinction, the
// keep-the-newest direction, and the positional-scan transposition.
//
// Run: go test ./internal/server/ -run 'TestTruncateCompletedSteps|TestCompletedStepsQuery' (no database needed)

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func step(id string) CompletedStep { return CompletedStep{StepID: id} }

func stepIDs(rows []CompletedStep) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.StepID
	}
	return out
}

// TestTruncateCompletedSteps pins the cap arithmetic and, more importantly, the
// DIRECTION of the drop and the nil handling.
//
// The direction matters because both directions "truncate correctly" by row
// count: dropping from the back also yields `limit` rows, and a test that only
// asserts the length passes on the mutant that throws away the recent history
// and keeps the first 200 attempts of a runaway loop — the opposite of what a
// resuming agent needs.
func TestTruncateCompletedSteps(t *testing.T) {
	mk := func(ids ...string) []CompletedStep {
		out := make([]CompletedStep, 0, len(ids))
		for _, id := range ids {
			out = append(out, step(id))
		}
		return out
	}

	cases := []struct {
		name      string
		in        []CompletedStep
		limit     int
		wantIDs   []string
		wantTrunc bool
	}{
		{"nil becomes empty, never nil", nil, 3, []string{}, false},
		{"empty stays empty", mk(), 3, []string{}, false},
		{"under the cap is untouched", mk("a", "b"), 3, []string{"a", "b"}, false},
		{"exactly at the cap is NOT truncated", mk("a", "b", "c"), 3, []string{"a", "b", "c"}, false},
		{"one over drops the OLDEST, which is at the front", mk("a", "b", "c", "d"), 3, []string{"b", "c", "d"}, true},
		{"far over keeps the newest window, still oldest-first", mk("a", "b", "c", "d", "e", "f"), 2, []string{"e", "f"}, true},
		{"limit 1 keeps only the newest", mk("a", "b", "c"), 1, []string{"c"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, trunc := truncateCompletedSteps(tc.in, tc.limit)
			if got == nil {
				t.Fatal("returned a nil slice; it marshals to null, which is a third spelling of " +
					"\"empty\" on a field whose contract is that [] and absent differ")
			}
			if !reflect.DeepEqual(stepIDs(got), tc.wantIDs) {
				t.Errorf("ids = %v, want %v", stepIDs(got), tc.wantIDs)
			}
			if trunc != tc.wantTrunc {
				t.Errorf("truncated = %v, want %v — a cap that fires without disclosing it is the "+
					"defect completed_steps_truncated exists to prevent", trunc, tc.wantTrunc)
			}
			if len(got) > tc.limit {
				t.Errorf("returned %d rows, over the cap of %d", len(got), tc.limit)
			}
		})
	}

	// The real call site asks the database for completedStepsFetch rows. Pin that
	// it is strictly more than the cap, because the off-by-one is invisible from
	// either side alone: fetching exactly `limit` rows makes
	// truncateCompletedSteps unable to EVER report truncation, and a history that
	// is exactly full would be served as if it were complete. Measured: without
	// this assertion, changing the fetch size to completedStepsLimit keeps every
	// other arm in this file green.
	if completedStepsFetch != completedStepsLimit+1 {
		t.Errorf("completedStepsFetch = %d, want completedStepsLimit+1 = %d; the extra row is the "+
			"only thing that lets a full page be distinguished from an overflowing one",
			completedStepsFetch, completedStepsLimit+1)
	}
	full := make([]CompletedStep, completedStepsLimit)
	if _, trunc := truncateCompletedSteps(full, completedStepsLimit); trunc {
		t.Error("a history of exactly completedStepsLimit rows reported as truncated")
	}
	over := make([]CompletedStep, completedStepsLimit+1)
	got, trunc := truncateCompletedSteps(over, completedStepsLimit)
	if !trunc {
		t.Error("completedStepsLimit+1 rows — what loadCompletedSteps actually fetches — did not " +
			"report truncation, so the extra row bought nothing")
	}
	if len(got) != completedStepsLimit {
		t.Errorf("truncated to %d rows, want %d", len(got), completedStepsLimit)
	}
}

// outerSelectList extracts the column expressions of completedStepsQuery's OUTER
// SELECT — the one whose order the positional Scan consumes.
//
// The query has two SELECTs; taking the first one is deliberate and is the outer
// one because the subquery is nested inside FROM ( ... ). The test below fails
// loudly if the shape stops matching, rather than silently comparing the wrong
// list.
var outerSelectRE = regexp.MustCompile(`(?s)^\s*SELECT\s+(.*?)\s+FROM\s*\(`)

// TestCompletedStepsQueryMatchesStructOrder is the other half of the
// positional-scan contract.
//
// scanTargets builds its argument list from CompletedStep by reflection, so the
// SCAN side cannot drift from the struct. That leaves exactly one thing able to
// drift: the SQL column order. Swapping two same-typed columns there —
// artifact_summary and error_type are both nullable text — would file every
// step's summary under error_type with no compile error, no vet warning, no
// driver error, and no failure in any test that merely checks a row came back.
func TestCompletedStepsQueryMatchesStructOrder(t *testing.T) {
	m := outerSelectRE.FindStringSubmatch(completedStepsQuery)
	if m == nil {
		t.Fatal("could not find completedStepsQuery's outer SELECT ... FROM ( — the query's shape " +
			"changed, so this guard is no longer reading the column list it thinks it is. Fix the " +
			"pattern deliberately rather than deleting the test.")
	}

	// Split on top-level commas only: COALESCE(escalated, false) contains one.
	var cols []string
	depth, cur := 0, strings.Builder{}
	for _, r := range m[1] {
		switch {
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			cols = append(cols, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		cols = append(cols, s)
	}

	// A column may be wrapped in a function; the name it delivers is the first
	// bare identifier inside. COALESCE(escalated, false) -> escalated.
	bareName := regexp.MustCompile(`[a-z_][a-z0-9_]*`)
	got := make([]string, len(cols))
	for i, c := range cols {
		if idx := strings.IndexByte(c, '('); idx >= 0 {
			inner := c[idx+1:]
			got[i] = bareName.FindString(inner)
		} else {
			got[i] = strings.TrimSpace(c)
		}
	}

	typ := reflect.TypeOf(CompletedStep{})
	want := make([]string, typ.NumField())
	for i := range want {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		want[i] = name
	}

	if len(want) == 0 || len(got) == 0 {
		t.Fatalf("empty column list (sql=%v struct=%v); this guard would pass vacuously", got, want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("completedStepsQuery's outer SELECT is\n  %v\nbut CompletedStep's fields are\n  %v\n"+
			"scanTargets is positional and derives from the struct, so these two lists ARE the "+
			"contract. Any disagreement silently files one column's values under another field.",
			got, want)
	}
}

// TestScanTargetsCoversEveryField stops the reflection helper from quietly
// missing a field — e.g. if someone converts it back to a hand-written list, or
// adds a field to CompletedStep that the SELECT does not provide.
func TestScanTargetsCoversEveryField(t *testing.T) {
	var cs CompletedStep
	targets := cs.scanTargets()
	typ := reflect.TypeOf(cs)
	if len(targets) != typ.NumField() {
		t.Fatalf("scanTargets returns %d pointers for a struct with %d fields; a positional Scan "+
			"with the wrong arity fails at runtime against a real database and nowhere else",
			len(targets), typ.NumField())
	}
	val := reflect.ValueOf(&cs).Elem()
	for i := range targets {
		want := val.Field(i).Addr().Interface()
		if targets[i] != want {
			t.Errorf("scanTargets[%d] does not point at field %q", i, typ.Field(i).Name)
		}
	}
}
