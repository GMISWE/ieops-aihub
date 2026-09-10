package domain

// aihub#543 probe wave 2 — where an out-of-vocabulary `kind` is refused, and
// where it is not.
//
// docs/mcp-cards/pf_create_dependency.md says "`kind` is prose rather than an
// enum, so an out-of-vocabulary value is not refused before the handler runs",
// and its Open section stopped there. Both sentences are true and together they
// read as "nothing checks this value", which is false: CreateDependency refuses
// a fourth kind against dependencyKinds — the set
// db_check_policy_test.go holds equal to wi_dependencies_kind_check — before it
// touches the database at all.
//
// The asymmetry with the DELETE side is the other half, and it is what makes
// docs/mcp-cards/pf_remove_dependency.md's "a typo in `kind` addresses a
// different edge rather than failing to parse" true: DeleteDependency validates
// nothing, so an unrecognised kind is simply an address no row has. The
// row-level consequence (NOT_FOUND, and the real edge untouched) is a subtest of
// TestDeleteDependency_OtherBlockerRemains_StaysBlocked; what is here is the
// structural fact that one path validates and the other does not.
//
// No database: the create arm passes a NIL pool, which is the proof that the
// refusal happens before any query. A regression that moved the check below the
// first query would panic here rather than pass.
//
//	GOWORK=off go test ./internal/domain/ -run TestDependencyKind -count=1 -v

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestDependencyKindRefusalHappensInTheDomainBeforeAnyQuery is the create side.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M99  delete the validateDependencyKind call in
//	     CreateDependency                                     RED  (nil-pool panic
//	                                                          becomes the failure,
//	                                                          reported as such)
//	M100 move the call below the first pool.QueryRow          RED  (same, and it is
//	                                                          the ordering claim)
//	M101 dependencyKinds gains a fourth value equal to the
//	     fixture's bogus one                                  RED  (vocabulary arm)
//	M102 vocabularyErr stops listing the allowed values       RED  (message arm)
//	M103 green control: reword the kind's doc comment          GREEN
func TestDependencyKindRefusalHappensInTheDomainBeforeAnyQuery(t *testing.T) {
	const bogus = "supersedes-ish"
	for _, legal := range DependencyKindList() {
		if legal == bogus {
			t.Fatalf("the fixture's %q is a legal kind, so this arm tests nothing", bogus)
		}
	}

	// 🔴 A NIL pool. If the refusal ever moves below the first query this call
	// panics instead of returning, which is a loud failure rather than a quiet
	// change of behaviour — and "before the handler touches the database" is
	// exactly what the card's sentence implies about where the check is.
	aerr := CreateDependency(context.Background(), nil, &CreateDependencyRequest{
		BlockedWIID:  "wi_KINDBLOCKED1",
		BlockingWIID: "wi_KINDBLOCKING",
		Kind:         bogus,
	}, "u_kind", map[string]string{}, "writer")

	if aerr == nil {
		t.Fatalf("CreateDependency accepted kind=%q. The published schema declares no enum, so this "+
			"is the only refusal in the whole path; without it a caller's typo becomes a row the "+
			"column's CHECK rejects with a driver message, or a live edge of a kind nothing reads.",
			bogus)
	}
	if aerr.Code != ErrBadRequest {
		t.Errorf("CreateDependency refused kind=%q with %s, want %s — the value came from the "+
			"caller, so it is a 400 rather than a 500 carrying the driver's text",
			bogus, aerr.Code, ErrBadRequest)
	}
	for _, legal := range DependencyKindList() {
		if !strings.Contains(aerr.Message, legal) {
			t.Errorf("the refusal %q does not name the legal value %q. `kind` is published as prose "+
				"with no enum, so the refusal is where a caller learns the vocabulary.",
				aerr.Message, legal)
		}
	}

	// The positive half: every published value passes the same validator, so the
	// refusal above is about this value rather than about the validator refusing
	// everything.
	for _, legal := range DependencyKindList() {
		if err := validateDependencyKind(legal); err != nil {
			t.Errorf("validateDependencyKind(%q) refused a value the card, the tool descriptions and "+
				"the column's CHECK all publish: %v", legal, err)
		}
	}
}

// TestDependencyKindIsValidatedOnCreateAndNotOnDelete is the asymmetry, read out
// of the two functions rather than asserted from memory.
//
// Mutants (2026-09-10):
//
//	M104 DeleteDependency gains a validateDependencyKind call  RED — and then
//	                                                           pf_remove_dependency's
//	                                                           "addresses a different
//	                                                           edge" sentence is false
//	M105 CreateDependency loses its call                       RED
//	M106 green control: rename a local in either function      GREEN
func TestDependencyKindIsValidatedOnCreateAndNotOnDelete(t *testing.T) {
	const path = "dependencies.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	validates := map[string]bool{}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "validateDependencyKind" {
				validates[fd.Name.Name] = true
			}
			return true
		})
	}

	if !validates["CreateDependency"] {
		t.Errorf("CreateDependency no longer calls validateDependencyKind. The set it validates " +
			"against is held equal to wi_dependencies_kind_check by " +
			"TestDBCheckRegistry_MirrorsMatchTheMigration, and with the call gone that equality " +
			"bounds nothing that runs.")
	}
	if validates["DeleteDependency"] {
		t.Errorf("DeleteDependency now validates the kind. That is a defensible change, and it makes " +
			"docs/mcp-cards/pf_remove_dependency.md's \"a typo in `kind` addresses a different edge " +
			"rather than failing to parse\" false: the typo would be refused instead. Update the card " +
			"in the same diff.")
	}
	if len(validates) == 0 {
		t.Errorf("the walk found no caller of validateDependencyKind in %s at all, so both "+
			"assertions above are about a scan that matches nothing", path)
	}
}
