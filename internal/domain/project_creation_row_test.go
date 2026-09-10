package domain

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_create_project.md` hop-4 bullet
// about the row this tool writes, and the hop-4 bullet about members.
//
//	"- Creates the project row with the caller as `owner_user_id`, an empty
//	 `members` list, and `wi_seq` at its initial value — the counter that produces
//	 `project#N` slugs."
//	"- No members can be set at creation: the only way to add one is
//	 `pf_update_project`, which replaces the whole list."
//
// 🔴 WHY THIS IS A ROW-SHAPE ARM AND NOT A DATABASE ONE. The three facts the
// first bullet states are not three behaviours; they are one INSERT and two
// column defaults. `owner_user_id` is bound in the statement, and `members` and
// `wi_seq` are absent from it — which is the whole content of "empty" and "at its
// initial value", because a column an INSERT does not name takes the value the
// schema declares. A database arm would observe the same three values and could
// not distinguish "the statement sets them" from "the schema does"; that
// distinction is the claim, and it is the one a later diff can break by adding
// the columns to the statement while every observed value stays the same.
//
// The members half is the same shape read the other way round. Nothing local
// filters it — `internal/mcp/project_publication_contract_test.go`
// (`TestProjectMembersAreSettableOnlyThroughTheUpdateTool`) measures the argument
// map being forwarded WHOLE, `members` included — so what makes the card's "no
// members can be set at creation" true is that this package's request struct has
// no field to bind it to. That is a property of a Go type, and it is checked here
// because it is the only place it exists.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run TestProjectCreation -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// projectsSourcePath is this package's projects file, and the migration below is
// the schema half. Both are read as text inside an AST-bounded range rather than
// by line number: C1 bans line anchors under docs/ for the reason that applies
// just as well here, which is that a line number stops matching silently.
const (
	projectsSourcePath  = "projects.go"
	workItemsSourcePath = "work_items.go"
	projectsDDLPath     = "../db/migrations/0012_projects.sql"
)

// domainFuncSource returns one top-level function's source text, bounded by the
// parser's own idea of where the function starts and ends.
//
// 🔴 The bound is structural and the search inside it is textual, deliberately.
// The questions this file asks are "does this statement name that column" and
// "which argument is in that position", which are questions about text; the
// question "is that text inside THIS function" is the one a text scan gets wrong,
// because the next function's statements read identically from a grep's point of
// view. A missing function is a FAILURE and not an empty pass: every assertion
// below is a substring check against this string, and an empty one passes them
// all by containing nothing.
func domainFuncSource(t *testing.T, path, fn string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — this file is one side of every comparison below", path, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	base := fset.File(parsed.Pos()).Base()
	for _, decl := range parsed.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Recv != nil || d.Name.Name != fn {
			continue
		}
		return string(src[int(d.Pos())-base : int(d.End())-base])
	}
	t.Fatalf("%s declares no top-level func %s. Either it was renamed — in which case "+
		"every assertion below is about a function that no longer exists — or the claim it "+
		"carried has moved somewhere this arm cannot see.", path, fn)
	return ""
}

// insertColumnsRe pulls the column list out of an INSERT INTO <table> (…) clause.
var insertColumnsRe = regexp.MustCompile(`(?s)INSERT INTO projects \(([^)]*)\)`)

// insertBindingsRe pulls the argument list that follows the SQL, i.e. everything
// between the statement's closing backtick and the call's closing paren.
var insertBindingsRe = regexp.MustCompile("(?s)RETURNING `\\+projectSelectCols,\\s*(.*?)\\n\\s*\\)")

// ddlColumnDefaultRe reads one column's declared default out of the migration.
func ddlColumnDefault(t *testing.T, ddl, column string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(column) + `\s+([^,\n]+),?\s*$`)
	m := re.FindStringSubmatch(ddl)
	if m == nil {
		t.Fatalf("%s declares no %s column. The card's claim about that column's initial "+
			"value is a claim about this declaration, so a missing one is a failure rather "+
			"than a green.", projectsDDLPath, column)
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), ","))
}

