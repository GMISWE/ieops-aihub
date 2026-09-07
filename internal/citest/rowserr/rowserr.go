// Package rowserr finds `for rows.Next()` loops that never ask `rows.Err()`.
//
// # The defect this exists to prevent
//
// pgx v5's Query returns only SEND-time errors. Anything the server reports
// while EXECUTING the statement — a failed cast, a permission denial, a
// connection lost mid-stream — is deferred to rows.Err(), readable only after
// the loop. A loop that never asks sees "no more rows" and returns an empty
// result with a nil error, so an execution failure is delivered as a successful
// empty page. That is the dangerous direction: an empty result is
// indistinguishable from "nothing matched", so the caller cannot tell them
// apart and neither can a monitor.
//
// aihub#382 fixed one instance (listWorkItemsPage's text path answered
// `cursor=garbage` with HTTP 200 {"items":[]}); aihub#386 swept the class and
// left this scanner behind so the class cannot come back.
//
// # Why this is syntactic, and what that costs
//
// It does NOT type-check. Loading packages through go/types would make the gate
// depend on the module building, in the DB-free unit step where it needs to run
// even when something else is broken. Instead a value is treated as query rows
// when the SOURCE says so, by either of two rules:
//
//  1. it is assigned in this function from a call to Query / QueryContext, or
//  2. its declared type — parameter, result, receiver, local var, or a struct
//     field DECLARED IN THE SAME FILE — spells a Rows type (pgx.Rows,
//     *sql.Rows, pgxmock.Rows, ...).
//
// 🔴 "in the same file" is a real limit, not a hedge, and it is stated because
// this comment used to claim "struct field of the receiver" outright while
// rowsValuesIn looked only at the signature and at local `var` declarations —
// so `for s.rows.Next()` was invisible and invisible read as compliant
// (aihub#409). The scanner does not type-check, so it cannot resolve a
// receiver's field types across files; it recognises a field by NAME, taken
// from struct types declared in the file being scanned. A rows field whose
// struct is declared in another file of the same package is still invisible.
// TestScannerSeesRowsHeldInAStructField pins the half that works and
// TestScannerCannotSeeAStructFieldDeclaredElsewhere pins the half that does
// not, so the two halves of this paragraph cannot drift apart from the code.
//
// 🔴 That scoping is load-bearing rather than tidy, and the reason is in this
// repo: internal/render/postsanitize.go loops on `z.Next()`, an
// html.Tokenizer. A scanner keyed on "anything with a Next() method" reports it
// and every future iterator as a violation, and the cheapest way to silence a
// gate that cries wolf is to delete the gate. TestScannerIgnoresANonRowsIterator
// is the arm that keeps this honest.
//
// The cost is the mirror risk: rows reached by a route neither rule describes
// are INVISIBLE, and invisible reads as compliant. That is what
// TestScannerStillSeesEveryKnownRowsLoop is for — it asserts the scanner still
// sees a floor number of loops across the repo, so a change that narrows the
// recogniser fails loudly instead of reporting a clean tree.
package rowserr

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Loop is one `for X.Next()` loop over query rows.
type Loop struct {
	// File is slash-separated and relative to the root passed to ScanDir.
	File string
	// Line is the line of the `for` keyword.
	Line int
	// Func names the enclosing declaration, methods as "(Recv).Name", so the
	// report points at a unique site.
	Func string
	// Rows is the source text of the expression Next() was called on, e.g.
	// "rows" or "blockingRows". Two loops in one function are told apart by
	// this plus Line.
	Rows string
	// Checked is true when a call to <Rows>.Err() appears after this loop and
	// before the next loop over the same expression.
	Checked bool
}

// Key is the allowlist key for a loop: "<file>:<func>:<rows>".
//
// Deliberately WITHOUT the line number. An allowlist entry is a statement about
// a site, and a line number changes whenever anything above it is edited, so a
// keyed-by-line entry silently stops matching and the exemption evaporates —
// or, worse, starts matching a different loop that moved onto that line. The
// function plus the variable name identifies the loop for as long as the loop
// exists, and renaming either is exactly the kind of change that should make
// somebody re-read the exemption.
//
// ⚠️ Known coarseness, stated rather than left to be found: a function with
// SEVERAL loops over one variable gives them all the same Key, so a single
// exemption there would cover every one of them. PredictConflicts is exactly
// that shape — five loops over `rows`. Adding the line number would fix the
// coarseness and reintroduce the silent expiry above, which is the worse of the
// two, so the resolution is that the allowlist is expected to stay EMPTY: every
// entry is a decision somebody has to defend, and an entry on a multi-loop
// function has to defend all of them at once.
func (l Loop) Key() string {
	return l.File + ":" + l.Func + ":" + l.Rows
}

