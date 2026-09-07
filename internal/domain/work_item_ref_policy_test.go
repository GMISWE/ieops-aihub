package domain

// Structural gate for aihub#402's policy row: ONE id-or-slug resolution rule
// for the whole repo — `id = $1 OR slug = $1`, scoped to the caller's visible
// projects where the caller has any. No prefix dispatch anywhere.
//
// This runs with no database, so it executes in CI's "Unit tests" step and on a
// developer's laptop. That matters: the behavioural half
// (work_item_ref_db_test.go) is AIHUB_TEST_DB-gated and SKIPs outside its own
// scoped CI step, so it is this file that a new prefix dispatch cannot walk
// past.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prefixDispatchExemptions is the escape hatch, and it is deliberately in the
// GATE'S OWN FILE rather than in a data file beside it.
//
// Adding an entry is therefore a diff to this test, which a reviewer reads as
// "somebody is exempting themselves from the aihub#402 policy" — not as a
// routine data edit. That asymmetry is the point: the cheapest way to satisfy
// this gate must be to use the union, never to add a line here.
//
// Empty, and expected to stay empty. aihub#402 removed both dispatches it found
// (GetWorkItem's, and the unused FormatIDOrSlug) rather than exempting either.
var prefixDispatchExemptions = map[string]bool{}

// TestNoWorkItemPrefixDispatch fails when any non-test Go file decides between
// the id column and the slug column by testing a `wi_` prefix.
//
// 🔴 It asks "does this idiom exist", not "do the known resolvers behave". Every
// resolver in this repo already took the union when aihub#402 landed; what the
// audit found was one that did not, plus a helper that could not. A gate written
// as "GetWorkItem uses the OR form" would say nothing about the NEXT resolver
// somebody writes, and writing the prefix test is the only way to reintroduce
// the class.
//
// Why the `wi_` prefix specifically is the right thing to forbid: work item ids
// are `wi_` + 8 base62 chars and slugs are `project || '#' || seq`
// (migration 0002), and the project-name regex `^[a-z][a-z0-9_-]{0,39}$` does
// NOT reserve `wi_`. So the prefix does not distinguish an id from a slug — a
// project named `wi_lab` yields slugs that begin with it — and any code branching
// on it has assumed something untrue about the data.
func TestNoWorkItemPrefixDispatch(t *testing.T) {
	root := repoRootForPolicy(t)
	files := nonTestGoFiles(t, root)
	if len(files) < 50 {
		t.Fatalf("found only %d non-test .go files under %s; this gate is not looking at the "+
			"repo it claims to police", len(files), root)
	}

	found := 0
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)

		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		lines, err := findPrefixDispatches(src, rel)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if prefixDispatchExemptions[rel] {
			continue
		}
		for _, line := range lines {
			found++
			t.Errorf(`%s:%d tests a "wi_" prefix to tell a work item id from a slug.`+"\n"+
				"    That distinction is not real: project names are validated by "+
				"^[a-z][a-z0-9_-]{0,39}$, which does not reserve `wi_`, and slug is "+
				"project||'#'||seq — so a project named `wi_lab` has slugs starting `wi_` too.\n"+
				"    aihub#402's policy is one rule everywhere: resolve with "+
				"`id = $1 OR slug = $1` (scoped to visible projects where the caller has any). "+
				"The union cannot match two rows: id is the PRIMARY KEY, idx_wi_slug is UNIQUE, "+
				"and a slug always contains '#' while an id never does.", rel, line)
		}
	}
	if found > 0 {
		t.Logf("%d prefix dispatch(es) found; see the errors above", found)
	}
}

