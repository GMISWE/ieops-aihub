package mcp_test

// aihub#501 — the live-response arm for the six git-dependent tools.
//
// ─── The hole this closes ───────────────────────────────────────────────────
//
// K10 (card_response_keys_live_e2e_db_test.go) is the only arm of the contract-
// card gate that reads a response off a running server; every other arm compares
// one checked-in file with another. Until this file, K10 drove 39 of the 45
// published tools and named the other six in liveWalkOutOfReach: pf_commit,
// pf_diff, pf_pr, pf_push, pf_ship and pf_wrap, each needing a git worktree and
// a remote. Their `response_keys_observed` therefore rested on the aihub#412
// corpus copy ALONE — which is exactly the both-sides-are-files blind spot K10
// exists to close, left open on 2,388 recorded calls (1,082 + 2 + 662 + 527 + 25
// + 90).
//
// This file drives all six against a REAL git repository and folds the keys they
// return into K10's same walk, so one assertion phase and one golden file
// (docs/mcp-cards/live-response-keys.json) cover all 45 tools. It deliberately
// does NOT add a second Test function: K10's is already registered in
// internal/citest/dbtestcov/gated_tests.txt and run by ci.yml's "aihub#482 live
// response-key contract DB tests" step, and the golden file is checked by
// EQUALITY against (live \ card) — a property that only holds while there is one
// walk to be equal to. Two walks with two golden files would be two mechanisms
// for one invariant.
//
// ─── What is real, and the one thing that is not ────────────────────────────
//
// REAL: the git repository. A bare repo is the origin and a clone is the
// worktree, both under t.TempDir(), so `git commit`, `git push
// --force-with-lease`, `git ls-remote` and `git diff` all run for real against
// real objects and the push is observable on the remote. The server half is real
// too — pf_commit's and pf_ship's lock gate reconciles against the live Postgres
// through the real router with the credentials the real pf_claim_work_item
// minted, and pf_wrap's complete_attempt really terminates the attempt.
//
// 🔴 QUALIFIED: `gh` is a stub on PATH. It has to be — the alternative is a test
// that opens pull requests on GitHub — and the header of K10 rejects fakes in as
// many words, so the distinction being relied on is stated rather than assumed:
//
//   - Faking aihub's OWN server would make the arm a mirror of the fixture, which
//     is the copy-to-copy failure the whole card set exists to end. That is not
//     what this does; the server here is the real one.
//   - `gh` is a third-party binary. Stubbing it costs exactly one thing: this arm
//     cannot see a change in what GITHUB or `gh` call their fields. It still sees
//     every change on aihub's side of that boundary — which field set aihub asks
//     for (coding.ghGetPRFields), how it parses what comes back (GHCreatePR's
//     URL fallback, GHGetPR's decode), the PR-coverage decision (deliveredByPR),
//     and the response projections (shipPayload, prPayload, pf_wrap's result
//     map). Those are the hop-5 surface the cards describe.
//
// 🔴 AND THE STUB DERIVES ITS FIELD SET FROM THE FLAG IT IS PASSED, which is the
// property that keeps this non-circular. `gh pr list --json <fields>` answers
// with exactly the fields named in <fields> and nothing else, so the keys pf_pr
// is observed to return are governed by aihub's ghGetPRFields constant, not by a
// list written into this file. Add a field there and this arm reddens with
// K10 LIVE_UNDECLARED until the card names it. A stub with the five field names
// hardcoded would have been the one shape that hides the only change worth
// catching.
//
// The residual blind spot, stated because it is real: the ratchet runs
// live ⊆ declared, so REMOVING a field from ghGetPRFields leaves the card's
// larger list satisfied and nothing goes red. That is a property of the ratchet's
// direction (argued in K10's header) rather than of the stub.
//
// ─── pf_diff carries no keys, and that is checked rather than assumed ───────
//
// pf_diff answers with a raw unified diff — prose, not a JSON object — so it
// contributes no top-level keys to compare and its card correctly lists none.
// Leaving it in liveWalkOutOfReach would have said "cannot be driven", which is
// false. It is declared in liveWalkProseOnly instead, and that arm holds three
// things: the walk really drove it, the result really was not a JSON object, and
// the card really claims no keys. The walk additionally asserts the diff names
// the file it changed, so "driven" cannot degrade into "returned an empty string".

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// liveGitRepoName is the repo the six tools are driven against. It is not
// "aihub": the worktree is a throwaway clone under t.TempDir(), and naming it
// after the real repo invites a reader to think this test touches one.
const liveGitRepoName = "livekeys-fixture"

