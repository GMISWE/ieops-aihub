// errarm.go finds rows.Err() CHECK ARMS the error can fall out of.
//
// # The defect this exists to prevent (aihub#623)
//
// The two sibling analyses police the loop's exit (rowserr.go: a `for X.Next()`
// that never asks X.Err()) and the loop's body (swallow.go: a per-row Scan
// error answered with `continue`). This one polices the CHECK ITSELF: code that
// does ask X.Err(), gets non-nil, and then lets control flow continue past the
// check with the error unsurfaced.
//
// The specimen is FnClaimWorkItem's idempotent lock re-query (fixed in
// aihub#608): its Err arm classified class 40 through retryConflictErr and
// returned — but a decode error is not class 40, so the non-conflict half fell
// through, and the truncated existingLocks was published as the idempotent
// claim's complete AcquiredLocks. To every other gate that site looked healthy:
// the aihub#386 gate saw an Err() call after the loop and called the loop
// checked, and checked it was — the CHECK existed, the ARM leaked. "Only
// retryConflictErr, no else-return" is the caught-in-the-act form, but the class is any
// partial handling: an arm that only logs, an arm that only recognises one
// error value, a condition narrowed so some errors never enter the arm at all.
//
// # What is recognised
//
// Every `X.Err()` call where X is a recognised rows expression (rowsValuesIn /
// rowsFieldNamesIn — the SAME recognition as the sibling analyses, same blind
// spots, so the three gates police one population; ctx.Err() and a
// bufio.Scanner's sc.Err() are excluded by the recognition, not by luck).
// Each call is classified by the statement form it appears in:
//
//   - `if err := X.Err(); err != nil { ARM }`  — judge ARM (the repo's idiom)
//   - `if X.Err() != nil { ARM }`              — judge ARM
//   - `err := X.Err()` then a later `if err != nil { ARM }` or `return err`
//     in the same block                        — judge ARM / compliant
//   - `return …, X.Err()` (anywhere in a return) — compliant: the error leaves
//
// A judged ARM is compliant when the error cannot fall out of it: the arm is a
// TERMINATING statement list — it ends in a return, in a call that ends the
// process (panic / os.Exit / log.Fatal*), or in an if/else whose branches all
// terminate (so `if classified { return a }; return b` and a fully-returning
// if/else both pass). Everything else is reported:
//
//   - fall-through: the arm does not terminate — a log-then-nothing, a bare
//     `continue` targeting an outer loop, or the specimen's "only the class-40
//     half returns". Also the `== nil` success-guard whose else is missing.
//   - published-as-success: the arm terminates via a return whose final result
//     is the literal nil (or a bare return) while the function declares
//     results — the failure leaves as a normal answer (swallow.go's shape, one
//     statement later).
//   - narrowed-condition: the guard's condition is not a plain nil-comparison
//     of the checked error — `err != nil && !errors.Is(err, X)` runs the arm
//     for SOME non-nil errors and falls through for the rest by construction,
//     whatever the arm does.
//   - unjudged-check-form: an X.Err() call in a shape this analysis cannot
//     judge (a bare log argument, an append operand, a closure this walk does
//     not enter). Reported rather than skipped: the aihub#409 lesson is that
//     invisible reads as compliant, so the catch-all keeps every recognised
//     Err() call accounted for — compliance is proven, not presumed.
//
// # The bar an exemption has to clear
//
// allowlist.txt (the aihub#386 gate's) says a best-effort site "just logs
// instead of returning, and logging satisfies the gate". That is still true
// THERE — its gate polices never-asking. It is deliberately NOT sufficient
// here: aihub#623's mandate is that an Err arm's else path must return or
// carry a written basis. err_arm_allowlist.txt beside this file is the ledger
// of those written bases — like scan_swallow_allowlist.txt it is NOT expected
// to be empty, every entry cites the documented policy that makes the
// fall-through defensible, and a stale entry is itself a failure because it
// pre-authorises whatever lands on that key next.
package rowserr

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
)