// TestPrefixDispatchGateStillSeesTheIdiom is why a clean run of the test above
// means anything.
//
// That gate passes when it finds no prefix dispatch, and it also passes when its
// detector is broken and finds nothing at all. Those are opposite facts with the
// same exit code — the shape that lets a structural gate rot into a no-op. This
// arm feeds the detector the exact idiom aihub#402 removed and requires it to
// fire, so "the repo is clean" is distinguishable from "the scanner is blind".
func TestPrefixDispatchGateStillSeesTheIdiom(t *testing.T) {
	// Byte-for-byte the shape deleted from GetWorkItem and FormatIDOrSlug.
	const reintroduced = `package p

import "strings"

func resolve(idOrSlug string) (string, string) {
	if strings.HasPrefix(idOrSlug, "wi_") {
		return "id", idOrSlug
	}
	return "slug", idOrSlug
}
`
	if got := countPrefixDispatches(t, reintroduced); got != 1 {
		t.Errorf("the detector found %d prefix dispatches in a file that plainly contains one; "+
			"TestNoWorkItemPrefixDispatch would report a clean repo no matter what it contained", got)
	}

	// And it must not fire on prefix tests that are about something else, or the
	// gate becomes noise and gets deleted. `ra_` and `mem_` id prefixes are
	// tested legitimately elsewhere in this repo.
	const unrelated = `package p

import "strings"

func f(s string) bool {
	return strings.HasPrefix(s, "ra_") || strings.HasPrefix(s, "mem_") || strings.HasPrefix(s, "wi")
}
`
	if got := countPrefixDispatches(t, unrelated); got != 0 {
		t.Errorf("the detector fired %d time(s) on prefixes that are not `wi_`; it would report "+
			"unrelated id handling as a policy violation", got)
	}
}

// prefixDispatchFuncs are the strings functions that ANSWER the forbidden
// question — "does this string start with wi_" — and are therefore a dispatch.
//
// TrimPrefix is deliberately absent, and the boundary is the point rather than
// an oversight. HasPrefix and CutPrefix both hand the caller a boolean about
// the prefix, which is the decision aihub#402 forbids; TrimPrefix hands back
// the remainder and is normalisation, which is legitimate and common. Adding it
// would make the gate fire on correct code, and a gate that cries wolf is one
// somebody deletes — see TestPrefixDispatchGateStillSeesTheIdiom, which pins
// both directions.
var prefixDispatchFuncs = map[string]bool{"HasPrefix": true, "CutPrefix": true}

// findPrefixDispatches is THE detector — the single copy. It reports the line
// of every `strings.HasPrefix(<expr>, "wi_")` and `strings.CutPrefix(<expr>,
// "wi_")` in src, whether the prefix is written as a literal or reached through
// a same-file string constant.
//
// 🔴 One function, called by both the repo-wide gate and its self-test below.
// An earlier draft of this file had the walk written out twice, once in each,
// which is the mistake this comment exists to prevent: the self-test then
// measures a COPY of the detector, so the copy can keep passing while the
// detector the gate actually runs goes blind. A self-test that does not exercise
// the real code path is decoration.
//
// 🔴 aihub#409 widened it twice, because matching only the literal call shape
// meant the gate could be satisfied by rewriting rather than by fixing. Both
// escapes are one keystroke from the forbidden form: `const wiPrefix = "wi_"`
// followed by strings.HasPrefix(s, wiPrefix), and strings.CutPrefix, which
// answers the same question and has been in the standard library since Go 1.20.
// A gate that a rename walks past is not a gate. The needle is resolved through
// file-level `const`/`var` declarations ONLY — no cross-file or cross-package
// resolution, because this detector does not type-check — so a prefix constant
// declared in another file is still invisible, which
// TestPrefixDispatchGateStillSeesTheIdiom records as a known limit rather than
// leaving it to be discovered.
func findPrefixDispatches(src []byte, name string) ([]int, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	consts := stringConstsIn(file)
	var lines []int
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !prefixDispatchFuncs[sel.Sel.Name] {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
			return true
		}
		if needleValue(call.Args[1], consts) == "wi_" {
			lines = append(lines, fset.Position(call.Pos()).Line)
		}
		return true
	})
	return lines, nil
}

// needleValue resolves the prefix argument to its string value: a literal
// directly, or an identifier bound to a file-level string constant.
//
// Returning "" for anything it cannot resolve is correct rather than lazy —
// "" is not "wi_", so an unresolvable needle is simply not reported, and the
// gate stays a statement about what it can see.
func needleValue(arg ast.Expr, consts map[string]string) string {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind != token.STRING {
			return ""
		}
		return strings.Trim(a.Value, "`\"")
	case *ast.Ident:
		return consts[a.Name]
	}
	return ""
}

// stringConstsIn maps file-level const/var names to their string literal value.
//
// Only untyped single-literal bindings are collected: the purpose is to see
// through `const wiPrefix = "wi_"`, not to evaluate expressions. A concatenation
// or a function call is left unresolved, which the comment on needleValue
// explains is a non-report rather than a wrong report.
func stringConstsIn(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, nm := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[nm.Name] = strings.Trim(lit.Value, "`\"")
			}
		}
	}
	return out
}

