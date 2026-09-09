package main

// This file adds the check aihub#508 exists for: every `--- PASS:` assertion a
// ci.yml step makes must name a test path that actually EXISTS in the tree, and
// that the step's own `-run` selects.
//
// # The blind spot it closes (measured 2026-09-09, aihub#416's executor)
//
// Everything else in this command compares FUNCTION names: the inventory lists
// functions, the manifest lists functions, `coveredBy` matches functions
// against a step's `-run`. But the assertion a DB step actually makes is finer
// than that — it greps for `--- PASS: TestX/a_named_subtest` — and no gate saw
// the second half of that name. So a renamed subtest left every check here
// green while the step itself went red on the runner, and `go test ./...` was
// green locally throughout, because nothing outside CI executes a step's `run:`
// body. The rename was found by pushing.
//
// The report that filed aihub#508 proposed extracting each step's `run:` from
// the YAML and executing it locally. That is the stronger check and it is also
// the one nobody will run: it needs Postgres, the migrations and several
// minutes, so it cannot live in `go test ./...` where the signal was missing.
// What can live there is the half that rots: the NAMES. This file enumerates
// the test-name tree from the source and matches every assertion against it, so
// a rename that used to need a push to discover is now red in the unit tests
// and in the aihub#303 gate step.
//
// # What it does and does not prove
//
// It proves the assertion's target exists and is selected. It does NOT prove
// the test passes, nor that the step's shell is otherwise correct — a `tee`
// typo or a broken `for` list is still only visible by running the step. The
// residual holes are named in checkAssertedNames' comment rather than left
// implicit, because a gate that quietly covers less than its name suggests is
// the failure this whole command exists to prevent.
//
// # Why the source, and not the inventory
//
// The DB-free inventory (`go test ./... -json` with AIHUB_TEST_DB unset) cannot
// supply subtest names: these tests call `t.Skip` at the top of the function,
// before the first `t.Run`, so no subtest is ever created there. The names have
// to come from the AST, which means the enumeration is only as good as what it
// can fold — and an expression it cannot fold is recorded as `Opaque` and
// reported, rather than silently narrowing the candidate set (which would turn
// an unverifiable assertion into a confident "no such test").

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// passMarker is the prefix `go test -v` prints for a test that passed. The DB
// steps' `--- PASS:` assertions are what make "the step ran the tests it names"
// checkable at all — `go test` prints "ok" and exits 0 when everything it
// selected SKIPped — so the marker's spelling is part of the contract, and the
// patterns below are matched INCLUDING it.
const passMarker = "--- PASS: "

// ------------------------------------------------------------- source oracle

// SubtestScope is the statically enumerable subtest structure of one scope: the
// names its `t.Run` calls create, keyed by the name `go test` would print, and
// the `t.Run` name expressions this command could not enumerate.
//
// Opaque is not decoration. It is the difference between "this assertion names
// a subtest that does not exist" and "this command cannot tell", and those two
// need different fixes — the first is a stale ci.yml line, the second is a test
// whose subtest names are computed at run time.
type SubtestScope struct {
	Children map[string]*SubtestScope
	Opaque   []string
}

func newSubtestScope() *SubtestScope {
	return &SubtestScope{Children: map[string]*SubtestScope{}}
}

// collectTestNames returns, per package import path, the test functions in that
// package and the subtest tree each one creates.
//
// Only _test.go files are searched for test functions, but constants are folded
// over ALL the package's files, for the same reason checkSkipMessages does it:
// a name declared in one file has to be visible when another file is read.
func collectTestNames(root, module string) (map[string]map[string]*SubtestScope, error) {
	pkgs, err := goPackages(root)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(pkgs))
	for dir := range pkgs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	out := map[string]map[string]*SubtestScope{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		hasTest := false
		for _, p := range pkgs[dir] {
			if strings.HasSuffix(p, "_test.go") {
				hasTest = true
				break
			}
		}
		if !hasTest {
			continue
		}
		files, err := readGoFiles(pkgs[dir])
		if err != nil {
			return nil, err
		}
		for i := range files {
			f := &files[i]
			f.ast, err = parser.ParseFile(fset, f.path, f.src, parser.ParseComments)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", f.path, err)
			}
		}
		importPath, err := importPathOf(root, dir, module)
		if err != nil {
			return nil, err
		}
		// Group by package NAME: one directory legally holds `package p` and
		// `package p_test`, and both may declare the same constant with
		// different values. They compile into one test binary, so the FUNCTIONS
		// are pooled under one import path while the constants are not.
		byPkg := map[string][]goFile{}
		for _, f := range files {
			byPkg[f.ast.Name.Name] = append(byPkg[f.ast.Name.Name], f)
		}
		funcs := map[string]*SubtestScope{}
		for _, pkgFiles := range byPkg {
			consts := packageStringConsts(pkgFiles)
			pkgVars := packageCompositeVars(pkgFiles)
			for _, f := range pkgFiles {
				if !strings.HasSuffix(f.path, "_test.go") {
					continue
				}
				for _, decl := range f.ast.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok || fn.Recv != nil || fn.Body == nil || !isTestFunc(fn) {
						continue
					}
					funcs[fn.Name.Name] = collectSubtests(fn.Body, consts, pkgVars, fset)
				}
			}
		}
		if len(funcs) > 0 {
			out[importPath] = funcs
		}
	}
	return out, nil
}

