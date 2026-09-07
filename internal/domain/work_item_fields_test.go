package domain

// aihub#396, the pure half: the Go vocabularies must equal the CHECK
// constraints, and EVERY CHECK-constrained work_items column that a caller can
// supply must have a Go validator.
//
// The behavioural half — that the answer is a 400 naming the field rather than
// the 500 the constraint used to produce — is in
// work_item_field_validation_db_test.go and needs a database. This file needs
// none, so it runs on every `go test ./...` including the CI step that sets no
// AIHUB_TEST_DB.
//
// ─── Why the class gate, and not four assertions ──────────────────────────
//
// The owner's decision on this wi records a repo-wide policy: *a vocabulary or
// limit that a DB CHECK enforces is ALSO validated in Go and answered 400
// naming the field; the CHECK is the last line of defence, never the
// caller-facing one.* Four hand-written assertions would pin today's four
// fields and say nothing about the fifth. So the gate DERIVES the set of
// CHECK-constrained columns from the migrations and requires each one to be
// either validated in Go or written down as not caller-supplied, with a reason.
//
// A column added tomorrow with a CHECK and no Go validator fails here, which is
// the only version of this policy that survives contact with a new column. It
// already earned that: the census found `scenario`, a fifth CHECK-constrained
// caller-supplied column that aihub#396's own list of four did not mention.
//
// 🔴 WHAT THIS GATE IS NOT SENSITIVE TO, stated so a green run is not
// over-read: it checks that every CHECK-constrained column has a validator
// RECORDED, not that the validator is CALLED. Deleting the call sites in
// CreateWorkItem leaves this test green. That half is
// work_item_field_validation_db_test.go's, which drives the real production
// path against a real database and fails with the actual 500. The two are
// complementary and neither substitutes for the other — do not "consolidate"
// them.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const migrationsDir = "../db/migrations"

// goValidatedWorkItemColumns names the CHECK-constrained work_items columns a
// caller can supply and that Go therefore refuses BEFORE the INSERT.
//
// The value is where the check lives, so a reader can go and read it — and so
// that "validated" is a claim about a specific function rather than a tick in a
// list.
//
// ⚠️ What this map asserts is "an illegal value cannot reach Postgres", NOT "the
// answer is 400". Those are different claims and only the first is the policy.
// `scenario` is the case that makes the difference visible, and it is recorded
// with its real status code rather than tidied into the others.
var goValidatedWorkItemColumns = map[string]string{
	"goal":     "CreateWorkItem: length and newline checks, predating this wi",
	"priority": "validateWorkItemPriority (work_item_fields.go)",
	"source":   "validateWorkItemSource (work_item_fields.go)",
	"labels":   "validateWorkItemLabels (work_item_fields.go)",
	"content":  "validateWorkItemContent (work_item_fields.go)",
	"scenario": "CreateWorkItem's `req.Scenario != \"coding\"` guard, which is STRICTER than the " +
		"CHECK ('coding','writing','data') — so no value can reach the constraint and the 500 " +
		"this policy exists to prevent is unreachable here. It answers 501 NOT_IMPLEMENTED, not " +
		"400, and that is deliberate for a reserved-but-unbuilt scenario; whether an " +
		"OUT-of-vocabulary scenario should be a 400 instead is a separate question and is NOT " +
		"settled by this entry. This column was surfaced BY this gate, which is what the gate is " +
		"for — it was not on aihub#396's list of four.",
}

// workItemColumnsNotCallerSupplied names CHECK-constrained columns no caller can
// set, so a Go validator would guard against this server's own bugs rather than
// against a request. Each needs a reason, because "not caller-supplied" is
// otherwise indistinguishable from "we forgot".
var workItemColumnsNotCallerSupplied = map[string]string{
	"status": "server-controlled: the INSERT hard-codes 'queued' and every later " +
		"transition is a named lifecycle call (claim, complete, cancel, the unblock sweep). " +
		"Neither CreateWorkItemRequest nor UpdateWorkItemRequest binds it.",
	"external_share_type": "set by the external-share path, not by create/update: neither request " +
		"struct binds it, and the value comes from which integration is being attached.",
}

