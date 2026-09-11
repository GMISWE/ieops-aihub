package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// aihub#222: file_scope lock keys must be namespaced by project so that
// byte-identical relative paths in different projects (a fork repo and its
// parent) do not share a lock key and hard-block each other. git_branch and
// deploy_env keys must stay unaffected by project.

func TestResourceToLock_FileScopeNamespacedByProject(t *testing.T) {
	lt, lk := resourceToLock(DeclaredResourceItem{Type: "path", URI: "file:internal/domain/engine.go"}, "aihub")
	if lt != "file_scope" {
		t.Fatalf("lockType = %q, want file_scope", lt)
	}
	if want := "aihub:internal/domain/engine.go"; lk != want {
		t.Errorf("file_scope key = %q, want %q", lk, want)
	}
}

// The core regression: the same relative path in two different projects must
// produce DIFFERENT lock keys, so a fork repo's wi can no longer hard-block the
// parent repo's wi over an identical path (the global-routing#1 / ieops#215
// incident).
func TestResourceToLock_SamePathDifferentProjectsDontCollide(t *testing.T) {
	res := DeclaredResourceItem{Type: "path", URI: "file:pkg/gateway/engine.go"}
	_, keyParent := resourceToLock(res, "ieops")
	_, keyFork := resourceToLock(res, "global-routing")
	if keyParent == keyFork {
		t.Fatalf("cross-project keys collided: both %q — fork would still hard-block parent", keyParent)
	}
}

// Same path within the SAME project must still produce the SAME key so two wi's
// touching one file still conflict (no over-loosening).
func TestResourceToLock_SamePathSameProjectStillCollides(t *testing.T) {
	res := DeclaredResourceItem{Type: "path", URI: "file:internal/domain/x.go"}
	_, k1 := resourceToLock(res, "aihub")
	_, k2 := resourceToLock(res, "aihub")
	if k1 != k2 {
		t.Fatalf("same project/path produced different keys %q vs %q", k1, k2)
	}
}

// aihub#416 retired what this test used to assert.
//
// It was TestResourceToLock_BranchAndEnvKeysUnaffectedByProject, and it pinned
// the KEY FORMATS of the two derivations that no longer happen: that a repo
// entry keyed "<repo>/<branch>" ignoring the project, and that a service entry
// keyed the bare service name so cross-project deploys to one environment still
// collided. Both statements are now false, and the second one's REASON — that
// two projects deploying to one environment must conflict — is precisely what
// the de-locking ruling withdrew: exclusion is replaced by the observer's
// generation check, and deploy exclusion moves to the runbook.
//
// It is replaced rather than deleted, because the property it protected still
// exists in the neighbourhood: `project` must not leak into a key it does not
// belong in. That question now has only one type to ask it of, and
// TestFileScopeLockKey_Shape below already pins the answer — so what is kept
// here is the RETIREMENT itself, phrased so this file cannot silently regain a
// project-namespacing bug on a resurrected type.
//
// The behavioural arm lives in lock_derivation_retired_test.go; this one is the
// note that stops a reader of THIS file concluding the old formats still hold.
func TestResourceToLock_NoProjectSensitiveKeyOutsideFileScope(t *testing.T) {
	// Every declared type, both projects, one assertion: the only type that may
	// produce a key at all is file_scope, and it is the only one whose key may
	// differ between projects.
	for _, typ := range []string{"repo", "service", "external_ref"} {
		res := DeclaredResourceItem{Type: typ, URI: typ + ":x", TaskBranch: "polyforge/x"}
		for _, project := range []string{"ieops", "global-routing"} {
			if lt, lk := resourceToLock(res, project); lt != "" || lk != "" {
				t.Errorf("resourceToLock(%s, project=%q) = (%q, %q), want no lock — aihub#416 retired "+
					"the git_branch and deploy_env derivations, so no key format survives to be "+
					"project-sensitive or not", typ, project, lt, lk)
			}
		}
	}
}

// fileScopeLockKey is the single source of the file_scope key shape. Both forms
// are pinned here: the unqualified one is what every row written before
// aihub#261 contains, and the whole no-migration argument rests on the new code
// re-deriving it byte-for-byte from a declaration with no repo.
func TestFileScopeLockKey_Shape(t *testing.T) {
	if got := fileScopeLockKey("aihub", "", "file:a/b.go"); got != "aihub:a/b.go" {
		t.Errorf("fileScopeLockKey(no repo) = %q, want %q", got, "aihub:a/b.go")
	}
	if got := fileScopeLockKey("aihub", "ieops-core", "file:a/b.go"); got != "aihub:ieops-core:a/b.go" {
		t.Errorf("fileScopeLockKey(repo) = %q, want %q", got, "aihub:ieops-core:a/b.go")
	}
}

