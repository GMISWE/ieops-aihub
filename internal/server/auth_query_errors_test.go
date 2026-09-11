package server

// aihub#522 — the two auth paths must answer every membership-query error.
//
// The aihub#500 census named BearerAuth (middleware.go) and loadUserByAPIKeyID
// (ui_handlers_auth.go): each ran its projects.members query under
// `if perr == nil { … }` with no else, so a send-time failure AUTHENTICATED the
// caller with zero ProjectRoles. Every project then answered the membership 404
// (errNotVisible) — an availability fault delivered as a visibility verdict,
// which is precisely the "second lie" hideNotFound's own comment forbids: a
// denial that sends the caller checking their invitation while the database is
// down. Both functions already answered the SAME failure of their first query
// (the key-hash lookup) loudly, so the swallow was a function disagreeing with
// itself, not a policy — aihub#500's decision template, one package over.
//
// The rule here is the domain scanner's (ready_queue_query_errors_test.go)
// minus the classifier requirement, and that subtraction is stated rather than
// implied: no class-40 classifier is exported to this package, these are single
// SELECTs outside any transaction (class 40 is a property of transactions —
// retryable_conflict_guard_test.go's scoping argument), and the compliant
// sibling branches return a plain 500 / a plain error. Requiring what the
// correct code cannot spell is how a gate gets deleted.
//
// No database:
//
//	GOWORK=off go test ./internal/server/ -run TestAuthAnswers -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// authQueryViolation is one Query whose error is not answered by a return.
type authQueryViolation struct {
	Line int
	Rows string
	Why  string
}

// scanAuthQueryErrors reports every `rows, err := <recv>.Query(…)` in fn —
// including inside nested function literals, because BearerAuth is a middleware
// factory whose queries live two closures down — whose error is not immediately
// checked with `err != nil` and returned.
func scanAuthQueryErrors(fn *ast.FuncDecl, fset *token.FileSet) (sites int, violations []authQueryViolation) {
	var walk func(stmts []ast.Stmt)
	walk = func(stmts []ast.Stmt) {
		for i, s := range stmts {
			// Recurse into nested blocks and into any function literal carried
			// by this statement (return func(…) { … }, assignments, calls).
			switch n := s.(type) {
			case *ast.IfStmt:
				walk(n.Body.List)
				for e := n.Else; e != nil; {
					switch el := e.(type) {
					case *ast.BlockStmt:
						walk(el.List)
						e = nil
					case *ast.IfStmt:
						walk(el.Body.List)
						e = el.Else
					default:
						e = nil
					}
				}
			case *ast.ForStmt:
				walk(n.Body.List)
			case *ast.RangeStmt:
				walk(n.Body.List)
			case *ast.BlockStmt:
				walk(n.List)
			default:
				ast.Inspect(s, func(m ast.Node) bool {
					if lit, ok := m.(*ast.FuncLit); ok {
						walk(lit.Body.List)
						return false
					}
					return true
				})
			}

			as, ok := s.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
				continue
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Query" {
				continue
			}
			sites++

			line := fset.Position(as.Pos()).Line
			rowsName := "?"
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				rowsName = id.Name
			}
			errIdent, ok := as.Lhs[1].(*ast.Ident)
			if !ok {
				violations = append(violations, authQueryViolation{line, rowsName,
					"the second result is not a plain identifier, so the error cannot be traced"})
				continue
			}
			if i+1 >= len(stmts) {
				violations = append(violations, authQueryViolation{line, rowsName,
					"nothing follows the call, so " + errIdent.Name + " is discarded"})
				continue
			}
			ifs, ok := stmts[i+1].(*ast.IfStmt)
			if !ok {
				violations = append(violations, authQueryViolation{line, rowsName,
					"the next statement is not an `if`, so " + errIdent.Name + " is not answered"})
				continue
			}
			bin, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok {
				violations = append(violations, authQueryViolation{line, rowsName,
					"the guard is not a comparison against " + errIdent.Name})
				continue
			}
			lhs, ok := bin.X.(*ast.Ident)
			if !ok || lhs.Name != errIdent.Name {
				violations = append(violations, authQueryViolation{line, rowsName,
					"the guard does not test " + errIdent.Name})
				continue
			}
			if bin.Op != token.NEQ {
				violations = append(violations, authQueryViolation{line, rowsName,
					"the guard is `" + errIdent.Name + " " + bin.Op.String() + " nil`, not `" +
						errIdent.Name + " != nil` — the failure branch has no body, so a database outage " +
						"authenticates the caller with zero ProjectRoles and every project answers the " +
						"membership 404"})
				continue
			}
			if len(ifs.Body.List) == 0 {
				violations = append(violations, authQueryViolation{line, rowsName,
					"the `" + errIdent.Name + " != nil` branch is empty"})
				continue
			}
			if _, ok := ifs.Body.List[len(ifs.Body.List)-1].(*ast.ReturnStmt); !ok {
				violations = append(violations, authQueryViolation{line, rowsName,
					"the `" + errIdent.Name + " != nil` branch does not return, so the failure falls through"})
			}
		}
	}
	walk(fn.Body.List)
	return sites, violations
}