// liveGitBranch is the task branch. It must not be main/master/dev/tot —
// coding.GitPush refuses those, and a refusal would cost this walk four tools.
const liveGitBranch = "polyforge/livekeys-501"

// liveGitFixture is a real git repository whose origin is a local bare repo, so
// every push in this walk is a real push with a real observable effect and no
// network.
type liveGitFixture struct {
	wt     string // the worktree the tools operate on
	bare   string // the local bare repo standing in for origin
	ghDir  string // the stub gh's state directory
	ghPath string // the stub gh itself, by absolute path
}

// requireGitAndShForLiveWalk is requireGitAndSh's sibling, and it FATALS where
// that one SKIPS. The difference is deliberate: there, the two coding rows were
// a corner of a wider test that still ran without them. Here a skip would take
// the whole of K10 with it — the one arm in the repo that reads a live response —
// and "the gate did not run" must not be reachable by a missing binary.
func requireGitAndShForLiveWalk(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"git", "sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s is not on PATH, so the six git-dependent tools cannot be driven and "+
				"K10 would report on 39 of 45 tools while claiming 45: %v", bin, err)
		}
	}
}

// newLiveGitFixture builds the repository and puts the stub gh on PATH.
func newLiveGitFixture(t *testing.T, root string) *liveGitFixture {
	t.Helper()
	requireGitAndShForLiveWalk(t)

	fx := &liveGitFixture{
		wt:    filepath.Join(root, "wt"),
		bare:  filepath.Join(root, "origin.git"),
		ghDir: filepath.Join(root, "gh-state"),
	}
	runGit(t, "", "init", "--bare", "-q", fx.bare)
	runGit(t, "", "clone", "-q", fx.bare, fx.wt)
	// Set locally rather than relying on the operator's global config: an
	// identity git cannot resolve turns every commit below into a failure that
	// looks like a defect in pf_commit.
	runGit(t, fx.wt, "config", "user.email", "livekeys501@example.invalid")
	runGit(t, fx.wt, "config", "user.name", "aihub#501 live key walk")
	runGit(t, fx.wt, "config", "commit.gpgsign", "false")

	// seed.txt is TRACKED from the first commit on purpose. pf_diff runs
	// `git diff HEAD`, which does not list untracked files, so a walk that
	// created a new file and then asked for a diff would get an empty string
	// back and could not tell that apart from a diff that failed to run.
	fx.write(t, "seed.txt", "the aihub#501 live git walk starts here\n")
	runGit(t, fx.wt, "add", "-A")
	runGit(t, fx.wt, "commit", "-q", "-m", "seed the live key walk fixture")
	runGit(t, fx.wt, "checkout", "-q", "-b", liveGitBranch)

	if err := os.MkdirAll(fx.ghDir, 0o755); err != nil {
		t.Fatalf("mkdir gh state: %v", err)
	}
	fx.installStubGH(t, root)
	return fx
}