// ─── deriving the columns and their CHECKs from the migrations ──────────────

// workItemsStatements returns every CREATE TABLE / ALTER TABLE statement in the
// migrations that targets work_items.
//
// Scoped to statements naming the table, rather than to two known filenames,
// so a CHECK added to work_items by a FUTURE migration is covered the day it
// lands. Scoped to work_items rather than to all CHECKs so a `status` CHECK on
// some other table cannot be attributed here.
func workItemsStatements(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read %s: %v", migrationsDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(migrationsDir, e.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		// stripSQLComments and stmtTableRE are this package's existing
		// migration-parsing helpers (work_items_status_vocab_test.go), reused
		// rather than reimplemented — and both are load-bearing for reasons that
		// file paid for and this one would otherwise pay again:
		//
		//   * comments are stripped BEFORE the `;` split, because 0002 contains
		//     "-- v1.22: kind field removed; wi_type is the single field…" INSIDE
		//     the CREATE TABLE body. An earlier draft of this file split first,
		//     cut the statement in half, and censused six columns instead of
		//     twenty. The sanity check below caught it — a vacuous gate would not
		//     have.
		//   * the statement's TABLE is matched, not merely mentioned. The
		//     CREATE TABLE for `memories` names work_items in a foreign key AND
		//     has its own `status` CHECK, so "the statement mentions work_items"
		//     attributes another table's constraint to this one. That is a real
		//     defect that already happened once, in the neighbour above.
		for _, stmt := range strings.Split(stripSQLComments(string(b)), ";") {
			m := stmtTableRE.FindStringSubmatch(stmt)
			if m == nil || strings.ToLower(m[1]) != "work_items" {
				continue
			}
			out = append(out, stmt)
		}
	}
	if len(out) == 0 {
		t.Fatalf("found no CREATE/ALTER TABLE work_items statement in %s — the walk is broken, "+
			"and every assertion built on it would be vacuous", migrationsDir)
	}
	return out
}

// checkExpressions returns the body of every `CHECK ( … )` in stmt.
//
// Parentheses are matched by counting rather than by regex, because the bodies
// nest: `CHECK (cardinality(labels) <= 20)` would be truncated at the first
// `)` by a lazy pattern and over-captured to the end of the column list by a
// greedy one.
func checkExpressions(stmt string) []string {
	var out []string
	upper := strings.ToUpper(stmt)
	for i := 0; ; {
		j := strings.Index(upper[i:], "CHECK")
		if j < 0 {
			return out
		}
		k := i + j + len("CHECK")
		for k < len(stmt) && (stmt[k] == ' ' || stmt[k] == '\n' || stmt[k] == '\t' || stmt[k] == '\r') {
			k++
		}
		if k >= len(stmt) || stmt[k] != '(' {
			i = i + j + len("CHECK")
			continue
		}
		depth, start := 0, k+1
		for ; k < len(stmt); k++ {
			switch stmt[k] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					out = append(out, stmt[start:k])
				}
			}
			if depth == 0 && k >= start {
				break
			}
		}
		i = k + 1
	}
}

// identRe finds candidate column names. quotedRE is the package's existing
// single-quoted-literal pattern (work_items_status_vocab_test.go) and is reused
// rather than copied.
var identRe = regexp.MustCompile(`[a-z_][a-z0-9_]*`)

// workItemColumns returns the column names declared for work_items.
func workItemColumns(t *testing.T, stmts []string) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	// CREATE TABLE: one column per indented line starting with the name.
	colLine := regexp.MustCompile(`(?m)^\s+([a-z_][a-z0-9_]*)\s+[A-Za-z]`)
	addColumn := regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	for _, stmt := range stmts {
		for _, m := range colLine.FindAllStringSubmatch(stmt, -1) {
			cols[m[1]] = true
		}
		for _, m := range addColumn.FindAllStringSubmatch(stmt, -1) {
			cols[m[2]] = true
		}
	}
	// Sanity: a broken parse would make the intersection below empty and every
	// assertion vacuous. These four are the ones this wi is about and none of
	// them can disappear without a migration.
	for _, want := range []string{"goal", "priority", "source", "labels"} {
		if !cols[want] {
			t.Fatalf("column census did not find %q among %v — the parse is broken, not the schema",
				want, sortedSetMembers(cols))
		}
	}
	return cols
}

