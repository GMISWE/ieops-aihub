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

// A SCREAMING_CASE fragment of a test NAME must never be read as a second
// required environment variable — that would silently move the test out of the
// required set, which is the failure mode this whole command exists to prevent.
func TestParseInventory_TestNameIsNotMistakenForAnEnvVar(t *testing.T) {
	stream := jsonEvents(t, "p", [][3]string{
		{"output", "TestHTTP_API_Thing", "=== RUN   TestHTTP_API_Thing\n"},
		{"output", "TestHTTP_API_Thing", "    a_test.go:1: set AIHUB_TEST_DB to run this integration test\n"},
		{"output", "TestHTTP_API_Thing", "--- SKIP: TestHTTP_API_Thing (0.00s)\n"},
		{"skip", "TestHTTP_API_Thing", ""},
	})
	got, err := ParseInventory(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if len(got[0].ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v, want empty (the header line naming the test must be stripped)", got[0].ExtraEnv)
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
