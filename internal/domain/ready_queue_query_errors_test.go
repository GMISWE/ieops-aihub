package domain

// aihub#500 — every ready-queue segment must fail the whole call.
//
// GetReadyQueue runs one pool.Query per segment. Until this wi, five of the seven
// discarded the error that call returns: stalled, paused, needs_human_session,
// unclassified and stale_running each wrapped their entire drain in
// `if err == nil { … }` with no else, so a send-time failure produced an empty
// segment inside an HTTP 200. Only items[] and running[] checked `err != nil`.
//
// Why that is worth a gate rather than just a fix. aihub#449 removed
// stale_running's `omitempty` so that all seven keys are always present, on the
// stated rule that an absent key is acceptable only while its absence asserts
// NOTHING. A segment that renders `[]` because its query never ran defeats that
// exactly: the caller is told "nothing is here" by a response that means "no data
// reached you". The two repairs are one contract, and only one of them had a test.
//
// 🔴 This gate deliberately does NOT read a list of segment names. The defect it
// guards is "a Query error goes unanswered", which an eighth segment added
// tomorrow can carry in, and a gate enumerating today's seven would pass that
// eighth by default. It reads the shape instead, and pins the COUNT so that the
// detector going blind is also red — see the exact-7 assertion below. The section
// count itself is owned by internal/mcp/ready_queue_section_count_test.go; the
// number appears here only as a floor for the scanner, not as a second authority
// on how many segments exist.
//
// aihub#548 added one rule to the same scanner: the answer must CLASSIFY, not
// just return. #500's five new guards were written as bare
// NewErr(ErrInternalError, …) while the rows.Err() branch of the same segment
// returns dbErrCause — so a class-40 rollback (SQLSTATE 40001/40P01) was a
// retryable 409 on one error path and a 500 on the other, for the same
// underlying condition. That is #500's own defect one axis over: the segment
// disagreed WITH ITSELF about what a rollback means. The rule reuses the
// conflictClassifiers set from retryable_conflict_guard_test.go rather than
// naming dbErr here, so a classifier added there is accepted here without an
// edit — the aihub#334 guard itself cannot cover this function, because it is
// scoped to transactional functions and GetReadyQueue opens no transaction.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// queryErrViolation is one pool.Query whose error is not answered by a return.
type queryErrViolation struct {
	Line int
	Rows string
	Why  string
}

