// Command dbtestcov fails the build when a DB-gated test function exists that
// no CI step actually executes.
//
// # Why this exists (aihub#303)
//
// ci.yml's "Unit tests" step deliberately does NOT set AIHUB_TEST_DB, so every
// test guarded on that variable SKIPs there. The real DB coverage is the union
// of the `-run` regexes on the handful of steps that DO set it. A test whose
// name falls outside that union never runs anywhere — and `go test` prints "ok"
// and exits 0 when everything it selected SKIPped, so nothing goes red. That is
// how 28 of the 83 DB-gated test functions that need only a database (85 in
// total, less the 2 that also need EMBEDDING_BASE_URL) came to be invisible to
// CI while every step stayed green.
//
// # How it decides what is "DB-gated"
//
// From behaviour, not from source structure. The inventory is a
// `go test ./... -json` run taken with AIHUB_TEST_DB *unset*, and a test
// function is DB-gated iff it SKIPped and the message it passed to t.Skip names
// AIHUB_TEST_DB.
//
// Be precise about what that does and does not buy. It means there is no list
// of guard helpers to keep in sync: a test is classified by what it did. It
// does NOT mean the classification is free of a convention. The convention is
// the skip MESSAGE, and wording it wrongly is the cheapest way to make this
// gate go quiet — cheaper than fixing the coverage: a DB test that skips with
// `t.Skip("no test database")` never enters the required set, and -min-gated
// cannot see it because it was never counted.
//
// checkSkipMessages closes that door by auditing the SOURCE, and it has to be
// robust rather than merely present. Ten separate shapes silenced it while
// leaving the gated count UNCHANGED at 85 — measured, with the first eight
// written into this repository at once: an env read hidden in a bool-returning
// helper, os.LookupEnv instead of os.Getenv, a constant or concatenated
// variable name, a bare `return` instead of a skip, a TestMain gate, a build
// tag, and any of those living in a non-test file. A second review added two
// more, both one edit from the first: the same read in a func literal bound to
// a package-level var, and in a plain `var dsn = os.Getenv(...)` — neither is
// an *ast.FuncDecl, so walking only function bodies missed both.
//
// Note the common property of all ten: none of them moves the gated count, so
// raising -min-gated could never have caught any of them. See
// checkSkipMessages for what closes each one.
//
// A test whose skip message names a second environment variable (e.g.
// EMBEDDING_BASE_URL) needs more than a database, so CI cannot run it: it is
// excluded from the required set, and — symmetrically — it is a failure to name
// it in a DB step's `-run`, because it would SKIP there and trip that step's own
// anti-silent-SKIP guard. Both halves are derived from the test's own skip
// message, so dropping the extra requirement automatically makes the test
// required again. An extra variable that is not in knownExtraEnv is an ERROR
// rather than a silent exclusion: quietly dropping a test out of the required
// set is the exact failure this command exists to prevent.
//
// # How it decides what CI covers
//
// It parses ci.yml, finds every step that sets AIHUB_TEST_DB, and applies
// `go test`'s own `-run` semantics (see SplitRunPattern) to the test names in
// the packages that step names. A `go test` invocation with no `-run` covers
// its whole package.
//
// The hazard on this side is crediting coverage for a `go test` that CI does
// not actually execute, or whose failure does not actually fail the step. So
// the parser models shell structure rather than searching lines, and every
// construct it cannot model is a hard error rather than a silent
// approximation:
//
//   - `-skip`, `if:` and `continue-on-error:` are rejected (unmodellable, or
//     conditional on the triggering event);
//   - `-count=0` is rejected: it selects zero tests, so the invocation exits 0
//     with no SKIP line — it defeats the inventory and the guard at once;
//   - a repeated `-run` is rejected, because `go test` obeys the LAST one while
//     a reader sees the first;
//   - a `go test` inside a quoted string, inside an `if`/`for`/`while`/`case`
//     body, after `&&`/`||`, negated with `!`, or backgrounded with `&` is
//     rejected. The quoted case is the sharpest: one `echo` can carry the
//     invocation, the tee target AND the SKIP marker, so in prose it both
//     credits coverage and satisfies the guard meant to prove it ran.
//
// Every DB `go test` invocation must also tee its output to a log AND be
// followed by a guard grepping THAT log for "--- SKIP". Coverage that is merely
// named by a `-run` is nominal; the guard is what makes it real — so the guard
// is matched against an allowlist of spellings (see skipGuardRE) rather than
// searched for, because `grep -q ... || exit 1` (inverted: it REQUIRES a skip)
// and `... || true` (can never fail) both mention the marker while asserting
// the opposite of, or nothing about, what the guard is for.
//
// Usage:
//
//	go test ./... -count=1 -json > inv.json     # with AIHUB_TEST_DB UNSET
//	go run ./internal/citest/dbtestcov -inventory inv.json -workflow .github/workflows/ci.yml
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// dbEnvVar is the variable whose presence switches the DB-backed suites on.
const dbEnvVar = "AIHUB_TEST_DB"

// skipGuardMarker is the string a DB step's script must grep for. Every DB step
// in ci.yml runs `go test -v` and then asserts `! grep -q -- '--- SKIP' <log>`;
// without that assertion the step passes when the tests it names all SKIPped,
// which is exactly the failure this command exists to prevent.
const skipGuardMarker = "--- SKIP"

// knownExtraEnv lists the additional environment variables a DB-gated test is
// allowed to require. A test requiring one of these cannot run in CI, so it is
// excluded from the required set — which is a real hole, deliberately opened.
// Anything else found in a skip message is an error, so that the hole can only
// be widened on purpose.
var knownExtraEnv = map[string]bool{
	"EMBEDDING_BASE_URL": true,
}

// GatedTest is one top-level test function that SKIPped for want of a database.
type GatedTest struct {
	Package string
	Name    string
	// ExtraEnv lists environment variables other than AIHUB_TEST_DB that the
	// test's own skip message asks for. Non-empty means CI cannot run it.
	ExtraEnv []string
}

// Invocation is one `go test` command found inside a step that sets AIHUB_TEST_DB.
type Invocation struct {
	Step string
	// Packages holds the import paths (or prefixes, for `./...` patterns) the
	// invocation selects.
	Packages []PackageSel
	// Run is the -run argument, or "" when the invocation has none (which
	// selects every test in the packages).
	Run string
	// Log is the file the invocation tees its -v output to, or "" if none.
	Log string
}

// PackageSel is a resolved package argument: either an exact import path or a
// prefix produced by a `...` wildcard.
type PackageSel struct {
	Path     string
	Wildcard bool
}

// Matches reports whether the given import path is selected.
func (p PackageSel) Matches(importPath string) bool {
	if p.Wildcard {
		return importPath == strings.TrimSuffix(p.Path, "/") || strings.HasPrefix(importPath, p.Path)
	}
	return importPath == p.Path
}

// WorkflowScan is everything the workflow parse learned.
type WorkflowScan struct {
	Invocations []Invocation
	// Unguarded describes DB `go test` invocations whose SKIPs nothing asserts on.
	Unguarded []string
	// Dropped describes `go test` lines inside a DB step whose package
	// arguments could not be resolved, so they credit no coverage. Silently
	// dropping them would under-credit (safe) but invisibly (not safe).
	Dropped []string
}

func main() {
	inventory := flag.String("inventory", "", "path to a `go test ./... -json` run taken with "+dbEnvVar+" UNSET")
	workflow := flag.String("workflow", ".github/workflows/ci.yml", "path to the CI workflow to audit")
	gomod := flag.String("gomod", "go.mod", "path to go.mod, read for the module path")
	sourceRoot := flag.String("source-root", ".", "repository root, walked for .go files to audit the "+dbEnvVar+" skip-guard convention")
	minGated := flag.Int("min-gated", 1, "ratchet floor: fail if the inventory holds fewer DB-gated tests than this. Raise it when you add DB tests; it is what catches an inventory that stopped enumerating (for example because "+dbEnvVar+" leaked into the run that produced it, making everything pass instead of skip). It canNOT substitute for the source audit in checkSkipMessages: every known way of silencing a DB guard leaves this count unchanged.")
	flag.Parse()

	if *inventory == "" {
		fmt.Fprintln(os.Stderr, "dbtestcov: -inventory is required")
		os.Exit(2)
	}

	if err := run(*inventory, *workflow, *gomod, *sourceRoot, *minGated, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(1)
	}
}

