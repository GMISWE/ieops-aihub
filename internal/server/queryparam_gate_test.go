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

// ─────────────────────────────────────────────────────────────────────────────
//
//	aihub#432 — hop 2: the SAME policy, one package upstream
//
// ─────────────────────────────────────────────────────────────────────────────
//
// Everything above walks package server and stops there, while the policy it
// enforces is a property of the REPO. "Whose mistake is it?" does not stop being
// the question because the value arrived in an MCP argument map instead of a
// query string, and aihub#411's T1-2 audit found what that gap let through:
// recallNumArg in internal/mcp read `similarity_threshold: "notanumber"`,
// swallowed strconv's error and returned 0 — which is that parameter's OFF value
// — so buildRecallParams dropped the parameter and the caller got an UNFILTERED
// page with no error at any of the four hops. That is Rule 1's original defect
// (the `?similarity_threshold=notanumber` row of the measured table in
// queryparam.go), reproduced one hop upstream of its fix, in the one package
// this gate could not see.
//
// The response is a second SCOPE, not a second policy. internal/mcp has its own
// readers of caller-supplied argument text — strArg, scalarArg, numArg,
// parseBoolArg, parseNumArg — and they all live in helpers.go, which makes that
// file hop 2's queryparam.go. The invariant is then the same sentence with two
// nouns changed:
//
//	no file in package mcp may turn an MCP tool argument into a non-string
//	value except through the readers in helpers.go.
//
// ─── What this scope deliberately does NOT check, and why ───────────────────
//
//   - The COMPARISON half. `strArg(args, "fields") == "brief"` is a legitimate
//     hop-2 decision (aihub#313: `fields` is consumed in this process and has no
//     server hop to be dropped on), and there are more like it. Flagging those
//     would need an exemption list longer than the rule, so the vocabulary half
//     of the policy is left where it is enforceable — in package server, where
//     every such comparison IS a handler deciding a wire vocabulary by hand.
//
//   - json.Unmarshal, strings.Split and time.Parse, all three of which ARE in
//     the package-server set above. At hop 2 they are how a JSON array arrives
//     (tools_lifecycle.go decodes `members` with json.Unmarshal by design) and
//     how response bodies are taken apart; the numeric family below is the part
//     that carries the "malformed value becomes a plausible default" failure.
//     Widening this set is a real improvement and a separate change — it needs
//     the exemptions written first, and this one must not be blocked on them.
//
//   - `cursor`, the third escape in the same audit row. It never reaches a
//     conversion at all, so no scope of THIS shape can see it; that half is
//     filed as aihub#411's T1-6 wi and is deliberately untouched here.
//
// A limit stated is not a limit closed. What is closed is the numeric one, and
// it is closed the same way in both packages.

// toolArgPolicyFile is hop 2's queryparam.go: the one file in package mcp
// allowed to convert a caller-supplied argument into something else.
const toolArgPolicyFile = "helpers.go"

// mcpPackageDir is package mcp's source directory relative to this one. `go
// test` runs each package with its own directory as the working directory, so
// the relative path is stable; mcpSourceFiles fails loudly if it stops
// resolving, because a broken walk is how a gate reports green forever.
const mcpPackageDir = "../mcp"

// toolArgParseCalls is the conversion set for hop 2 — the numeric/scalar family
// only. See the note above for the three call families in parseCalls that are
// deliberately absent from it.
var toolArgParseCalls = map[string]bool{
	"strconv.Atoi":         true,
	"strconv.ParseFloat":   true,
	"strconv.ParseInt":     true,
	"strconv.ParseUint":    true,
	"strconv.ParseBool":    true,
	"strconv.ParseComplex": true,
	"strconv.Unquote":      true,
	"fmt.Sscan":            true,
	"fmt.Sscanf":           true,
	"fmt.Sscanln":          true,
}

// toolArgReaderFuncs are package mcp's own readers of caller-supplied argument
// text. A value one of them returns is still the caller's, so it taints exactly
// as a direct `args[key]` does — the same hole trimmedParam opened in the gate
// above, closed here before it can be opened.
var toolArgReaderFuncs = map[string]bool{
	"strArg":       true,
	"scalarArg":    true,
	"numArg":       true,
	"boolArg":      true,
	"parseBoolArg": true,
	"parseNumArg":  true,
	"csvArg":       true,
	"strSliceArg":  true,
}

// toolArgMapNames is the identifier package mcp binds the decoded argument map
// to. Every registered tool handler opens with `args, err := parseArgs(...)` and
// every helper takes `args map[string]any`; the other `map[string]any`
// parameters in the package (`result`, `slim`, `m`, `body`) hold RESPONSES,
// which are the server's bytes and not the caller's, and must not taint.
//
// Keying on the name is what an AST walk can decide without a type checker, and
// it fails in the safe direction twice over: renaming the parameter is visible
// in review, and the activity floor below goes red if the name ever stops
// appearing.
var toolArgMapNames = map[string]bool{"args": true}

// mcpSourceFiles lists package mcp's non-test .go files, by path.
func mcpSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(mcpPackageDir)
	if err != nil {
		t.Fatalf("read %s: %v — this gate walks a sibling package by relative path, and a "+
			"path that no longer resolves makes every assertion below vacuous", mcpPackageDir, err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(mcpPackageDir, n))
	}
	if len(out) < 10 {
		t.Fatalf("only found %d source files in package mcp — the walk is broken, not the package", len(out))
	}
	return out
}

