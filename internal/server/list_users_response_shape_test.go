package server

// aihub#543 probe wave 2 — what GET /v1/admin/users really returns, which is
// what pf_list_users publishes as its response.
//
//	"Returns … id, display name, email, `user_type`, global role and author
//	 aliases"   -> TestListUsersReturnsTheColumnsItSelectsAndCapsAtOneHundred
//
// 🔴 That sentence was MEASURED FALSE in two ways and is corrected rather than
// pinned:
//
//   - `author_aliases` is NOT in this response. handleListUsers selects five
//     columns and builds each item from five keys; the column exists on `users`
//     but nothing reads it here — and since aihub#587 (2026-09-10) nothing
//     writes it either: the parameter is withdrawn from pf_create_user and
//     pf_update_user and the column is dormant. The pf_list_users card went
//     further still and had called it "the field that matters most in this
//     response".
//   - "every user row" is not what it returns: the query carries `ORDER BY
//     created_at DESC LIMIT 100`, so a deployment with more than a hundred users
//     is answered with the hundred newest and no signal that anything was left
//     out. There is no cursor and no total on this endpoint.
//
// The arm reads the handler's AST rather than driving a database, for the reason
// aihub#543 spec §3.3 gives: the claim is about the SELECT list and the response
// map — two declarations — and neither becomes more true for having rows behind
// it. TestArtifactSummaryCapMatchesTheMigration in this package is the same
// shape for the same reason.
//
//	GOWORK=off go test ./internal/server/ -run TestListUsersReturns -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// listUsersColumns are the five the corrected card names, sorted.
var listUsersColumns = []string{"display_name", "email", "id", "role", "user_type"}

// TestListUsersReturnsTheColumnsItSelectsAndCapsAtOneHundred holds all three
// halves of the corrected sentence: which columns arrive, that author_aliases is
// not one of them, and that the answer is capped.
//
// Each half fails on a different mutant, and the third is the one no existing
// arm could see: a cap with no cursor is invisible in every response that is
// under it, which is every response this deployment has ever produced.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M11 enforcement: add author_aliases to the SELECT
//	                                        RED  the_five_columns_are_the_response
//	                                             (and the corrected card sentence
//	                                             would then have to move back)
//	M12 enforcement: drop `role` from the item map, keeping it in the SELECT
//	                                        RED  the_five_columns_are_the_response
//	M13 enforcement: raise the LIMIT to 1000
//	                                        RED  the_answer_is_capped_at_one_hundred
//	M14 enforcement: delete the LIMIT clause
//	                                        RED  the_answer_is_capped_at_one_hundred
//	P10 publication: rename this arm        RED  K12 ARM_CITATION_UNRESOLVED, on
//	                                             all three pf_list_users sentences
//	                                             that cite it
func TestListUsersReturnsTheColumnsItSelectsAndCapsAtOneHundred(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("parse router.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == "handleListUsers" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("router.go declares no handleListUsers. It is the only handler bound to " +
			"GET /v1/admin/users, which is the endpoint pf_list_users calls, so a rename has to be " +
			"reviewed rather than absorbed by a scan that finds nothing")
	}

	var query string
	ast.Inspect(fn, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, uerr := strconv.Unquote(lit.Value); uerr == nil && strings.Contains(strings.ToUpper(v), "SELECT") {
			query = v
		}
		return true
	})
	if query == "" {
		t.Fatal("handleListUsers contains no SELECT literal; every assertion below would be about " +
			"an empty string, which is the vacuous green this walk exists to avoid")
	}

	t.Run("the_five_columns_are_the_response", func(t *testing.T) {
		selectRE := regexp.MustCompile(`(?is)select\s+(.*?)\s+from\s`)
		m := selectRE.FindStringSubmatch(query)
		if m == nil {
			t.Fatalf("could not read a SELECT list out of %q", query)
		}
		var selected []string
		for _, c := range strings.Split(m[1], ",") {
			if c = strings.TrimSpace(c); c != "" {
				selected = append(selected, c)
			}
		}
		sort.Strings(selected)
		if strings.Join(selected, ",") != strings.Join(listUsersColumns, ",") {
			t.Errorf("handleListUsers selects %v; the pf_list_users card names %v. A column the "+
				"query does not read cannot reach a caller, and a column it reads that the card "+
				"does not name is a promise nobody made — author_aliases was published as this "+
				"response's most important field while being in neither list.",
				selected, listUsersColumns)
		}

		// The item map is the second half: a selected column that is not put
		// into the response map is scanned and dropped.
		keys := responseMapKeys(fn)
		sort.Strings(keys)
		if strings.Join(keys, ",") != strings.Join(listUsersColumns, ",") {
			t.Errorf("handleListUsers builds each item from %v, and the card names %v. The SELECT "+
				"list and this map are two places one field has to appear, which is why both are "+
				"read here rather than either standing for the other.", keys, listUsersColumns)
		}
	})

	t.Run("the_answer_is_capped_at_one_hundred", func(t *testing.T) {
		// The card says "at most 100, newest first". Both clauses come from this
		// one query, and neither is observable from any response under the cap.
		limitRE := regexp.MustCompile(`(?i)limit\s+(\d+)`)
		m := limitRE.FindStringSubmatch(query)
		if m == nil {
			t.Fatalf("the users query carries no LIMIT:\n    %s\nThe card says the answer is capped "+
				"at a hundred rows; without a cap the sentence is wrong in the other direction, "+
				"and an unbounded admin list is a different contract", query)
		}
		if m[1] != "100" {
			t.Errorf("the users query is capped at %s rows and the card says 100. This endpoint "+
				"publishes no cursor and no total, so the number in the card is the only place a "+
				"caller can learn where the list stops.", m[1])
		}
		if !regexp.MustCompile(`(?i)order\s+by\s+created_at\s+desc`).MatchString(query) {
			t.Errorf("the users query does not order by created_at DESC:\n    %s\nWhich hundred rows "+
				"the cap keeps is part of the same sentence — under a different order the cap "+
				"silently changes which users a caller can see at all", query)
		}
	})
}

// responseMapKeys returns the keys of the largest string-keyed composite literal
// in fn, which for these handlers is the response item.
//
// The largest rather than the first: a handler may build a smaller map for
// something else, and picking by position would make this walk depend on
// statement order.
func responseMapKeys(fn *ast.FuncDecl) []string {
	var best []string
	ast.Inspect(fn, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var keys []string
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			lit, ok := kv.Key.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			keys = append(keys, v)
		}
		if len(keys) > len(best) {
			best = keys
		}
		return true
	})
	return best
}