func run(inventoryPath, workflowPath, gomodPath, sourceRoot string, minGated int, out io.Writer) error {
	module, err := readModulePath(gomodPath)
	if err != nil {
		return err
	}

	invFile, err := os.Open(inventoryPath) // #nosec G304 -- path is a CI-controlled flag
	if err != nil {
		return fmt.Errorf("open inventory: %w", err)
	}
	defer func() { _ = invFile.Close() }()

	gated, err := ParseInventory(invFile)
	if err != nil {
		return fmt.Errorf("parse inventory %s: %w", inventoryPath, err)
	}
	if len(gated) < minGated {
		return fmt.Errorf(
			"inventory %s holds only %d %s-gated test functions, floor is %d — the inventory run is not measuring what it should. "+
				"The usual cause is %s being set for the run that produced it: everything then PASSes instead of SKIPping and this gate "+
				"passes vacuously. Re-take it with `env -u %s go test ./... -count=1 -json`",
			inventoryPath, len(gated), dbEnvVar, minGated, dbEnvVar, dbEnvVar)
	}

	wfData, err := os.ReadFile(workflowPath) // #nosec G304 -- path is a CI-controlled flag
	if err != nil {
		return fmt.Errorf("read workflow: %w", err)
	}
	scan, err := ParseWorkflow(wfData, module)
	if err != nil {
		return fmt.Errorf("parse workflow %s: %w", workflowPath, err)
	}

	var required, extraEnv []GatedTest
	var unknownEnv []string
	for _, g := range gated {
		if len(g.ExtraEnv) > 0 {
			for _, v := range g.ExtraEnv {
				if !knownExtraEnv[v] {
					unknownEnv = append(unknownEnv,
						fmt.Sprintf("%s.%s names %q in its skip message", shortPkg(g.Package), g.Name, v))
				}
			}
			extraEnv = append(extraEnv, g)
			continue
		}
		required = append(required, g)
	}

	var missing []GatedTest
	for _, g := range required {
		covered, err := coveredBy(scan.Invocations, g)
		if err != nil {
			return err
		}
		if !covered {
			missing = append(missing, g)
		}
	}

	// Symmetric half: a test that needs more than a database must NOT be named
	// by a DB step, because it would SKIP there and trip that step's own
	// `! grep -q -- '--- SKIP'` guard — turning an unrunnable test into a
	// confusing red rather than an explicit exclusion.
	var wronglyCovered []GatedTest
	for _, g := range extraEnv {
		covered, err := coveredBy(scan.Invocations, g)
		if err != nil {
			return err
		}
		if covered {
			wronglyCovered = append(wronglyCovered, g)
		}
	}

	convictions, err := checkSkipMessages(sourceRoot)
	if err != nil {
		return err
	}

	report(out, "dbtestcov: %s-gated test functions: %d (require only a DB: %d, need extra env: %d)\n",
		dbEnvVar, len(gated), len(required), len(extraEnv))
	report(out, "dbtestcov: %s go test invocations found in %s: %d\n", dbEnvVar, workflowPath, len(scan.Invocations))
	for _, g := range extraEnv {
		report(out, "dbtestcov: excluded (needs %s as well as %s): %s.%s\n",
			strings.Join(g.ExtraEnv, ", "), dbEnvVar, shortPkg(g.Package), g.Name)
	}
	report(out, "dbtestcov: covered by a CI step: %d/%d\n", len(required)-len(missing), len(required))

	var problems []string
	if len(missing) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d %s-gated test function(s) are executed by NO CI step, so they SKIP forever and CI stays green:",
			len(missing), dbEnvVar)
		for _, g := range missing {
			fmt.Fprintf(&b, "\n    %s\t%s", shortPkg(g.Package), g.Name)
		}
		b.WriteString("\n  Fix: name them in the -run of a ci.yml step that sets " + dbEnvVar +
			" (with a `--- PASS:` assertion and the `! grep -q -- '--- SKIP'` guard, like the steps already there).")
		problems = append(problems, b.String())
	}
	if len(wronglyCovered) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d test(s) need more than a database yet are named by a CI step's -run; they will SKIP there and trip that step's own SKIP guard:", len(wronglyCovered))
		for _, g := range wronglyCovered {
			fmt.Fprintf(&b, "\n    %s\t%s\t(also needs %s)", shortPkg(g.Package), g.Name, strings.Join(g.ExtraEnv, ", "))
		}
		problems = append(problems, b.String())
	}
	if len(unknownEnv) > 0 {
		var b strings.Builder
		b.WriteString("a skip message names an environment variable this command does not know about, so the test would be silently dropped from the required set:")
		for _, s := range unknownEnv {
			fmt.Fprintf(&b, "\n    %s", s)
		}
		b.WriteString("\n  Fix: if it really is an extra requirement CI cannot satisfy, add it to knownExtraEnv deliberately; otherwise reword the skip message so it names only " + dbEnvVar + ".")
		problems = append(problems, b.String())
	}
	if len(scan.Unguarded) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d DB `go test` invocation(s) have no working %q guard, so they pass when every test they select SKIPs:", len(scan.Unguarded), skipGuardMarker)
		for _, s := range scan.Unguarded {
			fmt.Fprintf(&b, "\n    %s", s)
		}
		problems = append(problems, b.String())
	}
	if len(scan.Dropped) > 0 {
		var b strings.Builder
		b.WriteString("a DB step runs `go test` in a form this command cannot resolve to packages, so its coverage is not counted:")
		for _, s := range scan.Dropped {
			fmt.Fprintf(&b, "\n    %s", s)
		}
		problems = append(problems, b.String())
	}
	if len(convictions) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d skip guard(s) read %s but do not name it in the message they skip with, so the test they guard would not be classified as DB-gated at all:", len(convictions), dbEnvVar)
		for _, s := range convictions {
			fmt.Fprintf(&b, "\n    %s", s)
		}
		problems = append(problems, b.String())
	}

	if len(problems) > 0 {
		return fmt.Errorf("dbtestcov: %s", strings.Join(problems, "\n  "))
	}
	report(out, "dbtestcov: OK — every DB-gated test function is executed by a CI step.\n")
	return nil
}

