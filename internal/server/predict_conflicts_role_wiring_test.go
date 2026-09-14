package server

// aihub#662 — the wiring hop of PredictConflicts' visibility fold.
//
// 🔴 THIS FILE EXISTS BECAUSE A MUTANT SURVIVED. The fix for the fold itself is
// held by a table test on domain.canSeeProject, and three mutants of that
// function turn it red: dropping the admin arm, returning true unconditionally,
// and reading any non-empty callerRole as admin. A fourth mutant, applied one
// layer up, stayed GREEN against everything in the repo:
//
//	domain.PredictConflicts(ctx, pool, &req, u.ProjectRoles, "admin")
//
// — the handler telling the domain that every caller is an administrator, which
// switches the redaction off for all of them. The decision function is still
// perfect; nothing reaches it. That is the classic wiring gap, and it is the
// permissive direction, so nothing else would ever notice.
//
// It is checked in SOURCE rather than over HTTP because the behavioural version
// needs a database (a running holder, a lock row, and a caller with no role in
// the holder's project), and every DB-gated test in this repo also has to be
// named by a `-run` regex in ci.yml or it runs nowhere. A source gate costs one
// file, runs on every `go test ./...`, and kills the mutant that survived.
//
// ⚠️ WHAT IT CANNOT SEE, stated so nobody reads it as more than it is: that the
// VALUE in u.Role is the authenticated user's. It reads the shape of the
// argument, not its provenance. A handler that assigned `u.Role = "admin"` on
// the line above would pass. The behavioural gate for that is what a database
// would buy.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// exprText (routes_memory_methodology_test.go) renders a node back to source.

// TestPredictConflictsIsGivenTheAuthenticatedRole requires the callerRole
// argument of the domain.PredictConflicts call inside handlePredictConflicts to
// be read off the authenticated user, and refuses any literal.
func TestPredictConflictsIsGivenTheAuthenticatedRole(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse router.go: %v", err)
	}

	var handler *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "handlePredictConflicts" {
			handler = fn
			break
		}
	}
	if handler == nil {
		t.Fatal("router.go no longer declares handlePredictConflicts. If the route moved, move this " +
			"gate with it: an arm that cannot find its target is an arm that checks nothing")
	}

	var calls []*ast.CallExpr
	ast.Inspect(handler.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "PredictConflicts" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "domain" {
				calls = append(calls, call)
			}
		}
		return true
	})
	if len(calls) != 1 {
		t.Fatalf("handlePredictConflicts makes %d domain.PredictConflicts call(s), want 1", len(calls))
	}

	args := calls[0].Args
	const wantArgs = 5 // ctx, pool, req, callerProjectRoles, callerRole
	if len(args) != wantArgs {
		t.Fatalf("domain.PredictConflicts is called with %d argument(s), want %d. The callerRole "+
			"parameter is the aihub#662 addition; a call that dropped it would not compile, so a "+
			"different count here means the signature changed and this gate has to be re-derived "+
			"against it rather than renumbered", len(args), wantArgs)
	}

	for _, arg := range []struct {
		i    int
		name string
		why  string
	}{
		{3, "ProjectRoles", "the per-project membership map"},
		{4, "Role", "the global role, which is the ONLY thing that distinguishes an admin: " +
			"an admin's ProjectRoles map is empty by design (aihub#227), so the map alone " +
			"reads administrator-of-everything as member-of-nothing"},
	} {
		sel, ok := args[arg.i].(*ast.SelectorExpr)
		if !ok {
			t.Errorf("argument %d is %s, want a field of the authenticated user (%s — %s). A "+
				"literal here is a visibility decision the request cannot influence and the "+
				"caller's credential cannot either: hard-coding \"admin\" switches the redaction "+
				"off for every caller, and nothing downstream can tell",
				arg.i, exprText(fset, args[arg.i]), arg.name, arg.why)
			continue
		}
		if sel.Sel.Name != arg.name {
			t.Errorf("argument %d reads .%s, want .%s — %s", arg.i, sel.Sel.Name, arg.name, arg.why)
		}
		// And off the SAME value the rest of the handler authenticates against,
		// so the two cannot come from different places.
		base, ok := sel.X.(*ast.Ident)
		if !ok || base.Name != "u" {
			t.Errorf("argument %d reads %s, want it off `u`, the GetUser(c) value this handler "+
				"already resolved. Two sources for one credential is how they come to disagree",
				arg.i, exprText(fset, args[arg.i]))
		}
	}

	// The positive control: `u` really is the authenticated user here, not some
	// other local. Without this the assertions above are satisfied by any
	// variable happening to be named `u`.
	var sawGetUser bool
	ast.Inspect(handler.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || lhs.Name != "u" {
			return true
		}
		if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "GetUser" {
				sawGetUser = true
			}
		}
		return true
	})
	if !sawGetUser {
		t.Error("handlePredictConflicts never assigns `u` from GetUser(c), so the `u.Role` this " +
			"gate accepted is not known to be the authenticated user's role")
	}
}