// checkedWorkItemColumns is the census: which work_items columns are named
// inside a CHECK constraint.
func checkedWorkItemColumns(t *testing.T) map[string]bool {
	t.Helper()
	stmts := workItemsStatements(t)
	cols := workItemColumns(t, stmts)

	checked := map[string]bool{}
	for _, stmt := range stmts {
		for _, expr := range checkExpressions(stmt) {
			// Strip string literals first: 'sync_github' would otherwise contribute
			// the identifier `sync_github`, and a value is not a column.
			bare := quotedRE.ReplaceAllString(expr, "''")
			for _, id := range identRe.FindAllString(strings.ToLower(bare), -1) {
				if cols[id] {
					checked[id] = true
				}
			}
		}
	}
	if len(checked) < 4 {
		t.Fatalf("only %d work_items column(s) look CHECK-constrained (%v) — the CHECK parse is "+
			"broken; the migrations constrain more than that",
			len(checked), sortedSetMembers(checked))
	}
	t.Logf("CHECK-constrained work_items columns: %v", sortedSetMembers(checked))
	return checked
}

// TestEveryCheckedWorkItemColumnIsValidatedInGo is the class gate for the
// policy this wi establishes.
//
// It FAILS on the pre-fix tree naming priority, source, labels and content: all
// four were CHECK-constrained, all four caller-supplied, and none validated in
// Go, so each one answered 500 INTERNAL_ERROR with a SQLSTATE in the message.
func TestEveryCheckedWorkItemColumnIsValidatedInGo(t *testing.T) {
	checked := checkedWorkItemColumns(t)

	for col := range checked {
		if where, ok := goValidatedWorkItemColumns[col]; ok {
			_ = where
			continue
		}
		if reason, ok := workItemColumnsNotCallerSupplied[col]; ok {
			_ = reason
			continue
		}
		t.Errorf("work_items.%s carries a CHECK constraint and nothing in Go validates it, so an "+
			"illegal value reaches Postgres and comes back as 500 INTERNAL_ERROR with a SQLSTATE "+
			"in the message — a status that tells the caller to retry something that can never "+
			"succeed, and that carries none of the legal values to retry WITH.\n"+
			"Either add a validator and record it in goValidatedWorkItemColumns, or, if no caller "+
			"can supply this column, record it in workItemColumnsNotCallerSupplied with the "+
			"reason. (aihub#396)", col)
	}

	// Both maps must stay honest: an entry for a column that is no longer
	// CHECK-constrained is a claim about nothing, and the next reader would take
	// it as evidence the policy is being applied.
	for col, where := range goValidatedWorkItemColumns {
		if !checked[col] {
			t.Errorf("goValidatedWorkItemColumns claims work_items.%s is validated (%s), but no "+
				"CHECK constrains it any more — stale entry", col, where)
		}
	}
	for col, reason := range workItemColumnsNotCallerSupplied {
		if !checked[col] {
			t.Errorf("workItemColumnsNotCallerSupplied exempts work_items.%s (%s), but no CHECK "+
				"constrains it any more — stale exemption", col, reason)
		}
	}
}