// ErrArm is one rows.Err() check whose arm can leak the error.
type ErrArm struct {
	// File is slash-separated and relative to the root passed to ScanDirErrArms.
	File string
	// Line is the line of the check (the `if`, or the call for unjudged forms).
	Line int
	// Func names the enclosing declaration, methods as "(Recv).Name".
	Func string
	// Rows is the source text of the expression Err() was called on.
	Rows string
	// Shape says how the error can leak: "fall-through",
	// "published-as-success", "narrowed-condition" or "unjudged-check-form".
	Shape string
}

// Key is the allowlist key, same construction and same deliberate coarseness
// as Loop.Key and Swallow.Key: no line number, so an entry survives edits
// above it, and a function with several checks over one variable name shares
// one key — an entry there has to defend all of them at once (fetchWIFacets
// is exactly that: two drains, two Err arms, one key).
func (a ErrArm) Key() string {
	return a.File + ":" + a.Func + ":" + a.Rows
}

func (a ErrArm) String() string {
	return fmt.Sprintf("%s:%d %s over %s (%s)", a.File, a.Line, a.Func, a.Rows, a.Shape)
}

// ScanDirErrArms walks root and returns every leaking Err arm in a non-test
// .go file, sorted by file then line. Same walk, same skips as ScanDir.
func ScanDirErrArms(root string) ([]ErrArm, error) {
	var out []ErrArm
	err := walkGoFiles(root, func(path, rel string) error {
		arms, scanErr := ScanFileErrArms(path, rel)
		if scanErr != nil {
			return scanErr
		}
		out = append(out, arms...)
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

// ScanFileErrArms parses one file. reportAs is the name used in the report.
func ScanFileErrArms(path, reportAs string) ([]ErrArm, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ScanSourceErrArms(src, reportAs)
}

// ScanSourceErrArms is the whole analysis over one file's bytes. Exported so
// the detector's own behaviour is testable on fixtures, like its siblings.
func ScanSourceErrArms(src []byte, reportAs string) ([]ErrArm, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, reportAs, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", reportAs, err)
	}
	rowsFields := rowsFieldNamesIn(file)

	var out []ErrArm
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fnName := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			fnName = "(" + exprString(fn.Recv.List[0].Type) + ")." + fnName
		}
		out = append(out, scanFuncErrArms(fset, fn, fnName, reportAs, rowsFields)...)
	}
	return out, nil
}

// errArmScan carries one function's analysis state.
type errArmScan struct {
	fset       *token.FileSet
	rowsNames  map[string]bool
	rowsFields map[string]bool
	// errCalls holds every recognised X.Err() call in the function, by the
	// call's position; consumed marks the ones a recognised form accounted
	// for. Whatever is left at the end is reported unjudged-check-form, so no
	// recognised call can be invisible.
	errCalls map[token.Pos]string
	consumed map[token.Pos]bool
	out      []ErrArm
}

// scanFuncErrArms analyses one function body.
func scanFuncErrArms(fset *token.FileSet, fn *ast.FuncDecl, fnName, reportAs string, rowsFields map[string]bool) []ErrArm {
	s := &errArmScan{
		fset:       fset,
		rowsNames:  rowsValuesIn(fn),
		rowsFields: rowsFields,
		errCalls:   map[token.Pos]string{},
		consumed:   map[token.Pos]bool{},
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if rowsExpr, isErr := zeroArgMethodCall(call, "Err"); isErr && isRowsExpr(rowsExpr, s.rowsNames, s.rowsFields) {
				s.errCalls[call.Pos()] = rowsExpr
			}
		}
		return true
	})
	if len(s.errCalls) == 0 {
		return nil
	}

	s.walkStmts(fn.Body.List, fn.Type.Results != nil && len(fn.Type.Results.List) > 0)

	// The catch-all: a recognised Err() call no judged form accounted for.
	for pos, rowsExpr := range s.errCalls {
		if s.consumed[pos] {
			continue
		}
		s.report(pos, rowsExpr, "unjudged-check-form")
	}

	for i := range s.out {
		s.out[i].File = reportAs
		s.out[i].Func = fnName
	}
	sort.Slice(s.out, func(i, j int) bool { return s.out[i].Line < s.out[j].Line })
	return s.out
}

