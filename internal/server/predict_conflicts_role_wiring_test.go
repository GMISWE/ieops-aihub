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
	const wantArgs = 6 // ctx, pool, req, callerProjectRoles, callerRole, callerProjectScope
	if len(args) != wantArgs {
		t.Fatalf("domain.PredictConflicts is called with %d argument(s), want %d. callerRole is the "+
			"aihub#662 addition and callerProjectScope the aihub#665 one; a call that dropped "+
			"either would not compile, so a different count here means the signature changed and "+
			"this gate has to be re-derived against it rather than renumbered", len(args), wantArgs)
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
		{5, "ProjectScope", "the api key's confinement, and the third clause of the one access " +
			"rule (hasProjectAccess). Since aihub#665 the domain function REFUSES a project " +
			"the caller may not see rather than only redacting the answer, so a hard-coded " +
			"nil here would silently ship a membership-only copy of that rule: a key issued " +
			"for one project would read another through a user who is a member of both"},
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

// TestPredictConflictsDeniesWithTheSharedNotVisibleResponse is aihub#665's
// wiring hop, and it exists for the same reason the one above does: the decision
// is correct in the domain and one layer of plumbing can make it worthless.
//
// domain.PredictConflicts refuses a project the caller may not see with
// ErrNotFound carrying a message of its own. That message must NOT reach the
// wire. aihub#377's invariant is that "you may not see this" and "this does not
// exist" are byte-identical, and the whole repo spells the survivor
// notVisibleMessage — so the handler funnels the refusal through hideNotFound,
// which replaces ErrNotFound with errNotVisible() and leaves every other code
// alone.
//
// 🔴 THE MUTANT THIS KILLS is `return writeError(c, aihubErr)` — the line that
// was there before, which compiles, keeps every behavioural assertion about
// refusing green, and answers a distinguishable 404. A caller sweeping project
// names would read the domain's own wording back and learn which projects exist.
//
// ⚠️ What it cannot see: that hideNotFound still means what it means. That is
// held where it is defined (middleware.go) and by the byte-identity suites.
func TestPredictConflictsDeniesWithTheSharedNotVisibleResponse(t *testing.T) {
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
		}
	}
	if handler == nil {
		t.Fatal("router.go no longer declares handlePredictConflicts; re-point this gate rather " +
			"than deleting it")
	}

	// The identifier the domain error lands in, taken from the call itself so a
	// rename cannot quietly turn this arm into an assertion about nothing.
	errName := ""
	ast.Inspect(handler.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) != 2 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "PredictConflicts" {
			return true
		}
		if id, ok := assign.Lhs[1].(*ast.Ident); ok {
			errName = id.Name
		}
		return true
	})
	if errName == "" {
		t.Fatal("no `resp, err := domain.PredictConflicts(...)` assignment found in " +
			"handlePredictConflicts — this gate cannot tell which value carries the refusal")
	}

	var wrapped, bare []string
	ast.Inspect(handler.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "writeError" {
			return true
		}
		for _, a := range call.Args {
			switch v := a.(type) {
			case *ast.Ident:
				if v.Name == errName {
					bare = append(bare, fset.Position(call.Pos()).String())
				}
			case *ast.CallExpr:
				inner, ok := v.Fun.(*ast.Ident)
				if !ok || inner.Name != "hideNotFound" || len(v.Args) != 1 {
					continue
				}
				if arg, ok := v.Args[0].(*ast.Ident); ok && arg.Name == errName {
					wrapped = append(wrapped, fset.Position(call.Pos()).String())
				}
			}
		}
		return true
	})

	for _, pos := range bare {
		t.Errorf("router.go:%s writes %s straight to the response. The domain refusal for a "+
			"project the caller may not see is an ErrNotFound carrying its own wording, and a "+
			"404 whose body differs from notVisibleMessage is still an oracle: it tells a "+
			"caller sweeping project names which ones exist. Wrap it: "+
			"writeError(c, hideNotFound(%s)).", pos, errName, errName)
	}
	if len(wrapped) == 0 {
		t.Errorf("handlePredictConflicts never passes %s through hideNotFound. Without it the "+
			"aihub#665 authorization gate answers a distinguishable 404 and trades one "+
			"disclosure for a quieter one.", errName)
	}
}
