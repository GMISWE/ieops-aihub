package server

// aihub#265 — DB-free gates for GET /v1/work_items/:id/step's step history.
//
// Why these exist in this shape. The behaviour they guard is assembled inside a
// handler that needs a database, so the two parts that CAN be reached without
// one are pulled out and pinned here: the truncation arithmetic, and the
// SQL-to-struct column order. They run on every PR with no service container.
//
// Be honest about what is left over. Neither test proves that handleGetStep
// passes the real query result into truncateCompletedSteps — an edit that hands
// it a nil slice keeps both of these green, and that mutant was measured green
// on all five gates before aihub#354. Nor can the column-order guard see
// through the subquery: `error_type AS artifact_summary` inside FROM ( ... )
// would satisfy the outer SELECT list it reads. Those are the handler-shaped
// holes, and they are why routes_step_dbgated_test.go exists — that file is the
// behavioural half, gated on AIHUB_TEST_DB and named by its own CI step.
//
// Run: go test ./internal/server/ -run 'TestTruncateCompletedSteps|TestCompletedStepsQuery|TestScanTargets|TestColumnIdentifier' (no database needed)

import (
	"fmt"
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
	//
	// The parenthesis depth and the commas are read off a MASK in which comments
	// and string literals have been blanked out, because a literal is allowed to
	// contain either. `COALESCE(status, 'no :) status')` desynchronises a counter
	// that reads the raw text, and the resulting mis-split is reported as a
	// column list nobody wrote. The mask is length-preserving, so the expressions
	// handed to columnIdentifier and printed on failure are still verbatim.
	mask := sqlNoise.ReplaceAllStringFunc(m[1], func(s string) string {
		return strings.Repeat(" ", len(s))
	})
	if len(mask) != len(m[1]) {
		t.Fatalf("noise mask is %d bytes for a %d-byte column list; the split below would read "+
			"the wrong offsets", len(mask), len(m[1]))
	}
	var cols []string
	depth, start := 0, 0
	for i := 0; i < len(mask); i++ {
		switch mask[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				cols = append(cols, strings.TrimSpace(m[1][start:i]))
				start = i + 1
			}
		}
	}
	if s := strings.TrimSpace(m[1][start:]); s != "" {
		cols = append(cols, s)
	}

	got := make([]string, len(cols))
	for i, c := range cols {
		name, err := columnIdentifier(c)
		if err != nil {
			// Reported here as well as through the DeepEqual below, because the
			// list comparison can only say "position i is wrong" while this says
			// which expression and why.
			t.Errorf("completedStepsQuery's outer SELECT position %d is %q, which %v.\n"+
				"scanTargets reads this list positionally, so every position has to be pinned to "+
				"exactly one column. Rewrite the expression, or widen columnIdentifier deliberately "+
				"and say in the same change why the new shape still delivers one column's value.",
				i, c, err)
			got[i] = "<" + err.Error() + ">"
			continue
		}
		got[i] = name
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

// sqlValueKeywords are the bare words a SELECT-list expression may contain that
// are NOT references to a table column.
//
// Deliberately three entries, and deliberately not growing into a SQL keyword
// table. A keyword blocklist is the same shape as the function-name reasoning
// this reduction replaced: it is a list of the spellings someone has seen, and
// the next spelling walks through. Everything structural — function names, cast
// targets, qualifiers, aliases, literals, comments — is recognised by its
// POSITION in the token stream below rather than by being named here.
//
// The consequence is that an expression like `escalated IS NOT NULL` or
// `NOT escalated` is refused. That is correct rather than unfortunate: those
// deliver a value DERIVED from the column, not the column, and a positional scan
// into CompletedStep.Escalated would be filing a different quantity under that
// field.
var sqlValueKeywords = map[string]bool{"true": true, "false": true, "null": true}

// sqlNoise strips the three things whose CONTENTS must never be read as SQL:
// line comments, block comments, and single-quoted string literals (`”` is the
// escape for a quote inside one).
//
// 🔴 Not cosmetic. Without it `'step_id'` reduces to the column name step_id and
// a SELECT list of bare constants passes the guard while every row carries the
// constant instead of the column — measured, and the reason this exists. A
// literal containing a parenthesis also desynchronises the top-level-comma
// splitter, so this runs BEFORE the split, not after.
//
// Replaced by a space rather than removed so that stripping cannot fuse two
// neighbouring tokens into one identifier.
var sqlNoise = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/|'(?:[^']|'')*'`)