// report writes a progress line. A failed write to the CI log is not worth
// aborting the audit over, and the verdict travels in the exit code, not here.
func report(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

func coveredBy(invocations []Invocation, g GatedTest) (bool, error) {
	for _, inv := range invocations {
		selected := false
		for _, p := range inv.Packages {
			if p.Matches(g.Package) {
				selected = true
				break
			}
		}
		if !selected {
			continue
		}
		if inv.Run == "" {
			return true, nil
		}
		ok, err := MatchesRun(inv.Run, g.Name)
		if err != nil {
			return false, fmt.Errorf("step %q: -run %q: %w", inv.Step, inv.Run, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func shortPkg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ---------------------------------------------------------------- inventory

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

// envVarRE matches shell-style environment variable names: all-caps with at
// least one underscore. Test function names are mixed case, so they cannot
// match, but skip-header lines are dropped anyway before this is applied.
var envVarRE = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`)

// ParseInventory reads a `go test -json` stream and returns every top-level
// test function that SKIPped with a message naming AIHUB_TEST_DB.
//
// Subtests are ignored: `-run` selection is decided by the top-level name, and
// a subtest cannot be gated independently of the function that owns it.
func ParseInventory(r io.Reader) ([]GatedTest, error) {
	type key struct{ pkg, test string }
	actions := map[key]string{}
	outputs := map[key]*strings.Builder{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// A build failure prints plain text on the same stream; skip it
			// here — an empty inventory is caught by the -min-gated floor.
			continue
		}
		if ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue
		}
		k := key{ev.Package, ev.Test}
		switch ev.Action {
		case "output":
			b, ok := outputs[k]
			if !ok {
				b = &strings.Builder{}
				outputs[k] = b
			}
			b.WriteString(ev.Output)
		case "pass", "fail", "skip":
			actions[k] = ev.Action
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	var gated []GatedTest
	for k, action := range actions {
		if action != "skip" {
			continue
		}
		b, ok := outputs[k]
		if !ok {
			continue
		}
		reason := skipReason(b.String())
		if !strings.Contains(reason, dbEnvVar) {
			continue
		}
		var extra []string
		for _, v := range envVarRE.FindAllString(reason, -1) {
			if v != dbEnvVar && !contains(extra, v) {
				extra = append(extra, v)
			}
		}
		sort.Strings(extra)
		gated = append(gated, GatedTest{Package: k.pkg, Name: k.test, ExtraEnv: extra})
	}
	sort.Slice(gated, func(i, j int) bool {
		if gated[i].Package != gated[j].Package {
			return gated[i].Package < gated[j].Package
		}
		return gated[i].Name < gated[j].Name
	})
	return gated, nil
}

// skipReason strips go test's own framing lines ("=== RUN   TestX",
// "--- SKIP: TestX (0.00s)") so that only the message the test itself passed to
// t.Skip is left. Without this, a test *name* could be mistaken for an
// environment variable.
func skipReason(out string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "=== ") || strings.HasPrefix(t, "--- ") {
			continue
		}
		keep = append(keep, t)
	}
	return strings.Join(keep, "\n")
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ------------------------------------------------- skip-message convention

var skipFuncNames = map[string]bool{"Skip": true, "Skipf": true, "SkipNow": true}

// envReadFuncs are the selector names through which Go code reads the process
// environment. Matching on the selector alone — not on the package it came
// from — is deliberate: an aliased import, a dot-import or a same-named local
// wrapper all still read the environment, and this check's whole job is to be
// hard to walk around. `Environ` names no single variable, so it only matters
// together with the scope-text check in dbGuardsIn.
var envReadFuncs = map[string]bool{
	"Getenv":    true, // os.Getenv, syscall.Getenv
	"LookupEnv": true, // os.LookupEnv
	"Environ":   true, // os.Environ, syscall.Environ — reads all of it
}

// checkSkipMessages enforces the convention ParseInventory depends on: a
// function whose behaviour depends on AIHUB_TEST_DB must SKIP, and must name
// AIHUB_TEST_DB in every message it skips with. Otherwise the test it guards
// stops running for want of a database and this command never learns that it
// did — the original defect, one level up, and the cheapest possible way to
// make this gate go quiet.
//
// It has to be robust rather than merely present, because silencing the gate
// must not be cheaper than complying with it. Every one of these shapes used to
// be invisible, all of them leaving the gated count unchanged — so no
// -min-gated floor could ever have caught them:
//
//	if !haveDB() { t.Skip("no db") }         // the env read is in a helper
//	os.LookupEnv(dbEnvVar)                   // not os.Getenv
//	os.Getenv(dbEnvConst)                    // argument is an identifier
//	os.Getenv("AIHUB_TEST" + "_DB")          // argument is a concatenation
//	if os.Getenv(...) == "" { return }       // returns instead of skipping
//	func TestMain(m) { ...; os.Exit(0) }     // gates the whole package
//	//go:build dbtest                        // excluded from the default build
//	// ...and any of the above in helper.go rather than helper_test.go
//
// What closes them:
//
//   - ALL .go files in a package are scanned, not just _test.go, because a
//     guard helper is just as effective from a non-test file;
//   - the prefilter is per PACKAGE, not per file, so a constant declared in one
//     file cannot hide a use of it in another;
//   - the env reader's ARGUMENT is constant-folded (literals, package-level
//     string constants and `+` concatenations of them) rather than
//     pattern-matched, and an argument that cannot be folded is itself a
//     violation — otherwise "make the name unfoldable" becomes the cheap way
//     out;
//   - a function whose scope reads the variable and never reaches a Skip call
//     is a violation, which is what catches both the bool-returning helper and
//     the bare `return`;
//   - a read that is not inside ANY function body — a package-level var, with
//     or without a func literal around it — is a violation unconditionally,
//     because a declaration has no way to skip;
//   - constants are folded per PACKAGE, not per directory: one directory holds
//     both `package p` and `package p_test`, and folding across it let one
//     package's constant silently supply the other's value;
//   - any build constraint on a _test.go file in such a package is a violation,
//     because the inventory is a default-build `go test ./...` run.
//
// The residual hole is a guard that reads the variable through a name this
// command cannot fold AND lives in a package that never mentions the variable
// at all. Getting there costs more than writing a correct skip message, which
// is the property that matters.
func checkSkipMessages(root string) ([]string, error) {
	pkgs, err := goPackages(root)
	if err != nil {
		return nil, err
	}
	var violations []string
	fset := token.NewFileSet()
	dirs := make([]string, 0, len(pkgs))
	for dir := range pkgs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		files, err := readGoFiles(pkgs[dir])
		if err != nil {
			return nil, err
		}
		// Package-granularity prefilter. Per FILE (as this used to be) a
		// `const dbEnv = "AIHUB_TEST_DB"` in one file hides every use of it in
		// the others, and the argument check below never runs at all.
		mentions := false
		for _, f := range files {
			if strings.Contains(string(f.src), dbEnvVar) {
				mentions = true
				break
			}
		}
		if !mentions {
			continue
		}
		for i := range files {
			f := &files[i]
			f.ast, err = parser.ParseFile(fset, f.path, f.src, parser.ParseComments)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", f.path, err)
			}
		}
		// Group by PACKAGE NAME, not just directory: one directory legally
		// holds `package p` and `package p_test`, and both may declare the
		// same bare constant name with different values. Folding across the
		// whole directory let the external test package's value win by sorted
		// filename — a fold that is not merely absent but WRONG, and wrong in
		// the unsafe direction (a real guard reads as a guard on some other
		// variable and is skipped silently).
		byPkg := map[string][]goFile{}
		for _, f := range files {
			byPkg[f.ast.Name.Name] = append(byPkg[f.ast.Name.Name], f)
		}
		for _, pkgFiles := range byPkg {
			violations = append(violations, checkPackage(fset, pkgFiles, packageStringConsts(pkgFiles))...)
		}
	}
	sort.Strings(violations)
	return violations, nil
}

// goFile is one parsed source file plus the bytes it was parsed from, which the
// function-scope text check needs.
type goFile struct {
	path string
	src  []byte
	ast  *ast.File
}

// goPackages returns the .go files under root, grouped by directory. It skips
// what the go tool itself skips — `vendor`, `testdata`, and any directory whose
// name begins with '.' or '_' — so the set of files scanned is the set that
// actually gets compiled.
func goPackages(root string) (map[string][]string, error) {
	pkgs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path == root {
				return nil
			}
			if name == "vendor" || name == "testdata" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			dir := filepath.Dir(path)
			pkgs[dir] = append(pkgs[dir], path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pkgs, nil
}

func readGoFiles(paths []string) ([]goFile, error) {
	sort.Strings(paths)
	files := make([]goFile, 0, len(paths))
	for _, p := range paths {
		src, err := os.ReadFile(p) // #nosec G304 -- walking a CI-controlled root
		if err != nil {
			return nil, err
		}
		files = append(files, goFile{path: p, src: src})
	}
	return files, nil
}

// packageStringConsts collects every package-level string constant (and
// string-literal var) in the package, so that `os.Getenv(dbEnvName)` folds to
// the same thing as `os.Getenv("AIHUB_TEST_DB")`. A var is included because
// naming the variable through one is exactly as invisible as naming it through
// a constant.
func packageStringConsts(files []goFile) map[string]string {
	consts := map[string]string{}
	// Two passes, so that a constant defined in terms of another resolves
	// regardless of declaration order.
	for pass := 0; pass < 2; pass++ {
		for _, f := range files {
			for _, decl := range f.ast.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
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
						if v, ok := constString(vs.Values[i], consts); ok {
							consts[name.Name] = v
						}
					}
				}
			}
		}
	}
	return consts
}

// constString folds an expression to a string when it can, handling the shapes
// an author reaches for when the argument is not a bare literal: an identifier
// bound to a constant, a qualified constant, and `+` concatenation.
func constString(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.SelectorExpr:
		// pkg.Const: the import alias says nothing useful, so resolve on the
		// selector's own name.
		s, ok := consts[v.Sel.Name]
		return s, ok
	case *ast.ParenExpr:
		return constString(v.X, consts)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := constString(v.X, consts)
		r, rok := constString(v.Y, consts)
		if !lok || !rok {
			return "", false
		}
		return l + r, true
	}
	return "", false
}

// funcInfo is what the package-scope pass knows about one function.
type funcInfo struct {
	file *goFile
	decl *ast.FuncDecl
	// guardsDB means the function's behaviour depends on dbEnvVar.
	guardsDB bool
	skips    []*ast.CallExpr
	// namingSkip: it has at least one skip and every one of them names
	// dbEnvVar, i.e. a test stopped here IS classifiable from its output.
	namingSkip bool
	// callees are the same-package function names it calls.
	callees map[string]bool
}

// checkPackage audits one Go package. It is package- rather than file-scoped
// because the two questions that decide a false positive from a real hole —
// "does anything call this helper?" and "does that caller skip properly?" —
// cannot be answered from one file.
func checkPackage(fset *token.FileSet, files []goFile, consts map[string]string) []string {
	var violations []string

	for i := range files {
		f := &files[i]
		// A constrained _test.go file is absent from the default build, so its
		// tests never appear in the inventory at all — the same invisibility as
		// a mis-worded skip message, reached by a different door. Enumerating
		// tags is not attempted; carrying one at all is the violation.
		if strings.HasSuffix(f.path, "_test.go") {
			if c := buildConstraintOf(f.src); c != "" {
				violations = append(violations, fmt.Sprintf(
					"%s:1: carries the build constraint %q, so it is absent from the default build that produces dbtestcov's "+
						"inventory — every test in it is invisible to this gate. Drop the constraint, or teach dbtestcov the tag",
					f.path, c))
			}
		}
		// A package-level declaration can read the environment too, and it
		// cannot possibly skip:
		//
		//	var haveDB = func() bool { return os.Getenv(dbEnvVar) != "" }
		//	var dsn     = os.Getenv(dbEnvVar)
		//
		// Both are one edit away from the bool-returning helper this check is
		// built to catch, and neither is an *ast.FuncDecl.
		for _, r := range envReadsOutsideFuncs(f, consts) {
			pos := fset.Position(r.pos)
			violations = append(violations, fmt.Sprintf(
				"%s:%d: a package-level declaration reads %s; a declaration cannot SKIP, so the tests that consult it are "+
					"never classified as %s-gated. Move the read into the test helper that skips, naming %s in the message",
				pos.Filename, pos.Line, dbEnvVar, dbEnvVar, dbEnvVar))
		}
	}

	// ---- pass 1a: describe every function.
	var funcs []*funcInfo
	callers := map[string][]*funcInfo{}
	scopes := map[*funcInfo]string{}
	readsOf := map[*funcInfo][]envRead{}
	envReaders := map[string]bool{}
	for i := range files {
		f := &files[i]
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fi := &funcInfo{file: f, decl: fn, skips: skipCalls(fn.Body), callees: calleeNames(fn.Body)}
			reads := envReadsIn(fn.Body, consts)
			readsOf[fi] = reads
			scopes[fi] = funcSource(fset, f, fn)
			if len(reads) > 0 {
				envReaders[fn.Name.Name] = true
			}
			fi.namingSkip = len(fi.skips) > 0
			for _, call := range fi.skips {
				msg, hasMsg := skipMessage(call, consts)
				if !hasMsg || !strings.Contains(msg, dbEnvVar) {
					fi.namingSkip = false
				}
			}
			funcs = append(funcs, fi)
		}
	}

	// ---- pass 1b: decide which functions gate on the database, and report the
	// unresolvable-argument hazard.
	for _, fi := range funcs {
		fn := fi.decl
		reads := readsOf[fi]

		// A function gates on the database only if it actually TOUCHES the
		// environment — reads it, or hands the variable's name to a
		// same-package function that reads it. Merely mentioning the name is
		// not enough: internal/domain's mustNotSkip names AIHUB_TEST_DB in a
		// t.Fatalf string to say which database to point at, and this command's
		// own tests carry it inside fixture source strings. Nor is passing it
		// to anything at all enough — that flagged dbtestcov's own
		// fmt.Sprintf("... %s ...", dbEnvVar) error messages. Both were
		// measured as false positives while narrowing this.
		passesName := passesDBEnvToAnEnvReader(fn.Body, consts, envReaders)
		touchesEnv := len(reads) > 0 || passesName
		namesDB := strings.Contains(scopes[fi], dbEnvVar) || passesName
		for _, r := range reads {
			if r.resolved && r.name == dbEnvVar {
				namesDB = true
			}
		}
		fi.guardsDB = touchesEnv && namesDB

		// An env read whose variable name is decided somewhere this command
		// cannot see would make every check below optional. But it is only a
		// hazard in a function that can actually GATE something, so the rule is
		// scoped to functions that skip. Unscoped it rejects an ordinary
		// table-driven env test — a shape that appears in the four most-edited
		// packages in this repo — and a gate that fires on ordinary work is a
		// gate that gets switched off. The shape it existed to close,
		// `envSet(dbEnvConst)` where envSet takes the name as a parameter, is
		// now caught by the caller analysis in pass 2 instead.
		if len(fi.skips) == 0 {
			continue
		}
		for _, r := range reads {
			if !r.resolved && !r.wildcard {
				pos := fset.Position(r.pos)
				violations = append(violations, fmt.Sprintf(
					"%s:%d: %s reads an environment variable whose name dbtestcov cannot determine statically AND skips, so "+
						"this may be a %s guard the gate cannot see. Name the variable with a string literal or a "+
						"package-level string constant",
					pos.Filename, pos.Line, fn.Name.Name, dbEnvVar))
			}
		}
	}

	for _, fi := range funcs {
		for callee := range fi.callees {
			callers[callee] = append(callers[callee], fi)
		}
	}

	// ---- pass 2: the verdicts.
	reaches := namingSkipReachability(funcs, callers)
	for _, fi := range funcs {
		if !fi.guardsDB {
			continue
		}
		fn := fi.decl
		pos := fset.Position(fn.Pos())

		// TestMain cannot skip: it either runs the package's tests or it does
		// not. Gating it on the database makes every test in the package vanish
		// from the inventory while `go test` still exits 0.
		if fn.Name.Name == "TestMain" {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: TestMain reads %s; a TestMain cannot SKIP, so gating it on the database removes every test in this "+
					"package from dbtestcov's inventory while the package still exits 0. Gate the individual tests instead",
				pos.Filename, pos.Line, dbEnvVar))
			continue
		}

		if len(fi.skips) == 0 {
			// It reads the variable and never skips. That is only a problem if
			// no caller skips properly either: a helper that merely RETURNS the
			// DSN is fine when the setup around it skips naming the variable,
			// because the test then SKIPs with a classifiable message. Saying
			// otherwise would reject an ordinary accessor refactor — and would
			// say something untrue while doing it.
			if reaches[fn.Name.Name] {
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s:%d: %s reads %s, never calls t.Skip, and %s. A test stopped this way emits no SKIP line naming %s, so "+
					"the inventory cannot classify it and no CI step is ever required to run it. Either skip here naming %s, "+
					"or make every caller skip with a message that names it",
				pos.Filename, pos.Line, fn.Name.Name, dbEnvVar,
				describeCallers(fset, callers[fn.Name.Name], reaches), dbEnvVar, dbEnvVar))
			continue
		}

		for _, call := range fi.skips {
			msg, hasMsg := skipMessage(call, consts)
			if hasMsg && strings.Contains(msg, dbEnvVar) {
				continue
			}
			cpos := fset.Position(call.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: %s reads %s but skips with %s",
				cpos.Filename, cpos.Line, fn.Name.Name, dbEnvVar, describeSkipMsg(msg, hasMsg)))
		}
	}
	return violations
}

// namingSkipReachability answers, for every function in the package, "does
// stopping inside this function still produce a SKIP whose message names
// dbEnvVar?" — directly, or through every one of its callers.
//
// A least-fixpoint over callers, not a single hop: `dsn()` <- `setup()` <-
// `TestX()` is an ordinary two-level helper chain, and answering it one level
// deep would reject it. Unknown callers and cycles resolve to false, so the
// uncertain answer is the strict one.
func namingSkipReachability(funcs []*funcInfo, callers map[string][]*funcInfo) map[string]bool {
	byName := map[string]*funcInfo{}
	ambiguous := map[string]bool{}
	for _, fi := range funcs {
		n := fi.decl.Name.Name
		if _, dup := byName[n]; dup {
			ambiguous[n] = true
		}
		byName[n] = fi
	}

	const (
		unknown = iota
		inProgress
		yes
		no
	)
	state := map[string]int{}
	var ok func(name string) bool
	ok = func(name string) bool {
		switch state[name] {
		case yes:
			return true
		case no, inProgress: // a cycle proves nothing, so it proves nothing good
			return false
		}
		fi, found := byName[name]
		if !found || ambiguous[name] {
			state[name] = no
			return false
		}
		state[name] = inProgress
		result := false
		switch {
		case fi.namingSkip:
			result = true
		case len(fi.skips) > 0:
			// It skips, but not with a message that names the variable. That is
			// reported separately; it cannot rescue anything.
			result = false
		default:
			cs := callers[name]
			result = len(cs) > 0
			for _, c := range cs {
				if !ok(c.decl.Name.Name) {
					result = false
					break
				}
			}
		}
		if result {
			state[name] = yes
		} else {
			state[name] = no
		}
		return result
	}

	out := map[string]bool{}
	for _, fi := range funcs {
		out[fi.decl.Name.Name] = ok(fi.decl.Name.Name)
	}
	return out
}

// describeCallers renders the reason a helper's callers do not rescue it, so
// the message says what is actually wrong rather than a generic claim.
func describeCallers(fset *token.FileSet, cs []*funcInfo, reaches map[string]bool) string {
	if len(cs) == 0 {
		return "nothing in this package calls it, so there is no caller whose skip message could classify it"
	}
	var bad []string
	for _, c := range cs {
		if !reaches[c.decl.Name.Name] {
			pos := fset.Position(c.decl.Pos())
			bad = append(bad, fmt.Sprintf("%s (%s:%d)", c.decl.Name.Name, filepath.Base(pos.Filename), pos.Line))
		}
	}
	sort.Strings(bad)
	return "its caller(s) " + strings.Join(bad, ", ") + " do not skip with a message naming " + dbEnvVar
}

// calleeNames returns the same-package function names a body calls. Method
// selectors are included by their final name: resolving receivers is out of
// scope, and being generous here only ever makes a helper look MORE rescued,
// which is the direction that avoids false positives.
func calleeNames(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			out[fun.Name] = true
		case *ast.SelectorExpr:
			out[fun.Sel.Name] = true
		}
		return true
	})
	return out
}

// passesDBEnvToAnEnvReader reports whether the body hands a constant folding to
// dbEnvVar to a same-package function that reads the environment.
//
// Both halves of that are load-bearing, and each was measured. Without the
// constant fold, `envSet(dbEnvConst)` — where envSet takes the variable name as
// a parameter — is invisible. Without the "is an env reader" restriction, every
// `fmt.Sprintf("... %s ...", dbEnvVar)` in this very file counts, and the gate
// reports itself.
func passesDBEnvToAnEnvReader(body *ast.BlockStmt, consts map[string]string, envReaders map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if !envReaders[name] {
			return true
		}
		for _, arg := range call.Args {
			if v, ok := constString(arg, consts); ok && v == dbEnvVar {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// buildConstraintOf returns the file's first //go:build or // +build line, or
// "" when it has none. Only the header — everything before the package clause —
// is examined, which is the only place the go tool honours one.
func buildConstraintOf(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "package ") {
			return ""
		}
		if strings.HasPrefix(t, "//go:build") || strings.HasPrefix(t, "// +build") {
			return t
		}
	}
	return ""
}

// funcSource returns the source text of a function declaration, from the `func`
// keyword to the closing brace. The doc comment is deliberately outside that
// range: prose about AIHUB_TEST_DB is not a guard on it.
func funcSource(fset *token.FileSet, f *goFile, fn *ast.FuncDecl) string {
	tf := fset.File(fn.Pos())
	if tf == nil {
		return ""
	}
	start, end := tf.Offset(fn.Pos()), tf.Offset(fn.End())
	if start < 0 || end > len(f.src) || start >= end {
		return ""
	}
	return string(f.src[start:end])
}

// envReadsOutsideFuncs returns the environment reads that resolve to dbEnvVar
// and do NOT sit inside any function declaration's body — i.e. the ones in
// package-level var/const initialisers, including inside a func literal bound
// to a var. Those are reported unconditionally, because a declaration has no
// way to skip.
func envReadsOutsideFuncs(f *goFile, consts map[string]string) []envRead {
	type span struct{ lo, hi token.Pos }
	var bodies []span
	for _, decl := range f.ast.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			bodies = append(bodies, span{fn.Body.Pos(), fn.Body.End()})
		}
	}
	inABody := func(p token.Pos) bool {
		for _, b := range bodies {
			if p >= b.lo && p < b.hi {
				return true
			}
		}
		return false
	}

	var out []envRead
	for _, r := range envReadsInNode(f.ast, consts) {
		if inABody(r.pos) {
			continue
		}
		// Only a read this command can pin to dbEnvVar is reported here. An
		// unresolvable one at package level is already covered by the
		// per-function rule wherever it is consumed, and reporting it twice
		// would make the message confusing.
		if r.resolved && r.name == dbEnvVar {
			out = append(out, r)
		}
	}
	return out
}

// envRead is one call into the process environment found inside a function.
type envRead struct {
	name     string
	resolved bool
	// wildcard marks os.Environ(), which reads everything and so names no
	// single variable.
	wildcard bool
	pos      token.Pos
}

func envReadsIn(body *ast.BlockStmt, consts map[string]string) []envRead {
	return envReadsInNode(body, consts)
}

func envReadsInNode(root ast.Node, consts map[string]string) []envRead {
	var reads []envRead
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		case *ast.Ident:
			name = fun.Name
		}
		if !envReadFuncs[name] {
			return true
		}
		r := envRead{pos: call.Pos()}
		if name == "Environ" || len(call.Args) == 0 {
			r.wildcard = true
		} else {
			r.name, r.resolved = constString(call.Args[0], consts)
		}
		reads = append(reads, r)
		return true
	})
	return reads
}

func describeSkipMsg(msg string, hasMsg bool) string {
	if !hasMsg {
		return "no constant message"
	}
	return strconv.Quote(msg)
}

func skipCalls(body *ast.BlockStmt) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && skipFuncNames[sel.Sel.Name] {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// skipMessage folds a skip call's first argument to a string, using the same
// constant folding as the env-read argument: a message assembled from constants
// still names the variable.
func skipMessage(call *ast.CallExpr, consts map[string]string) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	return constString(call.Args[0], consts)
}

// ---------------------------------------------------------------- workflow

type wfStep struct {
	Name            string         `yaml:"name"`
	Env             map[string]any `yaml:"env"`
	Run             string         `yaml:"run"`
	If              any            `yaml:"if"`
	ContinueOnError any            `yaml:"continue-on-error"`
}

type wfJob struct {
	Env             map[string]any `yaml:"env"`
	Steps           []wfStep       `yaml:"steps"`
	If              any            `yaml:"if"`
	ContinueOnError any            `yaml:"continue-on-error"`
}

type wfFile struct {
	Env  map[string]any   `yaml:"env"`
	Jobs map[string]wfJob `yaml:"jobs"`
}

var (
	goTestRE     = regexp.MustCompile(`(?:^|[;&|(]|\s)(go\s+test)\s`)
	runFlagRE    = regexp.MustCompile(`-run(?:\s+|=)(?:'([^']*)'|"([^"]*)"|(\S+))`)
	skipFlagRE   = regexp.MustCompile(`-skip(?:\s+|=)`)
	countFlagRE  = regexp.MustCompile(`-count(?:\s+|=)(\S+)`)
	pkgArgRE     = regexp.MustCompile(`(?:^|\s)(\.{1,2}/[^\s'"]*)`)
	teeRE        = regexp.MustCompile(`\|\s*tee\s+(?:-a\s+)?(\S+)`)
	inlineDBEnv  = regexp.MustCompile(`(?:^|\s)` + dbEnvVar + `=`)
	continuation = regexp.MustCompile(`\\\n\s*`)

	// skipGuardRE is the ONE accepted spelling of the anti-silent-SKIP guard:
	//
	//	! grep -q -- '--- SKIP' <log> || exit 1
	//	! grep -q -- '--- SKIP' <log> || { echo "..."; exit 1; }
	//
	// An allowlist rather than a search, because "some line mentions the marker
	// and the log" is satisfied by lines that assert the OPPOSITE of what the
	// guard is for, and a text search cannot tell them apart:
	//
	//	grep -q -- '--- SKIP' a.log || exit 1     # passes only if a test DID skip
	//	! grep -c -- '--- SKIP' a.log || true     # can never fail the step
	//	echo "then run: ! grep -q -- '--- SKIP' a.log"
	//
	// All three used to count as a guard. An allowlist is the right shape here
	// because these lines are CI-authored, not user-authored: the cost of the
	// narrowness is that a new guard must be spelled the same way as the twenty
	// already in ci.yml, and the benefit is that no clever spelling can be
	// wrong in a direction nobody notices. The trailing `exit` must be a
	// NON-ZERO status, so `|| exit 0` is rejected along with `|| true`.
	//
	// Inside the `{ ... }` tail the `exit` must be the LAST command of the
	// block, which is why the alternative before it has to end in `;`. Without
	// that the allowlist admits `|| { echo x || exit 1; }`, where the echo
	// succeeds, the exit never runs, the block returns 0 and the step is green
	// on a SKIP — a false green that matches the pattern for correct guards.
	skipGuardRE = regexp.MustCompile(
		`^!\s+grep\s+-q\s+--\s+(?:'` + regexp.QuoteMeta(skipGuardMarker) + `'|"` + regexp.QuoteMeta(skipGuardMarker) +
			`")\s+(\S+)\s*\|\|\s*(?:exit\s+[1-9][0-9]*|\{\s*(?:[^{}]*;\s*)?exit\s+[1-9][0-9]*\s*;?\s*\})\s*$`)
)

// ------------------------------------------------------------ shell modelling

// quoteMask returns, for each byte of s, the quote character it sits inside
// ('\” or '"'), or 0 when it is unquoted. Backslash escapes are honoured
// everywhere except inside single quotes, where the shell treats them
// literally.
//
// Every structural decision below is taken over unquoted text only. Without
// that, `echo "to reproduce: go test ./... | tee a.log"` reads as a real
// invocation — and because one quoted line can carry the `go test`, the `tee`
// target AND the SKIP marker, it would credit coverage and satisfy the guard
// that is supposed to prove the coverage ran. One line, complete bypass.
func quoteMask(s string) []byte {
	mask, _ := quoteMaskFrom(s, 0)
	return mask
}

// quoteMaskFrom is quoteMask with an INCOMING quote state, and it returns the
// outgoing one. The carry is what makes a multi-line quoted string data rather
// than commands:
//
//	echo "Reproduce locally:
//	go test ./internal/domain/ -run '^(TestGhost)$' -v 2>&1 | tee ghost.log
//	! grep -q -- '--- SKIP' ghost.log || exit 1
//	"
//
// Nothing there runs. Computed per line, from a fresh state each time, lines 2
// and 3 read as a real invocation AND its guard — the same complete bypass a
// single-line quoted string used to be, reached by adding a newline. So quote
// state is threaded through the whole script, not restarted at every '\n'.
func quoteMaskFrom(s string, q byte) ([]byte, byte) {
	mask := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			mask[i] = q
			if q == '"' && c == '\\' && i+1 < len(s) {
				i++
				mask[i] = q
				continue
			}
			if c == q {
				q = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			q = c
			mask[i] = q
		case c == '\\' && i+1 < len(s):
			i++ // an escaped character is literal, never a quote or an operator
		}
	}
	return mask, q
}

// scriptLine is one line of a step's `run:` block after comments and
// here-document bodies have been blanked, together with the shell context in
// force at its START. The context is what makes the SKIP guard checkable: a
// guard inside `if ... fi`, or reached only via `&&`, or sitting inside a
// multi-line quoted string, asserts nothing — and the `go test` side of this
// parser rejects all three while the guard side used to accept them.
type scriptLine struct {
	Text string
	// Depth is the enclosing if/while/until/for/case depth.
	Depth int
	// Quote is the quote character still open from an earlier line, or 0.
	Quote byte
	// PendingOp is the `&&` or `||` a PREVIOUS line ended with, which makes
	// this line's first command conditional on that line's result. A shell
	// line ending in `&&` continues onto the next line, so a guard placed
	// there runs only sometimes.
	PendingOp string
}

// shellCommand is one command from a step's script: the text between unquoted
// `;`, `&&`, `||` and `&` separators, with enough context to say whether it
// actually runs and whether its failure actually fails the step.
type shellCommand struct {
	// Line is the 0-based index into shellScript.Lines the command starts on.
	Line int
	Text string
	// Quote is the quote character in force at the start of the command, or 0.
	// Non-zero means the text is data inside a multi-line string.
	Quote byte
	// Ungated is empty when the command runs unconditionally and its exit
	// status fails the step. Otherwise it says why not, for the error message.
	Ungated string
}

// shellScript is a step's `run:` block, modelled.
type shellScript struct {
	Lines    []scriptLine
	Commands []shellCommand
}

// Texts returns the blanked line texts, for the whole-script regexp probes.
func (s *shellScript) Texts() []string {
	out := make([]string, len(s.Lines))
	for i, l := range s.Lines {
		out[i] = l.Text
	}
	return out
}

// shellPart is one span of a line plus the operator that separated it from the
// previous span and the quote state at its start.
type shellPart struct {
	text  string
	op    string
	quote byte
}

// splitUnquoted splits a line at unquoted `;`, `&&`, `||` and `&`, starting
// from the given open-quote state and returning the state left at the end.
//
// `&` is only a separator when it is neither half of `&&` nor part of a
// redirection: `2>&1` ends every `go test` line in ci.yml, and splitting there
// would cut each invocation in half.
func splitUnquoted(line string, inQuote byte) ([]shellPart, byte) {
	mask, outQuote := quoteMaskFrom(line, inQuote)
	var parts []shellPart
	prevOp := ""
	start := 0
	startQuote := inQuote
	for i := 0; i < len(line); i++ {
		if mask[i] != 0 {
			continue
		}
		var op string
		switch {
		case strings.HasPrefix(line[i:], "&&"):
			op = "&&"
		case strings.HasPrefix(line[i:], "||"):
			op = "||"
		case line[i] == ';':
			op = ";"
		case line[i] == '&' && isBackgroundAmp(line, i):
			op = "&"
		default:
			continue
		}
		parts = append(parts, shellPart{text: line[start:i], op: prevOp, quote: startQuote})
		// A separator is only recognised outside quotes, so whatever follows it
		// starts unquoted.
		startQuote = 0
		prevOp = op
		i += len(op) - 1
		start = i + 1
	}
	return append(parts, shellPart{text: line[start:], op: prevOp, quote: startQuote}), outQuote
}

// isBackgroundAmp reports whether the `&` at index i backgrounds a command,
// rather than being part of a redirection such as `2>&1`, `>&2` or `&>log`.
func isBackgroundAmp(line string, i int) bool {
	if i > 0 && (line[i-1] == '>' || line[i-1] == '<') {
		return false
	}
	if i+1 < len(line) {
		switch c := line[i+1]; {
		case c == '>' || c == '<':
			return false
		case c >= '0' && c <= '9':
			return false
		}
	}
	return true
}

var (
	blockOpeners = map[string]bool{"if": true, "while": true, "until": true, "for": true, "case": true}
	blockClosers = map[string]bool{"fi": true, "done": true, "esac": true}
	// condPosition holds the keywords after which a command's exit status is
	// consumed by the compound statement instead of failing the step.
	condPosition = map[string]bool{"if": true, "elif": true, "while": true, "until": true}
)

// parseShellScript models a step's `run:` block: it blanks `#` comments and
// here-document bodies, threads open-quote state and block depth across lines,
// and splits every line into commands.
//
// One pass, one carried state, because the three ways a `go test` line can look
// executable without being executed — a comment, a quoted string, a heredoc
// body — interact. A `#` inside a multi-line quoted string is data, not a
// comment; a heredoc delimiter inside quotes is data too. Deciding them
// independently, per line, is what let each one become a bypass in turn.
func parseShellScript(script string) *shellScript {
	raw := strings.Split(script, "\n")
	sc := &shellScript{Lines: make([]scriptLine, len(raw))}
	var quote byte
	depth := 0
	heredoc := ""
	pendingOp := ""

	for i, line := range raw {
		ctx := scriptLine{Depth: depth, Quote: quote, PendingOp: pendingOp}

		// Inside a here-document the lines are data. Blank them, and do not let
		// them move quote state or block depth.
		if heredoc != "" {
			if strings.TrimSpace(line) == heredoc {
				heredoc = ""
			}
			sc.Lines[i] = ctx
			continue
		}
		// A whole-line comment, but only when we are not inside a string.
		if quote == 0 && strings.HasPrefix(strings.TrimSpace(line), "#") {
			sc.Lines[i] = ctx
			continue
		}

		text := line
		if quote == 0 {
			text = cutTrailingComment(text)
			heredoc = heredocDelim(text)
		}
		ctx.Text = text
		sc.Lines[i] = ctx

		parts, outQuote := splitUnquoted(text, quote)
		for j, part := range parts {
			t := strings.TrimSpace(part.text)
			if t == "" {
				continue
			}
			// The operator that makes this command conditional is the one
			// inside the line, or — for the line's first command — the one a
			// previous line ended with.
			op := part.op
			if j == 0 && op == "" {
				op = pendingOp
			}
			cmd := shellCommand{Line: i, Text: t, Quote: part.quote}
			switch {
			case depth > 0:
				cmd.Ungated = "inside a shell block body (depth " + strconv.Itoa(depth) + ")"
			case op == "&&" || op == "||":
				cmd.Ungated = "reached only via `" + op + "`"
			}
			// Classification by keyword, and depth accounting, are only valid
			// on text the shell would actually parse as a command.
			if part.quote == 0 {
				word := firstWord(t)
				switch {
				case condPosition[word]:
					cmd.Ungated = "in the condition of a shell `" + word + "`"
				case strings.HasPrefix(t, "!"):
					cmd.Ungated = "negated with `!`, so its failure passes the step"
				case j+1 < len(parts) && parts[j+1].op == "&":
					cmd.Ungated = "backgrounded with `&`, so its exit status is discarded"
				case j+1 < len(parts) && (parts[j+1].op == "&&" || parts[j+1].op == "||"):
					// Looking FORWARD matters as much as looking back:
					// `go test ... || true` is as ungated as `x && go test ...`,
					// and only the second used to be rejected.
					cmd.Ungated = "followed by `" + parts[j+1].op +
						"`, so its exit status is consumed instead of failing the step"
				}
				switch {
				case blockOpeners[word]:
					depth++
				case blockClosers[word] && depth > 0:
					depth--
				}
			}
			sc.Commands = append(sc.Commands, cmd)
		}

		// A line whose last span is empty after an `&&`/`||` continues onto the
		// next line.
		pendingOp = ""
		if last := parts[len(parts)-1]; strings.TrimSpace(last.text) == "" &&
			(last.op == "&&" || last.op == "||") {
			pendingOp = last.op
		}
		quote = outQuote
	}
	return sc
}

// pipefailRE matches the `set` that makes a pipeline report its first failure.
var pipefailRE = regexp.MustCompile(`^set\s+[^;&|]*\bpipefail\b`)

// setsPipefailBefore reports whether an unquoted, unconditional `set … pipefail`
// runs earlier in the script than the given line.
//
// Without it, `go test … | tee log` exits with TEE's status, so a FAILING test
// leaves the step green. That is the same "exit status consumed" class as
// `go test … || true` and `&`-backgrounding, which are already rejected — this
// is the member of it that lives one command away instead of on the same line.
// The SKIP guard still catches skips; what goes silently green here is a real
// failure.
func (sc *shellScript) setsPipefailBefore(line int) bool {
	for _, c := range sc.Commands {
		if c.Line >= line {
			break
		}
		if c.Quote != 0 || c.Ungated != "" {
			continue
		}
		if pipefailRE.MatchString(c.Text) {
			return true
		}
	}
	return false
}

// firstWord returns the first shell word of a command, skipping a leading `!`.
func firstWord(text string) string {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "!"))
	if i := strings.IndexAny(text, " \t"); i >= 0 {
		return text[:i]
	}
	return text
}

// goTestHit is one `go test` occurrence inside a command.
type goTestHit struct {
	// start is the offset of the `go`; end is the offset just past the `test `,
	// where the argument list begins.
	start, end int
	quoted     bool
}

func goTestHits(s string, inQuote byte) []goTestHit {
	var hits []goTestHit
	mask, _ := quoteMaskFrom(s, inQuote)
	for _, m := range goTestRE.FindAllStringSubmatchIndex(s, -1) {
		// The `(go\s+test)` group is mandatory, so m[2] is always a real
		// offset. Guarded anyway: if a future edit made the group optional,
		// m[2] would be -1 and the indexing below would panic — and a crash in
		// a gate is indistinguishable from the gate being removed.
		if m[2] < 0 || m[2] >= len(mask) {
			continue
		}
		hits = append(hits, goTestHit{start: m[2], end: m[1], quoted: mask[m[2]] != 0})
	}
	return hits
}

// ParseWorkflow returns every `go test` invocation that runs with AIHUB_TEST_DB
// set, plus the invocations whose SKIPs nothing guards and the lines it could
// not model.
func ParseWorkflow(data []byte, module string) (*WorkflowScan, error) {
	var wf wfFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}

	jobNames := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		jobNames = append(jobNames, name)
	}
	sort.Strings(jobNames)

	scan := &WorkflowScan{}
	for _, jobName := range jobNames {
		job := wf.Jobs[jobName]
		for _, st := range job.Steps {
			if st.Run == "" {
				continue
			}
			script := continuation.ReplaceAllString(st.Run, " ")
			sc := parseShellScript(script)
			lines := sc.Texts()

			hasDB := hasKey(st.Env, dbEnvVar) || hasKey(job.Env, dbEnvVar) || hasKey(wf.Env, dbEnvVar) ||
				anyLineMatches(lines, inlineDBEnv)
			if !hasDB {
				continue
			}
			stepName := st.Name
			if stepName == "" {
				stepName = jobName + " (unnamed step)"
			}

			// Conditional execution would make coverage depend on the event
			// that triggered the run. Refusing to model it is safer than
			// crediting coverage a pull-request build never executes.
			if isSet(st.If) || isTruthy(st.ContinueOnError) || isSet(job.If) || isTruthy(job.ContinueOnError) {
				if anyLineMatches(lines, goTestRE) {
					return nil, fmt.Errorf(
						"step %q (job %q) sets %s and runs `go test`, but it or its job carries `if:`/`continue-on-error:`; "+
							"conditional coverage is not modelled by dbtestcov — make the step unconditional or teach dbtestcov about it",
						stepName, jobName, dbEnvVar)
				}
				continue
			}

			for _, cmd := range sc.Commands {
				for _, hit := range goTestHits(cmd.Text, cmd.Quote) {
					// A `go test` nobody executes must not credit coverage.
					// Quoted text is the sharpest form because the same line can
					// also carry the tee target and the SKIP marker, satisfying
					// in prose the guard that is supposed to prove the run.
					if hit.quoted {
						return nil, fmt.Errorf(
							"step %q (job %q), line %d: `go test` appears inside a quoted string, where nothing executes it, "+
								"yet its packages would be credited as covered — and one quoted line can carry the %q marker "+
								"and a log name too, satisfying the guard that is supposed to prove the coverage ran. "+
								"Move it to a `#` comment or delete it: %s",
							stepName, jobName, cmd.Line, skipGuardMarker, cmd.Text)
					}
					if cmd.Ungated != "" {
						return nil, fmt.Errorf(
							"step %q (job %q), line %d: `go test` is %s, so dbtestcov cannot model whether it runs or whether "+
								"its failure fails the step; crediting it as coverage would be a guess. Make it an "+
								"unconditional, ungated command, or teach dbtestcov about it: %s",
							stepName, jobName, cmd.Line, cmd.Ungated, cmd.Text)
					}
					inv, err := parseGoTestArgs(cmd.Text[hit.end:], module, stepName)
					if err != nil {
						return nil, err
					}
					if inv == nil {
						scan.Dropped = append(scan.Dropped, fmt.Sprintf("%s: %s", stepName, cmd.Text))
						continue
					}
					if inv.Log != "" && !sc.setsPipefailBefore(cmd.Line) {
						return nil, fmt.Errorf(
							"step %q (job %q), line %d: `go test` is piped into `tee` but the step never runs "+
								"`set -o pipefail`, so the pipeline exits with tee's status and a FAILING test leaves this "+
								"step green. Add `set -o pipefail` as the step's first command: %s",
							stepName, jobName, cmd.Line, cmd.Text)
					}
					scan.Invocations = append(scan.Invocations, *inv)
					if inv.Log == "" || inv.Log == "/dev/null" {
						why := "captures no output (no `| tee <log>`)"
						if inv.Log == "/dev/null" {
							why = "tees to /dev/null, which is always empty"
						}
						scan.Unguarded = append(scan.Unguarded, fmt.Sprintf(
							"%s: `%s` %s, so no %q assertion is possible",
							stepName, cmd.Text, why, skipGuardMarker))
						continue
					}
					switch verdict, offender := skipGuardFor(sc, inv.Log, cmd.Line); verdict {
					case guardMissing:
						scan.Unguarded = append(scan.Unguarded, fmt.Sprintf(
							"%s: nothing greps %s for %q, so this invocation passes even if every test it selects SKIPs",
							stepName, inv.Log, skipGuardMarker))
					case guardMalformed:
						scan.Unguarded = append(scan.Unguarded, fmt.Sprintf(
							"%s: the line that mentions %s and %q is not a working guard, so it asserts nothing about this "+
								"invocation. It must be spelled `! grep -q -- '%s' %s || exit 1` (a `|| { echo ...; exit 1; }` "+
								"tail is fine), must come AFTER the invocation, and must not sit in an `if`/`for`/`while` body, "+
								"behind `&&`/`||`, within quotes, or contain `exit 0` — an inverted "+
								"`grep ... || exit 1` REQUIRES a skip, a trailing `|| true` can never fail, and a guard placed "+
								"before its invocation greps a file that does not exist yet (exit 2, inverted to 0): %s",
							stepName, inv.Log, skipGuardMarker, skipGuardMarker, inv.Log, offender))
					}
				}
			}
		}
	}
	return scan, nil
}