// countPrefixDispatches is the self-test's view of the ONE detector above.
func countPrefixDispatches(t *testing.T, src string) int {
	t.Helper()
	lines, err := findPrefixDispatches([]byte(src), "fixture.go")
	if err != nil {
		t.Fatalf("%v", err)
	}
	return len(lines)
}

// repoRootForPolicy walks up to the module root. Named apart from any other
// helper so this file stays readable on its own.
func repoRootForPolicy(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.mod above the test's working directory; this gate cannot have run")
	return ""
}

// nonTestGoFiles lists the repo's non-test .go files.
func nonTestGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
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
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// TestPrefixDispatchGateSeesTheIdiomWrittenOtherWays is aihub#409's arm.
//
// The detector used to match one syntactic shape: `strings.HasPrefix(x, "wi_")`
// with the prefix written as a literal, right there in the call. Two rewrites
// one keystroke away from that form walked past it, and a structural gate that
// a rename defeats is not a gate — it is a spelling test that reports the repo
// clean while the forbidden decision is still being made.
//
// Each positive row below is a shape the detector MUST fire on and did not
// before this change. Each negative row is an exempt target that must SURVIVE
// the widening: without them, "fires on everything" would satisfy every
// positive row here, and the resulting noise is what gets a gate deleted rather
// than obeyed.
func TestPrefixDispatchGateSeesTheIdiomWrittenOtherWays(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{
			// The whole prefix-dispatch decision, with the needle hoisted into a
			// constant. Identical behaviour, invisible to a literal-only matcher.
			name: "HasPrefix with a const needle",
			src: `package p

import "strings"

const wiPrefix = "wi_"

func resolve(s string) string {
	if strings.HasPrefix(s, wiPrefix) {
		return "id"
	}
	return "slug"
}
`,
			want: 1,
		},
		{
			// CutPrefix answers exactly the same question and has been in the
			// standard library since Go 1.20, so it is the likeliest way for this
			// idiom to come back.
			name: "CutPrefix with a literal",
			src: `package p

import "strings"

func resolve(s string) string {
	if _, ok := strings.CutPrefix(s, "wi_"); ok {
		return "id"
	}
	return "slug"
}
`,
			want: 1,
		},
		{
			name: "CutPrefix with a const needle",
			src: `package p

import "strings"

var wiPrefix = "wi_"

func resolve(s string) string {
	if rest, ok := strings.CutPrefix(s, wiPrefix); ok {
		return rest
	}
	return s
}
`,
			want: 1,
		},
		{
			// NEGATIVE CONTROL. TrimPrefix returns the remainder rather than a
			// verdict about the prefix, which is normalisation and legitimate.
			// Firing here would make the gate report correct code, and the
			// cheapest response to a gate that cries wolf is to delete it.
			name: "TrimPrefix is normalisation, not dispatch",
			src: `package p

import "strings"

func normalise(s string) string {
	return strings.TrimPrefix(s, "wi_")
}
`,
			want: 0,
		},
		{
			// NEGATIVE CONTROL. The const path must resolve the VALUE, not merely
			// notice that a constant was passed — otherwise every prefix test in
			// the repo becomes a violation. `ra_` and `mem_` are tested
			// legitimately elsewhere here.
			name: "const needle holding an unrelated prefix",
			src: `package p

import "strings"

const attemptPrefix = "ra_"

func f(s string) bool {
	return strings.HasPrefix(s, attemptPrefix)
}
`,
			want: 0,
		},
		{
			// NEGATIVE CONTROL for the resolver's own limit, stated in
			// findPrefixDispatches' comment: the needle is resolved through
			// FILE-level declarations only. A constant defined in another file is
			// unresolvable, and an unresolvable needle is a non-report rather
			// than a wrong report. Asserted so the comment and the behaviour
			// cannot drift, and so anyone who later adds package-wide resolution
			// is told this paragraph must change with it.
			name: "needle from another file stays invisible",
			src: `package p

import "strings"

func f(s string) bool {
	return strings.HasPrefix(s, wiPrefixDefinedElsewhere)
}
`,
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countPrefixDispatches(t, tc.src); got != tc.want {
				t.Errorf("detector reported %d prefix dispatch(es), want %d.\n%s", got, tc.want, tc.src)
			}
		})
	}
}