// scanQueryErrorHandling reports every `X, e := <recv>.Query(…)` in fn whose
// error is not immediately checked with `e != nil`, returned, and classified.
//
// "Immediately" is the whole rule and is stricter than "eventually checked". A
// checker that accepted the error being consulted anywhere later in the function
// would accept `if err == nil { … }` — the very shape this exists to reject —
// because that also consults it. The compliant shape puts the guard in the
// statement directly after the call, which is what both untouched segments
// (items[], running[]) already did.
//
// "Classified" (aihub#548) means the branch consults one of the class-40
// classifiers (dbErr/dbErrCause/retryConflictErr/pgxErr — the
// conflictClassifiers set), the same question the segment's own rows.Err()
// branch already asks via dbErrCause. A bare NewErr(ErrInternalError, …) here
// answers a lost concurrency race with "the server is broken".
func scanQueryErrorHandling(fn *ast.FuncDecl, fset *token.FileSet) (sites int, violations []queryErrViolation) {
	var walk func(stmts []ast.Stmt)
	walk = func(stmts []ast.Stmt) {
		for i, s := range stmts {
			// Recurse into nested blocks first so a Query moved inside an if or
			// a loop stays visible rather than dropping out of the census.
			switch n := s.(type) {
			case *ast.IfStmt:
				walk(n.Body.List)
				// An `else if` chains an IfStmt rather than a BlockStmt, so
				// walking only the block form would leave every branch after the
				// first invisible — and invisible reads as compliant.
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
			if !ok || (sel.Sel.Name != "Query" && sel.Sel.Name != "QueryContext") {
				continue
			}
			sites++

			line := fset.Position(as.Pos()).Line
			rowsName := exprString(as.Lhs[0])
			errIdent, ok := as.Lhs[1].(*ast.Ident)
			if !ok {
				violations = append(violations, queryErrViolation{line, rowsName,
					"the second result is not a plain identifier, so the error cannot be traced"})
				continue
			}

			if i+1 >= len(stmts) {
				violations = append(violations, queryErrViolation{line, rowsName,
					"nothing follows the call, so " + errIdent.Name + " is discarded"})
				continue
			}
			ifs, ok := stmts[i+1].(*ast.IfStmt)
			if !ok {
				violations = append(violations, queryErrViolation{line, rowsName,
					"the next statement is not an `if`, so " + errIdent.Name + " is not answered before the rows are drained"})
				continue
			}
			bin, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok {
				violations = append(violations, queryErrViolation{line, rowsName,
					"the guard is not a comparison against " + errIdent.Name})
				continue
			}
			lhs, ok := bin.X.(*ast.Ident)
			if !ok || lhs.Name != errIdent.Name {
				violations = append(violations, queryErrViolation{line, rowsName,
					"the guard does not test " + errIdent.Name})
				continue
			}
			if bin.Op != token.NEQ {
				violations = append(violations, queryErrViolation{line, rowsName,
					fmt.Sprintf("the guard is `%s %s nil`, not `%s != nil` — the error branch has no body, "+
						"so a failed query renders an empty segment inside a 200",
						errIdent.Name, bin.Op, errIdent.Name)})
				continue
			}
			if !endsInReturn(ifs.Body.List) {
				violations = append(violations, queryErrViolation{line, rowsName,
					"the `" + errIdent.Name + " != nil` branch does not return, so the failure falls through"})
				continue
			}
			// aihub#549: the return must carry the failure out. A `…, nil`
			// return (or a bare one) inside the branch publishes success for a
			// failed query, and endsInReturn alone accepted it.
			if nilErrorReturn(ifs.Body.List) {
				violations = append(violations, queryErrViolation{line, rowsName,
					"a return inside the `" + errIdent.Name + " != nil` branch publishes success — its error " +
						"slot is nil (or the return is bare) — so the failed query leaves the guard as a normal " +
						"answer, the same silent-empty lie one statement further in. Return the classified " +
						"error explicitly: `return nil, dbErr(" + errIdent.Name + ", …)`"})
				continue
			}
			// aihub#548: the branch must also consult a class-40 classifier.
			// callsClassifier and conflictClassifiers live in
			// retryable_conflict_guard_test.go, same package.
			if !callsClassifier(ifs) {
				violations = append(violations, queryErrViolation{line, rowsName,
					"the `" + errIdent.Name + " != nil` branch returns without consulting a class-40 classifier " +
						"(dbErr/dbErrCause/retryConflictErr), so a serialization rollback answers 500 while the " +
						"same segment's rows.Err() branch answers the retryable 409 for the same condition"})
			}
		}
	}
	walk(fn.Body.List)
	return sites, violations
}

func endsInReturn(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	_, ok := stmts[len(stmts)-1].(*ast.ReturnStmt)
	return ok
}

// nilErrorReturn reports whether any return inside the error branch publishes
// SUCCESS instead of the failure: a return whose last result is the `nil`
// identifier, or a return with no results at all.
//
// aihub#549: endsInReturn alone accepted ANY return, so
// `if err != nil { return result, nil }` satisfied the #500 rule — the branch
// "answered" the failure by republishing it as a normal response, the exact
// silent-empty lie the rule exists to refuse, one statement further in. And
// because callsClassifier accepts a classifier call ANYWHERE in the branch,
// `err = dbErr(err, …); return result, nil` satisfied the #548 rule too. The
// failure must leave the branch as a non-nil error, which the scanner can only
// see when the return says so explicitly.
//
// A bare `return` is refused for the same reason from the other side: with
// named results it can be genuine propagation (`err = dbErr(err, …); return`),
// but the scanner cannot see a named result's value, and every governed
// function already uses the explicit shape — so the explicit shape is the
// compliant one, and a site that wants the named-result form should be
// rewritten rather than exempted. FuncLit subtrees are skipped: a closure's
// returns leave the closure, not the guard.
func nilErrorReturn(stmts []ast.Stmt) bool {
	found := false
	for _, s := range stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			if found {
				return false
			}
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			if len(ret.Results) == 0 {
				found = true
				return false
			}
			if id, ok := ret.Results[len(ret.Results)-1].(*ast.Ident); ok && id.Name == "nil" {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// TestReadyQueueAnswersEveryQueryError is the gate.
func TestReadyQueueAnswersEveryQueryError(t *testing.T) {
	fset, file := parseDomainSource(t, "work_items.go")
	fn := funcDeclNamed(t, fset, file, "GetReadyQueue")

	sites, violations := scanQueryErrorHandling(fn, fset)

	for _, v := range violations {
		t.Errorf("work_items.go:%d GetReadyQueue: rows %q — %s\n"+
			"    Every segment's pool.Query error must be answered by the statement right after it,\n"+
			"    through the class-40 classifier (aihub#548):\n"+
			"        rows, err := pool.Query(ctx, `…`, project)\n"+
			"        if err != nil {\n"+
			"            return nil, dbErr(err, \"failed to query <segment> items\")\n"+
			"        }\n"+
			"    Swallowing it renders that segment as an empty list inside an HTTP 200, which is\n"+
			"    indistinguishable from the segment genuinely being empty — the same silent-empty\n"+
			"    shape aihub#449 removed from this response when it dropped stale_running's\n"+
			"    `omitempty`. And answering with a bare NewErr(ErrInternalError, …) reports a\n"+
			"    class-40 rollback as \"the server is broken\" while the same segment's rows.Err()\n"+
			"    branch answers the retryable 409 — dbErr is byte-identical to the bare NewErr for\n"+
			"    every other error. If some segment ever really should be best-effort, that also has\n"+
			"    to downgrade its rows.Err() branch twelve lines below, and internal/citest/rowserr\n"+
			"    exists to stop exactly that — so bring a ruling, not an exemption.",
			v.Line, v.Rows, v.Why)
	}

	// The count arm. Without it a detector that stopped recognising pool.Query —
	// a rename, a helper wrapping the call, a segment moved into its own
	// function — would report zero violations and read as a clean tree.
	const wantSites = 7
	if sites != wantSites {
		t.Errorf("scanner found %d Query sites in GetReadyQueue, want %d.\n"+
			"    More: a segment was added — give its Query error the same `if err != nil { return … }`\n"+
			"    and raise this number in the same change.\n"+
			"    Fewer: either a segment was removed, or the scanner has gone blind and is no longer\n"+
			"    looking at the code it claims to guard. Check which before touching this number.",
			sites, wantSites)
	}
}

// TestQueryErrorScannerRejectsTheShapeItWasWrittenFor is the calibration arm.
//
// A gate that is green on the fixed tree proves nothing on its own — it is green
// on a tree where the detector matches nothing at all, too. These fixtures are
// the pre-aihub#500 code, so the scanner has to be RED on them or it is not
// measuring anything (aihub#361 learned this the hard way with a false positive;
// the mirror failure is a gate that can never be red).
func TestQueryErrorScannerRejectsTheShapeItWasWrittenFor(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{{
		name: "the exact pre-aihub#500 stale_running shape",
		src: `func f() {
			staleRows, staleErr := pool.Query(ctx, "SELECT 1")
			if staleErr == nil {
				defer staleRows.Close()
				for staleRows.Next() {
				}
			}
		}`,
		want: "not `staleErr != nil`",
	}, {
		name: "guarded but falls through instead of returning",
		src: `func f() {
			rows, err := pool.Query(ctx, "SELECT 1")
			if err != nil {
				log.Print(err)
			}
			for rows.Next() {
			}
		}`,
		want: "does not return",
	}, {
		name: "error never consulted at all",
		src: `func f() {
			rows, err := pool.Query(ctx, "SELECT 1")
			for rows.Next() {
			}
			_ = err
		}`,
		want: "not an `if`",
	}, {
		name: "checked later rather than immediately",
		src: `func f() {
			rows, err := pool.Query(ctx, "SELECT 1")
			for rows.Next() {
			}
			if err != nil {
				return
			}
		}`,
		want: "not an `if`",
	}, {
		// The exact shape aihub#500 left behind and aihub#548 exists to reject:
		// guarded, returning, and reporting a class-40 rollback as a 500.
		name: "guarded and returns, but bare NewErr instead of the classifier",
		src: `func f() {
			rows, err := pool.Query(ctx, "SELECT 1")
			if err != nil {
				return nil, NewErr(ErrInternalError, "failed to query things")
			}
			for rows.Next() {
			}
		}`,
		want: "class-40 classifier",
	}, {
		// aihub#549: endsInReturn accepted ANY return, so a branch that returned
		// the RESULT with a nil error satisfied it — the failure left the guard
		// as a success.
		name: "guarded, but the err-branch returns the result with a nil error",
		src: `func f() {
			rows, err := pool.Query(ctx, "SELECT 1")
			if err != nil {
				return result, nil
			}
			for rows.Next() {
			}
		}`,
		want: "publishes success",
	}, {
		// aihub#549's load-bearing evasion: one classifier call anywhere in the
		// branch satisfied the #548 rule, and any return satisfied the #500 rule,
		// so this shape — classified, returning, and still swallowing — was green.
		name: "classifier consulted, but the return still publishes success",
		src: `func f() {
			rows, err := pool.Query(ctx, "SELECT 1")
			if err != nil {
				err = dbErr(err, "failed to query things")
				return result, nil
			}
			for rows.Next() {
			}
		}`,
		want: "publishes success",
	}, {
		// A bare return can smuggle a nil error through named results, and the
		// scanner cannot see named-result assignment — the explicit shape is the
		// compliant one.
		name: "classifier consulted, but the branch ends in a bare return",
		src: `func f() (result int, err error) {
			rows, err := pool.Query(ctx, "SELECT 1")
			if err != nil {
				err = dbErr(err, "failed to query things")
				return
			}
			for rows.Next() {
			}
			return
		}`,
		want: "publishes success",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset, fn := parseFuncFixture(t, tc.src)
			sites, violations := scanQueryErrorHandling(fn, fset)
			if sites != 1 {
				t.Fatalf("fixture should hold exactly 1 Query site, scanner saw %d", sites)
			}
			if len(violations) != 1 {
				t.Fatalf("scanner accepted a shape aihub#500 exists to reject: got %d violations, want 1",
					len(violations))
			}
			if !strings.Contains(violations[0].Why, tc.want) {
				t.Errorf("violation reason = %q, want it to mention %q", violations[0].Why, tc.want)
			}
		})
	}
}

// TestQueryErrorScannerAcceptsTheFixedShape is the other half of the
// calibration: a scanner that reported everything would also be red on the tree
// above, and "always red" is as useless as "always green" — it just gets deleted
// faster.
func TestQueryErrorScannerAcceptsTheFixedShape(t *testing.T) {
	fset, fn := parseFuncFixture(t, `func f() {
		rows, err := pool.Query(ctx, "SELECT 1")
		if err != nil {
			return nil, dbErr(err, "failed to query things")
		}
		defer rows.Close()
		for rows.Next() {
		}
	}`)
	sites, violations := scanQueryErrorHandling(fn, fset)
	if sites != 1 {
		t.Fatalf("fixture should hold exactly 1 Query site, scanner saw %d", sites)
	}
	if len(violations) != 0 {
		t.Errorf("scanner reported the compliant shape as a violation: %+v", violations)
	}
}

func parseFuncFixture(t *testing.T, body string) (*token.FileSet, *ast.FuncDecl) {
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