// importPathOf turns a walked directory into the import path `go test`'s
// package arguments resolve to, so the two sides can be compared at all.
func importPathOf(root, dir, module string) (string, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", fmt.Errorf("relativise %s against %s: %w", dir, root, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return module, nil
	}
	return module + "/" + rel, nil
}

// isTestFunc reports whether fn is a test function `go test` would run: named
// `Test…` and taking exactly one *testing.T. The signature check is what keeps
// TestMain (a *testing.M) and helpers like `TestHelperProcess(t, args)` out.
func isTestFunc(fn *ast.FuncDecl) bool {
	if !strings.HasPrefix(fn.Name.Name, "Test") {
		return false
	}
	params := fn.Type.Params
	if params == nil || len(params.List) != 1 || len(params.List[0].Names) != 1 {
		return false
	}
	star, ok := params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

// collectSubtests enumerates the subtest names one scope creates.
//
// It descends through ordinary control flow — a `t.Run` inside a `for` or an
// `if` is still a subtest of this scope — but stops at each `t.Run` it finds
// and recurses into that call's function literal instead. That is what keeps
// the tree a TREE: without it a grandchild's name would also be recorded as a
// child, and `TestX/grandchild` would verify against a path `go test` never
// prints.
func collectSubtests(body *ast.BlockStmt, consts map[string]string, pkgVars map[string]*ast.CompositeLit, fset *token.FileSet) *SubtestScope {
	sc := newSubtestScope()
	tables := indexTables(body, pkgVars)
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		nameExpr, lit, ok := runCall(call)
		if !ok {
			return true
		}
		names, opaque := resolveRunName(nameExpr, tables, consts, fset)
		if opaque != "" {
			sc.Opaque = append(sc.Opaque, opaque)
		}
		for _, name := range names {
			child := newSubtestScope()
			if lit != nil {
				child = collectSubtests(lit.Body, consts, pkgVars, fset)
			} else {
				// A subtest body that is not a literal (a named function, a
				// stored closure) may create subtests of its own that this
				// walk cannot see. Say so rather than reporting an empty scope,
				// which would read as "it has no subtests".
				child.Opaque = append(child.Opaque,
					"the subtest body is not a function literal, so its own t.Run names are not enumerable")
			}
			key := rewriteTestName(name)
			if prev, dup := sc.Children[key]; dup {
				// `go test` disambiguates duplicate sibling names with #01, #02,
				// … Merging keeps both scopes' children reachable; the suffix
				// itself is handled where assertions are matched.
				for k, v := range child.Children {
					prev.Children[k] = v
				}
				prev.Opaque = append(prev.Opaque, child.Opaque...)
				continue
			}
			sc.Children[key] = child
		}
		return false // the call's own function literal was handled above
	})
	return sc
}

// runCall recognises a `t.Run(name, body)` call and returns its two arguments.
// Matching on the selector name alone — `Run`, whatever the receiver is called —
// is deliberate: the receiver is `t`, `tt`, `subT` or a wrapper in different
// files, and requiring two arguments is what keeps `exec.Command(…).Run()` out.
func runCall(call *ast.CallExpr) (ast.Expr, *ast.FuncLit, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Run" || len(call.Args) != 2 {
		return nil, nil, false
	}
	lit, _ := call.Args[1].(*ast.FuncLit)
	return call.Args[0], lit, true
}

// tableIndex holds the subtest names reachable through a range variable, which
// is how every table-driven test in this repository names its arms:
//
//	for _, tc := range []struct{ name string … }{{name: "a"}, …} { t.Run(tc.name, …) }
//	for key := range map[string]…{"a": …} { t.Run(key, …) }
//	for _, s := range []string{"a", …} { t.Run(s, …) }
//
// Without it, six of ci.yml's asserted subtest names (aihub#377's identity arms)
// are unverifiable, and "unverifiable" would be the majority answer for any
// future table.
type tableIndex struct {
	// values maps a range variable to the strings it takes.
	values map[string][]string
	// fields maps a range variable and a struct field to the strings that field
	// takes across the table's elements.
	fields map[string]map[string][]string
}