// TestWorkItemVocabulariesMatchTheMigrations closes the drift between the Go
// sets and the SQL that will actually refuse the value.
//
// Parsing the migration is the point: a test written against the Go literals
// would pass while the database rejected a value the server accepted, which is
// a 500 again by a longer route.
func TestWorkItemVocabulariesMatchTheMigrations(t *testing.T) {
	stmts := workItemsStatements(t)

	for _, tc := range []struct {
		column string
		fromGo []string
	}{
		{column: "priority", fromGo: WorkItemPriorityList()},
		{column: "source", fromGo: WorkItemSourceList()},
	} {
		t.Run(tc.column, func(t *testing.T) {
			fromSQL := inClauseValues(t, stmts, tc.column)
			sort.Strings(fromSQL)
			got := append([]string(nil), tc.fromGo...)
			sort.Strings(got)
			if strings.Join(fromSQL, ",") != strings.Join(got, ",") {
				t.Errorf("work_items.%s vocabulary drifted:\n  SQL CHECK: %v\n  Go set:    %v",
					tc.column, fromSQL, got)
			}
		})
	}

	t.Run("labels_cap", func(t *testing.T) {
		if got := numericLimit(t, stmts, `cardinality\(labels\)\s*<=\s*(\d+)`); got != maxWorkItemLabels {
			t.Errorf("the labels cap is %d in SQL and %d in Go", got, maxWorkItemLabels)
		}
	})

	t.Run("content_limit", func(t *testing.T) {
		if got := numericLimit(t, stmts, `length\(content\)\s*<=\s*(\d+)`); got != maxWorkItemContentRunes {
			t.Errorf("the content limit is %d in SQL and %d in Go", got, maxWorkItemContentRunes)
		}
	})
}

// inClauseValues extracts the quoted values of `<column> IN (…)`.
func inClauseValues(t *testing.T, stmts []string, column string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?is)` + regexp.QuoteMeta(column) + `\s+IN\s*\(([^)]*)\)`)
	for _, stmt := range stmts {
		m := re.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		var out []string
		for _, q := range quotedRE.FindAllStringSubmatch(m[1], -1) {
			out = append(out, q[1])
		}
		if len(out) == 0 {
			t.Fatalf("found the %s IN clause but no quoted values in it: %q", column, m[1])
		}
		return out
	}
	t.Fatalf("no `%s IN (…)` CHECK found in any work_items statement — either the constraint is "+
		"gone (in which case the Go set is now the only rule and this test should say so "+
		"deliberately) or this parse is broken", column)
	return nil
}

// numericLimit extracts the integer from a `<expr> <= N` CHECK.
func numericLimit(t *testing.T, stmts []string, pattern string) int {
	t.Helper()
	re := regexp.MustCompile(`(?is)` + pattern)
	for _, stmt := range stmts {
		if m := re.FindStringSubmatch(stmt); m != nil {
			n := 0
			for _, c := range m[1] {
				n = n*10 + int(c-'0')
			}
			return n
		}
	}
	t.Fatalf("no CHECK matching %q found in any work_items statement — this parse is broken, or "+
		"the limit was dropped", pattern)
	return 0
}

// TestUpdateWorkItemRequestBindsOnlyTheFieldsThisPathValidates pins the reason
// UpdateWorkItem validates three of the five fields rather than all five.
//
// It is a claim about the request struct, and it is the ONLY thing standing
// between "source is create-only by construction" and "somebody added source to
// the update path and nobody added its check". Without this, that addition is
// silent: the field would bind, the DB CHECK would fire, and the 500 this wi
// removed would come back through the update door.
func TestUpdateWorkItemRequestBindsOnlyTheFieldsThisPathValidates(t *testing.T) {
	bound := jsonTagsOf(t, UpdateWorkItemRequest{})

	for _, field := range []string{"priority", "labels", "content"} {
		if !bound[field] {
			t.Errorf("UpdateWorkItemRequest no longer binds %q — if the field moved, its "+
				"validation in UpdateWorkItem is now dead code guarding nothing", field)
		}
	}
	for _, field := range []string{"source", "parent_work_item_id"} {
		if bound[field] {
			t.Errorf("UpdateWorkItemRequest now binds %q, which UpdateWorkItem does NOT validate. "+
				"work_items.%s is CHECK-constrained (or a foreign key), so an illegal value now "+
				"reaches Postgres through the update path and comes back as a 500 — the exact "+
				"defect aihub#396 closed on the create path. Add the check next to the other "+
				"three in UpdateWorkItem, then update this test.", field, field)
		}
	}
}

// jsonTagsOf returns the json names a struct binds.
func jsonTagsOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	typ := reflect.TypeOf(v)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	if len(out) == 0 {
		t.Fatal("the struct binds no json names — the reflection is broken")
	}
	return out
}