// isToolArgRead reports whether e reads caller-supplied argument text: an index
// into the argument map, one of this package's readers, or either of those
// behind a type assertion (`args[key].(string)` is how the package spells most
// of them).
func isToolArgRead(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.IndexExpr:
		if id, ok := v.X.(*ast.Ident); ok {
			return toolArgMapNames[id.Name]
		}
	case *ast.TypeAssertExpr:
		return isToolArgRead(v.X)
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok {
			return toolArgReaderFuncs[id.Name]
		}
	}
	return false
}

// reachesToolArg reports whether n mentions a tainted identifier or performs an
// argument read itself.
func reachesToolArg(n ast.Node, tainted map[string]bool) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if id, ok := m.(*ast.Ident); ok && tainted[id.Name] {
			found = true
		}
		if expr, ok := m.(ast.Expr); ok && isToolArgRead(expr) {
			found = true
		}
		return true
	})
	return found
}

// toolArgTaint collects every identifier in body that holds caller-supplied
// argument text.
//
// Run to a fixed point rather than in one pass, so a value moved through a
// second local (`v := args[k]; s, _ := v.(string)`) stays tainted and so the
// result does not depend on the order ast.Inspect happens to visit statements
// in. That is strictly stronger than the intra-function single hop the gate
// above uses, and it costs nothing here: these are small function bodies.
func toolArgTaint(body ast.Node) map[string]bool {
	tainted := map[string]bool{}
	for round := 0; round < 5; round++ {
		before := len(tainted)
		ast.Inspect(body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range v.Rhs {
					if i >= len(v.Lhs) || !reachesToolArg(rhs, tainted) {
						continue
					}
					if id, ok := v.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
						tainted[id.Name] = true
					}
				}
				// A 2-value binding — `s, ok := args[k].(string)`, and the
				// `switch typed := v.(type)` header, which is an AssignStmt too
				// — has one RHS and two LHS names. The loop above pairs by
				// index and so never sees the first name when the RHS count is
				// the smaller of the two.
				if len(v.Rhs) == 1 && len(v.Lhs) > 1 && reachesToolArg(v.Rhs[0], tainted) {
					if id, ok := v.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
						tainted[id.Name] = true
					}
				}
			case *ast.ValueSpec:
				for i, val := range v.Values {
					if i < len(v.Names) && reachesToolArg(val, tainted) {
						tainted[v.Names[i].Name] = true
					}
				}
			}
			return true
		})
		if len(tainted) == before {
			break
		}
	}
	return tainted
}

// TestToolArgumentsGoThroughTheSharedReaders is the class gate for hop 2.
//
// Same shape as the package-server gate: walk every function body in the
// package, track the locals holding a caller-supplied argument, and fail on a
// numeric conversion whose arguments reach one of them.
func TestToolArgumentsGoThroughTheSharedReaders(t *testing.T) {
	fset := token.NewFileSet()
	conversions, taintedSites := 0, 0
	reported := map[string]bool{}
	for _, path := range mcpSourceFiles(t) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		policy := filepath.Base(path) == toolArgPolicyFile

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
			tainted := toolArgTaint(body)
			taintedSites += len(tainted)
			ast.Inspect(body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok || !toolArgParseCalls[calleeName(call)] {
					return true
				}
				// Counted in EVERY file including the policy file: after this
				// gate is satisfied the only conversions left are the ones in
				// helpers.go, so a count that skipped it would be 0 and could
				// not tell "the policy holds" from "the walk found nothing".
				conversions++
				if policy {
					return true
				}
				for _, arg := range call.Args {
					if !reachesToolArg(arg, tainted) {
						continue
					}
					pos := fset.Position(call.Pos()).String()
					if reported[pos] {
						return true
					}
					reported[pos] = true
					t.Errorf("%s: %s converts an MCP tool argument outside %s.\n"+
						"Hop 2 obeys the same policy as hop 3 (queryparam.go): a value the server "+
						"cannot interpret is the CALLER's mistake and must be refused naming the "+
						"parameter, never turned into a default. Returning 0 for unparseable text is "+
						"how `similarity_threshold: \"notanumber\"` became an unfiltered page — 0 is "+
						"that parameter's OFF value (aihub#411 T1-2, fixed in aihub#432). Read it "+
						"through the readers in %s.",
						fset.Position(call.Pos()), calleeName(call), toolArgPolicyFile, toolArgPolicyFile)
					return true
				}
				return true
			})
		}
	}
	// Two floors, because this gate has two independent ways to pass without
	// having looked at anything: a conversion set that matches nothing, and a
	// taint model that taints nothing. Both are set well under the measured
	// value so a refactor does not trip them and a broken walk does.
	if conversions < 2 {
		t.Errorf("only %d conversion calls found in package mcp — the conversion set matches "+
			"nothing and this gate is passing vacuously", conversions)
	}
	if taintedSites < 40 {
		t.Errorf("only %d tainted identifiers found across package mcp — the taint model has "+
			"stopped recognising how tool arguments are read, and this gate is passing vacuously",
			taintedSites)
	}
	t.Logf("inspected %d numeric conversions and %d tainted identifiers across package mcp",
		conversions, taintedSites)
}