func (l Loop) String() string {
	return fmt.Sprintf("%s:%d %s over %s", l.File, l.Line, l.Func, l.Rows)
}

// rowsTypeNames are the type-name suffixes rule 2 accepts.
var rowsTypeNames = []string{"Rows"}

// ScanDir walks root and returns every query-rows loop in a non-test .go file,
// each marked Checked or not, sorted by file then line.
//
// Skipped: _test.go files (a test that swallows an execute-time error misleads
// nobody in production, and the fixtures under testdata/ must not be reported
// as violations of the repo they are fixtures for), and any directory named
// testdata, vendor, .git or node_modules.
func ScanDir(root string) ([]Loop, error) {
	var out []Loop
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "vendor", ".git", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		loops, scanErr := ScanFile(path, filepath.ToSlash(rel))
		if scanErr != nil {
			return scanErr
		}
		out = append(out, loops...)
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

// ScanFile parses one file. reportAs is the name used in the returned Loops.
func ScanFile(path, reportAs string) ([]Loop, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ScanSource(src, reportAs)
}

// ScanSource is the whole analysis, over one file's bytes. Exported so the
// scanner's own behaviour can be tested on fixtures without a repo.
func ScanSource(src []byte, reportAs string) ([]Loop, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, reportAs, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", reportAs, err)
	}

	// Collected over the WHOLE file before any function is scanned: a struct
	// may be declared after the methods that loop over its fields, and a
	// single-pass walk would miss exactly those.
	rowsFields := rowsFieldNamesIn(file)

	var out []Loop
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fnName := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			fnName = "(" + exprString(fn.Recv.List[0].Type) + ")." + fnName
		}
		out = append(out, scanFunc(fset, fn, fnName, reportAs, rowsFields)...)
	}
	return out, nil
}

// rowsFieldNamesIn returns the names of struct fields in this file whose
// declared type spells a Rows type.
//
// By NAME rather than by (struct, field) pair, because the scanner is
// syntactic: at `for s.rows.Next()` it knows the text `s.rows` and nothing
// about what `s` is. Keying on the field name is what makes the recognition
// possible at all, and it is why the doc comment says a field is recognised
// when SOME struct in the file declares that name as a rows type.
//
// The looseness is bounded in the direction that matters. To produce a false
// positive a file would have to declare a rows-typed field `x`, and separately
// loop `for <anything>.x.Next()` on a DIFFERENT type that also has an `x` with
// a Next() method. TestScannerIgnoresANonRowsIterator covers the shape that
// actually occurs here (html.Tokenizer, a local variable), and a false positive
// costs one allowlist line, while the false NEGATIVE this replaces cost silent
// invisibility.
func rowsFieldNamesIn(file *ast.File) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, f := range st.Fields.List {
			if !typeIsRows(f.Type) {
				continue
			}
			// Embedded fields have no Names; there is nothing to match on.
			for _, nm := range f.Names {
				out[nm.Name] = true
			}
		}
		return true
	})
	return out
}

// isRowsExpr reports whether the source text of a Next()/Err() receiver names
// query rows: either a recognised plain value, or a selector whose final
// element is a rows field name from this file.
func isRowsExpr(expr string, names, fields map[string]bool) bool {
	if names[expr] {
		return true
	}
	if i := strings.LastIndex(expr, "."); i >= 0 {
		return fields[expr[i+1:]]
	}
	return false
}

// scanFunc analyses one function body.
func scanFunc(fset *token.FileSet, fn *ast.FuncDecl, fnName, reportAs string, rowsFields map[string]bool) []Loop {
	rowsNames := rowsValuesIn(fn)

	// Every `for X.Next()` loop over a recognised rows value, in source order.
	type site struct {
		rows  string
		start token.Pos // the `for` keyword
		end   token.Pos // one past the loop's closing brace
	}
	var sites []site
	// Every `X.Err()` call, by expression text and position.
	errCalls := map[string][]token.Pos{}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ForStmt:
			if node.Cond == nil {
				return true
			}
			if rowsExpr, ok := zeroArgMethodCall(node.Cond, "Next"); ok && isRowsExpr(rowsExpr, rowsNames, rowsFields) {
				sites = append(sites, site{rows: rowsExpr, start: node.For, end: node.End()})
			}
		case *ast.CallExpr:
			if rowsExpr, ok := zeroArgMethodCall(node, "Err"); ok {
				errCalls[rowsExpr] = append(errCalls[rowsExpr], node.Pos())
			}
		}
		return true
	})

	sort.Slice(sites, func(i, j int) bool { return sites[i].start < sites[j].start })

	out := make([]Loop, 0, len(sites))
	for i, s := range sites {
		// The window in which an Err() call counts for THIS loop: after the
		// loop ends, and before the next loop over the SAME expression starts.
		//
		// 🔴 The upper bound is what makes "two loops, one Err()" report the
		// first loop. Without it a single trailing check would cover every
		// earlier loop on the same variable, and the earlier ones are exactly
		// the reads whose execute-time failure nobody ever asks about. That
		// shape is not hypothetical: it is one of the two blind spots the
		// aihub#386 survey named in its own method note.
		windowEnd := token.Pos(-1) // no bound
		for _, later := range sites[i+1:] {
			if later.rows == s.rows {
				windowEnd = later.start
				break
			}
		}
		checked := false
		for _, p := range errCalls[s.rows] {
			if p < s.end {
				continue
			}
			if windowEnd >= 0 && p >= windowEnd {
				continue
			}
			checked = true
			break
		}
		out = append(out, Loop{
			File:    reportAs,
			Line:    fset.Position(s.start).Line,
			Func:    fnName,
			Rows:    s.rows,
			Checked: checked,
		})
	}
	return out
}