// guardVerdict is the outcome of looking for an invocation's SKIP guard.
type guardVerdict int

const (
	// guardOK: a line matching skipGuardRE names exactly this invocation's log.
	guardOK guardVerdict = iota
	// guardMissing: no line even mentions both the marker and this log.
	guardMissing
	// guardMalformed: a line mentions both, but is not a guard that can fail
	// the step. This is the dangerous one — it looks like coverage is asserted.
	guardMalformed
)

// exitZeroRE finds a successful exit inside a guard. `{ echo "..." && exit 0;
// exit 1; }` satisfies "the exit is the block's last command" while the earlier
// one wins, so the block returns 0 on a SKIP. A correct guard never contains
// `exit 0`.
var exitZeroRE = regexp.MustCompile(`\bexit\s+0\b`)

// skipGuardFor decides whether this invocation's SKIPs are actually asserted
// on. Checking per invocation rather than per step matters: a step with three
// `go test` lines and one guard would otherwise credit coverage for all three
// while asserting on only one.
//
// A guard has to satisfy four things, not just one, and only the first used to
// be checked:
//
//   - SHAPE: it matches skipGuardRE, and names this invocation's own log as a
//     whole shell word (ci.yml really has recall_unmatched.log next to
//     recall_unmatched_http.log, and a substring match would let one stand in
//     for the other);
//   - ORDER: it comes AFTER the invocation. Before it, the log does not exist
//     yet, so `grep` exits 2, the leading `!` inverts that to 0 and `|| exit 1`
//     never fires — a guard that CANNOT fail, produced by an ordinary
//     reordering edit;
//   - CONTEXT: it is not inside an `if`/`for`/`while`/`case` body, not reached
//     only via `&&`/`||` (including one a previous line ended with), and not
//     inside a multi-line quoted string. The `go test` side rejects all of
//     those as unmodellable; a guard that only sometimes runs is worth no more;
//   - EFFECT: no `exit 0` anywhere in it.
//
// A log of /dev/null is rejected too: it is always empty, so the guard can
// never fail however it is spelled.
func skipGuardFor(sc *shellScript, log string, afterLine int) (guardVerdict, string) {
	nameRE := regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(log) + `(\s|$)`)
	offender := ""
	for i, ln := range sc.Lines {
		if ln.Text == "" {
			continue
		}
		trimmed := strings.TrimSpace(ln.Text)
		if m := skipGuardRE.FindStringSubmatch(trimmed); m != nil && m[1] == log &&
			i > afterLine && ln.Depth == 0 && ln.Quote == 0 && ln.PendingOp == "" &&
			!exitZeroRE.MatchString(trimmed) {
			return guardOK, ""
		}
		if offender == "" && strings.Contains(ln.Text, skipGuardMarker) && nameRE.MatchString(ln.Text) {
			offender = trimmed
		}
	}
	if offender != "" {
		return guardMalformed, offender
	}
	return guardMissing, ""
}