func indexTables(body *ast.BlockStmt, pkgVars map[string]*ast.CompositeLit) *tableIndex {
	idx := &tableIndex{values: map[string][]string{}, fields: map[string]map[string][]string{}}
	// Local composite literals first, so `cases := []struct{…}{…}` followed by
	// `range cases` resolves as directly as an inline table does.
	locals := map[string]*ast.CompositeLit{}
	for k, v := range pkgVars {
		locals[k] = v
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || i >= len(s.Rhs) {
					continue
				}
				if cl, ok := s.Rhs[i].(*ast.CompositeLit); ok {
					locals[id.Name] = cl
				}
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if i >= len(s.Values) {
					continue
				}
				if cl, ok := s.Values[i].(*ast.CompositeLit); ok {
					locals[name.Name] = cl
				}
			}
		}
		return true
	})

	ast.Inspect(body, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		cl := compositeOf(rng.X, locals)
		if cl == nil {
			return true
		}
		if _, isMap := cl.Type.(*ast.MapType); isMap {
			// `for key := range map[string]T{…}`: the names are the KEYS.
			if id, ok := rng.Key.(*ast.Ident); ok && id.Name != "_" {
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if s, ok := stringLit(kv.Key); ok {
						idx.values[id.Name] = append(idx.values[id.Name], s)
					}
				}
			}
			return true
		}
		id, ok := rng.Value.(*ast.Ident)
		if !ok || id.Name == "_" {
			return true
		}
		fieldNames := structFieldNames(cl.Type)
		for _, elt := range cl.Elts {
			if s, ok := stringLit(elt); ok {
				idx.values[id.Name] = append(idx.values[id.Name], s)
				continue
			}
			row, ok := elt.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for i, f := range row.Elts {
				name := ""
				var value ast.Expr
				if kv, ok := f.(*ast.KeyValueExpr); ok {
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					name, value = key.Name, kv.Value
				} else if i < len(fieldNames) {
					// A positional row is only readable when the element type
					// is written out inline, which is the common shape here.
					name, value = fieldNames[i], f
				}
				s, ok := stringLit(value)
				if name == "" || !ok {
					continue
				}
				if idx.fields[id.Name] == nil {
					idx.fields[id.Name] = map[string][]string{}
				}
				idx.fields[id.Name][name] = append(idx.fields[id.Name][name], s)
			}
		}
		return true
	})
	return idx
}

// packageCompositeVars collects package-level composite literals, so a table
// declared as `var cases = []struct{…}{…}` next to the test resolves too.
func packageCompositeVars(files []goFile) map[string]*ast.CompositeLit {
	out := map[string]*ast.CompositeLit{}
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
						out[name.Name] = cl
					}
				}
			}
		}
	}
	return out
}

// compositeOf resolves a range expression to the composite literal it iterates,
// through one level of indirection (an identifier bound to a table) and the
// parentheses and address-of an author may have written around it.
func compositeOf(e ast.Expr, locals map[string]*ast.CompositeLit) *ast.CompositeLit {
	switch x := e.(type) {
	case *ast.CompositeLit:
		return x
	case *ast.ParenExpr:
		return compositeOf(x.X, locals)
	case *ast.UnaryExpr:
		return compositeOf(x.X, locals)
	case *ast.Ident:
		return locals[x.Name]
	}
	return nil
}

// structFieldNames returns the field names of an inline element type, in order,
// so that a positional table row can be read. An element type named by an
// identifier is not resolved: the declaration may be anywhere, and guessing
// would attribute a literal to the wrong field.
func structFieldNames(t ast.Expr) []string {
	arr, ok := t.(*ast.ArrayType)
	if !ok {
		return nil
	}
	st, ok := arr.Elt.(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil
	}
	var names []string
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// resolveRunName folds a `t.Run` name expression to the names it can produce.
// The second return value is non-empty when it could not, and carries the
// expression's source text for the report.
// A concatenation of a prefix and a table field — `t.Run("create_"+tc.name, …)`
// — is folded by cross-producing the two sides. When one range variable indexes
// two tables in the same function (aihub#396 does exactly that: a create table
// and an update table, both bound to `tc`) the product is WIDER than what
// `go test` prints. That over-approximation can only make this gate accept a
// name it should have questioned; it cannot invent a failure, which is the
// direction to err in for a gate that fails the build.
func resolveRunName(e ast.Expr, tables *tableIndex, consts map[string]string, fset *token.FileSet) ([]string, string) {
	switch x := e.(type) {
	case *ast.Ident:
		if vals := tables.values[x.Name]; len(vals) > 0 {
			return vals, ""
		}
	case *ast.SelectorExpr:
		if base, ok := x.X.(*ast.Ident); ok {
			if vals := tables.fields[base.Name][x.Sel.Name]; len(vals) > 0 {
				return vals, ""
			}
		}
	case *ast.ParenExpr:
		return resolveRunName(x.X, tables, consts, fset)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			left, lop := resolveRunName(x.X, tables, consts, fset)
			right, rop := resolveRunName(x.Y, tables, consts, fset)
			if lop == "" && rop == "" {
				var out []string
				for _, l := range left {
					for _, r := range right {
						out = append(out, l+r)
					}
				}
				if len(out) > 0 {
					return out, ""
				}
			}
		}
	}
	// constString handles the literal, the package-level constant and the
	// concatenation of those; it is tried after the table lookups so that a
	// range variable shadowing a constant name resolves to the table.
	if s, ok := constString(e, consts); ok {
		return []string{s}, ""
	}
	return nil, exprText(e, fset)
}

func exprText(e ast.Expr, fset *token.FileSet) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "<unprintable t.Run name expression>"
	}
	return b.String()
}

