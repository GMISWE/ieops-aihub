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

// findPrefixDispatches is THE detector — the single copy. It reports the line
// of every `strings.HasPrefix(<expr>, "wi_")` in src.
//
// 🔴 One function, called by both the repo-wide gate and its self-test below.
// An earlier draft of this file had the walk written out twice, once in each,
// which is the mistake this comment exists to prevent: the self-test then
// measures a COPY of the detector, so the copy can keep passing while the
// detector the gate actually runs goes blind. A self-test that does not exercise
// the real code path is decoration.
func findPrefixDispatches(src []byte, name string) ([]int, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	var lines []int
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HasPrefix" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Trim(lit.Value, "`\"") == "wi_" {
			lines = append(lines, fset.Position(call.Pos()).Line)
		}
		return true
	})
	return lines, nil
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