// aihub#511: no PredictConflicts containment operand may be a JSON literal
// spliced together in Go.
//
// Why a SOURCE-level arm, next to DB arms that already prove the four rules
// behave: the concatenated shape is a REGRESSION, and it has appeared twice.
// Rules 4 and 5 carried it from the start, and rule 2 — which bound its repo
// name as $1 in `resource_key LIKE $1 || '/%'` — had it re-introduced when
// aihub#416 rewrote that query into a containment test. The DB arms in
// delocking_db_test.go (declared names that look like json...) say the rules
// that exist today are right; this one fails the moment another query is
// written the old way, with no database and no fixture, which is what makes it
// worth its oddity.
//
// aihub#549 replaced the original line-regexp over conflicts.go with an AST
// walk over the whole package, because the original had two tested escape
// routes and disclosed only one of them: it read ONE FILE by name, so the same
// operand written into a new file was invisible; and it matched ONE SHAPE
// verbatim, so flipping the key order, adding whitespace inside the literal, or
// assembling the operand through fmt.Sprintf(`…%q…`) all passed. The detector
// now judges normalized string literals (whitespace stripped, `+`-chains joined
// so a fragment split cannot hide the opener from the keys) in every non-test
// file of THIS PACKAGE.
//
// The binding that remains, disclosed rather than hidden: a containment query
// moved into another package escapes this walk. The queries the rule governs
// are the ones PredictConflicts and its helpers run, and they live here — if
// they ever move, this arm has to move with them.
//
// The detector deliberately does NOT demand jsonb_build_object: building the
// operand with json.Marshal and binding it is equally safe, and a guard that
// outlawed the alternative would be enforcing a preference rather than the rule.
// What it catches is the assembly of the literal itself.

// containmentOperandShape is the normalized signature of a declared_resources
// operand written as JSON text in Go: an array-of-objects opener followed by
// either of the two keys every entry carries. One key rather than both,
// because the aihub#549 evasions reorder the keys or split them across
// concatenation fragments — demanding the pair in one literal is exactly the
// single-shape binding this arm used to have. Anchoring on the opener keeps it
// describing this payload rather than JSON in general: the package's one
// legitimate entry-shaped literal (declared_resources.go's `entry_shape` error
// example) is a bare object with no `[{`.
var containmentOperandShape = regexp.MustCompile(`\[\{"(type|uri)":`)

// concatenatedOperandFindings parses one Go source (filename with src == nil,
// or an in-memory fixture) and reports every string literal or `+`-chain whose
// normalized text carries the operand signature. AST rather than raw lines so
// comments are invisible by construction — the doc comment on
// declaresContainmentSQL quotes the broken operand verbatim, and the record of
// what the bug looked like is worth keeping.
func concatenatedOperandFindings(t *testing.T, filename string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — this guard is structural, so it has to be re-pointed when the "+
			"code moves rather than deleted", filename, err)
	}

	// A `a + b + c` chain parses as nested BinaryExprs; only the outermost one
	// should be judged as a joined text, so mark the inner ones.
	innerAdd := map[ast.Expr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			for _, side := range []ast.Expr{b.X, b.Y} {
				if sb, ok := side.(*ast.BinaryExpr); ok && sb.Op == token.ADD {
					innerAdd[sb] = true
				}
			}
		}
		return true
	})

	seen := map[int]bool{}
	var findings []string
	record := func(pos token.Pos, text string) {
		m := containmentOperandShape.FindString(text)
		if m == "" {
			return
		}
		line := fset.Position(pos).Line
		if seen[line] {
			return
		}
		seen[line] = true
		findings = append(findings, filename+":"+strconv.Itoa(line)+" ("+m+"…)")
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				record(v.Pos(), normalizedStringLit(v))
			}
		case *ast.BinaryExpr:
			if v.Op == token.ADD && !innerAdd[v] {
				record(v.Pos(), joinedChainText(v))
			}
		}
		return true
	})
	return findings
}