func (fx *liveGitFixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fx.wt, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// liveGitStubGH is the stub `gh`.
//
// `pr list` reads the field list out of the `--json` flag it was handed and
// answers with exactly those fields — see the header: that is what makes the
// observed key set a function of aihub's ghGetPRFields rather than of this file.
// Three of them need a value with a specific shape for the code downstream to
// work at all, and each is commented where it is produced; every other field
// gets a placeholder string, because the KEY is what this walk measures.
//
// `pr create` prints a bare URL, which is what real `gh pr create` does without
// `--json` — the case GHCreatePR's json.Unmarshal failure path turns into
// {"url": …}. With a `conflict` marker present it instead fails the way `gh`
// fails on an existing PR, which is the branch that routes GHCreatePR through
// GHGetPR and is the only way pf_pr ever returns more than `url`.
const liveGitStubGH = `#!/bin/sh
sub="$1 $2"
case "$sub" in
"pr list")
	fields=""
	while [ "$#" -gt 0 ]; do
		if [ "$1" = "--json" ]; then fields="$2"; fi
		shift
	done
	[ -f "$PF_FAKE_GH_STATE/pr" ] || { printf '[]\n'; exit 0; }
	out=""
	for f in $(printf '%s' "$fields" | tr ',' ' '); do
		case "$f" in
		commits)
			# An oid that is deliberately NOT the worktree HEAD, so
			# deliveredByPR answers "this PR does not cover HEAD" and the
			# push/PR half of Wrap and Ship actually executes.
			v='[{"oid":"0000000000000000000000000000000000000000"}]' ;;
		state)
			# Read from the state dir: OPEN is what routes a wrap/ship onto the
			# existing PR instead of opening a second one.
			v="\"$(cat "$PF_FAKE_GH_STATE/state")\"" ;;
		number)
			v='7' ;;
		*)
			v="\"stub-gh-$f\"" ;;
		esac
		out="$out,\"$f\":$v"
	done
	printf '[{%s}]\n' "${out#,}"
	;;
"pr create")
	if [ -f "$PF_FAKE_GH_STATE/conflict" ]; then
		printf 'a pull request for branch "%s" into branch "main" already exists\n' "$PF_FAKE_GH_BRANCH" >&2
		exit 1
	fi
	printf 'https://example.invalid/livekeys/pull/7\n'
	;;
*)
	printf 'stub gh: unhandled invocation: %s\n' "$*" >&2
	exit 1
	;;
esac
`

func (fx *liveGitFixture) installStubGH(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir stub gh dir: %v", err)
	}
	fx.ghPath = filepath.Join(dir, "gh")
	if err := os.WriteFile(fx.ghPath, []byte(liveGitStubGH), 0o755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
	t.Setenv("PF_FAKE_GH_STATE", fx.ghDir)
	t.Setenv("PF_FAKE_GH_BRANCH", liveGitBranch)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// declareExistingPR makes the stub report an existing PR in the given state, and
// makes `pr create` fail the way gh fails when one already exists.
func (fx *liveGitFixture) declareExistingPR(t *testing.T, state string) {
	t.Helper()
	for name, content := range map[string]string{
		"pr":       "1",
		"state":    state,
		"conflict": "1",
	} {
		if err := os.WriteFile(filepath.Join(fx.ghDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write stub gh state %s: %v", name, err)
		}
	}
}

// remoteSHA is the sha the bare origin holds for branch, or "" when it holds
// none. The push assertions read it rather than trusting pf_push's own `ok`.
func (fx *liveGitFixture) remoteSHA(t *testing.T, branch string) string {
	t.Helper()
	out := runGit(t, "", "ls-remote", "--heads", fx.bare, branch)
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// adoptLiveGitWorktree points the state file the real pf_claim_work_item just
// wrote at the fixture worktree, leaving every credential in it untouched.
//
// This is the one piece of caller-side state the walk supplies, and it is the
// same value a claim writes for itself in a project with repo pins: the e2e
// stack's project has none, so the claim had no repository to create a worktree
// in. Nothing about the tools' behaviour is faked by it — the attempt id, claim
// epoch and session secret the lock gate and complete_attempt authenticate with
// are the ones the server minted.
func adoptLiveGitWorktree(t *testing.T, w *liveKeyWalk, wiID, repo, wt string) {
	t.Helper()
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		t.Fatalf("resolve the state file pf_claim_work_item wrote for %s: %v — without it none of "+
			"the six git tools can resolve a worktree and K10 reports on 39 of 45 tools. "+
			"pf_claim_work_item refusals this run: %v", wiID, err, w.failed["pf_claim_work_item"])
	}
	if sf.AttemptID == "" || sf.SessionSecret == "" {
		// aihub#593 (2026-09-11): the refusal list is in this message because the
		// one time this fired in the wild (2026-09-10, aihub#587's lane) it read
		// as a state-file defect and had to be root-caused from scratch. What it
		// actually means is that the claim drive above did not succeed — the
		// stub keyed by wiID, with a secret and no attempt_id, is what
		// pf_claim_work_item writes BEFORE the server answers — and the refusal
		// the walk recorded is the answer to why (measured cause that day: a
		// retryable serialization 409, since retried by driveResult).
		t.Fatalf("the state file for %s carries no attempt credentials (attempt_id=%q, secret set=%v); "+
			"pf_commit's lock gate and pf_wrap's complete_attempt authenticate with them, so a walk "+
			"past this point would be measuring the failure path. The claim drive above must have "+
			"been refused — pf_claim_work_item refusals this run: %v",
			wiID, sf.AttemptID, sf.SessionSecret != "", w.failed["pf_claim_work_item"])
	}
	sf.Worktrees = map[string]string{repo: wt}
	if err := config.WriteStateFile(sf); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
}

// ─────────────────────────────── the walk ────────────────────────────────────

// runLiveGitKeyWalk drives the six git-dependent tools once each (pf_pr twice,
// for its two response shapes) and folds their keys into w.
//
// Order is a precondition chain, not a preference. pf_diff has to come before
// pf_commit, because `git diff HEAD` is empty once the change is committed and
// that tool's only evidence of having run is the diff text. pf_push needs the
// commit pf_commit made. pf_ship and pf_wrap are after the pf_pr rows because
// those flip the stub to "a PR is already open on this branch", which is what
// routes their push onto it and fills in pr/pr_action/pushed_sha instead of
// leaving the PR half unexercised. And pf_wrap is LAST because it terminates the
// attempt and deletes the state file every other tool here resolves through.
func runLiveGitKeyWalk(t *testing.T, w *liveKeyWalk) {
	t.Helper()
	s := w.s
	root := t.TempDir()
	// A workspace root of its own, so the state file this walk rewrites cannot
	// collide with the one runLiveKeyWalk's work item is using.
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)
	fx := newLiveGitFixture(t, root)
	// Checked before anything is driven: if the stub does not derive its field
	// set, pf_pr's half of this walk is a mirror of this file and the rest of the
	// run would be recording that mirror as evidence.
	fx.assertFieldSetIsDerived(t)
	stamp := time.Now().UnixNano()

	wi := w.drive(t, "pf_create_work_item", map[string]any{
		"project": s.project,
		"goal":    fmt.Sprintf("drive the six git-dependent tools against a real repository %d", stamp),
		"wi_type": "chore",
	})
	wiID, _ := wi["id"].(string)
	if wiID == "" {
		t.Fatalf("pf_create_work_item returned no id for the git walk's subject; refusals so far: %v", w.failed)
	}
	w.drive(t, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": fmt.Sprintf("livekeys-git-%d", stamp),
	})
	adoptLiveGitWorktree(t, w, wiID, liveGitRepoName, fx.wt)

	base := map[string]any{"work_item_id": wiID, "repo": liveGitRepoName}
	args := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// ── pf_diff: a tracked file, modified but not staged ────────────────────
	fx.write(t, "seed.txt", "the aihub#501 live git walk modified this line\n")
	diff, _ := w.driveResult(t, "pf_diff", args(nil))
	if !strings.Contains(diff, "seed.txt") {
		t.Errorf("pf_diff returned %q, which does not name the file the walk just changed. "+
			"liveWalkProseOnly asserts pf_diff carries no JSON keys; without this check that arm "+
			"is equally satisfied by a diff that never ran, which is the vacuous pass it exists "+
			"to refuse.", liveKeysAbbrev(diff))
	}

	// ── pf_commit: the lock gate reaches the live server here ───────────────
	//
	// `paths` is explicit so `files` in the response is a list rather than null,
	// and so the gate is asked about a path this attempt demonstrably does not
	// already hold — which is what makes lock_gate report `acquired` and puts
	// locks_acquired_for in the response. All six of pf_commit's card keys come
	// back from this one call.
	committed := w.drive(t, "pf_commit", args(map[string]any{
		"message": "aihub#501: the live key walk's first commit",
		"paths":   []any{"seed.txt"},
	}))
	if gate, _ := committed["lock_gate"].(string); gate != "acquired" {
		t.Errorf("pf_commit reported lock_gate=%q, want \"acquired\". The gate is the half of this "+
			"tool that talks to the server, so any other value means the response being measured "+
			"is not the one a real commit produces. Full response: %v", gate, committed)
	}

	// ── pf_push: a real push to the local bare origin ───────────────────────
	pushed := w.drive(t, "pf_push", args(nil))
	if got := fx.remoteSHA(t, liveGitBranch); got == "" || got != pushed["base_sha_at_push"] {
		t.Errorf("pf_push reported base_sha_at_push=%v but origin holds %q for %s. The keys this "+
			"walk records are only the live contract if the call really pushed.",
			pushed["base_sha_at_push"], got, liveGitBranch)
	}

	// ── pf_pr, both response shapes ─────────────────────────────────────────
	//
	// First with no PR on the branch: gh prints a bare URL, and GHCreatePR's
	// unmarshal-failure path turns that into {"url": …}. Then with one, so
	// GHCreatePR routes through GHGetPR and the response is the field set
	// ghGetPRFields names. The union is what pf_pr's card has to cover.
	w.drive(t, "pf_pr", args(map[string]any{
		"title": "aihub#501 live key walk", "body": "opened by the aihub#501 live git walk",
	}))
	fx.declareExistingPR(t, "OPEN")
	w.drive(t, "pf_pr", args(map[string]any{
		"title": "aihub#501 live key walk", "body": "the second call finds the PR already open",
	}))

	// ── pf_ship: commit + push + PR in one call ─────────────────────────────
	//
	// A second file, so the commit stage has something to do and the gate has a
	// path outside the attempt's lock set again. The open PR routes the push onto
	// it (pushed_to_existing_pr) rather than opening a second one.
	fx.write(t, "shipped.txt", "the file pf_ship commits\n")
	shipped := w.drive(t, "pf_ship", args(map[string]any{
		"message":  "aihub#501: the live key walk's shipped commit",
		"paths":    []any{"shipped.txt"},
		"pr_title": "aihub#501 live key walk",
		"pr_body":  "shipped by the aihub#501 live git walk",
	}))
	if stage, _ := shipped["stage"].(string); stage != "done" {
		t.Errorf("pf_ship stopped at stage=%q; only a chain that reached `done` produces the "+
			"success-path key set this walk is recording. Full response: %v", stage, shipped)
	}
	if head := runGit(t, fx.wt, "rev-parse", "HEAD"); head != fx.remoteSHA(t, liveGitBranch) {
		t.Errorf("pf_ship reported stage=done but worktree HEAD %s is not what origin holds for %s (%s)",
			head, liveGitBranch, fx.remoteSHA(t, liveGitBranch))
	}

	// ── pf_wrap: LAST. It completes the attempt and deletes the state file ──
	//
	// `note` is passed so note_emitted — a card key of this tool and of no other
	// path through it — is exercised.
	wrapped := w.drive(t, "pf_wrap", args(map[string]any{
		"pr_title": "aihub#501 live key walk",
		"pr_body":  "wrapped by the aihub#501 live git walk",
		"note":     "wrapped: the aihub#501 live git walk finished with this work item",
		// aihub#350: a wrap that omits derived is refused before the push half.
		"derived": []any{},
	}))
	if ok, _ := wrapped["ok"].(bool); !ok {
		t.Errorf("pf_wrap did not succeed, so the keys of its success path went unmeasured: %v\n"+
			"refusals across the whole walk: %v", wrapped, w.failed)
	}
	if _, err := config.ResolveStateFile(wiID); err == nil {
		t.Errorf("pf_wrap left the state file for %s in place; it deletes it on the terminal "+
			"transition, so a surviving file means the tool did not reach that transition and "+
			"complete_result in the response above is not the live one", wiID)
	}
}

// liveGitToolsDriven is the set runLiveGitKeyWalk is responsible for, asserted
// against what the walk actually observed so the six cannot fall out of it one at
// a time. It is the same double entry liveWalkOutOfReach provides for the tools
// nothing drives: a count on its own would be satisfied by any six tools.
var liveGitToolsDriven = []string{
	"pf_commit", "pf_diff", "pf_pr", "pf_push", "pf_ship", "pf_wrap",
}

// assertLiveGitWalkDroveAllSix is called from K10's assertion phase.
//
// pf_diff is checked through w.prose and the other five through w.observed,
// which is not a special case but the actual difference between them: a raw diff
// carries no top-level keys, so "observed" is not a thing that can be true of it.
func assertLiveGitWalkDroveAllSix(t *testing.T, w *liveKeyWalk) {
	t.Helper()
	for _, tool := range liveGitToolsDriven {
		if w.observed[tool] != nil || w.prose[tool] {
			continue
		}
		t.Errorf("K10 GIT_WALK_GAP: %s is one of the six tools aihub#501 added a live arm for, but "+
			"the git walk produced no observation of it (refusals: %v). Their cards rested on the "+
			"aihub#412 corpus copy alone before that arm existed, so a tool dropping out of it "+
			"returns them to a state where nothing measures them against a running server.",
			tool, w.failed[tool])
	}
}

// assertFieldSetIsDerived is the arm that holds the stub gh honest.
//
// The whole non-circularity claim in this file's header is that the stub answers
// `pr list` with the fields aihub ASKED for, so pf_pr's observed key set tracks
// coding.ghGetPRFields instead of a list written here. That claim is about the
// stub's behaviour, so it is measured rather than asserted in a comment: the stub
// is invoked with a field list of this test's own choosing — deliberately not the
// one aihub uses — and its output keys are compared against it.
//
// 🔴 IT EXECS fx.ghPath, NEVER `gh` OFF PATH. Resolving the name would find the
// operator's real `gh` the moment anything about the PATH override changed, and
// the package directory this test runs in is inside a repository with a GitHub
// remote — so a probe that missed the stub would not fail, it would query
// GitHub. An arm written to prove there is no network in this test must not be
// the one thing in it that reaches out.
//
// It runs against its own state directory for the same reason: the walk's state
// changes as it goes, and a probe whose answer depended on when it was called
// would be measuring the walk rather than the stub.
func (fx *liveGitFixture) assertFieldSetIsDerived(t *testing.T) {
	t.Helper()
	probeState := t.TempDir()
	if err := os.WriteFile(filepath.Join(probeState, "pr"), []byte("1"), 0o644); err != nil {
		t.Fatalf("write probe state: %v", err)
	}
	want := []string{"url", "number", "aihub501Probe"}
	cmd := exec.Command(fx.ghPath, "pr", "list", "--head", liveGitBranch,
		"--state", "all", "--json", strings.Join(want, ","))
	cmd.Env = append(os.Environ(), "PF_FAKE_GH_STATE="+probeState)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("K10 STUB_GH_UNUSABLE: invoking the stub gh at %s failed: %v", fx.ghPath, err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("K10 STUB_GH_UNUSABLE: the stub gh answered %q, which is not one JSON row: %v", out, err)
	}
	for _, k := range want {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("K10 STUB_GH_NOT_DERIVED: the stub gh was asked for --json %v and answered "+
				"without %q. pf_pr's live key set is then a copy of a list in this file rather "+
				"than of coding.ghGetPRFields, which makes that tool's arm a mirror of its own "+
				"fixture — the failure the whole card set exists to end.", want, k)
		}
	}
	if len(rows[0]) != len(want) {
		t.Errorf("K10 STUB_GH_NOT_DERIVED: the stub gh was asked for %d field(s) and answered with "+
			"%d (%v). Extra fields are as bad as missing ones: they would be recorded as part of "+
			"pf_pr's live surface while coming from nowhere in aihub.",
			len(want), len(rows[0]), rows[0])
	}
}
