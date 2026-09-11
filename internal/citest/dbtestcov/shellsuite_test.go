package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// suiteFixtureWorkflow builds a one-step workflow that runs a shell suite and
// asserts the given wants plus a floor, in exactly the spelling ci.yml uses.
func suiteFixtureWorkflow(suitePath string, wants []string, floor int) string {
	var b strings.Builder
	b.WriteString("jobs:\n  test:\n    steps:\n      - name: suite gate\n        run: |\n")
	b.WriteString("          set -o pipefail\n")
	fmt.Fprintf(&b, "          bash %s 2>&1 | tee suite.log\n", suitePath)
	if len(wants) > 0 {
		b.WriteString("          for want in")
		for _, w := range wants {
			fmt.Fprintf(&b, ` "%s"`, w)
		}
		b.WriteString("; do\n")
		b.WriteString("            grep -qF -- \"$want\" suite.log || { echo \"::error::missing $want\"; exit 1; }\n")
		b.WriteString("          done\n")
	}
	if floor > 0 {
		b.WriteString("          n=$(grep -c '^  PASS: ' suite.log || true)\n")
		fmt.Fprintf(&b, "          [ \"${n:-0}\" -ge %d ] || { echo \"::error::floor\"; exit 1; }\n", floor)
	}
	return b.String()
}

// runShellSuiteCheck parses the fixture workflow and runs the shell-suite check
// against the given source root.
func runShellSuiteCheck(t *testing.T, workflow, root string) (problems, reportLines []string, scan *WorkflowScan) {
	t.Helper()
	scan, err := ParseWorkflow([]byte(workflow), "example.com/m")
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	problems, reportLines, err = checkShellSuites(scan, root)
	if err != nil {
		t.Fatalf("checkShellSuites: %v", err)
	}
	return problems, reportLines, scan
}

// writeSuite drops a suite fixture under root at rel and returns its path.
func writeSuite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
}

const fixtureSuite = `#!/usr/bin/env bash
set -uo pipefail
fails=0
ck()      { if [ "$1" = "$2" ]; then echo "  PASS: $3"; else echo "  FAIL: $3" >&2; fails=$((fails+1)); fi; }
ck_empty(){ if [ -z "$1" ]; then echo "  PASS: $2"; else echo "  FAIL: $2" >&2; fails=$((fails+1)); fi; }
for sk in pf-spec pf-plan; do
  for mode in off on; do
    ck "a" "a" "$sk (sp $mode) carries IR1"
    ck "a" "a" "$sk (sp $mode) carries the output format"
  done
done
ck "a" "a" "execute native main loop injected"
ck_empty "" "pf-help -> no injection"
if true; then
  echo "  PASS: guard exits 0 (inert, not a crash)"
fi
echo
[ "$fails" -eq 0 ] && { echo "ALL PASS"; exit 0; } || { echo "$fails CHECK(S) FAILED" >&2; exit 1; }
`

// The fixture prints 2*2*2 = 8 loop checks + 2 direct checks + 1 literal echo,
// so 11 lines match '^  PASS: '.
const fixtureSuiteMax = 11

func TestCheckShellSuites_VerifiesWantsAndFloor(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", fixtureSuite)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{
		"PASS: pf-spec (sp off) carries IR1",
		"PASS: pf-plan (sp on) carries the output format",
		"PASS: execute native main loop injected",
		"PASS: guard exits 0 (inert, not a crash)",
		"ALL PASS",
	}, fixtureSuiteMax)
	problems, reportLines, _ := runShellSuiteCheck(t, wf, root)
	for _, p := range problems {
		t.Errorf("unexpected problem: %s", p)
	}
	joined := strings.Join(reportLines, "")
	if !strings.Contains(joined, fmt.Sprintf("floor %d on \"^  PASS: \" ok", fixtureSuiteMax)) ||
		!strings.Contains(joined, fmt.Sprintf("can print up to %d", fixtureSuiteMax)) {
		t.Errorf("floor verification not reported, or the maximum is off:\n%s", joined)
	}
}

// The aihub#416 shape, shell face: rename a check in the suite and the ci.yml
// assertion must go red HERE, naming the want, not on the runner.
func TestCheckShellSuites_CatchesARenamedCheck(t *testing.T) {
	root := t.TempDir()
	renamed := strings.ReplaceAll(fixtureSuite, "carries the output format", "carries the output fmt")
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", renamed)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{
		"PASS: pf-plan (sp on) carries the output format",
	}, 0)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	if len(problems) != 1 {
		t.Fatalf("want exactly 1 problem, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "PASS: pf-plan (sp on) carries the output format") ||
		!strings.Contains(problems[0], "renamed or deleted") {
		t.Errorf("the problem does not name the stale want: %s", problems[0])
	}
}

// Loop folding is what makes a wrong ARM name catchable: `$sk` must not be a
// wildcard when its `for sk in …` list is right there.
func TestCheckShellSuites_CatchesAWrongLoopArmName(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", fixtureSuite)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{
		"PASS: pf-exec (sp off) carries IR1", // the list says pf-spec pf-plan
	}, 0)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	if len(problems) != 1 {
		t.Fatalf("want exactly 1 problem, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "pf-exec (sp off) carries IR1") {
		t.Errorf("the problem does not name the stale want: %s", problems[0])
	}
}