// TestProjectCreationSetsTheCallerAsOwnerAndLeavesMembersAndTheCounterToTheSchema
// is the hop-4 row bullet and the members bullet, encoded as the things they tell
// a reader.
//
// MUTANTS (applied to this tree; the verdict is what RAN, not what was expected):
//
//	M1 enforcement: add `members` to the INSERT column list and `'[]'` to its
//	   VALUES                                    RED  the_statement_names_six_columns
//	                                                  + members_and_the_counter_come_from_the_schema
//	M2 enforcement: bind `$6` to a literal instead of `owner.ID`
//	                                             RED  the_caller_is_the_owner
//	M3 enforcement: drop `NOT NULL DEFAULT '[]'` from the migration's `members`
//	   column                                    RED  members_and_the_counter_come_from_the_schema
//	M4 enforcement: give CreateProjectRequest a `Members` field with a json tag
//	                                             RED  the_request_struct_has_nowhere_to_bind_members
//	M5 enforcement: point the slug counter at a sequence instead of
//	   projects.wi_seq                           RED  the_counter_is_the_project_row_column
//	M6 publication: delete the `owner_user_id` back-tick from the card's bullet
//	                                             RED  the_card_names_the_three_columns
//	M7 publication: reword the card's bullet to say `members_version` where it says
//	   `members`                                 RED  the_card_names_the_three_columns
//	G1 control:     rename an unrelated local in CreateProject
//	                                           GREEN  the arm is bound to the statement's
//	                                                  columns and bindings, not to the
//	                                                  function's other text
func TestProjectCreationSetsTheCallerAsOwnerAndLeavesMembersAndTheCounterToTheSchema(t *testing.T) {
	body := domainFuncSource(t, projectsSourcePath, "CreateProject")

	m := insertColumnsRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("domain.CreateProject carries no `INSERT INTO projects (…)` statement. The " +
			"card's hop-4 bullet is entirely about that statement's column list, so with it " +
			"gone this arm is comparing nothing and every subtest below is vacuous.")
	}
	var columns []string
	for _, raw := range strings.Split(m[1], ",") {
		if c := strings.TrimSpace(raw); c != "" {
			columns = append(columns, c)
		}
	}
	if len(columns) < 3 {
		t.Fatalf("the INSERT names %d column(s) (%v) — too few to be this statement, so the "+
			"extraction is broken rather than the code", len(columns), columns)
	}

	ddlRaw, err := os.ReadFile(filepath.Clean(projectsDDLPath))
	if err != nil {
		t.Fatalf("read %s: %v — the schema is the other half of \"at its initial value\"",
			projectsDDLPath, err)
	}
	ddl := string(ddlRaw)

	// The three columns the card names, read OUT OF THE CARD rather than written
	// here. A literal list in this file goes green on the day the card starts
	// claiming something else, which is the one day it was needed — the
	// base-strength arm's rule, applied to a bullet instead of to a range.
	cardColumns := projectCardRowColumns(t)

	t.Run("the_card_names_the_three_columns", func(t *testing.T) {
		for _, want := range []string{"owner_user_id", "members", "wi_seq"} {
			if !cardColumns[want] {
				t.Errorf("docs/mcp-cards/pf_create_project.md's hop-4 row bullet no longer names "+
					"`%s` (it names %v). This arm takes its subject from the card, so a bullet "+
					"that stopped mentioning a column would leave that column unchecked here "+
					"while the card still made a claim about the row.", want, sortedNameSet(cardColumns))
			}
		}
	})

	t.Run("the_statement_names_six_columns", func(t *testing.T) {
		want := []string{"name", "description", "visible", "repos", "scenario", "owner_user_id"}
		if len(columns) != len(want) {
			t.Errorf("the INSERT names %d columns (%v), and the card's bullet accounts for %d. "+
				"A seventh column is a seventh thing creation decides, and the bullet is the "+
				"only place a reader is told what creation decides at all.",
				len(columns), columns, len(want))
		}
		have := map[string]bool{}
		for _, c := range columns {
			have[c] = true
		}
		for _, w := range want {
			if !have[w] {
				t.Errorf("the INSERT does not name %q (it names %v)", w, columns)
			}
		}
	})

	t.Run("the_caller_is_the_owner", func(t *testing.T) {
		b := insertBindingsRe.FindStringSubmatch(body)
		if b == nil {
			t.Fatal("could not read CreateProject's INSERT bindings. The card says the CALLER " +
				"becomes owner_user_id, which is a claim about which expression sits in that " +
				"position; without the list there is no position to look at.")
		}
		var bindings []string
		for _, raw := range strings.Split(b[1], ",") {
			if v := strings.TrimSpace(raw); v != "" {
				bindings = append(bindings, v)
			}
		}
		if len(bindings) != len(columns) {
			t.Fatalf("the INSERT names %d columns and passes %d binding(s) (%v) — the two lists "+
				"cannot be aligned, so \"which expression fills owner_user_id\" has no answer "+
				"here", len(columns), len(bindings), bindings)
		}
		idx := -1
		for i, c := range columns {
			if c == "owner_user_id" {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatal("the INSERT names no owner_user_id column, so nothing binds the caller")
		}
		if got := bindings[idx]; got != "owner.ID" {
			t.Errorf("owner_user_id is bound to %q, and the card says the row is created with "+
				"the CALLER as its owner. A project whose owner_user_id is anybody else is a "+
				"project its creator cannot rotate the identifier of, cannot transfer, and "+
				"cannot add a member to — level 2 of checkProjectAccess is the only rung that "+
				"reads this column.", got)
		}
	})

	t.Run("members_and_the_counter_come_from_the_schema", func(t *testing.T) {
		for _, absent := range []string{"members", "wi_seq"} {
			for _, c := range columns {
				if c == absent {
					t.Errorf("the INSERT names %q. The card says that column starts at the value "+
						"the SCHEMA gives it; a statement that sets it takes the decision away "+
						"from the migration, and the two can then disagree with nothing to say so.",
						absent)
				}
			}
		}
		if got := ddlColumnDefault(t, ddl, "members"); !strings.Contains(got, "DEFAULT '[]'") {
			t.Errorf("the projects.members column is declared %q, and the card says a new "+
				"project starts with an EMPTY members list. That emptiness is this default and "+
				"nothing else, because CreateProject never names the column.", got)
		}
		if got := ddlColumnDefault(t, ddl, "wi_seq"); !strings.Contains(got, "DEFAULT 0") {
			t.Errorf("the projects.wi_seq column is declared %q, and the card says the counter "+
				"starts at its initial value. Nothing else sets it at creation, so a changed "+
				"default is a changed first slug.", got)
		}
	})

	t.Run("the_counter_is_the_project_row_column", func(t *testing.T) {
		// "the counter that produces `project#N` slugs" is a claim about a DIFFERENT
		// statement, in a different file: the one that hands out the next seq. Read
		// here because the bullet ties the two together, and because migration 0002
		// records that this repo once allocated seqs from per-project SEQUENCEs
		// instead — so "which counter" is a question with a wrong answer available.
		alloc := domainFuncSource(t, workItemsSourcePath, "CreateWorkItem")
		if !strings.Contains(alloc, "UPDATE projects SET wi_seq = wi_seq + 1") {
			t.Errorf("domain.CreateWorkItem does not increment projects.wi_seq. The card calls " +
				"that column \"the counter that produces project#N slugs\"; if the seq comes " +
				"from somewhere else, the column the card points a reader at is a number " +
				"nothing consumes.")
		}
		if !strings.Contains(alloc, "RETURNING wi_seq") {
			t.Error("domain.CreateWorkItem does not read the incremented projects.wi_seq back. " +
				"Incrementing without returning would leave the slug taken from something " +
				"other than this column, which is the half the card's \"produces\" asserts.")
		}
	})

	t.Run("the_request_struct_has_nowhere_to_bind_members", func(t *testing.T) {
		typ := reflect.TypeOf(CreateProjectRequest{})
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag == "members" || tag == "members_version" {
				t.Errorf("CreateProjectRequest declares a %q field. The card says no members can "+
					"be set at creation, and the ONLY thing making that true is this struct: the "+
					"MCP tool forwards its whole argument map, so a `members` key does reach this "+
					"handler and is dropped here for want of a field to bind to.", tag)
			}
		}
		// The floor: a struct this walk found no fields in would pass the loop above
		// by iterating zero times.
		if typ.NumField() < 3 {
			t.Fatalf("CreateProjectRequest has %d field(s) — too few to be the create body, so "+
				"the absence asserted above is an absence from nothing", typ.NumField())
		}
	})
}

// projectCardRowColumns returns the backticked column names in the card's hop-4
// row bullet.
func projectCardRowColumns(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp-cards", "pf_create_project.md"))
	if err != nil {
		t.Fatalf("read the card: %v — it is the publication side of this arm", err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	i := strings.Index(flat, "Creates the project row")
	if i < 0 {
		t.Fatal("docs/mcp-cards/pf_create_project.md's hop 4 no longer opens a bullet with " +
			"\"Creates the project row\". Either the bullet was rewritten — in which case this " +
			"arm is checking a claim nobody makes — or it is gone and this file should go too.")
	}
	rest := flat[i:]
	if j := strings.Index(rest, " - "); j > 0 {
		rest = rest[:j]
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(rest, -1) {
		out[m[1]] = true
	}
	if len(out) < 3 {
		t.Fatalf("the card's row bullet names %d backticked column(s) (%v); it claims three. "+
			"Fewer means the extraction found the wrong text.", len(out), sortedNameSet(out))
	}
	return out
}

// sortedNameSet is a stable rendering of a name set for an error message.
func sortedNameSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