// rowsValuesIn returns the set of expression texts in fn that name query rows,
// by either of the two rules in this package's doc comment.
func rowsValuesIn(fn *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}

	// Rule 2, on the signature: a parameter or result whose type spells Rows.
	// Includes the receiver, so a method on a rows-carrying type is covered.
	for _, field := range fieldsOf(fn.Recv, fn.Type.Params, fn.Type.Results) {
		if !typeIsRows(field.Type) {
			continue
		}
		for _, nm := range field.Names {
			names[nm.Name] = true
		}
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			// Rule 1: x, err := <recv>.Query(...) / .QueryContext(...)
			// Rule 2: x := <expr> where the RHS is itself a recognised rows
			// value is NOT inferred — aliasing is rare and guessing at it would
			// widen the recogniser without a site to justify it.
			if len(node.Rhs) != 1 {
				assignRowsByType(node, names)
				return true
			}
			call, ok := node.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "Query" && sel.Sel.Name != "QueryContext" {
				return true
			}
			// The rows value is the first assigned name that is not "_" and
			// not the trailing error.
			for i, lhs := range node.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				// pgx returns (Rows, error); the last result is the error.
				if i == len(node.Lhs)-1 && len(node.Lhs) > 1 {
					continue
				}
				names[id.Name] = true
			}
		case *ast.DeclStmt:
			// Rule 2, on a local declaration: var rows pgx.Rows
			gen, ok := node.Decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				return true
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type == nil || !typeIsRows(vs.Type) {
					continue
				}
				for _, nm := range vs.Names {
					names[nm.Name] = true
				}
			}
		}
		return true
	})
	return names
}

// assignRowsByType handles `rows, err = pool.Query(...)` split across a
// multi-value RHS, which the single-RHS path above does not see.
func assignRowsByType(node *ast.AssignStmt, names map[string]bool) {
	for i, rhs := range node.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Query" && sel.Sel.Name != "QueryContext") {
			continue
		}
		if i >= len(node.Lhs) {
			continue
		}
		if id, ok := node.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
			names[id.Name] = true
		}
	}
}

func fieldsOf(lists ...*ast.FieldList) []*ast.Field {
	var out []*ast.Field
	for _, l := range lists {
		if l == nil {
			continue
		}
		out = append(out, l.List...)
	}
	return out
}

// typeIsRows reports whether a type expression spells a rows type: the base
// identifier ends in "Rows", after stripping pointers and package qualifiers.
// So pgx.Rows, *sql.Rows and pgxmock.Rows all match; io.Reader does not.
func typeIsRows(t ast.Expr) bool {
	s := exprString(t)
	s = strings.TrimPrefix(s, "*")
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	for _, want := range rowsTypeNames {
		if s == want {
			return true
		}
	}
	return false
}

// zeroArgMethodCall reports the receiver's source text when e is a call to
// method `name` with no arguments, e.g. rows.Next() or rows.Err().
//
// No arguments is part of the test on purpose: it keeps a same-named method
// with a different shape from being read as this one.
func zeroArgMethodCall(e ast.Expr, name string) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return "", false
	}
	return exprString(sel.X), true
}

// exprString renders an expression back to source text, so two mentions of the
// same variable compare equal and a report names what the reader will find.
func exprString(e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), e); err != nil {
		return fmt.Sprintf("<unprintable %T>", e)
	}
	return buf.String()
}

// Unchecked filters loops down to the violations.
func Unchecked(loops []Loop) []Loop {
	var out []Loop
	for _, l := range loops {
		if !l.Checked {
			out = append(out, l)
		}
	}
	return out
}

// ParseAllowlist reads an allowlist file: one Key() per line, `#` comments and
// blank lines ignored. A missing file is an empty allowlist, not an error —
// "no exemptions" is the intended steady state.
func ParseAllowlist(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out, nil
}
