package main

// Tests for the aihub#508 check: a ci.yml step's `--- PASS:` assertions must
// name test paths that exist and that the step's own `-run` selects.
//
// The fixtures below are the measured incident (a renamed subtest) plus the
// shapes that could make the check itself lie — a shared log, a table-driven
// name, a concatenated name, an assertion inside a quoted string. Each one is
// red against a check that is missing the corresponding piece, which is why
// they are written as separate fixtures rather than folded into one workflow.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertFixture writes one package and one workflow, then returns the verdict:
// the stale-name problems, and the scan for the parse-level ones.
func assertFixture(t *testing.T, source, workflow string) ([]string, *WorkflowScan) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "internal", "domain", "x_test.go"), source)
	scan, err := ParseWorkflow([]byte(workflow), testModule)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	trees, err := collectTestNames(dir, testModule)
	if err != nil {
		t.Fatalf("collectTestNames: %v", err)
	}
	problems, err := checkAssertedNames(scan, trees)
	if err != nil {
		t.Fatalf("checkAssertedNames: %v", err)
	}
	return problems, scan
}

// dbStep wraps a script in a DB step, since every fixture needs the same
// envelope.
func dbStep(script string) string {
	var b strings.Builder
	b.WriteString("jobs:\n  test:\n    steps:\n      - name: db suite\n        env:\n" +
		"          AIHUB_TEST_DB: postgres://x\n        run: |\n          set -o pipefail\n")
	for _, line := range strings.Split(strings.TrimRight(script, "\n"), "\n") {
		b.WriteString("          " + line + "\n")
	}
	return b.String()
}

const twoSubtests = `package domain

import "testing"

func TestAlpha(t *testing.T) {
	t.Run("the first arm", func(t *testing.T) {})
	t.Run("the second arm", func(t *testing.T) {
		t.Run("a nested arm", func(t *testing.T) {})
	})
}
`

