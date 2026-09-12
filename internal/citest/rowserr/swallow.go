// swallow.go finds drain loops that discard a ROW-LEVEL Scan failure.
//
// # The defect this exists to prevent (aihub#608)
//
// The sibling analysis in rowserr.go polices the loop's EXIT: a `for X.Next()`
// that never asks X.Err() reports an execute-time failure as an empty result.
// This one polices the loop's BODY: a per-row Scan error answered with
// `continue` (or a success-only `if err == nil` guard). What that shape SPELLS
// is "drop this row, publish the rest as the complete answer" — and when
// nothing downstream intervenes it is exactly that lie: aihub#206's stalled
// wis with a NULL actor_display vanished from the ready queue's stalled[]
// segment, invisibly, back when no rows.Err() arm existed.
//
// What the shape DOES today is governed by a driver side effect, measured
// during aihub#608 rather than assumed: pgx v5's Rows.Scan calls rows.fatal()
// on every error (rows.go, v5.9.2), so a failed Scan closes the rows, Next()
// answers false, and the loop's rows.Err() arm sees the error. The swallow's
// real behaviour therefore forks on that arm:
//
//   - Err arm returns → the call fails anyway, but under the READ arm's
//     message, misattributing the site — and the correctness of "continue"
//     rests entirely on an undocumented pgx behaviour a driver upgrade could
//     change (GetReadyQueue's seven segments were this).
//   - Err arm logs, swallows, or classifies only class 40 → the truncated
//     result IS published as the complete one (BearerAuth's membership drain
//     authenticated callers with partial ProjectRoles; FnClaimWorkItem's
//     idempotent lock re-query answered with partial AcquiredLocks).
//
// Both forks argue for one compliant shape: the Scan arm itself surfaces the
// failure, naming its own site. aihub#500/#548 made GetReadyQueue answer its
// SEND-time errors, aihub#382/#386 its EXECUTE-time errors, aihub#607 the
// remaining non-transactional send sites; aihub#608 closed the Scan arms and
// left this scanner so the shape cannot come back.
//
// # What is recognised, and what is deliberately not
//
// Inside every loop the sibling recogniser already accepts (rowsValuesIn /
// rowsFieldNamesIn — same rules, same blind spots), an if-statement is a
// Scan guard when its condition compares an error against nil and that error
// is SCAN-DERIVED:
//
//   - assigned in the if's init from a Scan call: `if err := X.Scan(…); err != nil`
//   - assigned by the statement directly above:   `m, err := scanHelper(rows)`
//     — a call is a Scan call when its selector is `.Scan` OR when it takes the
//     loop's rows expression as an argument, which is how helper scanners
//     (scanMemoryLite) are seen without type-checking.
//
// A guard is a swallow when its failure path stays inside the loop:
//
//   - guarded-continue: the `err != nil` arm does not end in a return
//     (a `continue`, a log-then-continue, or a fall-through all qualify);
//   - success-only: the guard is `== nil`, so the failure path is empty by
//     construction — unless an else branch ends in a return;
//   - published-as-success: the arm ends in a return whose final result is the
//     literal `nil` while the function declares results — the failure leaves as
//     a normal answer, aihub#549's shape one loop deeper.
//
// `if !found { continue }` filters are not flagged: their condition is not an
// error derived from a Scan. Nested for/range bodies and FuncLits are not
// walked for the guard analysis — a continue there does not target this loop,
// and a closure's returns do not leave the enclosing function.
//
// # Best-effort is an allowlist entry, not a shape
//
// Some drains discard rows on purpose (GC sweeps that record the error in
// GCResult.Error and retry next tick; documented page decorations). Those carry
// entries in scan_swallow_allowlist.txt, each with the reason beside it —
// mirroring aihub#607's justification ledger: the entry lives in one reviewable
// place, and a stale entry is itself a failure, because it pre-authorises
// whatever lands on that key next. Unlike allowlist.txt this file is NOT
// expected to be empty; it is the ledger of adjudicated best-effort drains.
package rowserr

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

// Swallow is one place inside a query-rows loop where a row-level Scan failure
// is discarded instead of surfacing.
type Swallow struct {
	// File is slash-separated and relative to the root passed to ScanDirSwallows.
	File string
	// Line is the line of the guard (the `if`).
	Line int
	// Func names the enclosing declaration, methods as "(Recv).Name".
	Func string
	// Rows is the source text of the expression the loop calls Next() on.
	Rows string
	// Shape says how the failure is discarded: "guarded-continue",
	// "success-only" or "published-as-success".
	Shape string
}

// Key is the allowlist key, same construction and same deliberate coarseness
// as Loop.Key: no line number, so an entry survives edits above it, and a
// function with several drains over one variable name shares one key — an
// entry there has to defend all of them at once.
func (s Swallow) Key() string {
	return s.File + ":" + s.Func + ":" + s.Rows
}