func (s *errArmScan) report(pos token.Pos, rowsExpr, shape string) {
	s.out = append(s.out, ErrArm{
		Line:  s.fset.Position(pos).Line,
		Rows:  rowsExpr,
		Shape: shape,
	})
}

// errCallsIn returns the recognised Err() calls under node, NOT descending
// into FuncLits: a closure's control flow is its own, and its calls are walked
// (or reported unjudged) when walkStmts visits the closure body itself.
func (s *errArmScan) errCallsIn(node ast.Node) []token.Pos {
	var out []token.Pos
	if node == nil {
		return out
	}
	ast.Inspect(node, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if _, tracked := s.errCalls[call.Pos()]; tracked {
				out = append(out, call.Pos())
			}
		}
		return true
	})
	return out
}

func (s *errArmScan) consumeIn(node ast.Node) {
	for _, pos := range s.errCallsIn(node) {
		s.consumed[pos] = true
	}
}

// walkStmts visits one statement list. hasResults belongs to the innermost
// function whose body this list is in — a FuncLit's list is walked with the
// lit's own result count, because its returns leave the lit, not the function.
func (s *errArmScan) walkStmts(stmts []ast.Stmt, hasResults bool) {
	for i, st := range stmts {
		s.walkStmt(st, stmts, i, hasResults)
	}
}

func (s *errArmScan) walkStmt(st ast.Stmt, stmts []ast.Stmt, i int, hasResults bool) {
	switch n := st.(type) {
	case *ast.IfStmt:
		s.handleIf(n, hasResults)
	case *ast.ReturnStmt:
		// The error leaves the function (possibly through a classifier).
		s.consumeIn(n)
		s.walkFuncLitsIn(n)
	case *ast.AssignStmt:
		s.handleAssign(n, stmts, i, hasResults)
		s.walkFuncLitsIn(n)
	case *ast.ExprStmt, *ast.DeferStmt, *ast.GoStmt, *ast.DeclStmt, *ast.SendStmt:
		// An Err call here (a bare log argument, an append operand) is not a
		// judgeable guard; it stays unconsumed and the catch-all reports it.
		s.walkFuncLitsIn(st)
	case *ast.BlockStmt:
		s.walkStmts(n.List, hasResults)
	case *ast.ForStmt:
		s.walkStmts(n.Body.List, hasResults)
	case *ast.RangeStmt:
		s.walkStmts(n.Body.List, hasResults)
	case *ast.SwitchStmt:
		for _, c := range n.Body.List {
			if cc, ok := c.(*ast.CaseClause); ok {
				s.walkStmts(cc.Body, hasResults)
			}
		}
	case *ast.TypeSwitchStmt:
		for _, c := range n.Body.List {
			if cc, ok := c.(*ast.CaseClause); ok {
				s.walkStmts(cc.Body, hasResults)
			}
		}
	case *ast.SelectStmt:
		for _, c := range n.Body.List {
			if cc, ok := c.(*ast.CommClause); ok {
				s.walkStmts(cc.Body, hasResults)
			}
		}
	case *ast.LabeledStmt:
		s.walkStmt(n.Stmt, stmts, i, hasResults)
	}
}

// walkFuncLitsIn walks the bodies of FuncLits directly under node, each with
// its OWN result count. Inspection stops at each lit; nesting is handled by
// the recursive walk of the lit's own statements.
func (s *errArmScan) walkFuncLitsIn(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok {
			litHasResults := lit.Type.Results != nil && len(lit.Type.Results.List) > 0
			s.walkStmts(lit.Body.List, litHasResults)
			return false
		}
		return true
	})
}