// normalizedStringLit unquotes a string literal (raw or interpreted) and strips
// every whitespace rune, so `[ { "type" :` and the compact form are one shape.
func normalizedStringLit(lit *ast.BasicLit) string {
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		s = lit.Value
	}
	return strings.Join(strings.Fields(s), "")
}

// joinedChainText flattens a `+`-chain into one normalized text, with a
// placeholder byte for every non-literal operand — so the runtime value spliced
// between two fragments does not hide that the fragments assemble one operand.
func joinedChainText(e ast.Expr) string {
	var b strings.Builder
	var walk func(ast.Expr)
	walk = func(x ast.Expr) {
		switch v := x.(type) {
		case *ast.BinaryExpr:
			if v.Op == token.ADD {
				walk(v.X)
				walk(v.Y)
				return
			}
			b.WriteByte(0)
		case *ast.ParenExpr:
			walk(v.X)
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				b.WriteString(normalizedStringLit(v))
				return
			}
			b.WriteByte(0)
		default:
			b.WriteByte(0)
		}
	}
	walk(e)
	return b.String()
}

func TestPredictContainmentOperandsAreNotConcatenatedJSON(t *testing.T) {
	// Positive controls, and they run FIRST: a detector that fires on nothing
	// reports a clean package exactly the way it reports files it cannot read.
	// The first is the pre-aihub#511 rule 2 operand verbatim; the rest are the
	// aihub#549 evasions, each of which the original single-shape regexp passed.
	controls := []struct{ name, src string }{
		{"the pre-aihub#511 rule 2 operand, verbatim",
			"package p\nvar x = `[{\"type\":\"repo\",\"uri\":\"repo:` + repoName + `\"}]`"},
		{"key order flipped",
			"package p\nvar x = `[{\"uri\":\"repo:` + repoName + `\",\"type\":\"repo\"}]`"},
		{"whitespace inside the literal",
			"package p\nvar x = `[ { \"type\": \"repo\", \"uri\": \"repo:` + repoName + `\" } ]`"},
		{"%q-assembled through Sprintf",
			"package p\nvar x = fmt.Sprintf(`[{\"type\":%q,\"uri\":%q}]`, typ, uri)"},
		{"opener split across fragments",
			"package p\nvar x = \"[\" + \"{\" + `\"type\":\"repo\",\"uri\":\"repo:` + repoName + `\"}]`"},
	}
	for _, c := range controls {
		if len(concatenatedOperandFindings(t, "control.go", c.src)) == 0 {
			t.Fatalf("the detector does not fire on %s — a clean sweep below would be evidence "+
				"about the detector and not about the package", c.name)
		}
	}

	// Negative control: the one legitimate entry-shaped literal in the package
	// (declared_resources.go's `entry_shape` error example) is a bare object
	// with no array opener, and must stay green — a false positive here is not
	// a lesser failure than a miss, it is a faster one (aihub#361).
	if got := concatenatedOperandFindings(t, "negcontrol.go",
		"package p\nvar x = `{\"type\":\"path\",\"uri\":\"file:<repo-relative-path>\",\"intent\":\"write\"}`"); len(got) != 0 {
		t.Fatalf("the detector fires on the bare entry-shape example literal (%v), which is correct "+
			"as written in declared_resources.go — it would be red on a clean tree", got)
	}

	// The sweep: every non-test file in this package, so an operand written the
	// old way into a NEW file is no longer invisible.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	sawConflictsGo := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "conflicts.go" {
			sawConflictsGo = true
		}
		for _, finding := range concatenatedOperandFindings(t, name, nil) {
			t.Errorf("%s builds a declared_resources operand as JSON text in Go source: "+
				"pass the declared name as a bound PARAMETER instead (declaresContainmentSQL / "+
				"declaresIntentContainmentSQL, or json.Marshal + $n). A `\"` in a repo or service name "+
				"makes this a 22P02 the caller never sees, and a `\\b` makes it valid json for a "+
				"DIFFERENT string with nothing logged at all — both answer "+
				`{"predictions":[],"severity":"info"}, which is byte-identical to a real all-clear`,
				finding)
		}
	}
	if !sawConflictsGo {
		t.Error("the walk never saw conflicts.go — the file the rule was written about is gone or " +
			"renamed, so a clean sweep above says nothing; re-point this arm at wherever the " +
			"containment queries live now")
	}
}