// sqlNumber strips numeric literals, including exponent form.
//
// The leading \b is what keeps it away from identifiers: in `a1` there is no
// word boundary before the digit, so `a1` survives intact, while `1e5` is
// removed whole. Without this, `1e5` tokenises as the identifier `e5` — the
// claim that "digits cannot start an identifier" is true and still does not
// keep numeric literals out on its own.
var sqlNumber = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?`)

func stripSQLNoise(expr string) string {
	return sqlNumber.ReplaceAllString(sqlNoise.ReplaceAllString(expr, " "), " ")
}

// sqlIdentRE matches one unquoted SQL identifier.
var sqlIdentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

// qualifiedName is one `a.b.c` chain, already split, plus the two facts about
// its surroundings that decide whether it names a column.
type qualifiedName struct {
	parts      []string
	lastQuoted bool // the final segment was written "like this"
	isFunction bool // immediately followed by `(`
	isCastType bool // immediately preceded by `::`
	afterAS    bool // immediately preceded by the AS keyword
}

// scanQualifiedNames walks an expression and returns its identifier chains.
//
// Chains rather than loose tokens is the whole point. The version this replaces
// collected identifiers one at a time and, on seeing a `.`, popped whatever it
// had recorded last — assuming that was the qualifier. It is not always: when
// the token before the dot was skipped (a cast target, a function name), the pop
// removes a REAL column instead. Measured escape, both directions:
//
//	COALESCE(artifact_summary, error_type)::pg_catalog.varchar(255)
//
// `pg_catalog` was dropped by the `::` rule so it was never recorded; `varchar`
// then popped `error_type` and was itself skipped for being followed by `(`.
// Net: one real column reference deleted, none added, expression accepted as
// `artifact_summary` — and the live handler returned one step's summary under
// both artifact_summary and error_type. Reading the chain as a unit removes the
// guess.
func scanQualifiedNames(expr string) []qualifiedName {
	var out []qualifiedName
	i, prevWasAS := 0, false
	for i < len(expr) {
		// Skip whitespace, remembering nothing.
		if expr[i] == ' ' || expr[i] == '\t' || expr[i] == '\n' || expr[i] == '\r' {
			i++
			continue
		}
		// A cast operator; the chain that follows is a TYPE name.
		if strings.HasPrefix(expr[i:], "::") {
			i += 2
			skipSpace(expr, &i)
			if q, next, ok := readChain(expr, i); ok {
				q.isCastType = true
				out = append(out, q)
				i = next
				prevWasAS = false
				continue
			}
			continue
		}
		if q, next, ok := readChain(expr, i); ok {
			q.afterAS = prevWasAS
			out = append(out, q)
			prevWasAS = !q.lastQuoted && len(q.parts) == 1 &&
				strings.EqualFold(q.parts[0], "as")
			i = next
			continue
		}
		i++
	}
	return out
}

func skipSpace(s string, i *int) {
	for *i < len(s) && (s[*i] == ' ' || s[*i] == '\t' || s[*i] == '\n' || s[*i] == '\r') {
		*i++
	}
}

// readChain reads `ident ( '.' ident )*` at position i, where each ident is
// either bare or "quoted", and reports whether the chain is a function name.
func readChain(s string, i int) (qualifiedName, int, bool) {
	var q qualifiedName
	for {
		part, quoted, next, ok := readIdent(s, i)
		if !ok {
			if len(q.parts) == 0 {
				return q, i, false
			}
			break
		}
		q.parts = append(q.parts, part)
		q.lastQuoted = quoted
		i = next
		j := i
		skipSpace(s, &j)
		if j < len(s) && s[j] == '.' {
			j++
			skipSpace(s, &j)
			i = j
			continue
		}
		break
	}
	j := i
	skipSpace(s, &j)
	q.isFunction = j < len(s) && s[j] == '('
	return q, i, true
}

func readIdent(s string, i int) (name string, quoted bool, next int, ok bool) {
	if i < len(s) && s[i] == '"' {
		if end := strings.IndexByte(s[i+1:], '"'); end >= 0 {
			return s[i+1 : i+1+end], true, i + end + 2, true
		}
		return "", false, i, false
	}
	if m := sqlIdentRE.FindString(s[i:]); m != "" {
		return m, false, i + len(m), true
	}
	return "", false, i, false
}

// columnRefs returns the DISTINCT table columns one SELECT-list expression
// references, in order of first appearance.
//
// 🔴 Why every column and not the first identifier (aihub#354). The version this
// replaces took the first bare identifier inside the first parenthesis, so
// COALESCE(artifact_summary, error_type) reduced to artifact_summary and
// COALESCE(error_type, artifact_summary) reduced to error_type. Both directions
// therefore equalled the name CompletedStep's field order expected, and the
// guard stayed green while the handler served — measured against a live database
// — one completed step's summary under BOTH "artifact_summary" and "error_type",
// and one failed step's error under both. Build, vet, go test, go test with a
// database, and golangci-lint were all green on that mutant.
//
// The invariant that actually holds is not "the expected name appears somewhere"
// but "this expression can only ever deliver ONE column's value". An expression
// naming two columns cannot be statically pinned to either, whatever spelling it
// uses — COALESCE, NULLIF, GREATEST, CASE, `||`, a function nobody has written
// yet. Refusing all of them closes the class; enumerating the ones seen so far
// only moves the escape one rename away.
//
// Four things are NOT column references and are dropped by position, never by
// name: a function name (chain followed by `(`), a cast target (chain preceded
// by `::`), the qualifier half of a chain, and an alias (chain preceded by the
// AS keyword). Dropping the alias rather than trusting it is deliberate:
// `error_type AS artifact_summary` then reduces to error_type and is caught,
// where believing the alias would have made it the outer SELECT's own version of
// the subquery hole.
//
// Distinct, because `COALESCE(NULLIF(error_type, ”), error_type)` names one
// column twice and IS pinnable to it.
func columnRefs(expr string) []string {
	var refs []string
	seen := map[string]bool{}
	for _, q := range scanQualifiedNames(stripSQLNoise(expr)) {
		if q.isFunction || q.isCastType || q.afterAS || len(q.parts) == 0 {
			continue
		}
		name := q.parts[len(q.parts)-1]
		// Only an UNQUOTED true/false/null is the literal; "null" in quotes is a
		// column that had to be quoted because it collides with a keyword.
		if !q.lastQuoted {
			if sqlValueKeywords[strings.ToLower(name)] {
				continue
			}
			if strings.EqualFold(name, "as") {
				continue
			}
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		refs = append(refs, name)
	}
	return refs
}

// columnIdentifier names the single table column a SELECT-list expression
// delivers, or explains why the expression cannot be pinned to one.
func columnIdentifier(expr string) (string, error) {
	refs := columnRefs(expr)
	switch len(refs) {
	case 1:
		return refs[0], nil
	case 0:
		return "", fmt.Errorf("names no column at all, so nothing pins it to a struct field")
	default:
		return "", fmt.Errorf("names %d distinct columns (%s), so which one it delivers depends on "+
			"the row's data rather than on the query — a NULL in one makes a sibling's value surface "+
			"under this field. (If some of those words are SQL keywords rather than columns, this "+
			"expression is beyond what columnRefs models; simplify it or extend columnRefs, but do "+
			"not relax the one-column rule)", len(refs), strings.Join(refs, ", "))
	}
}

// TestColumnIdentifierNamesExactlyOneColumn pins BOTH states of the reduction
// TestCompletedStepsQueryMatchesStructOrder is built on.
//
// Both halves are the point. A guard that only rejects is a guard tuned to be
// permanently red, and the legitimate expression it must keep accepting is not
// hypothetical: completedStepsQuery selects COALESCE(escalated, false) today,
// because escalated is a nullable column scanned into a non-pointer bool.
//
// The accept list deliberately reaches past the shapes the query uses now. Every
// entry marked with a source below was found by an adversarial review of the
// FIRST version of this reduction, which accepted two of the reject cases and
// refused four of the accept cases.
func TestColumnIdentifierNamesExactlyOneColumn(t *testing.T) {
	t.Run("accepts expressions that deliver one column", func(t *testing.T) {
		for _, tc := range []struct{ expr, want string }{
			{"step_id", "step_id"},
			{"  artifact_summary  ", "artifact_summary"},
			{"COALESCE(escalated, false)", "escalated"}, // the live query's own shape
			{"coalesce(escalated, FALSE)", "escalated"}, // case is not the invariant
			{"COALESCE(run_attempt_id, NULL)", "run_attempt_id"},
			{"recent.escalated", "escalated"},     // a qualifier is not a second column
			{"escalated :: boolean", "escalated"}, // nor is a cast target
			{"error_type::pg_catalog.text", "error_type"},
			{`escalated::"boolean"`, "escalated"},
			{"NULLIF(error_type, '')", "error_type"},
			// An alias renames the output; the scan stays positional, so the
			// expression underneath it is what has to be pinned.
			{"COALESCE(escalated, false) AS escalated", "escalated"},
			{"CAST(escalated AS boolean)", "escalated"},
			// An alias does NOT launder the expression under it: this reduces to
			// error_type, so at the position where CompletedStep expects
			// artifact_summary the list comparison in
			// TestCompletedStepsQueryMatchesStructOrder goes red. Believing the
			// alias instead would have reproduced the subquery hole in the outer
			// SELECT.
			{"error_type AS artifact_summary", "error_type"},
			// A string literal is a value, not a column, and neither is a comment.
			{"COALESCE(error_type, 'unknown')", "error_type"},
			{"artifact_summary /* was error_type */", "artifact_summary"},
			{"COALESCE(NULLIF(error_type, ''), error_type)", "error_type"},
			{`"error_type"`, "error_type"},
			{`"null"`, "null"}, // a column that had to be quoted, not the literal
		} {
			got, err := columnIdentifier(tc.expr)
			if err != nil {
				t.Errorf("columnIdentifier(%q) refused a legitimate single-column expression: %v",
					tc.expr, err)
				continue
			}
			if got != tc.want {
				t.Errorf("columnIdentifier(%q) = %q, want %q", tc.expr, got, tc.want)
			}
		}
	})

	t.Run("refuses expressions whose value depends on the data", func(t *testing.T) {
		for _, expr := range []string{
			// The aihub#354 mutant, both directions. Under the old reduction each
			// of these matched the field name at its own position and every gate
			// stayed green.
			"COALESCE(artifact_summary, error_type)",
			"COALESCE(error_type, artifact_summary)",
			// The same mutant wearing a cast. The FIRST version of this reduction
			// accepted both of these as artifact_summary / error_type, because its
			// qualifier handling popped the real second column and then skipped the
			// type name for being followed by `(`.
			"COALESCE(artifact_summary, error_type)::pg_catalog.varchar(255)",
			"COALESCE(error_type, artifact_summary)::pg_catalog.varchar(255)",
			// Same defect, other spellings — the guard has to answer these without
			// having been told about them one at a time.
			"artifact_summary || error_type",
			"GREATEST(completed_at, updated_at)",
			"CASE WHEN escalated THEN error_type ELSE artifact_summary END",
			// No column at all. A constant in a positional scan fills a field with
			// a value no row ever carried, and the FIRST version of this reduction
			// accepted 'step_id' AS THE COLUMN step_id.
			"'step_id'",
			"'artifact_summary'",
			"'a literal'",
			"42",
			"1e5",
		} {
			if got, err := columnIdentifier(expr); err == nil {
				t.Errorf("columnIdentifier(%q) accepted it as column %q. This expression's value "+
					"depends on the row, so pinning it to one struct field is exactly the drift "+
					"TestCompletedStepsQueryMatchesStructOrder exists to catch.", expr, got)
			}
		}
	})
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