func (s Swallow) String() string {
	return fmt.Sprintf("%s:%d %s over %s (%s)", s.File, s.Line, s.Func, s.Rows, s.Shape)
}

// ScanDirSwallows walks root and returns every Scan swallow in a non-test .go
// file, sorted by file then line. Same walk, same skips as ScanDir.
func ScanDirSwallows(root string) ([]Swallow, error) {
	var out []Swallow
	err := walkGoFiles(root, func(path, rel string) error {
		swallows, scanErr := ScanFileSwallows(path, rel)
		if scanErr != nil {
			return scanErr
		}
		out = append(out, swallows...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// ScanFileSwallows parses one file. reportAs is the name used in the report.
func ScanFileSwallows(path, reportAs string) ([]Swallow, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ScanSourceSwallows(src, reportAs)
}

// ScanSourceSwallows is the whole analysis over one file's bytes. Exported so
// the detector's own behaviour is testable on fixtures, like ScanSource.
func ScanSourceSwallows(src []byte, reportAs string) ([]Swallow, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, reportAs, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", reportAs, err)
	}
	rowsFields := rowsFieldNamesIn(file)

	var out []Swallow
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fnName := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			fnName = "(" + exprString(fn.Recv.List[0].Type) + ")." + fnName
		}
		out = append(out, scanFuncSwallows(fset, fn, fnName, reportAs, rowsFields)...)
	}
	return out, nil
}

// scanFuncSwallows analyses one function body: find every recognised rows loop
// (the SAME recognition the Err() analysis uses, so the two gates police one
// population), then judge the Scan guards inside each loop body.
func scanFuncSwallows(fset *token.FileSet, fn *ast.FuncDecl, fnName, reportAs string, rowsFields map[string]bool) []Swallow {
	rowsNames := rowsValuesIn(fn)
	fnHasResults := fn.Type.Results != nil && len(fn.Type.Results.List) > 0

	var out []Swallow
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		loop, ok := n.(*ast.ForStmt)
		if !ok || loop.Cond == nil {
			return true
		}
		rowsExpr, isNext := zeroArgMethodCall(loop.Cond, "Next")
		if !isNext || !isRowsExpr(rowsExpr, rowsNames, rowsFields) {
			return true
		}
		for _, sw := range swallowsInLoopBody(fset, loop.Body.List, rowsExpr, fnHasResults) {
			sw.File = reportAs
			sw.Func = fnName
			sw.Rows = rowsExpr
			out = append(out, sw)
		}
		return true
	})
	return out
}

// swallowsInLoopBody walks the statements of one drain-loop body. It recurses
// into if/else and bare blocks but NOT into nested for/range loops (a continue
// there targets the inner loop) and NOT into FuncLits (their returns do not
// leave the function). Nested rows loops are judged by their own visit in
// scanFuncSwallows.
func swallowsInLoopBody(fset *token.FileSet, stmts []ast.Stmt, rowsExpr string, fnHasResults bool) []Swallow {
	var out []Swallow
	var walk func(stmts []ast.Stmt)
	walk = func(stmts []ast.Stmt) {
		for i, s := range stmts {
			switch n := s.(type) {
			case *ast.IfStmt:
				var prev ast.Stmt
				if i > 0 {
					prev = stmts[i-1]
				}
				if sw, bad := judgeScanGuard(fset, n, prev, rowsExpr, fnHasResults); bad {
					out = append(out, sw)
				}
				walk(n.Body.List)
				for e := n.Else; e != nil; {
					switch el := e.(type) {
					case *ast.BlockStmt:
						walk(el.List)
						e = nil
					case *ast.IfStmt:
						// An else-if guard has no meaningful "preceding
						// statement" in this block; judge it with none.
						if sw, bad := judgeScanGuard(fset, el, nil, rowsExpr, fnHasResults); bad {
							out = append(out, sw)
						}
						walk(el.Body.List)
						e = el.Else
					default:
						e = nil
					}
				}
			case *ast.BlockStmt:
				walk(n.List)
			case *ast.SwitchStmt:
				for _, c := range n.Body.List {
					if cc, ok := c.(*ast.CaseClause); ok {
						walk(cc.Body)
					}
				}
			}
			// ForStmt / RangeStmt / FuncLit: deliberately not walked.
		}
	}
	walk(stmts)
	return out
}