// The measured incident (2026-09-09, aihub#416's executor): the function is
// still there and still covered, the step's `-run` still selects it, `go test
// ./...` is green — and the step is red on the runner because the SUBTEST was
// renamed. Nothing in dbtestcov could see it before this check.
func TestCheckAssertedNames_CatchesARenamedSubtest(t *testing.T) {
	problems, _ := assertFixture(t, twoSubtests, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
grep -q -- '--- PASS: TestAlpha/the_first_arm' a.log || exit 1
grep -q -- '--- PASS: TestAlpha/the_arm_it_used_to_be_called' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(problems) != 1 {
		t.Fatalf("want exactly the stale name reported, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "the_arm_it_used_to_be_called") {
		t.Errorf("the report does not name the stale subtest: %s", problems[0])
	}
	if !strings.Contains(problems[0], "no test of that name exists") {
		t.Errorf("the report does not say the name is absent: %s", problems[0])
	}
}

// A want-list is the same assertion written as a loop, and it is how nearly
// every step in ci.yml writes it — so the expansion has to be part of the
// check, not a special case bolted on. The good entries must stay silent.
func TestCheckAssertedNames_ExpandsAWantList(t *testing.T) {
	problems, scan := assertFixture(t, twoSubtests, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
for want in 'TestAlpha/the_first_arm' \
            'TestAlpha/the_second_arm' \
            'TestAlpha/the_second_arm/a_nested_arm' \
            'TestAlpha/a_third_arm_nobody_wrote'; do
  grep -q -- "--- PASS: $want" a.log \
    || { echo "::error::$want did not run"; exit 1; }
done
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(scan.Assertions) != 4 {
		t.Fatalf("the want-list expanded to %d assertions, want 4: %+v", len(scan.Assertions), scan.Assertions)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "a_third_arm_nobody_wrote") {
		t.Fatalf("want only the invented entry reported, got %v", problems)
	}
	// The message must name the want-list VALUE: the text on the line is
	// `$want`, which identifies nothing to whoever has to fix it.
	if !strings.Contains(problems[0], "want-list entry") {
		t.Errorf("the report does not attribute the failure to a want-list entry: %s", problems[0])
	}
}

// The enumeration has to be a TREE, not a flat set of names. `go test` prints
// every descendant under its ancestors' full path, so `TestAlpha/a_nested_arm`
// — the leaf with its parent segment dropped — is a line that never appears.
// Accepting it would leave exactly one class of stale assertion uncaught: the
// one where an intermediate subtest was removed or renamed and the leaf was
// not. Measured: this fixture goes GREEN if collectSubtests descends into a
// `t.Run` body while ALSO recursing into it, which registers grandchildren
// twice — the one mutant of that function no other test here can see.
func TestCheckAssertedNames_AFlattenedPathIsNotAPath(t *testing.T) {
	problems, _ := assertFixture(t, twoSubtests, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
grep -q -- '--- PASS: TestAlpha/the_second_arm/a_nested_arm' a.log || exit 1
grep -q -- '--- PASS: TestAlpha/a_nested_arm' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(problems) != 1 {
		t.Fatalf("want only the flattened path reported, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "'--- PASS: TestAlpha/a_nested_arm'") &&
		!strings.Contains(problems[0], `"--- PASS: TestAlpha/a_nested_arm"`) {
		t.Errorf("the report does not name the flattened path: %s", problems[0])
	}
}

// A name that exists but is not selected is a different defect with a different
// fix, and reporting it as "no such test" is how a correct assertion gets
// deleted instead of the `-run` being widened.
func TestCheckAssertedNames_ReportsANameTheRunDoesNotSelect(t *testing.T) {
	source := twoSubtests + `
func TestBeta(t *testing.T) {
	t.Run("beta arm", func(t *testing.T) {})
}
`
	problems, _ := assertFixture(t, source, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
grep -q -- '--- PASS: TestBeta/beta_arm' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	if !strings.Contains(problems[0], "`-run` does not select it") {
		t.Errorf("the report does not distinguish an unselected name: %s", problems[0])
	}
}

// The same distinction one level up: selected by the `-run` but in a package
// the step does not run.
func TestCheckAssertedNames_ReportsANameFromAnotherPackage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "internal", "domain", "x_test.go"), twoSubtests)
	writeFile(t, filepath.Join(dir, "internal", "server", "y_test.go"), `package server

import "testing"

func TestGamma(t *testing.T) {
	t.Run("gamma arm", func(t *testing.T) {})
}
`)
	wf := dbStep(`
go test ./internal/domain/ -count=1 -v 2>&1 | tee a.log
grep -q -- '--- PASS: TestGamma/gamma_arm' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow([]byte(wf), testModule)
	if err != nil {
		t.Fatal(err)
	}
	trees, err := collectTestNames(dir, testModule)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := checkAssertedNames(scan, trees)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "not in a package this step runs") {
		t.Fatalf("want the wrong-package diagnosis, got %v", problems)
	}
}

// ci.yml's aihub#148 step runs two `go test` commands into ONE log (`tee` then
// `tee -a`) and asserts across both. Attributing its assertions to the first
// invocation alone reported three perfectly good names as unrunnable — measured
// while writing this check, and the reason invocationsFor returns a slice.
func TestCheckAssertedNames_SharedLogUnionsBothInvocations(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "internal", "domain", "x_test.go"), twoSubtests)
	writeFile(t, filepath.Join(dir, "internal", "server", "y_test.go"), `package server

import "testing"

func TestGamma(t *testing.T) {
	t.Run("gamma arm", func(t *testing.T) {})
}
`)
	wf := dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
go test ./internal/server/ -run '^TestGamma$' -count=1 -v 2>&1 | tee -a a.log
grep -q -- '--- PASS: TestAlpha/the_first_arm' a.log || exit 1
grep -q -- '--- PASS: TestGamma/gamma_arm' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow([]byte(wf), testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 2 {
		t.Fatalf("want both invocations parsed, got %+v", scan.Invocations)
	}
	trees, err := collectTestNames(dir, testModule)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := checkAssertedNames(scan, trees)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("both assertions are satisfied by one of the two invocations; got %v", problems)
	}
}

// The aihub#316 shape: a `||` chain where the first pattern is expected to miss
// and the fallback carries the match. Checking the alternatives independently
// would report a stale name for a chain the shell is perfectly happy with —
// and the patterns end in " (", which is a LITERAL open paren in grep's basic
// regexp dialect and a syntax error in Go's.
func TestCheckAssertedNames_OrChainIsSatisfiedByEitherAlternative(t *testing.T) {
	problems, _ := assertFixture(t, twoSubtests, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
for want in '/a_nested_arm (' ; do
  grep -q -- "--- PASS: TestAlpha$want" a.log \
    || grep -q -- "--- PASS: .*$want" a.log \
    || { echo "::error::$want did not run"; exit 1; }
done
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(problems) != 0 {
		t.Fatalf("the fallback alternative matches the nested arm; got %v", problems)
	}
}

// Table-driven arms are how this repository names most subtests, and three
// spellings are all in the tree: a struct field, a map key and a plain string
// element. If any of them were unresolvable, "unverifiable" would be the
// majority answer and the check would be worth little.
func TestCollectTestNames_ResolvesTableDrivenArms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "internal", "domain", "x_test.go"), `package domain

import "testing"

func TestTables(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
	}{
		{name: "a struct field arm", n: 1},
		{"a positional arm", 2},
	} {
		t.Run(tc.name, func(t *testing.T) { _ = tc.n })
		t.Run("prefixed_"+tc.name, func(t *testing.T) {})
	}
	for key := range map[string]int{"a map key arm": 1} {
		t.Run(key, func(t *testing.T) {})
	}
	for _, s := range []string{"a string element arm"} {
		t.Run(s, func(t *testing.T) {})
	}
	cases := []struct{ label string }{{label: "a named table arm"}}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {})
	}
}
`)
	trees, err := collectTestNames(dir, testModule)
	if err != nil {
		t.Fatal(err)
	}
	lines, opaque, err := testPaths(trees, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"TestTables/a_struct_field_arm",
		"TestTables/a_positional_arm",
		"TestTables/prefixed_a_struct_field_arm",
		"TestTables/a_map_key_arm",
		"TestTables/a_string_element_arm",
		"TestTables/a_named_table_arm",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was not enumerated; got:\n%s", want, got)
		}
	}
	if len(opaque) != 0 {
		t.Errorf("nothing here is computed at run time, yet it reported %v", opaque)
	}
}

// A name this command cannot fold must be reported as UNVERIFIABLE, not as
// absent. The two need different fixes, and calling the first one "absent"
// invites deleting an assertion that is perfectly correct.
func TestCheckAssertedNames_ComputedNameIsUnverifiableNotAbsent(t *testing.T) {
	source := `package domain

import (
	"fmt"
	"testing"
)

func TestAlpha(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("arm %d", i), func(t *testing.T) {})
	}
}
`
	problems, _ := assertFixture(t, source, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
grep -q -- '--- PASS: TestAlpha/arm_1' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	if !strings.Contains(problems[0], "cannot fold") {
		t.Errorf("the report does not say the name is unverifiable: %s", problems[0])
	}
	if strings.Contains(problems[0], "no test of that name exists in the tree") {
		t.Errorf("an unverifiable name must not be reported as absent: %s", problems[0])
	}
}

// The twin of the test above, and it is the one that decides whether either
// verdict is worth reading. internal/domain really does hold two dozen computed
// `t.Run` names, so "some test somewhere has an unfoldable name" is true on
// EVERY failure — collect opacity from the candidate set and the aihub#416
// rename is reported as "may exist", which is both wrong and the wrong
// instruction. Opacity therefore has to come from the function the assertion
// names. Measured: this fixture reported "may exist" before opaqueUnder scoped
// it, with the noise coming from a function the assertion never mentions.
func TestCheckAssertedNames_OpacityIsAttributedToTheNamedFunction(t *testing.T) {
	source := `package domain

import (
	"fmt"
	"testing"
)

func TestAlpha(t *testing.T) {
	t.Run("the first arm", func(t *testing.T) {})
}

func TestNoisy(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("arm %d", i), func(t *testing.T) {})
	}
}
`
	problems, _ := assertFixture(t, source, dbStep(`
