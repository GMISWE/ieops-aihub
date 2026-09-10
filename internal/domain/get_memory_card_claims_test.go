package domain

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_get_memory.md` sentence about
// which ids this tool will take:
//
//	"`memory_id` accepts any id in a lineage in the sense recall returns them; a
//	 version chain's `latest_id` cursor is what `pf_update_memory` advances."
//	    -> TestGetMemoryByIDTakesAnyLineageMemberAndCarriesTheHeadCursor
//
// ─── The two halves live in different places, deliberately ─────────────────
//
// "pf_update_memory advances the cursor" is a row transition and is held against
// a real database by TestUpdateMemory and TestSupersedeAdvancesCursor in this
// package — both DB-gated, both already registered in
// internal/citest/dbtestcov/gated_tests.txt, so the card cites them rather than
// this file growing a second copy.
//
// What NOTHING held is the read side, and it is the half a caller depends on: a
// by-id read has to accept an id that is no longer the head. Recall hands out
// head ids, but a caller can hold an older one from an earlier page, from an
// artifact link or from its own notes, and the failure mode of a version
// predicate on this query is a 404 — indistinguishable from a redaction, which
// is the one case this query really is supposed to hide. So the property is
// stated as the ABSENCE of a version predicate plus the PRESENCE of the cursor
// column, which together are what "accepts any id in a lineage" means at the
// SQL level.
//
// Non-DB: this is the shape of one query and the set of columns it selects.
//
//	GOWORK=off go test ./internal/domain/ -run TestGetMemoryByIDTakesAnyLineageMember -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
)

// domainFuncSQL returns the string constants inside ONE function of one non-test
// file, spliced and whitespace-collapsed.
//
// 🔴 Scoped to the function rather than matched by substring over the file. The
// first version of this arm looked for `FROM memories WHERE id = $1` anywhere in
// memory.go and found a recursive-CTE query that happens to open with the same
// clause — so it reported the by-id read as missing a column that query never
// had. A predicate that can match the wrong query is a predicate whose green is
// about a different subject.
func domainFuncSQL(t *testing.T, file, fnName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.BasicLit, *ast.BinaryExpr:
				if s, rendered := renderStringExpr(n.(ast.Expr)); rendered {
					out = append(out, strings.Join(strings.Fields(s), " "))
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no function %s with string constants in it, so this arm has no query "+
			"to read and would assert nothing", file, fnName)
	}
	return out
}

// getMemoryCardFromDomain is the published side of this claim.
const getMemoryCardFromDomain = "../../docs/mcp-cards/pf_get_memory.md"

// TestGetMemoryByIDTakesAnyLineageMemberAndCarriesTheHeadCursor holds the read
// half of the lineage sentence.
//
// 🔴 The absence assertion is the load-bearing one, and it is stated over the
// WHERE clause rather than over the whole query, because `latest_id` legitimately
// appears in the SELECT list — the card promises it does. An arm that simply
// looked for the column name would therefore pass whatever the predicate said,
// which is the direction that turns an accepted id into a 404.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  the by-id query adds `AND latest_id = id`                     RED (absence)
//	M2  the by-id query drops `latest_id` from its SELECT list        RED (cursor)
//	M3  the by-id query drops `status != 'redacted'`                  RED (redaction)
//	M4  the card renames the cursor column                            RED (publication)
//	M5  green control: reword the sentence keeping the column name
//	    and the tool it names                                         GREEN
func TestGetMemoryByIDTakesAnyLineageMemberAndCarriesTheHeadCursor(t *testing.T) {
	raw, err := os.ReadFile(getMemoryCardFromDomain)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published half of this claim, so an unreadable "+
			"card is a failure and not an empty pass", getMemoryCardFromDomain, err)
	}
	card := strings.Join(strings.Fields(string(raw)), " ")

	// The cursor column, and the tool the card says advances it, both off the card.
	m := regexp.MustCompile("`([a-z_]+)` cursor is what `(pf_[a-z_]+)` advances").FindStringSubmatch(card)
	if m == nil {
		t.Fatalf("%s no longer says which column is the version cursor, or which tool advances "+
			"it. Both are what make the sentence actionable — a caller holding a stale id needs "+
			"to know which field gets it to the head — and this arm has nothing to look for "+
			"without them.", getMemoryCardFromDomain)
	}
	cursorColumn, advancingTool := m[1], m[2]
	if !strings.Contains(card, "`"+advancingTool+"`") {
		t.Errorf("%s names %s as the advancing tool outside backticks; the card's own convention "+
			"is a backticked tool name, and K4's parameter check is what that convention feeds",
			getMemoryCardFromDomain, advancingTool)
	}

	// The by-id read's query.
	query := ""
	for _, lit := range domainFuncSQL(t, recallTextPathFile, "GetMemoryByID") {
		if strings.Contains(strings.ToLower(lit), "from memories") {
			query = lit
		}
	}
	if query == "" {
		t.Fatal("GetMemoryByID holds no query against `memories`, so the by-id read this card is " +
			"about either moved or was rewritten, and every assertion below would be about nothing")
	}
	where := query[strings.Index(strings.ToLower(query), "where "):]
	selectList := query[:strings.Index(strings.ToLower(query), "from memories")]

	if strings.Contains(strings.ToLower(where), cursorColumn) {
		t.Errorf("the by-id read's WHERE clause is %q and constrains %s. That makes the read "+
			"HEAD-ONLY: an id a caller legitimately holds from an earlier page, an artifact link "+
			"or its own notes stops resolving, and the answer is a 404 — the same answer a "+
			"redacted memory gives, so a caller cannot tell 'superseded' from 'deleted'.",
			where, cursorColumn)
	}
	if !strings.Contains(strings.ToLower(selectList), cursorColumn) {
		t.Errorf("the by-id read selects %q and does not carry %s. The card promises this column "+
			"as the cursor to the current head; without it a caller who reaches an old version "+
			"has the text they asked for and no way at all to discover a newer one exists.",
			strings.Join(strings.Fields(selectList), " "), cursorColumn)
	}
	if !strings.Contains(strings.ToLower(where), "status != 'redacted'") {
		t.Errorf("the by-id read's WHERE clause is %q and no longer excludes redacted rows. "+
			"'Accepts any id in a lineage' is bounded by exactly one exception, and this is it: "+
			"a redaction is the one case where handing back the row a caller asked for is the "+
			"wrong answer.", where)
	}
}