// judgeScanGuard decides whether one if-statement is a Scan guard that
// swallows. Returns (swallow, true) when it is.
func judgeScanGuard(fset *token.FileSet, ifs *ast.IfStmt, prev ast.Stmt, rowsExpr string, fnHasResults bool) (Swallow, bool) {
	bin, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || (bin.Op != token.NEQ && bin.Op != token.EQL) {
		return Swallow{}, false
	}
	if nilID, ok := bin.Y.(*ast.Ident); !ok || nilID.Name != "nil" {
		return Swallow{}, false
	}

	// Is the compared value scan-derived?
	scanDerived := false
	switch x := bin.X.(type) {
	case *ast.Ident:
		if as, ok := ifs.Init.(*ast.AssignStmt); ok {
			scanDerived = assignsFromScan(as, x.Name, rowsExpr)
		}
		if !scanDerived && prev != nil {
			if as, ok := prev.(*ast.AssignStmt); ok {
				scanDerived = assignsFromScan(as, x.Name, rowsExpr)
			}
		}
	case *ast.CallExpr:
		// `if rows.Scan(&s) == nil { … }` — the call IS the condition.
		scanDerived = isScanCall(x, rowsExpr)
	}
	if !scanDerived {
		return Swallow{}, false
	}

	line := fset.Position(ifs.Pos()).Line

	if bin.Op == token.EQL {
		// Success-only guard: the failure path is empty by construction,
		// unless an else branch surfaces it.
		if els, ok := ifs.Else.(*ast.BlockStmt); ok && endsInReturnStmt(els.List) {
			return Swallow{}, false
		}
		if _, ok := ifs.Else.(*ast.IfStmt); ok {
			// An else-if chain re-examines the error; judged on its own visit.
			return Swallow{}, false
		}
		return Swallow{Line: line, Shape: "success-only"}, true
	}

	// err != nil arm: compliant only when it ends in a statement the failure
	// cannot fall through — a return that carries it out, or a call that ends
	// the process (os.Exit / log.Fatal / panic, the CLI shapes).
	if !endsInReturnStmt(ifs.Body.List) {
		if endsInTerminatingCall(ifs.Body.List) {
			return Swallow{}, false
		}
		return Swallow{Line: line, Shape: "guarded-continue"}, true
	}
	if fnHasResults && lastReturnPublishesNil(ifs.Body) {
		return Swallow{Line: line, Shape: "published-as-success"}, true
	}
	return Swallow{}, false
}

// endsInTerminatingCall reports whether the block's last statement is a call
// that never returns: panic(...), os.Exit(...), or log.Fatal/Fatalf/Fatalln.
// cmd/aihub-embed-backfill answers a scan failure with stderr + os.Exit(1) —
// the failure surfaces (a non-zero exit), so it is not a swallow.
func endsInTerminatingCall(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	es, ok := stmts[len(stmts)-1].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "panic"
	case *ast.SelectorExpr:
		recv, ok := fun.X.(*ast.Ident)
		if !ok {
			return false
		}
		if recv.Name == "os" && fun.Sel.Name == "Exit" {
			return true
		}
		if recv.Name == "log" && strings.HasPrefix(fun.Sel.Name, "Fatal") {
			return true
		}
	}
	return false
}

// assignsFromScan reports whether as assigns ident from a Scan-bearing call.
func assignsFromScan(as *ast.AssignStmt, ident, rowsExpr string) bool {
	assignsIdent := false
	for _, l := range as.Lhs {
		if id, ok := l.(*ast.Ident); ok && id.Name == ident {
			assignsIdent = true
		}
	}
	if !assignsIdent || len(as.Rhs) != 1 {
		return false
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	return isScanCall(call, rowsExpr)
}

// isScanCall reports whether call is a row scan: its selector is `.Scan`, or
// it passes the loop's rows expression to a helper (scanMemoryLite(rows)).
func isScanCall(call *ast.CallExpr, rowsExpr string) bool {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Scan" {
		return true
	}
	for _, a := range call.Args {
		if exprString(a) == rowsExpr {
			return true
		}
	}
	return false
}

// endsInReturnStmt reports whether the last statement of a block is a return.
// The LAST statement, deliberately: `log; continue` contains no return, and
// `close(); return err` does — the same rule ready_queue_query_errors_test.go
// applies to the send-time guards.
func endsInReturnStmt(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	_, ok := stmts[len(stmts)-1].(*ast.ReturnStmt)
	return ok
}

// lastReturnPublishesNil reports whether the guard body's FINAL return hands
// back the literal nil in its last result (or is bare) — a failure republished
// as a normal answer (aihub#549's shape). Only the terminating return is
// judged: an early `if aerr != nil { return aerr }` before `return NewErr(…)`
// is the classified-propagation shape, not a swallow.
func lastReturnPublishesNil(body *ast.BlockStmt) bool {
	ret, ok := body.List[len(body.List)-1].(*ast.ReturnStmt)
	if !ok {
		return false
	}
	if len(ret.Results) == 0 {
		return true
	}
	if id, ok := ret.Results[len(ret.Results)-1].(*ast.Ident); ok && id.Name == "nil" {
		return true
	}
	return false
}