go test ./internal/domain/ -count=1 -v 2>&1 | tee a.log
grep -q -- '--- PASS: TestAlpha/an_arm_that_was_deleted' a.log || exit 1
grep -q -- '--- PASS: TestNoisy' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	if !strings.Contains(problems[0], "no test of that name exists in the tree") {
		t.Errorf("TestAlpha's names are all literal, so the verdict must be definite: %s", problems[0])
	}
	if strings.Contains(problems[0], "cannot fold") {
		t.Errorf("opacity from a function the assertion does not name leaked into the verdict: %s", problems[0])
	}
}

// An assertion spelled in a way this command cannot read is an UNCHECKED
// assertion, so "dbtestcov did not understand this line" must not have the
// same observable as "this line is fine". Each of these mentions the marker
// while asserting the opposite of, or nothing about, what it appears to.
func TestCollectPassAssertions_RefusesUnreadableSpellings(t *testing.T) {
	for _, tc := range []struct {
		name, line string
	}{
		{"a trailing || true can never fail", `grep -q -- '--- PASS: TestAlpha' a.log || true`},
		{"an inverted grep requires a failure", `! grep -q -- '--- PASS: TestAlpha' a.log || exit 1`},
		{"exit 0 makes the tail succeed", `grep -q -- '--- PASS: TestAlpha' a.log || { echo x; exit 0; }`},
		{"no failure tail at all", `grep -q -- '--- PASS: TestAlpha' a.log`},
		{"an unquoted pattern is not modelled", `grep -q -- --- PASS: TestAlpha a.log || exit 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, scan := assertFixture(t, twoSubtests, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
`+tc.line+`
! grep -q -- '--- SKIP' a.log || exit 1
`))
			if len(scan.AssertionProblems) == 0 {
				t.Errorf("the line was accepted or ignored rather than reported: %s", tc.line)
			}
			if len(scan.Assertions) != 0 {
				t.Errorf("the line was read as a working assertion: %+v", scan.Assertions)
			}
		})
	}
}

