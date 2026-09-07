package server

// The CLASS gate for aihub#255 / aihub#267 / aihub#340.
//
// Those three were filed as three bugs. They are one: the repo had no single
// answer to "what happens to a bad request parameter", so each handler invented
// its own, and the three answers in the same binary were `?status=<anything>`
// silently becoming a SQL filter, `limit=500` silently becoming 50, and
// `similarity_threshold=notanumber` silently becoming "filter off".
//
// Fixing the three sites does not close that. A fourth handler written next
// month would invent a fourth answer, and nothing would go red. So the gate is
// not "are those three sites correct" — it is a STRUCTURAL invariant:
//
//	no file in package server may turn a query parameter into a non-string
//	value except through the readers in queryparam.go.
//
// Written against the AST rather than against a grep, because the shapes that
// have to be caught are not textual: the value reaches the parser inline in
// `strconv.Atoi(c.QueryParam("x"))` in one handler and through a local in
// `if s := c.QueryParam("x"); s != "" { strconv.ParseFloat(s, 64) }` in the
// next, and a substring rule that catches the first misses the second.
//
// ⚠️ What this gate does NOT prove, stated so nobody reads more into a green run:
// it proves the parse goes through the shared readers, not that the handler then
// picks the RIGHT reader. A /v1 handler could call queryIntLenientUI and get the
// lenient behaviour. The second test below closes the one form of that which is
// checkable structurally — a lenient reader may only be called from a ui_*.go
// file — and the behavioural half is carried by the per-endpoint tests in
// queryparam_policy_test.go.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The policy lives in exactly one file, and that is the only file allowed to
// convert a query-param string into something else.
const queryParamPolicyFile = "queryparam.go"

// parseCalls are the conversions that must not be reached from a query param
// outside the policy file. Keyed by "package.Func".
var parseCalls = map[string]bool{
	"strconv.Atoi":         true,
	"strconv.ParseFloat":   true,
	"strconv.ParseInt":     true,
	"strconv.ParseUint":    true,
	"strconv.ParseBool":    true,
	"fmt.Sscanf":           true,
	"fmt.Sscan":            true,
	"fmt.Sscanln":          true,
	"strings.Split":        true,
	"strings.SplitN":       true,
	"strings.SplitAfter":   true,
	"strings.Fields":       true,
	"strings.Cut":          true,
	"time.Parse":           true,
	"time.ParseInLocation": true,
	"time.ParseDuration":   true,
	"json.Unmarshal":       true,
	"url.Parse":            true,
	"url.ParseQuery":       true,
	"base64.StdEncoding":   true,
	"uuid.Parse":           true,
	"strconv.Unquote":      true,
	"strconv.ParseComplex": true,
}

// lenientReaders may only be called from a /ui handler file. They are in
// queryparam.go with the strict ones so the exemption is readable in one place,
// which means file-scoping them needs its own check.
var lenientReaders = map[string]bool{
	"queryIntLenientUI":   true,
	"queryFloatLenientUI": true,
	"queryBoolLenientUI":  true,
}

func serverSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, n)
	}
	if len(out) < 10 {
		// A path/glob mistake would make every assertion below vacuous, and a
		// vacuous gate reports green forever. 10 is far below the real count and
		// far above zero, so it fails on a broken walk and not on a refactor.
		t.Fatalf("only found %d source files in package server — the walk is broken, not the package", len(out))
	}
	return out
}

// queryParamReaderFuncs are this package's own thin wrappers around
// echo.Context.QueryParam. A value they return is still caller-supplied query
// text, so it must taint exactly as the raw reader does.
//
// 🔴 Without this the gate had a hole it was silently passing through:
// `since := trimmedParam(c, "since")` followed by `time.Parse(..., since)` in
// handleListWorkItems went unnoticed, because the ident was assigned from
// trimmedParam rather than from c.QueryParam. That particular site happened to
// be CORRECT, which is worse than if it had been wrong — a gate whose one blind
// spot sits over compliant code reports green and nobody looks again.
var queryParamReaderFuncs = map[string]bool{
	"trimmedParam": true,
	"queryCSV":     true,
}