// handleIf classifies an if-statement: an Err guard is judged, anything else
// is recursed into.
func (s *errArmScan) handleIf(ifs *ast.IfStmt, hasResults bool) {
	// Form: `if err := X.Err(); <cond>` — the init assigns from the call.
	errIdent := ""
	var guardRows string
	if as, ok := ifs.Init.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
		if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
			if rowsExpr, isErr := zeroArgMethodCall(call, "Err"); isErr && isRowsExpr(rowsExpr, s.rowsNames, s.rowsFields) {
				if len(as.Lhs) == 1 {
					if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
						errIdent = id.Name
						guardRows = rowsExpr
						s.consumed[call.Pos()] = true
					}
				}
			}
		}
	}

	condCalls := s.errCallsIn(ifs.Cond)

	switch {
	case errIdent != "":
		// Judge the init-assigned guard. Whatever the condition also contains
		// is part of this guard's decision, so consume it.
		s.consumeIn(ifs.Cond)
		if op, ok := plainNilCompare(ifs.Cond, errIdent, ""); ok {
			s.judgeGuard(ifs, op, guardRows, hasResults)
		} else {
			s.report(ifs.Pos(), guardRows, "narrowed-condition")
		}
	case len(condCalls) > 0:
		// Form: `if X.Err() != nil` — the call is the condition.
		guardRows = s.errCalls[condCalls[0]]
		s.consumeIn(ifs.Cond)
		if op, ok := plainNilCompare(ifs.Cond, "", guardRows); ok {
			s.judgeGuard(ifs, op, guardRows, hasResults)
		} else {
			s.report(ifs.Pos(), guardRows, "narrowed-condition")
		}
	}

	s.walkStmts(ifs.Body.List, hasResults)
	switch e := ifs.Else.(type) {
	case *ast.BlockStmt:
		s.walkStmts(e.List, hasResults)
	case *ast.IfStmt:
		s.handleIf(e, hasResults)
	}
}

// handleAssign covers the split form: `err := X.Err()` (or `=`) followed by a
// judgment somewhere later in the same block.
func (s *errArmScan) handleAssign(as *ast.AssignStmt, stmts []ast.Stmt, i int, hasResults bool) {
	if len(as.Rhs) != 1 {
		return
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return
	}
	rowsExpr, isErr := zeroArgMethodCall(call, "Err")
	if !isErr || !isRowsExpr(rowsExpr, s.rowsNames, s.rowsFields) {
		return
	}
	errIdent := ""
	if len(as.Lhs) == 1 {
		if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
			errIdent = id.Name
		}
	}
	s.consumed[call.Pos()] = true
	if errIdent == "" {
		// `_ = X.Err()` — asked and thrown away.
		s.report(as.Pos(), rowsExpr, "unjudged-check-form")
		return
	}

	// Scan forward for the first statement that mentions the ident.
	for _, later := range stmts[i+1:] {
		if !mentionsIdent(later, errIdent) {
			continue
		}
		switch n := later.(type) {
		case *ast.IfStmt:
			if n.Init == nil && len(s.errCallsIn(n.Cond)) == 0 && mentionsIdent(n.Cond, errIdent) {
				if op, ok := plainNilCompare(n.Cond, errIdent, ""); ok {
					s.judgeGuard(n, op, rowsExpr, hasResults)
				} else {
					s.report(n.Pos(), rowsExpr, "narrowed-condition")
				}
				return
			}
		case *ast.ReturnStmt:
			// `err := X.Err(); …; return err` — the error leaves.
			return
		}
		// The ident's first use is something this analysis cannot judge.
		s.report(as.Pos(), rowsExpr, "unjudged-check-form")
		return
	}
	// Asked, assigned, never consulted.
	s.report(as.Pos(), rowsExpr, "unjudged-check-form")
}