// rewriteTestName is testing.rewrite: the transformation `go test` applies to a
// subtest name before printing it. Spaces become underscores and unprintable
// runes are escaped, which is why ci.yml's assertions are full of
// `a_named_subtest` while the source says "a named subtest".
//
// It is reimplemented rather than approximated with strings.ReplaceAll because
// the space class is wider than ' ' — and the em-dash group names in
// internal/domain's embedding-budget test sit right next to those runes.
func rewriteTestName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case isTestNameSpace(r):
			b.WriteByte('_')
		case !strconv.IsPrint(r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isTestNameSpace mirrors testing.isSpace, which is unexported.
func isTestNameSpace(r rune) bool {
	if 0x2000 <= r && r <= 0x200a {
		return true
	}
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return false
}

// testPaths returns every line `go test -v` could print a PASS on, given the
// packages a step selects and the `-run` it selects them with, together with
// the unenumerable `t.Run` names met on the way.
//
// The lines are built the way `go test` prints them — four spaces of indent per
// level — so that an anchored pattern behaves here as it does on the runner.
func testPaths(trees map[string]map[string]*SubtestScope, sel []PackageSel, run string, applyRun bool) (lines []string, opaque []string, err error) {
	pkgs := make([]string, 0, len(trees))
	for p := range trees {
		if !selects(sel, p) {
			continue
		}
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	if !applyRun {
		run = ""
	}
	for _, p := range pkgs {
		names := make([]string, 0, len(trees[p]))
		for n := range trees[p] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			ls, op, err := walkScope([]string{name}, trees[p][name], run)
			if err != nil {
				return nil, nil, err
			}
			lines = append(lines, ls...)
			opaque = append(opaque, op...)
		}
	}
	return lines, opaque, nil
}

// walkScope emits the PASS line for one path and recurses into its subtests,
// stopping wherever the `-run` pattern does: a descendant of an unselected
// scope never runs, so it never prints.
func walkScope(segs []string, sc *SubtestScope, run string) (lines []string, opaque []string, err error) {
	if run != "" {
		ok, err := matchesRunPath(run, segs)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, nil
		}
	}
	lines = append(lines, strings.Repeat("    ", len(segs)-1)+passMarker+strings.Join(segs, "/")+" (0.00s)")
	opaque = append(opaque, sc.Opaque...)
	kids := make([]string, 0, len(sc.Children))
	for k := range sc.Children {
		kids = append(kids, k)
	}
	sort.Strings(kids)
	for _, k := range kids {
		// A fresh slice per child: appending into segs' spare capacity would
		// have every sibling write over the previous one's segment.
		next := append(append(make([]string, 0, len(segs)+1), segs...), k)
		ls, op, err := walkScope(next, sc.Children[k], run)
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, ls...)
		opaque = append(opaque, op...)
	}
	return lines, opaque, nil
}

func selects(sel []PackageSel, importPath string) bool {
	if len(sel) == 0 {
		return true
	}
	for _, s := range sel {
		if s.Matches(importPath) {
			return true
		}
	}
	return false
}