// queryParamReaderMethods are the method names that hand back caller-supplied
// request text. `QueryParam` is the one echo idiom this package uses, but the
// others reach the same bytes by a different route and were each verified to
// slip past an earlier draft of this gate that only knew the first two:
//
//	c.Request().URL.Query().Get("x")   -> Get
//	c.FormValue("x")                   -> FormValue
//
// Get is deliberately unqualified. Narrowing it to "Get called on a Query()
// call" would be more precise and would also be the thing to write around, and a
// false positive here costs one call to the shared reader.
var queryParamReaderMethods = map[string]bool{
	"QueryParam":  true,
	"QueryParams": true,
	"FormValue":   true,
	"FormParams":  true,
	"Get":         true,
}

// isQueryParamCall reports whether e reads caller-supplied request text: an
// echo.Context reader whatever the receiver is named, or one of this package's
// own wrappers around one.
func isQueryParamCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		return queryParamReaderFuncs[id.Name]
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return queryParamReaderMethods[sel.Sel.Name]
}

// reachesQueryParam reports whether expression e mentions a tainted identifier
// or performs a request read itself.
func reachesQueryParam(e ast.Node, tainted map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
			found = true
		}
		if expr, ok := n.(ast.Expr); ok && isQueryParamCall(expr) {
			found = true
		}
		return true
	})
	return found
}

// taintedIdents collects every identifier in body that holds request text,
// through both spellings of a binding.
//
// 🔴 `var raw = c.QueryParam("x")` is an *ast.ValueSpec, not an *ast.AssignStmt.
// An earlier draft tracked only the latter, so the `var` spelling of the exact
// same code walked straight through a gate that caught `raw := ...`. Verified by
// injection, not by reading.
func taintedIdents(body ast.Node) map[string]bool {
	tainted := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range v.Rhs {
				if i < len(v.Lhs) && isQueryParamCall(rhs) {
					if id, ok := v.Lhs[i].(*ast.Ident); ok {
						tainted[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for i, val := range v.Values {
				if i < len(v.Names) && isQueryParamCall(val) {
					tainted[v.Names[i].Name] = true
				}
			}
		}
		return true
	})
	return tainted
}

// calleeName renders a call's callee as written: `pkg.Func` for a qualified
// call, `Func` for an unqualified one, "" for anything more complex.
//
// Shared by this gate and aihub#377's (project_visibility_gate_test.go), which
// needs the unqualified case because the loaders and access predicates it
// censuses are package-local: `loadMemoryFn`, `checkProjectAccessSoft`.
//
// Returning bare identifiers cannot affect THIS gate: every key in parseCalls,
// and in the maps consulted alongside it, is qualified (`strconv.Atoi`,
// `time.Parse`, …), and a bare name never equals one of those. Verified by
// reading parseCalls rather than assumed — a widened helper that quietly makes
// another gate match more is how one fix weakens a neighbour.
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok {
			return pkg.Name + "." + fn.Sel.Name
		}
	}
	return ""
}