// judgeGuard applies the arm rules to one plain nil-compared guard.
func (s *errArmScan) judgeGuard(ifs *ast.IfStmt, op token.Token, rowsExpr string, hasResults bool) {
	if op == token.EQL {
		// Success-only guard: the failure path is the else.
		var elseTerminates bool
		var elseLastReturn *ast.ReturnStmt
		switch e := ifs.Else.(type) {
		case *ast.BlockStmt:
			elseTerminates = stmtListTerminates(e.List)
			if len(e.List) > 0 {
				elseLastReturn, _ = e.List[len(e.List)-1].(*ast.ReturnStmt)
			}
		case *ast.IfStmt:
			elseTerminates = stmtTerminates(e)
		}
		if !elseTerminates {
			s.report(ifs.Pos(), rowsExpr, "fall-through")
			return
		}
		if hasResults && elseLastReturn != nil && returnPublishesNil(elseLastReturn) {
			s.report(ifs.Pos(), rowsExpr, "published-as-success")
		}
		return
	}

	// err != nil arm: the error must not fall out of it.
	body := ifs.Body.List
	if !stmtListTerminates(body) {
		s.report(ifs.Pos(), rowsExpr, "fall-through")
		return
	}
	if hasResults && len(body) > 0 {
		if ret, ok := body[len(body)-1].(*ast.ReturnStmt); ok && returnPublishesNil(ret) {
			s.report(ifs.Pos(), rowsExpr, "published-as-success")
		}
	}
}

// plainNilCompare reports whether cond is exactly `<checked> != nil` or
// `<checked> == nil`, where <checked> is the ident named errIdent (when
// non-empty) or a recognised Err() call on rowsExpr (when errIdent is empty).
func plainNilCompare(cond ast.Expr, errIdent, rowsExpr string) (token.Token, bool) {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || (bin.Op != token.NEQ && bin.Op != token.EQL) {
		return 0, false
	}
	if nilID, ok := bin.Y.(*ast.Ident); !ok || nilID.Name != "nil" {
		return 0, false
	}
	if errIdent != "" {
		if id, ok := bin.X.(*ast.Ident); ok && id.Name == errIdent {
			return bin.Op, true
		}
		return 0, false
	}
	if call, ok := bin.X.(*ast.CallExpr); ok {
		if expr, isErr := zeroArgMethodCall(call, "Err"); isErr && expr == rowsExpr {
			return bin.Op, true
		}
	}
	return 0, false
}

// mentionsIdent reports whether name appears as an identifier under node.
func mentionsIdent(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// stmtListTerminates reports whether a statement list cannot be fallen out of:
// its last statement terminates in the go-spec sense this analysis needs.
func stmtListTerminates(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	return stmtTerminates(stmts[len(stmts)-1])
}

// stmtTerminates is the recursive half: a return, a process-ending call, a
// block that terminates, or an if WITH an else where both branches terminate.
// A `continue` or `break` deliberately does NOT terminate — in an Err arm it
// targets an outer loop, which is exactly the best-effort skip this gate
// exists to adjudicate rather than wave through.
func stmtTerminates(st ast.Stmt) bool {
	switch n := st.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.ExprStmt:
		return endsInTerminatingCall([]ast.Stmt{n})
	case *ast.BlockStmt:
		return stmtListTerminates(n.List)
	case *ast.IfStmt:
		if n.Else == nil || !stmtListTerminates(n.Body.List) {
			return false
		}
		switch e := n.Else.(type) {
		case *ast.BlockStmt:
			return stmtListTerminates(e.List)
		case *ast.IfStmt:
			return stmtTerminates(e)
		}
		return false
	case *ast.LabeledStmt:
		return stmtTerminates(n.Stmt)
	}
	return false
}

// returnPublishesNil reports whether ret hands back the literal nil in its
// final result, or is bare — same rule as lastReturnPublishesNil, on the
// statement rather than the block.
func returnPublishesNil(ret *ast.ReturnStmt) bool {
	if len(ret.Results) == 0 {
		return true
	}
	if id, ok := ret.Results[len(ret.Results)-1].(*ast.Ident); ok && id.Name == "nil" {
		return true
	}
	return false
}