// A step's prose is not an assertion. One quoted string can carry the marker,
// a name and a log, so reading lines instead of tracking quote state would let
// a reproduction hint stand in for the assertion it describes — the same
// complete bypass the `go test` side of this parser already refuses.
func TestCollectPassAssertions_ProseInAQuotedStringIsNotAnAssertion(t *testing.T) {
	_, scan := assertFixture(t, twoSubtests, dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
grep -q -- '--- PASS: TestAlpha/the_first_arm' a.log || exit 1
echo "to reproduce, run:
grep -q -- '--- PASS: TestAlpha/a_name_that_does_not_exist' a.log || exit 1
"
! grep -q -- '--- SKIP' a.log || exit 1
`))
	if len(scan.Assertions) != 1 {
		t.Fatalf("want only the real assertion, got %+v", scan.Assertions)
	}
	if len(scan.AssertionProblems) != 0 {
		t.Errorf("quoted prose was reported as an unreadable assertion: %v", scan.AssertionProblems)
	}
}

// The escape hatch this closes: once a stale name is red, the cheapest answer
// is to delete the want-list entry rather than fix it. An invocation nothing
// asserts on is therefore a failure of its own. All 81 invocations in ci.yml
// carried an assertion when this was added, so it costs nothing but the hatch.
func TestParseWorkflow_InvocationWithNoPassAssertionIsReported(t *testing.T) {
	wf := dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow([]byte(wf), testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Unasserted) != 1 {
		t.Fatalf("want the unasserted invocation reported, got %v", scan.Unasserted)
	}
	if !strings.Contains(scan.Unasserted[0], "a.log") {
		t.Errorf("the report does not name the log: %s", scan.Unasserted[0])
	}
}

// Per invocation, not per step: a step with two `go test` lines and one
// assertion must not have the second credited by the first's want-list. Same
// shape as the SKIP guard's per-invocation rule, one level finer.
func TestParseWorkflow_PassAssertionIsRequiredPerInvocation(t *testing.T) {
	wf := dbStep(`
go test ./internal/domain/ -run '^TestAlpha$' -count=1 -v 2>&1 | tee a.log
go test ./internal/domain/ -run '^TestBeta$' -count=1 -v 2>&1 | tee b.log
grep -q -- '--- PASS: TestAlpha' a.log || exit 1
! grep -q -- '--- SKIP' a.log || exit 1
! grep -q -- '--- SKIP' b.log || exit 1
`)
	scan, err := ParseWorkflow([]byte(wf), testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Unasserted) != 1 || !strings.Contains(scan.Unasserted[0], "b.log") {
		t.Fatalf("want only b.log reported as unasserted, got %v", scan.Unasserted)
	}
}

// `go test` rewrites a subtest name before printing it, and the space class is
// wider than ' ' — the em-dash group names in internal/domain's
// embedding-budget test sit right next to those runes.
func TestRewriteTestName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a named subtest", "a_named_subtest"},
		{"budget below the deadline — all three fall back", "budget_below_the_deadline_—_all_three_fall_back"},
		{"tab\tand nbsp here", "tab_and_nbsp_here"},
		{"a bell\a", `a_bell\a`},
		{"already_underscored", "already_underscored"},
	} {
		if got := rewriteTestName(tc.in); got != tc.want {
			t.Errorf("rewriteTestName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// grep's default dialect is a BASIC regexp, where `(`, `)`, `+`, `?` and `|`
// are ordinary characters. Compiling those as Go regexps is a syntax error,
// and a gate that crashes is indistinguishable from a gate that was removed.
func TestGrepMatcher_BasicRegexpMetacharacters(t *testing.T) {
	m, err := grepMatcher("--- PASS: TestAlpha (", false)
	if err != nil {
		t.Fatalf("a literal paren must be accepted: %v", err)
	}
	if !m("--- PASS: TestAlpha (0.00s)") {
		t.Error("the pattern did not match the line it describes")
	}
	if m("--- PASS: TestAlphaBeta (0.00s)") {
		t.Error("the open paren must be matched literally, so the longer name must not match")
	}
	star, err := grepMatcher("--- PASS: .*/a_leaf (", false)
	if err != nil {
		t.Fatal(err)
	}
	if !star("    --- PASS: TestAlpha/group/a_leaf (0.00s)") {
		t.Error("`.*` must keep its regexp meaning")
	}
	if _, err := grepMatcher(`--- PASS: Test\(x\)`, false); err == nil {
		t.Error("a backslash escape diverges between the two dialects and must be refused, not guessed at")
	}
	fixed, err := grepMatcher("--- PASS: .*", true)
	if err != nil {
		t.Fatal(err)
	}
	if fixed("--- PASS: TestAlpha (0.00s)") {
		t.Error("grep -F is a literal substring, so `.*` must not match anything else")
	}
}

// A `-run` constrains every element of a test path, not just the first, so a
// pattern with a second element makes an assertion on any other subtest
// unsatisfiable. MatchesRun answers the first element only, which is the right
// question for coverage and the wrong one here.
func TestMatchesRunPath(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		segs    []string
		want    bool
	}{
		{"^TestAlpha$", []string{"TestAlpha"}, true},
		{"^TestAlpha$", []string{"TestAlpha", "anything"}, true},
		{"^TestAlpha$/^only$", []string{"TestAlpha", "only"}, true},
		{"^TestAlpha$/^only$", []string{"TestAlpha", "other"}, false},
		{"^TestAlpha$/^only$", []string{"TestAlpha"}, true},
		{"TestA/x|TestB", []string{"TestB", "y"}, true},
	} {
		got, err := matchesRunPath(tc.pattern, tc.segs)
		if err != nil {
			t.Fatalf("-run %q: %v", tc.pattern, err)
		}
		if got != tc.want {
			t.Errorf("matchesRunPath(%q, %v) = %v, want %v", tc.pattern, tc.segs, got, tc.want)
		}
	}
}

// The gate against the real inputs. This is the arm that would have caught the
// aihub#416 incident, so it is also the arm that must stay green: any failure
// here is a real ci.yml assertion that names a test the tree does not have.
func TestCheckAssertedNames_RealCIWorkflow(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	module, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ParseWorkflow(data, module)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	// Ratchet, not an exact count: raise it when you add assertions.
	if len(scan.Assertions) < 400 {
		t.Errorf("only %d %q assertions found in ci.yml, floor is 400 — the extractor has stopped seeing want-lists "+
			"that are there, which makes this whole check quietly vacuous", len(scan.Assertions), strings.TrimSpace(passMarker))
	}
	for _, p := range scan.AssertionProblems {
		t.Errorf("ci.yml asserts on %q in a form dbtestcov cannot read: %s", strings.TrimSpace(passMarker), p)
	}
	for _, p := range scan.Unasserted {
		t.Errorf("a DB invocation in ci.yml records nothing about WHICH tests it ran: %s", p)
	}
	trees, err := collectTestNames(root, module)
	if err != nil {
		t.Fatalf("collectTestNames: %v", err)
	}
	problems, err := checkAssertedNames(scan, trees)
	if err != nil {
		t.Fatalf("checkAssertedNames: %v", err)
	}
	for _, p := range problems {
		t.Errorf("stale ci.yml assertion: %s", p)
	}
}