// TestQueryParamsGoThroughTheSharedHelpers is the class gate.
//
// It walks every function in the package and, within each, tracks the local
// identifiers that hold a query-param value. It then fails on any conversion
// call whose arguments reach one of those identifiers or contain a QueryParam
// call directly.
//
// The tracking is intra-function and one hop deep, which is the depth the
// defect actually has: every site this covers reads the param and converts it
// within a few lines. A value laundered through a helper function would slip
// past, and that is a real limit rather than an oversight — closing it needs
// type-checked dataflow, and the cost of that is not repaid by a package where
// the whole idiom is read-then-parse.
func TestQueryParamsGoThroughTheSharedHelpers(t *testing.T) {
	fset := token.NewFileSet()
	conversions, comparisons := 0, 0
	reported := map[string]bool{}
	for _, name := range serverSourceFiles(t) {
		if name == queryParamPolicyFile {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		// Every function BODY in the file, whether it belongs to a declaration or
		// to a literal. `var h = func(c echo.Context) {...}` is a body too, and a
		// walk that only visits *ast.FuncDecl does not see inside it.
		//
		// A closure inside a function is therefore visited twice — once via the
		// enclosing declaration and once on its own — which is wanted, because the
		// taint set differs between the two views. `reported` deduplicates the
		// findings by position so one line is one message.
		var bodies []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.FuncDecl:
				if v.Body != nil {
					bodies = append(bodies, v.Body)
				}
			case *ast.FuncLit:
				if v.Body != nil {
					bodies = append(bodies, v.Body)
				}
			}
			return true
		})

		for _, body := range bodies {
			tainted := taintedIdents(body)

			// (1) CONVERSIONS: a request value reaching strconv/fmt/strings/time.
			ast.Inspect(body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok || !parseCalls[calleeName(call)] {
					return true
				}
				conversions++
				for _, arg := range call.Args {
					if reachesQueryParam(arg, tainted) {
						if reported[fset.Position(call.Pos()).String()] {
							return true
						}
						reported[fset.Position(call.Pos()).String()] = true
						t.Errorf("%s: %s reads a request parameter directly.\n"+
							"Every request parameter in package server must be read through the helpers in %s, "+
							"which is where the policy for a malformed or out-of-range value is decided "+
							"(aihub#255/#267/#340). Adding a fourth answer here is the defect those three are.",
							fset.Position(call.Pos()), calleeName(call), queryParamPolicyFile)
						return true
					}
				}
				return true
			})

			// (2) COMPARISONS: a request value tested against a non-empty string
			// literal.
			//
			// 🔴 This half exists because the conversion half does not cover the
			// defect. `if c.QueryParam("include_archived") == "true"` performs NO
			// conversion, and it was one of the sites this change fixed — `1`,
			// `True` and `yes` all read as false, silently, which is aihub#280's
			// finding restated. A gate that watched only strconv would have let
			// the identical line back in and stayed green.
			//
			// Comparison against "" is exempt: that is a presence test, not a
			// vocabulary decision, and it is how every handler here asks "did the
			// caller send this?".
			if strings.HasPrefix(filepath.Base(name), "ui_") {
				continue // /ui sentinels — see the exemption note in queryparam.go
			}
			ast.Inspect(body, func(m ast.Node) bool {
				bin, ok := m.(*ast.BinaryExpr)
				if !ok || (bin.Op != token.EQL && bin.Op != token.NEQ) {
					return true
				}
				for _, side := range [2]struct{ val, other ast.Expr }{{bin.X, bin.Y}, {bin.Y, bin.X}} {
					lit, ok := side.other.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING || lit.Value == `""` {
						continue
					}
					if reachesQueryParam(side.val, tainted) {
						if reported[fset.Position(bin.Pos()).String()] {
							continue
						}
						reported[fset.Position(bin.Pos()).String()] = true
						comparisons++
						t.Errorf("%s: a request parameter is compared against the literal %s.\n"+
							"That is a vocabulary decision made by hand, and it does not need a conversion to be "+
							"the aihub#340 defect: `== \"true\"` reads 1, True and yes as false, silently. Use "+
							"queryBool or queryEnumCSV in %s.",
							fset.Position(bin.Pos()), lit.Value, queryParamPolicyFile)
					}
				}
				return true
			})
		}
	}
	t.Logf("inspected %d conversion calls and flagged %d hand-rolled comparisons across package server",
		conversions, comparisons)
}

// TestLenientQueryReadersAreUIOnly keeps the documented /ui exemption from
// becoming a general-purpose opt-out.
//
// The exemption is justified by WHO the caller is — a browser following our own
// generated links, which cannot read a 400 — so it must not leak onto /v1, where
// the caller is a program that can. The check is by file name because that is
// what is decidable from the AST, and it is not an escape hatch worth having:
// getting a /v1 handler past this means renaming its file to ui_*.go, which
// review would catch and which contradicts the package's own naming.
func TestLenientQueryReadersAreUIOnly(t *testing.T) {
	fset := token.NewFileSet()
	found := 0
	for _, name := range serverSourceFiles(t) {
		if name == queryParamPolicyFile {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || !lenientReaders[id.Name] {
				return true
			}
			found++
			if !strings.HasPrefix(filepath.Base(name), "ui_") {
				t.Errorf("%s: %s is the /ui leniency exemption and must not be called from %s — "+
					"a /v1 caller is a program that can read a 400 (see the exemption note in %s)",
					fset.Position(call.Pos()), id.Name, name, queryParamPolicyFile)
			}
			return true
		})
	}
	if found == 0 {
		t.Fatal("found no calls to the lenient readers at all — this check is passing vacuously")
	}
	t.Logf("checked %d lenient-reader call sites", found)
}