// heredocRE matches a here-document redirection and captures its delimiter,
// quoted or bare.
// The bare alternative deliberately accepts ANY word, not just an identifier:
// `<<\EOF` (backslash-quoted, the third POSIX way to stop expansion) and
// `<<1EOF` (digit-leading) are both valid delimiters, and both were measured
// slipping through an `[A-Za-z_][A-Za-z0-9_]*` spelling as full bypasses.
var heredocRE = regexp.MustCompile(`<<-?\s*(?:'([^']+)'|"([^"]+)"|\\?([^\s;&|<>()'"]+))`)

func heredocDelim(line string) string {
	mask := quoteMask(line)
	for _, m := range heredocRE.FindAllStringSubmatchIndex(line, -1) {
		if m[0] >= len(mask) || mask[m[0]] != 0 {
			continue
		}
		// `<<<` is a here-STRING: one line, no body to skip.
		if m[0]+2 < len(line) && line[m[0]+2] == '<' {
			continue
		}
		for g := 1; g <= 3; g++ {
			if m[2*g] >= 0 {
				return line[m[2*g]:m[2*g+1]]
			}
		}
	}
	return ""
}

// cutTrailingComment truncates a line at an unquoted `#` that begins a word,
// which is where the shell starts a comment. A `#` in mid-word (`aihub#303`) is
// literal, and one inside quotes is data — ci.yml's `echo "::error::an
// aihub#289 test SKIPped"` must survive intact.
//
// A whole-line comment was already dropped above; this closes the trailing
// case, which is the same hole one column to the right. `echo hi # go test
// ./... -v 2>&1 | tee a.log` executes no test, but a parser that reads the
// whole line credits the package as covered and — because the comment can go on
// to mention the SKIP marker and the log — satisfies the guard as well.
func cutTrailingComment(line string) string {
	mask := quoteMask(line)
	for i := 0; i < len(line); i++ {
		if line[i] != '#' || mask[i] != 0 {
			continue
		}
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return line[:i]
		}
	}
	return line
}