// TestAuthAnswersEveryMembershipQueryError is the gate over both auth paths.
func TestAuthAnswersEveryMembershipQueryError(t *testing.T) {
	for _, target := range []struct {
		file, fn  string
		wantSites int
	}{
		// The key-hash query plus the membership query, on each path.
		{"middleware.go", "BearerAuth", 2},
		{"ui_handlers_auth.go", "loadUserByAPIKeyID", 2},
	} {
		t.Run(target.fn, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, target.file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", target.file, err)
			}
			var fn *ast.FuncDecl
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == target.fn && fd.Body != nil {
					fn = fd
					break
				}
			}
			if fn == nil {
				t.Fatalf("no top-level func %s in %s — the gate now checks nothing; re-point it", target.fn, target.file)
			}

			sites, violations := scanAuthQueryErrors(fn, fset)
			for _, v := range violations {
				t.Errorf("%s:%d %s: rows %q — %s", target.file, v.Line, target.fn, v.Rows, v.Why)
			}
			if sites != target.wantSites {
				t.Errorf("scanner found %d Query sites in %s, want %d. More: guard the new query's error "+
					"the same way and raise this number. Fewer: a site was removed or moved into a shape "+
					"this scanner cannot see, which reads as compliant — check which.",
					sites, target.fn, target.wantSites)
			}
		})
	}
}

// TestAuthQueryScannerRejectsTheSwallowShape is the calibration arm: the exact
// pre-aihub#522 membership-query shape must be red, and the fixed shape green,
// or the gate above is green on any tree.
func TestAuthQueryScannerRejectsTheSwallowShape(t *testing.T) {
	parse := func(t *testing.T, body string) (*token.FileSet, *ast.FuncDecl) {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "fixture.go", "package p\n"+body, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				return fset, fn
			}
		}
		t.Fatal("fixture holds no function")
		return nil, nil
	}

	t.Run("the pre-aihub#522 swallow, inside the middleware's nested closures", func(t *testing.T) {
		fset, fn := parse(t, `func f() any {
			return func(next handler) handler {
				return func(c ctx) error {
					prows, perr := pool.Query(c.Request().Context(), "SELECT 1")
					if perr == nil {
						for prows.Next() {
						}
						prows.Close()
					}
					return next(c)
				}
			}
		}`)
		sites, violations := scanAuthQueryErrors(fn, fset)
		if sites != 1 {
			t.Fatalf("fixture should hold exactly 1 Query site (the closure walk is broken), scanner saw %d", sites)
		}
		if len(violations) != 1 || !strings.Contains(violations[0].Why, "!= nil") {
			t.Fatalf("scanner accepted the swallow shape aihub#522 exists to reject: %+v", violations)
		}
	})

	t.Run("the fixed shape is green", func(t *testing.T) {
		fset, fn := parse(t, `func f() any {
			return func(c ctx) error {
				prows, perr := pool.Query(c.Request().Context(), "SELECT 1")
				if perr != nil {
					return c.JSON(500, "database error during auth")
				}
				for prows.Next() {
				}
				prows.Close()
				return nil
			}
		}`)
		sites, violations := scanAuthQueryErrors(fn, fset)
		if sites != 1 {
			t.Fatalf("fixture should hold exactly 1 Query site, scanner saw %d", sites)
		}
		if len(violations) != 0 {
			t.Errorf("scanner reported the compliant shape as a violation: %+v", violations)
		}
	})
}