// matchesRunPath applies `go test -run` semantics to a whole test PATH, not
// just its first element: `-run 'TestX/^only$'` runs TestX but prints a PASS
// line for no other subtest, so an assertion naming one is as stale as one
// naming a subtest that was deleted. MatchesRun answers the first element only,
// which is the right question for `coveredBy` (does the step run this function)
// and the wrong one here.
//
// Mirrors testing.matcher: element i of the path is constrained by element i of
// the pattern when the pattern has one, and unconstrained when it does not.
func matchesRunPath(pattern string, segs []string) (bool, error) {
	for _, alt := range SplitRunPattern(pattern) {
		ok := true
		for i, seg := range segs {
			if i >= len(alt) {
				break
			}
			re, err := regexp.Compile(alt[i])
			if err != nil {
				return false, fmt.Errorf("invalid -run regexp %q: %w", alt[i], err)
			}
			if !re.MatchString(seg) {
				ok = false
				break
			}
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// ------------------------------------------------------ assertions in ci.yml

// PassAssertion is one `--- PASS:` assertion a step makes: the alternatives
// whose success keeps the step green, and where in the script it lives.
//
// The alternatives are a list because ci.yml really uses a `||` chain — the
// aihub#316 step greps for a fully-qualified leaf and falls back to a `.*`
// prefix — and checking the members independently would report a stale name
// for a chain the shell is perfectly happy with.
type PassAssertion struct {
	Step string
	Line int
	Text string
	// Want is the loop value this assertion was expanded from, or "" when the
	// pattern was written out literally. It goes in the error message because
	// the offending text on the line is `$want`, which names nothing.
	Want string
	Alts []PassAlt
}

// PassAlt is one `grep` inside an assertion.
type PassAlt struct {
	// Pattern is the grep pattern with loop variables substituted, INCLUDING
	// the `--- PASS: ` prefix — the marker's spelling is part of what is
	// checked, since a step that greps for `-- PASS:` asserts nothing.
	Pattern string
	Log     string
	// Fixed is `grep -F`: the pattern is a literal substring, not a regexp.
	Fixed bool
}

var (
	// passChainRE is the accepted spelling of a `--- PASS:` assertion: one or
	// more `grep -q` alternatives, each `||`-chained, ending in a non-zero
	// exit. It is an allowlist for the same reason skipGuardRE is one — a line
	// that merely mentions the marker can assert the opposite of, or nothing
	// at all about, what it appears to assert:
	//
	//	grep -q -- '--- PASS: TestX' a.log || true      # can never fail
	//	! grep -q -- '--- PASS: TestX' a.log || exit 1  # requires a FAILURE
	//
	// Both used to be indistinguishable from a real assertion by text search.
	passChainRE = regexp.MustCompile(
		`^((?:grep\s+-q[A-Za-z]*\s+--\s+(?:'[^']*'|"[^"]*")\s+\S+\s*\|\|\s*)+)` +
			`(?:exit\s+[1-9][0-9]*|\{\s*(?:[^{}]*;\s*)?exit\s+[1-9][0-9]*\s*;?\s*\})$`)
	// passGrepRE picks the individual greps out of a chain passChainRE accepted.
	passGrepRE = regexp.MustCompile(`grep\s+-q([A-Za-z]*)\s+--\s+(?:'([^']*)'|"([^"]*)")\s+(\S+)`)
	// passCountRE is the second accepted spelling: a COUNT captured for a later
	// comparison, as the aihub#316 step does. The count itself is not this
	// gate's business; the pattern being matchable is.
	passCountRE = regexp.MustCompile(
		`^[A-Za-z_][A-Za-z0-9_]*=\$\(\s*grep\s+-c([A-Za-z]*)\s+--\s+(?:'([^']*)'|"([^"]*)")\s+(\S+)\s*(?:\|\|\s*true\s*)?\)$`)
	// forInRE recognises the loop the want-lists are written as.
	forInRE = regexp.MustCompile(`^for\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(.+)$`)
	// shellExpansionRE finds any surviving `$…` in a pattern.
	shellExpansionRE = regexp.MustCompile(`\$\{?[A-Za-z_(]`)
)

// loopScope is one `for var in …; do` the walk is inside.
type loopScope struct {
	variable string
	values   []string
}

// collectPassAssertions extracts every `--- PASS:` assertion in a step's script,
// expanding the `for want in …` lists the want-lists are written as.
//
// It works on LINES rather than on shellScript.Commands because the whole
// assertion — the greps, the `||` chain and the `{ echo …; exit 1; }` tail — is
// one line once YAML backslash continuations are folded, while the command
// splitter cuts it at every `;` and `||`. skipGuardFor does the same for the
// same reason.
//
// Lines inside a multi-line quoted string are skipped: they are data, and a
// step's prose ("to reproduce, grep for --- PASS: TestX") asserts nothing.
// Depth is deliberately NOT restricted — the want-list loop puts every real
// assertion at depth 1, and unlike a SKIP guard, whether an assertion RUNS
// does not change whether the name it asserts exists.
func collectPassAssertions(sc *shellScript, stepName string) ([]PassAssertion, []string) {
	var out []PassAssertion
	var problems []string
	var loops []loopScope

	for i, ln := range sc.Lines {
		text := strings.TrimSpace(ln.Text)
		if text != "" && ln.Quote == 0 && strings.Contains(text, passMarker) {
			as, probs := parsePassLine(text, stepName, i, loops)
			out = append(out, as...)
			problems = append(problems, probs...)
		}
		// Loop bookkeeping happens after the line is read, so that a `for` line
		// does not scope itself, and uses the same quote-aware splitter the
		// command model does.
		if ln.Text == "" || ln.Quote != 0 {
			continue
		}
		parts, _ := splitUnquoted(ln.Text, ln.Quote)
		for _, p := range parts {
			if p.quote != 0 {
				continue
			}
			t := strings.TrimSpace(p.text)
			switch {
			case t == "done" && len(loops) > 0:
				loops = loops[:len(loops)-1]
			case strings.HasPrefix(t, "for "):
				m := forInRE.FindStringSubmatch(t)
				if m == nil {
					continue
				}
				words, err := shellWords(m[2])
				if err != nil {
					problems = append(problems, fmt.Sprintf(
						"step %q, line %d: the `for %s in …` list cannot be read (%v), so any `%s` assertion inside it "+
							"is unchecked: %s", stepName, i, m[1], err, strings.TrimSpace(passMarker), t))
					continue
				}
				loops = append(loops, loopScope{variable: m[1], values: words})
			}
		}
	}
	return out, problems
}

// parsePassLine turns one line into assertions, one per loop value it expands
// to. A line that mentions the marker and is neither accepted spelling is a
// PROBLEM rather than a skip: an unrecognised assertion is an unchecked one,
// and "dbtestcov did not understand this line" must not be the same observable
// as "this line is fine".
func parsePassLine(text, stepName string, line int, loops []loopScope) ([]PassAssertion, []string) {
	var alts []PassAlt
	switch {
	case passChainRE.MatchString(text) && !exitZeroRE.MatchString(text):
		chain := passChainRE.FindStringSubmatch(text)[1]
		for _, m := range passGrepRE.FindAllStringSubmatch(chain, -1) {
			pat := m[2]
			if pat == "" {
				pat = m[3]
			}
			alts = append(alts, PassAlt{Pattern: pat, Log: m[4], Fixed: strings.Contains(m[1], "F")})
		}
	case passCountRE.MatchString(text):
		m := passCountRE.FindStringSubmatch(text)
		pat := m[2]
		if pat == "" {
			pat = m[3]
		}
		alts = append(alts, PassAlt{Pattern: pat, Log: m[4], Fixed: strings.Contains(m[1], "F")})
	default:
		return nil, []string{fmt.Sprintf(
			"step %q, line %d mentions %q but is not an assertion dbtestcov can check, so the name it names is not "+
				"verified to exist. It must be spelled `grep -q -- '%sTestX/sub' <log> || exit 1` (a `|| { echo …; exit 1; }` "+
				"tail and further `|| grep -q …` alternatives are fine), or `n=$(grep -c -- '%sTestX' <log>)` for a count: %s",
			stepName, line, strings.TrimSpace(passMarker), passMarker, passMarker, text)}
	}

	// A chain that mixes marker and non-marker greps is refused rather than
	// half-checked: the non-marker alternative could satisfy the shell on its
	// own, which would make a stale name in the marker alternative harmless
	// here and red on the runner — the wrong way round.
	marked := 0
	for _, a := range alts {
		if strings.Contains(a.Pattern, passMarker) {
			marked++
		}
	}
	if marked == 0 {
		return nil, nil
	}
	if marked != len(alts) {
		return nil, []string{fmt.Sprintf(
			"step %q, line %d chains a %q grep together with one that does not mention it; dbtestcov cannot tell which "+
				"alternative is supposed to hold, so neither is checked: %s",
			stepName, line, strings.TrimSpace(passMarker), text)}
	}

	// Expand the loop variable, if the patterns use one.
	variable, values := "", []string{""}
	for _, a := range alts {
		for i := len(loops) - 1; i >= 0; i-- {
			if strings.Contains(a.Pattern, "$"+loops[i].variable) ||
				strings.Contains(a.Pattern, "${"+loops[i].variable+"}") {
				variable, values = loops[i].variable, loops[i].values
				break
			}
		}
		if variable != "" {
			break
		}
	}

	var out []PassAssertion
	var problems []string
	for _, v := range values {
		as := PassAssertion{Step: stepName, Line: line, Text: text, Want: v}
		bad := false
		for _, a := range alts {
			pat := a.Pattern
			if variable != "" {
				pat = strings.ReplaceAll(pat, "${"+variable+"}", v)
				pat = strings.ReplaceAll(pat, "$"+variable, v)
			}
			if shellExpansionRE.MatchString(pat) {
				problems = append(problems, fmt.Sprintf(
					"step %q, line %d: the assertion pattern %q still holds a shell expansion after substitution, so "+
						"dbtestcov cannot tell what name it asserts. Write the name out, or put it in a `for … in` list: %s",
					stepName, line, pat, text))
				bad = true
				break
			}
			as.Alts = append(as.Alts, PassAlt{Pattern: pat, Log: a.Log, Fixed: a.Fixed})
		}
		if !bad {
			out = append(out, as)
		}
	}
	return out, problems
}

// shellWords splits a `for … in <list>` word list, honouring both quote styles.
// A bare or double-quoted word holding an expansion is an error: it would make
// the asserted name depend on something this command cannot see.
func shellWords(list string) ([]string, error) {
	var words []string
	i := 0
	for i < len(list) {
		for i < len(list) && (list[i] == ' ' || list[i] == '\t') {
			i++
		}
		if i >= len(list) {
			break
		}
		var w strings.Builder
		for i < len(list) && list[i] != ' ' && list[i] != '\t' {
			switch c := list[i]; c {
			case '\'', '"':
				j := strings.IndexByte(list[i+1:], c)
				if j < 0 {
					return nil, fmt.Errorf("unterminated %c quote", c)
				}
				w.WriteString(list[i+1 : i+1+j])
				i += j + 2
			default:
				w.WriteByte(c)
				i++
			}
		}
		word := w.String()
		if shellExpansionRE.MatchString(word) {
			return nil, fmt.Errorf("the word %q holds a shell expansion", word)
		}
		words = append(words, word)
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("the list is empty")
	}
	return words, nil
}

// grepMatcher turns a grep pattern into the predicate grep would apply.
//
// `grep -F` is a literal substring. Everything else is a basic regular
// expression, and the difference from Go's syntax is not cosmetic: in a BRE
// `(`, `)`, `{`, `}`, `+`, `?` and `|` are ORDINARY characters, and the
// aihub#316 step's patterns end in " (" — the open paren of Go's duration
// suffix. Compiling those as Go regexps is a syntax error, and "the gate
// crashed" is indistinguishable from "the gate was removed".
//
// A backslash is refused rather than guessed at: in a BRE it is what turns
// those same characters INTO metacharacters, so the two dialects diverge
// exactly there, and no assertion in ci.yml needs one.
func grepMatcher(pattern string, fixed bool) (func(string) bool, error) {
	if fixed {
		return func(line string) bool { return strings.Contains(line, pattern) }, nil
	}
	if strings.Contains(pattern, `\`) {
		return nil, fmt.Errorf("the pattern %q contains a backslash; basic-regexp escapes are not modelled by dbtestcov "+
			"(use `grep -F` for a literal, or drop the escape)", pattern)
	}
	// The overwhelming majority of these patterns are whole test names with no
	// metacharacter in them, and a substring scan of the candidate lines is
	// both faster and exactly what grep would do.
	if !strings.ContainsAny(pattern, ".*[]^$") {
		return func(line string) bool { return strings.Contains(line, pattern) }, nil
	}
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '.', '*', '[', ']', '^', '$':
			b.WriteRune(r) // BRE metacharacters, same meaning in Go's syntax
		case '(', ')', '{', '}', '+', '?', '|':
			b.WriteString(regexp.QuoteMeta(string(r))) // literal in a BRE
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("the pattern %q does not compile as a basic regexp: %w", pattern, err)
	}
	return re.MatchString, nil
}

// checkAssertedNames verifies that every `--- PASS:` assertion in the workflow
// could match a test that exists, in a package the step runs, under the `-run`
// the step passes.
//
// It reports three distinguishable failures, because they have three different
// fixes and reporting them as one ("no such test") is how a correct assertion
// gets deleted instead of corrected:
//
//   - the name exists but the step's `-run` does not select it, or its package
//     is not one the step runs — the step is asserting something it never asked
//     for, so the assertion can only ever fail;
//   - the name does not exist anywhere — the ordinary rename or deletion, which
//     is the case aihub#508 was filed for;
//   - the name does not exist AND the functions involved hold `t.Run` names
//     this command cannot enumerate — unverifiable, not absent.
//
// Residual holes, named rather than implied: it does not execute the step, so a
// wrong `tee` target, a mistyped log name or a `-run` that selects nothing are
// still only visible by running CI (the first two are caught by
// ParseWorkflow's guard checks, the third is not); a subtest created by a
// helper function or from a computed name is `Opaque`, so an assertion on it
// reports "unverifiable" rather than proving anything; and it says nothing
// about whether the test PASSES.
func checkAssertedNames(scan *WorkflowScan, trees map[string]map[string]*SubtestScope) ([]string, error) {
	cache := &candidateCache{trees: trees, sets: map[string]*candidateSet{}}
	var problems []string
	for _, as := range scan.Assertions {
		matched, verdict, err := matchAssertion(as, scan, cache)
		if err != nil {
			return nil, err
		}
		if matched {
			continue
		}
		problems = append(problems, verdict)
	}
	sort.Strings(problems)
	return problems, nil
}

// candidateSet is the set of PASS lines one selection could print.
type candidateSet struct {
	lines  []string
	opaque []string
}

// candidateCache memoises those sets. Without it the tree is re-walked once per
// assertion per widening level — 430 assertions over 1,945 test functions, which
// measured 27s and would have put a minute onto the aihub#303 step for nothing.
type candidateCache struct {
	trees map[string]map[string]*SubtestScope
	sets  map[string]*candidateSet
}

// forInvocations returns the union of what every given invocation could print,
// which is what a SHARED log requires. ci.yml's aihub#148 step runs two `go
// test` commands into one log (`tee` then `tee -a`) and asserts across both;
// attributing its assertions to the first invocation alone reported three
// perfectly good names as unrunnable — a false failure, and the shape that
// teaches a reader to delete the assertion.
func (c *candidateCache) forInvocations(invs []*Invocation, applyRun bool) (*candidateSet, error) {
	key := fmt.Sprintf("run=%v|", applyRun)
	for _, inv := range invs {
		key += fmt.Sprintf("%v;%s|", inv.Packages, inv.Run)
	}
	if set, ok := c.sets[key]; ok {
		return set, nil
	}
	set := &candidateSet{}
	if len(invs) == 0 {
		lines, opaque, err := testPaths(c.trees, nil, "", false)
		if err != nil {
			return nil, err
		}
		set.lines, set.opaque = lines, opaque
	}
	for _, inv := range invs {
		lines, opaque, err := testPaths(c.trees, inv.Packages, inv.Run, applyRun)
		if err != nil {
			return nil, err
		}
		set.lines = append(set.lines, lines...)
		set.opaque = append(set.opaque, opaque...)
	}
	c.sets[key] = set
	return set, nil
}

// matchAssertion decides one assertion and, when it fails, composes the verdict.
func matchAssertion(as PassAssertion, scan *WorkflowScan, cache *candidateCache) (bool, string, error) {
	// Try the assertion against progressively wider candidate sets, so that the
	// message can say WHY it did not match the real one.
	var (
		strictOpaque []string
		widest       = -1
	)
	for _, a := range as.Alts {
		invs := invocationsFor(scan, as.Step, a.Log)
		sets := make([]*candidateSet, 0, 3)
		if len(invs) > 0 {
			for _, applyRun := range []bool{true, false} {
				set, err := cache.forInvocations(invs, applyRun)
				if err != nil {
					return false, "", fmt.Errorf("step %q, line %d: %w", as.Step, as.Line, err)
				}
				sets = append(sets, set)
			}
		}
		all, err := cache.forInvocations(nil, false)
		if err != nil {
			return false, "", fmt.Errorf("step %q, line %d: %w", as.Step, as.Line, err)
		}
		sets = append(sets, all)

		match, err := grepMatcher(a.Pattern, a.Fixed)
		if err != nil {
			return false, "", fmt.Errorf("step %q, line %d: %w", as.Step, as.Line, err)
		}
		// Opacity is attributed to the test function the assertion NAMES, not
		// collected from the whole candidate set: internal/domain alone holds
		// two dozen computed `t.Run` names, so "some test somewhere has an
		// unfoldable name" is true on every failure and would report every
		// rename as merely unverifiable. That is the wrong answer to the
		// aihub#416 case, and the wrong instruction to the reader.
		strictOpaque = append(strictOpaque, opaqueUnder(cache.trees, invs, a.Pattern)...)

		for level, set := range sets {
			// When there is no invocation the only set IS the widest one, so
			// matching it is a pass rather than a diagnosis.
			if len(invs) == 0 {
				level = 0
			}
			hit := false
			for _, ln := range set.lines {
				if match(ln) {
					hit = true
					break
				}
			}
			if hit {
				if level == 0 {
					return true, "", nil
				}
				if level > widest {
					widest = level
				}
				break
			}
		}
	}

	var pats []string
	for _, a := range as.Alts {
		pats = append(pats, fmt.Sprintf("%q in %s", a.Pattern, a.Log))
	}
	// The line number counts from the start of the step's `run:` block, not
	// from the top of the file, so it is labelled as such and the offending
	// text travels with it — a want-list line reads `$want`, which on its own
	// tells the reader nothing about which entry to go and fix.
	where := fmt.Sprintf("step %q, line %d of its `run:` block asserts %s",
		as.Step, as.Line, strings.Join(pats, " or "))
	if as.Want != "" {
		where += fmt.Sprintf(" (from the want-list entry %q)", as.Want)
	}
	where += fmt.Sprintf(", in `%s`", capLine(as.Text))
	switch {
	case widest == 1:
		return false, where + ", and a test of that name exists but the step's `-run` does not select it, so the " +
			"assertion can only fail: widen the -run, or assert a name it selects", nil
	case widest == 2:
		return false, where + ", and a test of that name exists but not in a package this step runs, so the " +
			"assertion can only fail: name that package in the `go test` arguments, or assert a test that is there", nil
	case len(strictOpaque) > 0:
		return false, where + ", and no test of that name is enumerable — but the test function it names holds `t.Run` " +
			"names dbtestcov cannot fold, so it may exist: " + firstFew(strictOpaque) +
			". Either give the subtest a literal name, or stop asserting it by name", nil
	default:
		return false, where + ", and no test of that name exists in the tree — it was renamed or deleted while this " +
			"assertion stayed behind, which is exactly the state aihub#508 exists to catch. Fix the name here (or " +
			"restore the test); do NOT delete the assertion to go green", nil
	}
}

// invocationsFor finds every `go test` in this step that writes the log an
// assertion greps, which is what pins the assertion to a package set and a
// `-run`.
//
// Matching by step AND log matters in both directions: ci.yml has steps teeing
// recall_unmatched.log next to recall_unmatched_http.log, so the log has to be
// matched exactly; and it has a step appending two invocations to one log, so
// ALL of them count, not the first.
func invocationsFor(scan *WorkflowScan, step, log string) []*Invocation {
	var out []*Invocation
	for i := range scan.Invocations {
		if scan.Invocations[i].Step == step && scan.Invocations[i].Log == log {
			out = append(out, &scan.Invocations[i])
		}
	}
	return out
}

// testFuncNameRE picks the test function an assertion pattern names, which is
// the scope any explanation of the failure has to come from.
var testFuncNameRE = regexp.MustCompile(`Test[A-Za-z0-9_]+`)

// opaqueUnder returns the unenumerable `t.Run` names inside the test function
// an assertion names — the only opacity that can explain that assertion
// matching nothing. An empty result means the answer is definite: the name is
// absent, not merely unverifiable.
//
// A pattern that names no function at all (aihub#316's `--- PASS: .*<leaf>`)
// cannot be attributed, so it falls back to the whole selection: over-cautious
// there, definite everywhere else.
func opaqueUnder(trees map[string]map[string]*SubtestScope, invs []*Invocation, pattern string) []string {
	var sel []PackageSel
	for _, inv := range invs {
		sel = append(sel, inv.Packages...)
	}
	name := testFuncNameRE.FindString(pattern)
	var out []string
	for pkg, funcs := range trees {
		if !selects(sel, pkg) {
			continue
		}
		if name == "" {
			for _, sc := range funcs {
				out = append(out, collectOpaque(sc)...)
			}
			continue
		}
		if sc, ok := funcs[name]; ok {
			out = append(out, collectOpaque(sc)...)
		}
	}
	return out
}

func collectOpaque(sc *SubtestScope) []string {
	out := append([]string{}, sc.Opaque...)
	for _, child := range sc.Children {
		out = append(out, collectOpaque(child)...)
	}
	return out
}

// capLine shortens a script line for an error message. The tail of these lines
// is a long `echo "::error::…"` that repeats what the message already says.
//
// Cut by RUNE, not by byte: these lines carry em-dashes, and a byte cut can
// land inside one and print a replacement character where a name should be.
func capLine(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

func firstFew(xs []string) string {
	seen := map[string]bool{}
	var uniq []string
	for _, x := range xs {
		if seen[x] {
			continue
		}
		seen[x] = true
		uniq = append(uniq, x)
	}
	sort.Strings(uniq)
	if len(uniq) > 5 {
		return strings.Join(uniq[:5], "; ") + fmt.Sprintf("; … (%d more)", len(uniq)-5)
	}
	return strings.Join(uniq, "; ")
}