func anyLineMatches(lines []string, re *regexp.Regexp) bool {
	for _, l := range lines {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

// isSet reports whether a YAML scalar was present and non-empty.
func isSet(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case bool:
		return t
	default:
		return true
	}
}

// isTruthy reports whether a `continue-on-error:` value actually disables the
// step's failure. `false` (the default) is present-but-harmless.
func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s != "" && s != "false"
	default:
		return true
	}
}

// parseGoTestArgs models one `go test` invocation from everything that follows
// the `go test ` itself, up to the end of its pipeline. rest starts at the
// argument list and may run on into `| tee <log>`.
func parseGoTestArgs(rest, module, stepName string) (*Invocation, error) {
	// Cut the command at the first pipe / redirection so that `tee foo.log`
	// arguments cannot be mistaken for package or flag arguments. The cut has
	// to be quote-aware: a `-run` alternation is full of '|' characters that
	// are arguments, not pipes.
	args := cutAtShellOperator(rest)
	if skipFlagRE.MatchString(args) {
		return nil, fmt.Errorf("step %q: `go test -skip` is not modelled by dbtestcov, so its coverage cannot be trusted; "+
			"either drop -skip or teach dbtestcov about it", stepName)
	}
	// `-count=0` selects zero tests. That defeats BOTH halves of this gate at
	// once and in the same direction: there are no SKIP lines for the guard to
	// find, and `go test` exits 0, so the step is green while running nothing.
	// It is `-skip`'s twin and gets `-skip`'s treatment.
	for _, m := range countFlagRE.FindAllStringSubmatch(args, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("step %q: `go test -count %s` is not a literal number, so dbtestcov cannot tell whether "+
				"the invocation runs any test at all; use a literal count", stepName, m[1])
		}
		if n <= 0 {
			return nil, fmt.Errorf("step %q: `go test -count=%d` selects zero tests, so the invocation prints \"ok\", exits 0, "+
				"and emits no %q line for the guard to find — the step is green having run nothing. Use -count=1",
				stepName, n, skipGuardMarker)
		}
	}

	var pkgs []PackageSel
	for _, m := range pkgArgRE.FindAllStringSubmatch(args, -1) {
		p, ok := resolvePackage(m[1], module)
		if ok {
			pkgs = append(pkgs, p)
		}
	}
	if len(pkgs) == 0 {
		return nil, nil
	}

	// `go test` obeys the LAST -run; this command used to read the first, so
	// `-run 'TestFoo' -run 'TestNothingMatches'` credited TestFoo while CI ran
	// nothing. Reading the last would close the hole, but a repeated -run is
	// never intentional, so say so instead of silently picking a winner.
	runPat := ""
	if ms := runFlagRE.FindAllStringSubmatch(args, -1); len(ms) > 0 {
		if len(ms) > 1 {
			return nil, fmt.Errorf("step %q: `go test` is given -run %d times; only the LAST one has any effect, so the "+
				"earlier pattern(s) describe coverage that does not happen. Merge them into one -run: %s",
				stepName, len(ms), strings.TrimSpace(args))
		}
		for _, g := range ms[len(ms)-1][1:] {
			if g != "" {
				runPat = g
				break
			}
		}
	}
	log := ""
	if m := teeRE.FindStringSubmatch(rest); m != nil {
		log = m[1]
	}
	return &Invocation{Step: stepName, Packages: pkgs, Run: runPat, Log: log}, nil
}

