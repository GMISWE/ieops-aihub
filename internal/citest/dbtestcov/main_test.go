package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are pure — no database, no network — so they run on ci.yml's
// "Unit tests" step. That matters: dbtestcov is the thing that decides whether
// CI's DB coverage is complete, so if its own parsing silently degraded (found
// no invocations, or classified nothing as DB-gated) the gate would go quiet in
// the same way the defect it exists to catch does.

const testModule = "example.com/m"

func joined(alts [][]string) []string {
	out := make([]string, 0, len(alts))
	for _, a := range alts {
		out = append(out, strings.Join(a, "\x1f"))
	}
	return out
}

func TestSplitRunPattern(t *testing.T) {
	// Expectations mirror testing.splitRegexp in $GOROOT/src/testing/match.go:
	// one entry per alternative, elements joined by \x1f for comparison.
	cases := []struct {
		in   string
		want []string
	}{
		{"TestFoo", []string{"TestFoo"}},
		{"TestFoo/sub", []string{"TestFoo\x1fsub"}},
		// Top-level '|' splits into ALTERNATIVES, it does not stay in one regexp.
		{"TestA|TestB", []string{"TestA", "TestB"}},
		// The case that made the old '/'-only split wrong: real go test selects
		// top-level TestA *and* top-level TestC here.
		{"TestA/b|TestC", []string{"TestA\x1fb", "TestC"}},
		// '/' and '|' inside a group belong to the element.
		{"Test(A/B)", []string{"Test(A/B)"}},
		{"Test(A|B)", []string{"Test(A|B)"}},
		// ...and inside a character class.
		{"Test[a/b]", []string{"Test[a/b]"}},
		{"Test[a|b]", []string{"Test[a|b]"}},
		// ...and when escaped.
		{`Test\/x`, []string{`Test\/x`}},
		{`Test\|x`, []string{`Test\|x`}},
		{"^(TestA|TestB)$", []string{"^(TestA|TestB)$"}},
		{"TestA/x/y", []string{"TestA\x1fx\x1fy"}},
	}
	for _, c := range cases {
		got := joined(SplitRunPattern(c.in))
		if len(got) != len(c.want) {
			t.Errorf("SplitRunPattern(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("SplitRunPattern(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}

func TestMatchesRun(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		// go test's -run is UNANCHORED: a prefix selects everything under it.
		// This is why "TestDeleteDependency" covers both
		// TestDeleteDependency_LastBlockerRemoved_Requeues and its sibling.
		{"TestDeleteDependency", "TestDeleteDependency_LastBlockerRemoved_Requeues", true},
		{"TestRecallRouter", "TestRecallRouterMixedUnionReturnsBothHalves", true},
		// ...and it is a substring match, not just a prefix match.
		{"UpdateWorkItemCAS", "TestUpdateWorkItemCASVersionAdvancesAcrossWrites", true},
		// Alternation.
		{"TestHandleRecall_TotalAndLimitAlias|TestHandleGetMemory", "TestHandleGetMemory_NotFound", true},
		{"TestHandleUpdateStep_(NextStep|FusedEquals|FusedRespects)", "TestHandleUpdateStep_FusedEqualsTwoCalls", true},
		// The exact miss that made aihub#303's sharpest example invisible: a
		// sibling in the same file, outside the alternation.
		{"TestHandleUpdateStep_(NextStep|FusedEquals|FusedRespects)", "TestHandleUpdateStep_ArtifactSummary", false},
		// Anchored form used by the aihub#303 step.
		{"^(TestBackfillLatestID|TestGetLatestByID)$", "TestBackfillLatestID", true},
		{"^(TestBackfillLatestID|TestGetLatestByID)$", "TestBackfillLatestID_RedactedHead", false},
		// Only the first element of an alternative constrains a top-level name.
		{"TestFoo/sub", "TestFoo", true},
		{"TestFoo/sub", "TestBar", false},
		// A top-level '|' AFTER a '/' still selects its own top-level test.
		{"TestFoo/sub|TestBar", "TestBar", true},
		{"TestFoo/sub|TestBar", "TestFoo", true},
		{"TestFoo/sub|TestBar", "TestQux", false},
	}
	for _, c := range cases {
		got, err := MatchesRun(c.pattern, c.name)
		if err != nil {
			t.Errorf("MatchesRun(%q, %q): %v", c.pattern, c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("MatchesRun(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
	if _, err := MatchesRun("Test(", "TestFoo"); err == nil {
		t.Error("MatchesRun with an invalid regexp: want error, got nil")
	}
}

// jsonEvents renders a go test -json stream from (action, test, output) triples.
func jsonEvents(t *testing.T, pkg string, evs [][3]string) string {
	t.Helper()
	var b strings.Builder
	for _, e := range evs {
		line, err := json.Marshal(testEvent{Action: e[0], Package: pkg, Test: e[1], Output: e[2]})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestParseInventory(t *testing.T) {
	const pkg = "example.com/m/internal/domain"
	stream := jsonEvents(t, pkg, [][3]string{
		// A plain DB-gated skip.
		{"output", "TestNeedsDB", "=== RUN   TestNeedsDB\n"},
		{"output", "TestNeedsDB", "    x_test.go:27: set AIHUB_TEST_DB to run this integration test\n"},
		{"output", "TestNeedsDB", "--- SKIP: TestNeedsDB (0.00s)\n"},
		{"skip", "TestNeedsDB", ""},
		// A DB-gated skip that ALSO needs another variable.
		{"output", "TestNeedsDBAndEmbedding", "=== RUN   TestNeedsDBAndEmbedding\n"},
		{"output", "TestNeedsDBAndEmbedding", "    y_test.go:27: set AIHUB_TEST_DB and EMBEDDING_BASE_URL to run the live vector test\n"},
		{"skip", "TestNeedsDBAndEmbedding", ""},
		// A skip for an unrelated reason: not DB-gated.
		{"output", "TestSlow", "    z_test.go:8: slow; set AIHUB_SLOW_TESTS=1 to run\n"},
		{"skip", "TestSlow", ""},
		// A passing test is never DB-gated, whatever its output said.
		{"output", "TestPasses", "    w_test.go:1: AIHUB_TEST_DB mentioned but not skipped\n"},
		{"pass", "TestPasses", ""},
		// Subtests are not test functions; -run selection is by the parent.
		{"output", "TestNeedsDB/sub", "    x_test.go:30: set AIHUB_TEST_DB to run this integration test\n"},
		{"skip", "TestNeedsDB/sub", ""},
	})

	got, err := ParseInventory(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ParseInventory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d gated tests %+v, want 2", len(got), got)
	}
	if got[0].Name != "TestNeedsDB" || len(got[0].ExtraEnv) != 0 {
		t.Errorf("got[0] = %+v, want TestNeedsDB with no extra env", got[0])
	}
	if got[1].Name != "TestNeedsDBAndEmbedding" ||
		len(got[1].ExtraEnv) != 1 || got[1].ExtraEnv[0] != "EMBEDDING_BASE_URL" {
		t.Errorf("got[1] = %+v, want TestNeedsDBAndEmbedding needing EMBEDDING_BASE_URL", got[1])
	}
	if got[0].Package != pkg {
		t.Errorf("package = %q, want %q", got[0].Package, pkg)
	}
}

// A SCREAMING_CASE name in go test's own framing lines must never be read as a
// second required environment variable — that would silently move the test out
// of the required set, which is the failure mode this whole command exists to
// prevent.
//
// The name has to be a SUBTEST's for this to be a real probe, and that took a
// review to notice. A Go test FUNCTION name can never match envVarRE: it must
// start with the literal "Test", so the regexp's leading `\b[A-Z][A-Z0-9]*_`
// can only ever match the "T", and the required underscore never arrives. A
// name like TestHTTP_API_Thing therefore proves nothing — the assertion below
// held whether or not skipReason stripped anything, which is a test that cannot
// fail. A SUBTEST name is author-chosen and unconstrained, and it appears after
// a '/' in the framing lines, which supplies the word boundary. So
// `--- SKIP: TestNeedsDB/PATCH_ONLY` really would contribute "PATCH_ONLY" as a
// phantom second requirement if skipReason stopped dropping the framing lines.
//
// Verified by mutation: deleting the `=== `/`--- ` strip in skipReason turns
// this test red (ExtraEnv = [PATCH_ONLY]), while the TestHTTP_API_Thing form it
// replaces stayed green under that same mutant.
func TestParseInventory_FramingLineNameIsNotMistakenForAnEnvVar(t *testing.T) {
	stream := jsonEvents(t, "p", [][3]string{
		{"output", "TestNeedsDB", "=== RUN   TestNeedsDB\n"},
		{"output", "TestNeedsDB", "=== RUN   TestNeedsDB/PATCH_ONLY\n"},
		{"output", "TestNeedsDB", "    a_test.go:1: set AIHUB_TEST_DB to run this integration test\n"},
		{"output", "TestNeedsDB", "    --- SKIP: TestNeedsDB/PATCH_ONLY (0.00s)\n"},
		{"output", "TestNeedsDB", "--- SKIP: TestNeedsDB (0.00s)\n"},
		{"skip", "TestNeedsDB", ""},
	})
	got, err := ParseInventory(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if len(got[0].ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v, want empty (go test's own framing lines must be stripped before the env-var scan)", got[0].ExtraEnv)
	}

	// Positive control, so the assertion above cannot pass merely because the
	// scan found nothing anywhere: the SAME token in the test's own skip
	// message IS picked up.
	stream = jsonEvents(t, "p", [][3]string{
		{"output", "TestNeedsDB", "=== RUN   TestNeedsDB\n"},
		{"output", "TestNeedsDB", "    a_test.go:1: set AIHUB_TEST_DB and PATCH_ONLY to run this test\n"},
		{"output", "TestNeedsDB", "--- SKIP: TestNeedsDB (0.00s)\n"},
		{"skip", "TestNeedsDB", ""},
	})
	got, err = ParseInventory(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].ExtraEnv) != 1 || got[0].ExtraEnv[0] != "PATCH_ONLY" {
		t.Errorf("positive control: got %+v, want PATCH_ONLY read out of the skip MESSAGE", got)
	}
}

func TestParseWorkflow(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: Unit tests
        run: go test -count=1 -race ./...
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          set -o pipefail
          go test ./internal/domain/ -run 'TestAlpha|TestBeta' -count=1 -v 2>&1 | tee a.log
          grep -q -- "--- PASS: TestAlpha" a.log || exit 1
          ! grep -q -- '--- SKIP' a.log || exit 1
      - name: inline env
        run: |
          AIHUB_TEST_DB=postgres://y go test ./internal/server/ -run "TestGamma" -v 2>&1 | tee b.log
          ! grep -q -- '--- SKIP' b.log || exit 1
      - name: commented out
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          # go test ./internal/domain/ -run 'TestEpsilon' -v 2>&1 | tee e.log
          echo nothing to do
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	if len(scan.Invocations) != 2 {
		t.Fatalf("got %d invocations %+v, want 2", len(scan.Invocations), scan.Invocations)
	}
	inv := scan.Invocations[0]
	if inv.Run != "TestAlpha|TestBeta" || inv.Packages[0].Path != testModule+"/internal/domain" || inv.Log != "a.log" {
		t.Errorf("invocation 0 = %+v", inv)
	}
	// A step that only mentions the variable in a `VAR=... go test` prefix
	// still runs with a database, so it still counts as coverage.
	if scan.Invocations[1].Run != "TestGamma" || scan.Invocations[1].Packages[0].Path != testModule+"/internal/server" {
		t.Errorf("invocation 1 = %+v", scan.Invocations[1])
	}
	// The "Unit tests" step does not set AIHUB_TEST_DB, so it contributes no
	// coverage even though it runs every package. That asymmetry IS the bug
	// this gate guards.
	for _, inv := range scan.Invocations {
		if inv.Step == "Unit tests" {
			t.Errorf("the DB-free Unit tests step must contribute no coverage, got %+v", inv)
		}
		if inv.Run == "TestEpsilon" {
			t.Errorf("a commented-out invocation must not count as coverage, got %+v", inv)
		}
	}
	if len(scan.Unguarded) != 0 {
		t.Errorf("Unguarded = %v, want none", scan.Unguarded)
	}
	if len(scan.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", scan.Dropped)
	}
}

// The guard must be checked per INVOCATION, not per step: a step with three
// `go test` lines and one SKIP grep would otherwise credit coverage for all
// three while asserting on only one — the same false-green shape this command
// exists to close.
func TestParseWorkflow_GuardIsPerInvocationNotPerStep(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
          go test ./internal/server/ -run 'TestBeta' -v 2>&1 | tee b.log
          echo "no guard for b.log"
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 2 {
		t.Fatalf("want 2 invocations, got %+v", scan.Invocations)
	}
	if len(scan.Unguarded) != 1 || !strings.Contains(scan.Unguarded[0], "b.log") {
		t.Errorf("Unguarded = %v, want exactly the b.log invocation", scan.Unguarded)
	}
}

// A guard that greps a DIFFERENT log than the one the invocation wrote asserts
// nothing about it.
func TestParseWorkflow_GuardMustNameTheInvocationsOwnLog(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' other.log || exit 1
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Unguarded) != 1 {
		t.Errorf("Unguarded = %v, want the a.log invocation reported", scan.Unguarded)
	}
}

// A log name that is a suffix of another must not satisfy the guard by accident
// (ci.yml really has recall_unmatched.log next to recall_unmatched_http.log).
func TestParseWorkflow_GuardLogNameIsNotASubstringMatch(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee x.log
          ! grep -q -- '--- SKIP' prefix_x.log || exit 1
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Unguarded) != 1 {
		t.Errorf("Unguarded = %v, want x.log reported (prefix_x.log must not satisfy it)", scan.Unguarded)
	}
}

// An invocation that captures no output cannot be asserted on at all.
func TestParseWorkflow_NoTeeIsUnguarded(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: go test ./internal/domain/ -run 'TestAlpha' -v
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Unguarded) != 1 || !strings.Contains(scan.Unguarded[0], "captures no output") {
		t.Errorf("Unguarded = %v, want the no-tee invocation reported", scan.Unguarded)
	}
}

func TestParseWorkflow_RejectsSkipFlag(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'Test' -skip 'TestSlow' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(wf, testModule); err == nil {
		t.Fatal("want an error for an unmodelled -skip flag, got nil")
	}
}

// Coverage that only happens on some events is not coverage a pull request can
// rely on. Refusing to model it is safer than crediting it.
func TestParseWorkflow_RejectsConditionalDBSteps(t *testing.T) {
	stepIf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        if: github.event_name == 'push'
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(stepIf, testModule); err == nil {
		t.Error("want an error for a DB step carrying if:, got nil")
	}

	jobIf := []byte(`
jobs:
  test:
    if: startsWith(github.ref, 'refs/tags/v')
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(jobIf, testModule); err == nil {
		t.Error("want an error for a DB step in a conditional job, got nil")
	}

	coe := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        continue-on-error: true
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(coe, testModule); err == nil {
		t.Error("want an error for continue-on-error: true, got nil")
	}

	// continue-on-error: false is the default and must NOT be rejected.
	okWF := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        continue-on-error: false
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(okWF, testModule); err != nil {
		t.Errorf("continue-on-error: false must be accepted, got %v", err)
	}
}

// A non-DB job may carry if: without tripping the check — only DB steps are
// unmodellable when conditional.
func TestParseWorkflow_ConditionalNonDBJobIsFine(t *testing.T) {
	wf := []byte(`
jobs:
  release:
    if: startsWith(github.ref, 'refs/tags/v')
    steps:
      - name: build
        run: go build ./...
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatalf("a conditional job with no DB step must be fine, got %v", err)
	}
	if len(scan.Invocations) != 1 {
		t.Errorf("invocations = %+v, want 1", scan.Invocations)
	}
}

func TestParseWorkflow_NoRunFlagCoversWholePackage(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: everything
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./... -count=1 -v 2>&1 | tee all.log
          ! grep -q -- '--- SKIP' all.log || exit 1
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 1 || scan.Invocations[0].Run != "" {
		t.Fatalf("invocations = %+v, want one with an empty -run", scan.Invocations)
	}
	ok, err := coveredBy(scan.Invocations, GatedTest{Package: testModule + "/internal/domain", Name: "TestAnything"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("a ./... invocation with no -run must cover every test in every package")
	}
}

// An invocation whose package argument cannot be resolved credits no coverage.
// That direction is safe, but it must be VISIBLE, not silent.
func TestParseWorkflow_UnresolvablePackageIsReported(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test $PKG -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 0 {
		t.Errorf("invocations = %+v, want none", scan.Invocations)
	}
	if len(scan.Dropped) != 1 || !strings.Contains(scan.Dropped[0], "$PKG") {
		t.Errorf("Dropped = %v, want the $PKG line reported", scan.Dropped)
	}
}

func TestCutAtShellOperator(t *testing.T) {
	cases := []struct{ in, want string }{
		{` ./internal/domain/ -run 'TestA|TestB' -count=1 -v 2>&1 | tee a.log`, ` ./internal/domain/ -run 'TestA|TestB' -count=1 -v 2`},
		{` ./x -run "^(TestA|TestB)$" -v`, ` ./x -run "^(TestA|TestB)$" -v`},
		{` ./x -v && echo done`, ` ./x -v `},
		{` ./x -v; echo done`, ` ./x -v`},
		{` ./x -v > out.txt`, ` ./x -v `},
		{` ./x -run 'TestA'`, ` ./x -run 'TestA'`},
	}
	for _, c := range cases {
		if got := cutAtShellOperator(c.in); got != c.want {
			t.Errorf("cutAtShellOperator(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolvePackage(t *testing.T) {
	cases := []struct {
		arg      string
		want     string
		wildcard bool
		ok       bool
	}{
		{"./internal/domain/", testModule + "/internal/domain", false, true},
		{"./internal/domain", testModule + "/internal/domain", false, true},
		{"./...", testModule + "/", true, true},
		{"./internal/...", testModule + "/internal/", true, true},
		{".", testModule, false, true},
		{"./internal/d.../x", "", false, false},
	}
	for _, c := range cases {
		got, ok := resolvePackage(c.arg, testModule)
		if ok != c.ok {
			t.Errorf("resolvePackage(%q) ok = %v, want %v", c.arg, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Path != c.want || got.Wildcard != c.wildcard {
			t.Errorf("resolvePackage(%q) = %+v, want {%q %v}", c.arg, got, c.want, c.wildcard)
		}
	}
}

func TestPackageSelMatches(t *testing.T) {
	exact := PackageSel{Path: testModule + "/internal/domain"}
	if !exact.Matches(testModule + "/internal/domain") {
		t.Error("exact selector must match its own path")
	}
	if exact.Matches(testModule + "/internal/domainx") {
		t.Error("exact selector must not match a longer path")
	}
	wild := PackageSel{Path: testModule + "/internal/", Wildcard: true}
	if !wild.Matches(testModule + "/internal/domain") {
		t.Error("wildcard selector must match a package beneath it")
	}
	if wild.Matches(testModule + "/pkg/client") {
		t.Error("wildcard selector must not match outside its subtree")
	}
	if !wild.Matches(testModule + "/internal") {
		t.Error("wildcard selector must match the directory itself")
	}
}

// ------------------------------------------------- skip-message convention

func TestCheckSkipMessages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "good_test.go"), `package p

import (
	"os"
	"testing"
)

func setupGood(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
}
`)
	writeFile(t, filepath.Join(dir, "bad_test.go"), `package p

import (
	"os"
	"testing"
)

func setupBad(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("no test database configured")
	}
}

func setupSilent(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.SkipNow()
	}
}
`)
	// A skip guard on a DIFFERENT variable is none of this command's business.
	writeFile(t, filepath.Join(dir, "other_test.go"), `package p

import (
	"os"
	"testing"
)

func setupOther(t *testing.T) {
	if os.Getenv("AIHUB_SLOW_TESTS") == "" {
		t.Skip("slow; set AIHUB_SLOW_TESTS=1 to run")
	}
}
`)

	got, err := checkSkipMessages(dir)
	if err != nil {
		t.Fatalf("checkSkipMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d violations %v, want 2 (setupBad and setupSilent)", len(got), got)
	}
	joinedOut := strings.Join(got, "\n")
	for _, want := range []string{"setupBad", "setupSilent"} {
		if !strings.Contains(joinedOut, want) {
			t.Errorf("violations do not name %s:\n%s", want, joinedOut)
		}
	}
	if strings.Contains(joinedOut, "setupGood") || strings.Contains(joinedOut, "setupOther") {
		t.Errorf("violations wrongly include a compliant guard:\n%s", joinedOut)
	}
}

// The repo itself must satisfy the convention its classification rests on.
func TestCheckSkipMessages_RealRepo(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	got, err := checkSkipMessages(root)
	if err != nil {
		t.Fatalf("checkSkipMessages: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("this repo violates the skip-message convention dbtestcov depends on:\n  %s",
			strings.Join(got, "\n  "))
	}
}

// ---------------------------------------------------------------- run()

// The end-to-end shape of the gate, on synthetic inputs: one DB-gated test
// covered by a -run, one not. The uncovered one must be reported and the run
// must fail. This is the same assertion the CI mutation probe makes, in a form
// that costs no database.
func TestRun_ReportsAnUncoveredDBGatedTest(t *testing.T) {
	dir := t.TempDir()
	pkg := testModule + "/internal/domain"
	inv := filepath.Join(dir, "inv.json")
	writeFile(t, inv, jsonEvents(t, pkg, [][3]string{
		{"output", "TestCovered", "    a_test.go:1: set AIHUB_TEST_DB to run this integration test\n"},
		{"skip", "TestCovered", ""},
		{"output", "TestUncovered", "    a_test.go:2: set AIHUB_TEST_DB to run this integration test\n"},
		{"skip", "TestUncovered", ""},
	}))
	wf := filepath.Join(dir, "ci.yml")
	writeFile(t, wf, `
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run '^(TestCovered)$' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	gomod := filepath.Join(dir, "go.mod")
	writeFile(t, gomod, "module "+testModule+"\n\ngo 1.26.3\n")

	var out bytes.Buffer
	err := run(inv, wf, gomod, dir, 1, &out)
	if err == nil {
		t.Fatal("want a failure for the uncovered DB-gated test, got nil")
	}
	if !strings.Contains(err.Error(), "TestUncovered") {
		t.Errorf("error does not name the uncovered test: %v", err)
	}
	if strings.Contains(err.Error(), "TestCovered\n") {
		t.Errorf("error wrongly names the covered test: %v", err)
	}

	// Widening the -run to include it must make the same inputs pass.
	writeFile(t, wf, `
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run '^(TestCovered|TestUncovered)$' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	out.Reset()
	if err := run(inv, wf, gomod, dir, 1, &out); err != nil {
		t.Fatalf("want success once both tests are named, got %v", err)
	}
	if !strings.Contains(out.String(), "covered by a CI step: 2/2") {
		t.Errorf("summary did not report full coverage:\n%s", out.String())
	}
}

// An inventory taken with AIHUB_TEST_DB SET classifies nothing as DB-gated, so
// every check below it passes vacuously. The floor is what turns that into a
// hard failure instead of a green no-op.
func TestRun_FloorCatchesAVacuousInventory(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, "inv.json")
	writeFile(t, inv, jsonEvents(t, testModule+"/internal/domain", [][3]string{
		{"output", "TestNeedsDB", "    a_test.go:1: ok\n"},
		{"pass", "TestNeedsDB", ""},
	}))
	wf := filepath.Join(dir, "ci.yml")
	writeFile(t, wf, "jobs:\n  test:\n    steps: []\n")
	gomod := filepath.Join(dir, "go.mod")
	writeFile(t, gomod, "module "+testModule+"\n")

	var out bytes.Buffer
	err := run(inv, wf, gomod, dir, 5, &out)
	if err == nil {
		t.Fatal("want a failure when the inventory holds fewer gated tests than the floor, got nil")
	}
	if !strings.Contains(err.Error(), "floor is 5") {
		t.Errorf("error does not explain the floor: %v", err)
	}
}

// A test needing more than a database cannot be run by CI, so naming it in a
// -run would make that step SKIP and trip its own guard. The gate reports that
// rather than leaving it to be discovered as a confusing red.
func TestRun_RejectsCoveringATestThatNeedsExtraEnv(t *testing.T) {
	dir := t.TempDir()
	pkg := testModule + "/internal/domain"
	inv := filepath.Join(dir, "inv.json")
	writeFile(t, inv, jsonEvents(t, pkg, [][3]string{
		{"output", "TestLive", "    a_test.go:1: set AIHUB_TEST_DB and EMBEDDING_BASE_URL to run the live test\n"},
		{"skip", "TestLive", ""},
	}))
	gomod := filepath.Join(dir, "go.mod")
	writeFile(t, gomod, "module "+testModule+"\n")

	wf := filepath.Join(dir, "ci.yml")
	writeFile(t, wf, "jobs:\n  test:\n    steps: []\n")
	var out bytes.Buffer
	if err := run(inv, wf, gomod, dir, 1, &out); err != nil {
		t.Fatalf("an excluded test that no step names must be fine, got %v", err)
	}
	if !strings.Contains(out.String(), "EMBEDDING_BASE_URL") {
		t.Errorf("the exclusion must be reported, not silent:\n%s", out.String())
	}

	writeFile(t, wf, `
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run '^(TestLive)$' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	out.Reset()
	err := run(inv, wf, gomod, dir, 1, &out)
	if err == nil {
		t.Fatal("want a failure when a step names a test it cannot actually run, got nil")
	}
	if !strings.Contains(err.Error(), "TestLive") {
		t.Errorf("error does not name the test: %v", err)
	}
}

// An unrecognised second environment variable must be an ERROR, not a quiet
// exclusion — quietly shrinking the required set is exactly the failure this
// command exists to prevent, and a skip message is free-form text.
func TestRun_UnknownExtraEnvIsAnError(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, "inv.json")
	writeFile(t, inv, jsonEvents(t, testModule+"/internal/domain", [][3]string{
		{"output", "TestOdd", "    a_test.go:1: set AIHUB_TEST_DB — see CONTRIBUTING_DB.md\n"},
		{"skip", "TestOdd", ""},
	}))
	wf := filepath.Join(dir, "ci.yml")
	writeFile(t, wf, "jobs:\n  test:\n    steps: []\n")
	gomod := filepath.Join(dir, "go.mod")
	writeFile(t, gomod, "module "+testModule+"\n")

	var out bytes.Buffer
	err := run(inv, wf, gomod, dir, 1, &out)
	if err == nil {
		t.Fatal("want an error for an unknown extra environment variable, got nil")
	}
	if !strings.Contains(err.Error(), "CONTRIBUTING_DB") {
		t.Errorf("error does not name the offending token: %v", err)
	}
}

// The parser must find the real workflow's DB invocations. If a future edit to
// ci.yml's shape made ParseWorkflow return nothing, the gate would still be
// "working" — it would just report every DB test as uncovered, which is loud.
// The opposite drift is the dangerous one and is what this asserts against: the
// parser must keep seeing the steps that are actually there.
func TestParseWorkflow_RealCIWorkflow(t *testing.T) {
	path := filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	module, err := readModulePath(filepath.Join("..", "..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ParseWorkflow(data, module)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	// Ratchet, not an exact count: raise it when you add DB steps.
	if len(scan.Invocations) < 20 {
		t.Errorf("found only %d DB `go test` invocations in ci.yml, floor is 20 — the parser has stopped seeing steps that are there", len(scan.Invocations))
	}
	if len(scan.Unguarded) != 0 {
		t.Errorf("ci.yml has DB `go test` invocations with no %q guard: %v", skipGuardMarker, scan.Unguarded)
	}
	if len(scan.Dropped) != 0 {
		t.Errorf("ci.yml has DB `go test` lines this parser could not model: %v", scan.Dropped)
	}
	for _, inv := range scan.Invocations {
		if len(inv.Packages) == 0 {
			t.Errorf("invocation with no package resolved: %+v", inv)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pkgDir writes a whole synthetic package and returns its directory. The
// several-files-at-once shape is the point of most of the fixtures below: the
// prefilter that decides whether a file is examined at all is per PACKAGE, so
// what one file declares has to be visible when another file is checked.
func pkgDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	return dir
}

// ---------------------------------------- anti-silencing: the eight shapes
//
// Every fixture below was written into the real repository, and the gate said
// `dbtestcov: OK` with the gated count UNCHANGED at 85. That last part is what
// makes these blockers rather than gaps: -min-gated cannot catch a hole that
// does not move the count, so the fix has to be in the detector.
//
// Each subtest asserts a violation is reported. All eight are measured RED
// against the pre-fix detector.

func TestCheckSkipMessages_EnvReadInABoolHelperIsAViolation(t *testing.T) {
	// Shape 1. The read is in a helper that RETURNS the answer, so the skip
	// happens in a function that never mentions the variable and the message is
	// free to say nothing. Nothing here is DB-gated as far as the inventory is
	// concerned, so the count does not move.
	dir := pkgDir(t, map[string]string{"helper_test.go": `package p

import (
	"os"
	"testing"
)

func haveDB() bool { return os.Getenv("AIHUB_TEST_DB") != "" }

func TestNeedsDB(t *testing.T) {
	if !haveDB() {
		t.Skip("no test database")
	}
}
`})
	wantViolation(t, dir, "haveDB")
}

func TestCheckSkipMessages_LookupEnvIsAViolation(t *testing.T) {
	// Shape 2. os.LookupEnv reads exactly what os.Getenv reads.
	dir := pkgDir(t, map[string]string{"guard_test.go": `package p

import (
	"os"
	"testing"
)

func setupLookup(t *testing.T) {
	if _, ok := os.LookupEnv("AIHUB_TEST_DB"); !ok {
		t.Skip("no test database")
	}
}
`})
	wantViolation(t, dir, "setupLookup")
}

func TestCheckSkipMessages_ConstantNamedVariableIsAViolation(t *testing.T) {
	// Shape 3, in its sharpest form: the constant lives in a DIFFERENT file of
	// the same package, and a non-test one at that. Two separate mechanisms had
	// to be wrong for this to work — a per-file text prefilter (the guard file
	// never spells the variable) and an argument matched as a literal.
	dir := pkgDir(t, map[string]string{
		"env.go": `package p

const dbEnv = "AIHUB_TEST_DB"
`,
		"guard_test.go": `package p

import (
	"os"
	"testing"
)

func setupConst(t *testing.T) {
	if os.Getenv(dbEnv) == "" {
		t.Skip("no test database")
	}
}
`})
	wantViolation(t, dir, "setupConst")
}

func TestCheckSkipMessages_ReturningInsteadOfSkippingIsAViolation(t *testing.T) {
	// Shape 4, the subtlest: a test that RETURNS did not skip, so there is no
	// SKIP line for a SKIP-based inventory to classify. It is invisible by
	// construction, which is why it has to be caught here or not at all.
	dir := pkgDir(t, map[string]string{"guard_test.go": `package p

import (
	"os"
	"testing"
)

func TestSilentlyReturns(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		return
	}
	t.Log("would need a database")
}
`})
	wantViolation(t, dir, "TestSilentlyReturns")
}

func TestCheckSkipMessages_ConcatenatedVariableNameIsAViolation(t *testing.T) {
	// Shape 5. The sibling file is not decoration: the package-level prefilter
	// is a text search, so a package in which the variable is never spelled in
	// full is out of scope by construction (documented on checkSkipMessages as
	// the residual hole). Real packages with DB tests always spell it — the
	// skip-message convention this very check enforces requires them to.
	dir := pkgDir(t, map[string]string{
		"compliant_test.go": `package p

import (
	"os"
	"testing"
)

func setupCompliant(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
}
`,
		"sneaky_test.go": `package p

import (
	"os"
	"testing"
)

func setupConcat(t *testing.T) {
	if os.Getenv("AIHUB_TEST" + "_DB") == "" {
		t.Skip("no test database")
	}
}
`})
	wantViolation(t, dir, "setupConcat")
}

func TestCheckSkipMessages_TestMainGateIsAViolation(t *testing.T) {
	// Shape 6. A TestMain cannot skip, so gating it removes the whole package
	// from the inventory while `go test` still exits 0 — the original defect
	// with a blast radius of one package instead of one test.
	dir := pkgDir(t, map[string]string{"main_test.go": `package p

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
`})
	wantViolation(t, dir, "TestMain")
}

func TestCheckSkipMessages_BuildConstraintOnATestFileIsAViolation(t *testing.T) {
	// Shape 7. Note the guard itself is perfectly COMPLIANT: it reads the
	// variable and names it in the message. The file is still invisible,
	// because the inventory is a default-build run and this file is not in it.
	// So a check that only ever looked at guard bodies could not see this.
	dir := pkgDir(t, map[string]string{"tagged_test.go": `//go:build dbtest

package p

import (
	"os"
	"testing"
)

func setupTagged(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
}
`})
	wantViolation(t, dir, "build constraint")
}

func TestCheckSkipMessages_GuardInANonTestFileIsAViolation(t *testing.T) {
	// Shape 8, with the A/B that proves the file NAME was the whole difference:
	// byte-identical content is checked in helper_test.go and in helper.go, and
	// both must be reported. Before the fix only the first was.
	body := `package p

import (
	"os"
	"testing"
)

func setupHelper(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("no test database")
	}
}
`
	wantViolation(t, pkgDir(t, map[string]string{"helper_test.go": body}), "setupHelper")
	wantViolation(t, pkgDir(t, map[string]string{"helper.go": body}), "setupHelper")
}

// An environment read whose variable name is decided somewhere this command
// cannot see makes every check above optional — "pass the name in" would be the
// cheapest way to go quiet, cheaper than any of the eight shapes. So it is a
// violation on its own, and the message says how to comply.
func TestCheckSkipMessages_UnresolvableEnvVarNameIsAViolation(t *testing.T) {
	dir := pkgDir(t, map[string]string{
		"compliant_test.go": `package p

import (
	"os"
	"testing"
)

func setupCompliant(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
}
`,
		"indirect_test.go": `package p

import (
	"os"
	"testing"
)

func setupIndirect(t *testing.T, which string) {
	if os.Getenv(which) == "" {
		t.Skip("no test database")
	}
}
`})
	wantViolation(t, dir, "setupIndirect")
}

// The variable NAME has to matter, or the checks above would fire on every
// env-gated test in the repo and the fix would be worse than the hole. This is
// the assertion that was vacuous before: a guard on a different variable used
// to be dropped by the per-FILE prefilter (its file never spells AIHUB_TEST_DB)
// so the name comparison was never reached, and mutating it changed nothing.
// With the prefilter now per PACKAGE, the sibling file below puts this fixture
// in scope and the comparison is live.
//
// Verified by mutation: making the detector treat ANY resolved env read as a
// database read turns this test red on the first assertion.
func TestCheckSkipMessages_AGuardOnAnotherVariableIsNotOurBusiness(t *testing.T) {
	const compliant = `package p

import (
	"os"
	"testing"
)

func setupCompliant(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
}
`
	dir := pkgDir(t, map[string]string{
		"compliant_test.go": compliant,
		"slow_test.go": `package p

import (
	"os"
	"testing"
)

func setupSlow(t *testing.T) {
	if os.Getenv("AIHUB_SLOW_TESTS") == "" {
		t.Skip("slow; set AIHUB_SLOW_TESTS=1 to run")
	}
}
`})
	got, err := checkSkipMessages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a guard on an unrelated variable must not be reported, got:\n  %s", strings.Join(got, "\n  "))
	}

	// Positive control in the same fixture shape, so "no violations" above
	// cannot come from the package being out of scope: point the SAME guard at
	// AIHUB_TEST_DB and it must be reported.
	dir = pkgDir(t, map[string]string{
		"compliant_test.go": compliant,
		"slow_test.go": `package p

import (
	"os"
	"testing"
)

func setupSlow(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("slow; set AIHUB_SLOW_TESTS=1 to run")
	}
}
`})
	wantViolation(t, dir, "setupSlow")
}

func wantViolation(t *testing.T, dir, want string) {
	t.Helper()
	got, err := checkSkipMessages(dir)
	if err != nil {
		t.Fatalf("checkSkipMessages: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("want a violation naming %q, got none — this shape silences the gate without moving the gated count, "+
			"so nothing downstream can catch it", want)
	}
	if !strings.Contains(strings.Join(got, "\n"), want) {
		t.Errorf("violations do not name %q:\n  %s", want, strings.Join(got, "\n  "))
	}
}

// ------------------------------------------ workflow: credited but not run

// A `go test` inside a shell conditional is credited as unconditional coverage
// by a line-based parser. stripShellComments already established that this
// parser has to model shell structure; an `if` is the same class of mistake as
// a `#`.
func TestParseWorkflow_RejectsGoTestInsideAShellConditional(t *testing.T) {
	multiLine := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          if [ -n "$RUN_DB_TESTS" ]; then
            go test ./internal/domain/ -run 'TestAlpha' -count=1 -v 2>&1 | tee a.log
            ! grep -q -- '--- SKIP' a.log || exit 1
          fi
`)
	if _, err := ParseWorkflow(multiLine, testModule); err == nil {
		t.Error("want an error for `go test` inside an if-block, got nil")
	}

	oneLine := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          if [ -n "$RUN_DB_TESTS" ]; then go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log ; fi
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(oneLine, testModule); err == nil {
		t.Error("want an error for a single-line if/then/fi, got nil")
	}

	andGuarded := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          [ -n "$RUN_DB_TESTS" ] && go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(andGuarded, testModule); err == nil {
		t.Error("want an error for a `&&`-guarded go test, got nil")
	}

	// A `go test` whose failure is swallowed is credited just as wrongly as one
	// that never runs: `!` inverts it and `&` discards it.
	negated := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          ! go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(negated, testModule); err == nil {
		t.Error("want an error for a `!`-negated go test, got nil")
	}

	backgrounded := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v > a.log &
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	if _, err := ParseWorkflow(backgrounded, testModule); err == nil {
		t.Error("want an error for a backgrounded go test, got nil")
	}

	// The redirection every real DB step ends with must NOT be mistaken for
	// backgrounding — `2>&1` is the reason this needs a real check and not a
	// search for '&'.
	ok := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -count=1 -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow(ok, testModule)
	if err != nil {
		t.Fatalf("an ordinary `2>&1 | tee` invocation must be accepted, got %v", err)
	}
	if len(scan.Invocations) != 1 || len(scan.Unguarded) != 0 {
		t.Errorf("scan = %+v, want one guarded invocation", scan)
	}
}

// The complete bypass, in one line: a quoted `go test` credits coverage AND the
// same quoted text satisfies the guard that is supposed to prove it ran. Driven
// through run() rather than ParseWorkflow because "the whole gate says OK" is
// the claim being falsified.
func TestRun_RejectsAQuotedGoTest(t *testing.T) {
	dir := t.TempDir()
	pkg := testModule + "/internal/domain"
	inv := filepath.Join(dir, "inv.json")
	writeFile(t, inv, jsonEvents(t, pkg, [][3]string{
		{"output", "TestNeverRunsAnywhere", "    a_test.go:1: set AIHUB_TEST_DB to run this integration test\n"},
		{"skip", "TestNeverRunsAnywhere", ""},
	}))
	gomod := filepath.Join(dir, "go.mod")
	writeFile(t, gomod, "module "+testModule+"\n")
	wf := filepath.Join(dir, "ci.yml")
	writeFile(t, wf, `
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          echo "to reproduce locally: go test ./... -count=1 -v 2>&1 | tee a.log ; ! grep -q -- '--- SKIP' a.log || exit 1"
`)
	var out bytes.Buffer
	if err := run(inv, wf, gomod, dir, 1, &out); err == nil {
		t.Fatal("a quoted `go test` must not credit coverage (nor satisfy the SKIP guard), got a passing gate")
	}
}

// `go test` obeys the LAST -run; this command used to read the first, so the
// earlier pattern described coverage that did not happen.
func TestRun_RejectsADuplicatedRunFlag(t *testing.T) {
	dir := t.TempDir()
	pkg := testModule + "/internal/domain"
	inv := filepath.Join(dir, "inv.json")
	writeFile(t, inv, jsonEvents(t, pkg, [][3]string{
		{"output", "TestAlpha", "    a_test.go:1: set AIHUB_TEST_DB to run this integration test\n"},
		{"skip", "TestAlpha", ""},
	}))
	gomod := filepath.Join(dir, "go.mod")
	writeFile(t, gomod, "module "+testModule+"\n")
	wf := filepath.Join(dir, "ci.yml")
	writeFile(t, wf, `
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -run 'TestNothingMatchesThis' -count=1 -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	var out bytes.Buffer
	err := run(inv, wf, gomod, dir, 1, &out)
	if err == nil {
		t.Fatal("a second -run silently overrides the first; want an error, got a passing gate")
	}
	if !strings.Contains(err.Error(), "-run") {
		t.Errorf("error does not explain the duplicated flag: %v", err)
	}
}

// -count=0 selects zero tests, so the invocation prints "ok", exits 0 and emits
// no SKIP line: both halves of this gate pass while nothing ran. It is -skip's
// twin and it was not rejected at all — `-count` appeared nowhere in this file.
func TestRun_RejectsCountZero(t *testing.T) {
	dir := t.TempDir()
	pkg := testModule + "/internal/domain"
	inv := filepath.Join(dir, "inv.json")
	writeFile(t, inv, jsonEvents(t, pkg, [][3]string{
		{"output", "TestAlpha", "    a_test.go:1: set AIHUB_TEST_DB to run this integration test\n"},
		{"skip", "TestAlpha", ""},
	}))
	gomod := filepath.Join(dir, "go.mod")
	writeFile(t, gomod, "module "+testModule+"\n")
	wf := filepath.Join(dir, "ci.yml")

	for _, count := range []string{"-count=0", "-count 0", "-count=-1"} {
		writeFile(t, wf, `
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' `+count+` -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
		var out bytes.Buffer
		if err := run(inv, wf, gomod, dir, 1, &out); err == nil {
			t.Errorf("`go test %s` runs no test at all; want an error, got a passing gate", count)
		}
	}
}

// The SKIP guard is what turns nominal coverage into real coverage, so a text
// search for the marker is not enough: the lines below mention the marker and
// the log, and none of them fails the step when a test SKIPs. One demands a
// skip; the others cannot fail at all.
func TestParseWorkflow_RejectsAGuardThatCannotFail(t *testing.T) {
	for _, guard := range []string{
		`grep -q -- '--- SKIP' a.log || exit 1`,   // inverted: passes only if a test DID skip
		`! grep -c -- '--- SKIP' a.log || true`,   // can never fail the step
		`! grep -q -- '--- SKIP' a.log || exit 0`, // exits successfully
		`! grep -q -- '--- SKIP' a.log || :`,      // the no-op builtin
		// The echo succeeds, so the exit never runs, the block returns 0 and
		// the step is green on a SKIP. This is the one that LOOKS exactly like
		// the twenty correct guards in ci.yml, and it is why the `{ ... }` tail
		// has to require `exit` to be the block's LAST command.
		`! grep -q -- '--- SKIP' a.log || { echo "a test SKIPped" || exit 1; }`,
		`echo "remember: ! grep -q -- '--- SKIP' a.log || exit 1"`, // prose
	} {
		wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -count=1 -v 2>&1 | tee a.log
          ` + guard + `
`)
		scan, err := ParseWorkflow(wf, testModule)
		if err != nil {
			// A quoted `go test` in the prose case is itself rejected earlier,
			// which is also a pass for this test's purpose.
			continue
		}
		if len(scan.Unguarded) != 1 {
			t.Errorf("guard %q asserts nothing about a.log but was accepted; Unguarded = %v", guard, scan.Unguarded)
		}
	}

	// Both accepted spellings must keep working — an allowlist that rejected
	// the twenty guards already in ci.yml would just get widened back open.
	for _, guard := range []string{
		`! grep -q -- '--- SKIP' a.log || exit 1`,
		`! grep -q -- "--- SKIP" a.log || exit 1`,
		`! grep -q -- '--- SKIP' a.log || { echo "::error::a test SKIPped (x); AIHUB_TEST_DB is not reaching this step"; exit 1; }`,
	} {
		wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -count=1 -v 2>&1 | tee a.log
          ` + guard + `
`)
		scan, err := ParseWorkflow(wf, testModule)
		if err != nil {
			t.Errorf("guard %q must be accepted, got %v", guard, err)
			continue
		}
		if len(scan.Unguarded) != 0 {
			t.Errorf("guard %q must be accepted, got Unguarded = %v", guard, scan.Unguarded)
		}
	}
}

// A here-document body is DATA, not commands: `cat > repro.sh <<'SH'` followed
// by a `go test` line writes a file and executes nothing. It is the most
// complete form of the credited-but-not-run hole, because the body can hold the
// invocation, its tee target and the SKIP guard together — measured before the
// fix, the gate printed `OK` and exited 0 with an uncovered DB-gated test in the
// inventory. Found while re-reading this file, not by the review.
func TestParseWorkflow_HeredocBodyIsNotCoverage(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          cat > /tmp/repro.sh <<'SH'
          go test ./... -count=1 -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
          SH
          echo wrote it
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 0 {
		t.Errorf("a `go test` inside a heredoc body executes nothing; it must credit no coverage, got %+v", scan.Invocations)
	}

	// A REAL invocation after the heredoc closes must still be seen, or this
	// would trade one silent hole for another (coverage stops being found, the
	// tests it covers get reported as uncovered — loud, but still wrong).
	wf = []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          cat > /tmp/repro.sh <<'SH'
          not a command
          SH
          go test ./internal/domain/ -run 'TestAlpha' -count=1 -v 2>&1 | tee a.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err = ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 1 || len(scan.Unguarded) != 0 {
		t.Errorf("the invocation after the heredoc must still be found and guarded; scan = %+v", scan)
	}
}

// A whole-line `#` comment was already dropped; a TRAILING one is the same hole
// one column to the right, and it can carry the invocation, the tee target and
// the SKIP marker together. Found while re-reading this file, not by the review.
func TestParseWorkflow_TrailingCommentIsNotCoverage(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          echo hi # go test ./... -count=1 -v 2>&1 | tee a.log ; ! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 0 {
		t.Errorf("a `go test` in a trailing comment must credit no coverage, got %+v", scan.Invocations)
	}

	// ...and a '#' that is NOT a comment must survive: mid-word, and inside the
	// quoted error messages every real DB step carries.
	ok := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -count=1 -v 2>&1 | tee aihub#303.log
          ! grep -q -- '--- SKIP' aihub#303.log || { echo "::error::an aihub#289 test SKIPped"; exit 1; }
`)
	scan, err = ParseWorkflow(ok, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 1 || len(scan.Unguarded) != 0 {
		t.Errorf("a mid-word '#' is literal and a quoted one is data; scan = %+v", scan)
	}
}

// Two invocations on ONE line, separated by `;`. The parser used to model only
// the first occurrence per line, so the second credited nothing and — worse —
// its missing SKIP guard was never noticed.
func TestParseWorkflow_SeesEveryInvocationOnALine(t *testing.T) {
	wf := []byte(`
jobs:
  test:
    steps:
      - name: db suite
        env:
          AIHUB_TEST_DB: postgres://x
        run: |
          go test ./internal/domain/ -run 'TestAlpha' -v 2>&1 | tee a.log ; go test ./internal/server/ -run 'TestBeta' -v 2>&1 | tee b.log
          ! grep -q -- '--- SKIP' a.log || exit 1
`)
	scan, err := ParseWorkflow(wf, testModule)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Invocations) != 2 {
		t.Fatalf("invocations = %+v, want 2", scan.Invocations)
	}
	if len(scan.Unguarded) != 1 || !strings.Contains(scan.Unguarded[0], "b.log") {
		t.Errorf("Unguarded = %v, want exactly the unguarded b.log invocation", scan.Unguarded)
	}
}