// A generic marker helper must not satisfy arbitrary names: the whole point of
// matching the NAME part is that `echo "  PASS: $3"` proves nothing about it.
func TestCheckShellSuites_HelperDefinitionDoesNotSatisfyEveryWant(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", fixtureSuite)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{
		"PASS: a check nobody ever wrote anywhere",
	}, 0)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	if len(problems) != 1 {
		t.Fatalf("want exactly 1 problem, got %d: %v", len(problems), problems)
	}
}

// The floor side of aihub#535: a floor above what the suite can still print is
// the state where the step can only fail on the runner — red here instead.
func TestCheckShellSuites_FloorAboveTheMaximumIsRed(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", fixtureSuite)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{"ALL PASS"}, fixtureSuiteMax+1)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	if len(problems) != 1 {
		t.Fatalf("want exactly 1 problem, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], fmt.Sprintf("floor of %d", fixtureSuiteMax+1)) ||
		!strings.Contains(problems[0], fmt.Sprintf("can only print %d", fixtureSuiteMax)) {
		t.Errorf("the problem does not carry the numbers: %s", problems[0])
	}
}

// Break the derivation: deleting checks from the suite must move the measured
// maximum, which is what makes the floor a re-verified number rather than the
// bare integer aihub#320 retired.
func TestCheckShellSuites_DeletedChecksLowerTheMeasuredMaximum(t *testing.T) {
	root := t.TempDir()
	gutted := strings.ReplaceAll(fixtureSuite,
		"    ck \"a\" \"a\" \"$sk (sp $mode) carries the output format\"\n", "")
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", gutted)
	// 4 of the 11 emissions are gone; the old floor is now unreachable.
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{"ALL PASS"}, fixtureSuiteMax)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	if len(problems) != 1 {
		t.Fatalf("want exactly 1 problem, got %d: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], fmt.Sprintf("can only print %d", fixtureSuiteMax-4)) {
		t.Errorf("the measured maximum did not move with the deletion: %s", problems[0])
	}
}

// A suite whose output cannot be statically counted (an emission inside a
// while loop) must make the floor UNVERIFIABLE, not wrong in either direction.
func TestCheckShellSuites_UnboundedSuiteSkipsTheFloor(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", `#!/usr/bin/env bash
ok() { echo "  PASS: $1"; }
while read -r line; do
  ok "$line"
done < /dev/null
echo "ALL PASS"
`)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{"ALL PASS"}, 1000)
	problems, reportLines, _ := runShellSuiteCheck(t, wf, root)
	for _, p := range problems {
		t.Errorf("unexpected problem: %s", p)
	}
	joined := strings.Join(reportLines, "")
	if !strings.Contains(joined, "not statically checkable") {
		t.Errorf("the unverifiable floor is not reported as such:\n%s", joined)
	}
}

// A here-document that mentions the marker (python printing PASS lines) makes
// the count unverifiable too — an embedded interpreter's output has no static
// bound, and guessing one could go red on a healthy suite.
func TestCheckShellSuites_MarkerInsideAHeredocSkipsTheFloor(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", `#!/usr/bin/env bash
python3 - <<'PY'
for i in range(50):
    print("  PASS: generated check %d" % i)
PY
echo "ALL PASS"
`)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{"ALL PASS"}, 40)
	problems, reportLines, _ := runShellSuiteCheck(t, wf, root)
	for _, p := range problems {
		t.Errorf("unexpected problem: %s", p)
	}
	if !strings.Contains(strings.Join(reportLines, ""), "here-document") {
		t.Errorf("the heredoc reason is not reported:\n%s", strings.Join(reportLines, ""))
	}
}

// ...but a want printed from inside that here-document still verifies, holes
// and all: the python literal is the only place pi-runtime-shaped names live.
func TestCheckShellSuites_WantInsideAHeredocTemplateMatches(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", `#!/usr/bin/env bash
ok() { echo "  PASS: $1"; }
python3 - <<'PY'
key = "scriptMode"
print("PASS|settings.%s == %s" % (key, "false"))
PY
ok "read from python"
echo "ALL PASS"
`)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", []string{
		"PASS: settings.scriptMode == false",
	}, 0)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	for _, p := range problems {
		t.Errorf("unexpected problem: %s", p)
	}
}

// A teed suite log nothing asserts on is the state a stale want-list can be
// deleted into; mirror of scan.Unasserted on the Go side.
func TestCheckShellSuites_UnassertedSuiteLogIsReported(t *testing.T) {
	root := t.TempDir()
	writeSuite(t, root, "plugins/x/tests/fixture.test.sh", fixtureSuite)
	wf := suiteFixtureWorkflow("plugins/x/tests/fixture.test.sh", nil, 0)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	if len(problems) != 1 || !strings.Contains(problems[0], "asserts none") {
		t.Fatalf("want the unasserted-log problem, got: %v", problems)
	}
}

// A suite named by ci.yml but missing from the tree is a red, not a skip.
func TestCheckShellSuites_MissingSuiteFileIsReported(t *testing.T) {
	root := t.TempDir()
	wf := suiteFixtureWorkflow("plugins/x/tests/absent.test.sh", []string{"ALL PASS"}, 0)
	problems, _, _ := runShellSuiteCheck(t, wf, root)
	if len(problems) != 1 || !strings.Contains(problems[0], "cannot be read") {
		t.Fatalf("want the unreadable-suite problem, got: %v", problems)
	}
}

// The gate against the real inputs — the arm that catches a renamed shell
// check before a push does, so it must stay green: any failure here is a real
// ci.yml want that plugins/polyforge/tests/ can no longer print, or a real
// floor above what a suite can emit.
func TestCheckShellSuites_RealCIWorkflow(t *testing.T) {
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
	// Ratchets, not exact counts: raise them when you add suites or wants.
	if len(scan.ShellSuites) < 8 {
		t.Errorf("only %d shell-suite invocations found in ci.yml, floor is 8 — the extractor has stopped seeing them", len(scan.ShellSuites))
	}
	if len(scan.ShellWants) < 100 {
		t.Errorf("only %d shell-suite want assertions found in ci.yml, floor is 100 — the extractor has stopped seeing want-lists", len(scan.ShellWants))
	}
	if len(scan.ShellFloors) < 8 {
		t.Errorf("only %d shell-suite count floors found in ci.yml, floor is 8 — the extractor has stopped seeing them", len(scan.ShellFloors))
	}
	for _, p := range scan.ShellProblems {
		t.Errorf("ci.yml asserts on a shell-suite log in a form dbtestcov cannot read: %s", p)
	}
	problems, reportLines, err := checkShellSuites(scan, root)
	if err != nil {
		t.Fatalf("checkShellSuites: %v", err)
	}
	for _, p := range problems {
		t.Errorf("stale shell-suite assertion or floor: %s", p)
	}
	// The aihub#513 floor specifically: 57 must be verified against a measured
	// maximum, not skipped as unverifiable — that adjudication is the point of
	// aihub#535, so its decay would be silent without this pin.
	joined := strings.Join(reportLines, "")
	if !strings.Contains(joined, "floor 57 on \"^  PASS: \" ok: plugins/polyforge/tests/pf-skill-router.test.sh") {
		t.Errorf("the skill-router floor is no longer verified against a measured maximum:\n%s", joined)
	}
}

// The real-data mutation arm: rename one skill-router check in a COPY of the
// real suite and the real ci.yml's want-list must go red naming it. This is
// the aihub#416 reproduction, shell face, run on every `go test`.
func TestCheckShellSuites_RealWorkflowCatchesARealRename(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(repo, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	module, err := readModulePath(filepath.Join(repo, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ParseWorkflow(data, module)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}

	// Copy every suite ci.yml runs into a scratch root, then break one.
	root := t.TempDir()
	for _, inv := range scan.ShellSuites {
		src, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(inv.Path))) // #nosec G304 -- repo fixture
		if err != nil {
			t.Fatalf("read %s: %v", inv.Path, err)
		}
		writeSuite(t, root, inv.Path, string(src))
	}
	target := "plugins/polyforge/tests/pf-skill-router.test.sh"
	orig, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target)))
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(orig),
		`"$sk (sp $ws_name) carries the output format"`,
		`"$sk (sp $ws_name) carries the output fmt"`, 1)
	if mutated == string(orig) {
		t.Fatal("the mutation did not apply — the check moved; update this test's target string")
	}
	writeSuite(t, root, target, mutated)

	problems, _, err := checkShellSuites(scan, root)
	if err != nil {
		t.Fatalf("checkShellSuites: %v", err)
	}
	var hits []string
	for _, p := range problems {
		if strings.Contains(p, "carries the output format") && strings.Contains(p, "pf-skill-router") {
			hits = append(hits, p)
		}
	}
	// ci.yml asserts the renamed name for pf-spec and pf-plan in both engine
	// branches: four stale wants, each named.
	if len(hits) != 4 {
		t.Errorf("want 4 problems naming the renamed check, got %d:\n%s", len(hits), strings.Join(problems, "\n"))
	}
	for _, p := range problems {
		if !strings.Contains(p, "carries the output format") {
			t.Errorf("the mutation produced an unrelated problem: %s", p)
		}
	}
}