// cutAtShellOperator truncates a command's argument list at the first shell
// operator that is not inside single or double quotes: `| ; & > <` all end the
// `go test` invocation, but the same characters inside a quoted `-run` pattern
// are part of the argument.
func cutAtShellOperator(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '|' || c == ';' || c == '&' || c == '>' || c == '<':
			return s[:i]
		}
	}
	return s
}

// resolvePackage turns a `go test` package argument such as "./internal/domain/"
// or "./..." into an import path (or import-path prefix).
func resolvePackage(arg, module string) (PackageSel, bool) {
	arg = strings.TrimPrefix(arg, "./")
	arg = strings.TrimSuffix(arg, "/")
	if arg == "." || arg == "" {
		return PackageSel{Path: module}, true
	}
	if arg == "..." {
		return PackageSel{Path: module + "/", Wildcard: true}, true
	}
	if strings.HasSuffix(arg, "/...") {
		return PackageSel{Path: module + "/" + strings.TrimSuffix(arg, "/...") + "/", Wildcard: true}, true
	}
	if strings.Contains(arg, "...") {
		// Interior wildcards ("./internal/d.../x") are not modelled; refusing
		// to guess is safer than silently crediting coverage.
		return PackageSel{}, false
	}
	return PackageSel{Path: module + "/" + arg}, true
}

