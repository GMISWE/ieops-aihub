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
// how 28 of 83 DB-gated test functions came to be invisible to CI while every
// step stayed green.
//
// # How it decides what is "DB-gated"
//
// From behaviour, not from source structure. The inventory is a
// `go test ./... -json` run taken with AIHUB_TEST_DB *unset*, and a test
// function is DB-gated iff it SKIPped and the message it passed to t.Skip names
// AIHUB_TEST_DB.
//
// Be precise about what that does and does not buy. It means there is no list
// of guard helpers to keep in sync and no AST shape to be defeated: a test is
// classified by what it did, so a guard written in a form nobody anticipated is
// still caught. It does NOT mean the classification is free of a convention.
// The convention is the skip MESSAGE, and it is the cheapest way to make this
// gate go quiet: a DB test that skips with `t.Skip("no test database")` never
// enters the required set, and -min-gated cannot see it because it was never
// counted. checkSkipMessages closes that door inside this file — every function
// that reads AIHUB_TEST_DB must name the variable in the messages it skips
// with, or this command fails.
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
// its whole package. Constructs that would make coverage conditional or
// unmodellable (`-skip`, `if:`, `continue-on-error:`) are hard errors, not
// silent approximations.
//
// Every DB `go test` invocation must also tee its output to a log AND be
// followed by a guard grepping THAT log for "--- SKIP". Coverage that is merely
// named by a `-run` is nominal; the guard is what makes it real.
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
	sourceRoot := flag.String("source-root", ".", "repository root, walked for _test.go files to check the skip-message convention")
	minGated := flag.Int("min-gated", 1, "ratchet floor: fail if the inventory holds fewer DB-gated tests than this. Raise it when you add DB tests; it is what catches an inventory that stopped enumerating (for example because "+dbEnvVar+" leaked into the run that produced it, making everything pass instead of skip).")
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

// checkSkipMessages enforces the one convention ParseInventory depends on:
// a function that reads AIHUB_TEST_DB must name AIHUB_TEST_DB in every message
// it skips with. Otherwise the test it guards skips for want of a database and
// this command never learns that it did — the original defect, one level up,
// and the cheapest possible way to make this gate go quiet.
//
// It is deliberately narrow: same function, syntactic. A guard that reads the
// variable in one function and skips in another would slip past. That residual
// hole is much more expensive to walk through than writing a correct message,
// which is the property that matters.
func checkSkipMessages(root string) ([]string, error) {
	var violations []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) // #nosec G304 -- walking a CI-controlled root
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), dbEnvVar) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !readsDBEnv(fn.Body) {
				continue
			}
			for _, call := range skipCalls(fn.Body) {
				msg, hasMsg := firstStringArg(call)
				if hasMsg && strings.Contains(msg, dbEnvVar) {
					continue
				}
				pos := fset.Position(call.Pos())
				violations = append(violations, fmt.Sprintf(
					"%s:%d: %s reads %s but skips with %s",
					pos.Filename, pos.Line, fn.Name.Name, dbEnvVar, describeSkipMsg(msg, hasMsg)))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func describeSkipMsg(msg string, hasMsg bool) string {
	if !hasMsg {
		return "no constant message"
	}
	return strconv.Quote(msg)
}

func readsDBEnv(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Getenv" {
			return true
		}
		if v, has := firstStringArg(call); has && v == dbEnvVar {
			found = true
			return false
		}
		return true
	})
	return found
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

func firstStringArg(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
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
	goTestRE     = regexp.MustCompile(`(?:^|[;&|(]|\s)go\s+test\s`)
	runFlagRE    = regexp.MustCompile(`-run(?:\s+|=)(?:'([^']*)'|"([^"]*)"|(\S+))`)
	skipFlagRE   = regexp.MustCompile(`-skip(?:\s+|=)`)
	pkgArgRE     = regexp.MustCompile(`(?:^|\s)(\.{1,2}/[^\s'"]*)`)
	teeRE        = regexp.MustCompile(`\|\s*tee\s+(?:-a\s+)?(\S+)`)
	inlineDBEnv  = regexp.MustCompile(`(?:^|\s)` + dbEnvVar + `=`)
	continuation = regexp.MustCompile(`\\\n\s*`)
)

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
			lines := stripShellComments(script)

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

			for _, line := range lines {
				if !goTestRE.MatchString(line) {
					continue
				}
				inv, err := parseGoTestLine(line, module, stepName)
				if err != nil {
					return nil, err
				}
				if inv == nil {
					scan.Dropped = append(scan.Dropped,
						fmt.Sprintf("%s: %s", stepName, strings.TrimSpace(line)))
					continue
				}
				scan.Invocations = append(scan.Invocations, *inv)
				if inv.Log == "" {
					scan.Unguarded = append(scan.Unguarded, fmt.Sprintf(
						"%s: `%s` captures no output (no `| tee <log>`), so no %q assertion is possible",
						stepName, strings.TrimSpace(line), skipGuardMarker))
					continue
				}
				if !hasSkipGuardFor(lines, inv.Log) {
					scan.Unguarded = append(scan.Unguarded, fmt.Sprintf(
						"%s: nothing greps %s for %q, so this invocation passes even if every test it selects SKIPs",
						stepName, inv.Log, skipGuardMarker))
				}
			}
		}
	}
	return scan, nil
}

// hasSkipGuardFor reports whether some line both greps for the SKIP marker and
// names this invocation's own log file. Checking per invocation rather than per
// step matters: a step with three `go test` lines and one guard would otherwise
// credit coverage for all three while asserting on only one.
func hasSkipGuardFor(lines []string, log string) bool {
	nameRE := regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(log) + `(\s|$)`)
	for _, line := range lines {
		if strings.Contains(line, skipGuardMarker) && nameRE.MatchString(line) {
			return true
		}
	}
	return false
}

// stripShellComments splits a script into lines and blanks out full-line
// comments, so that a commented-out invocation is not credited as coverage and
// a marker mentioned in prose is not mistaken for a guard.
func stripShellComments(script string) []string {
	lines := strings.Split(script, "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			out[i] = ""
			continue
		}
		out[i] = l
	}
	return out
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

func parseGoTestLine(line, module, stepName string) (*Invocation, error) {
	loc := goTestRE.FindStringIndex(line)
	if loc == nil {
		return nil, nil
	}
	// Cut the command at the first pipe / redirection so that `tee foo.log`
	// arguments cannot be mistaken for package or flag arguments. The cut has
	// to be quote-aware: a `-run` alternation is full of '|' characters that
	// are arguments, not pipes.
	args := cutAtShellOperator(line[loc[1]:])
	if skipFlagRE.MatchString(args) {
		return nil, fmt.Errorf("step %q: `go test -skip` is not modelled by dbtestcov, so its coverage cannot be trusted; "+
			"either drop -skip or teach dbtestcov about it", stepName)
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

	runPat := ""
	if m := runFlagRE.FindStringSubmatch(args); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				runPat = g
				break
			}
		}
	}
	log := ""
	if m := teeRE.FindStringSubmatch(line); m != nil {
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