func hasKey(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	_, ok := m[k]
	return ok
}

func readModulePath(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is a CI-controlled flag
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("no module directive in %s", path)
}

// ----------------------------------------------------------- -run semantics

// MatchesRun reports whether `go test -run <pattern>` selects the top-level
// test function called name.
//
// Only the first element of each alternative constrains a top-level function
// (testing's simpleMatch stops at len(name)), and each element is an UNANCHORED
// regexp — so `-run TestFoo` selects TestFooBar too.
func MatchesRun(pattern, name string) (bool, error) {
	for _, alt := range SplitRunPattern(pattern) {
		re, err := regexp.Compile(alt[0])
		if err != nil {
			return false, fmt.Errorf("invalid -run regexp %q: %w", alt[0], err)
		}
		if re.MatchString(name) {
			return true, nil
		}
	}
	return false, nil
}

// SplitRunPattern splits a -run argument into alternatives, each a list of
// per-name-element regexps, mirroring testing.splitRegexp in
// $GOROOT/src/testing/match.go: it splits on '/' and on '|' when they are
// outside a character class, outside a group, and unescaped.
//
// Both splits matter. Dropping the '|' one would make `-run 'A/b|C'` look like
// the single element "A" and miss that real `go test` also selects the
// top-level test C.
func SplitRunPattern(s string) [][]string {
	var alternatives [][]string
	var a []string
	cs, cp := 0, 0
	i := 0
	for i < len(s) {
		switch s[i] {
		case '[':
			cs++
		case ']':
			if cs--; cs < 0 { // an unmatched ']' is legal
				cs = 0
			}
		case '(':
			if cs == 0 {
				cp++
			}
		case ')':
			if cs == 0 {
				cp--
			}
		case '\\':
			i++
		case '/':
			if cs == 0 && cp == 0 {
				a = append(a, s[:i])
				s = s[i+1:]
				i = 0
				continue
			}
		case '|':
			if cs == 0 && cp == 0 {
				a = append(a, s[:i])
				s = s[i+1:]
				i = 0
				alternatives = append(alternatives, a)
				a = nil
				continue
			}
		}
		i++
	}
	a = append(a, s)
	return append(alternatives, a)
}
