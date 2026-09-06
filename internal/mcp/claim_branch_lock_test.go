package mcp_test

// The CLIENT half of aihub#356: pf_claim_work_item tells the server which branch
// each declared repo's worktree is really about to be on, so the git_branch lock
// is keyed on that instead of on declared_resources[].task_branch.
//
// ─── What was wrong, measured ───────────────────────────────────────────────
//
//	lock key         ieops-ctlchain/polyforge/pin-bump-token       (declared)
//	worktree HEAD    polyforge/ieops-996-proxy-pin-bump-...        (derived)
//
// The declared name is not what anyone works on and never was — this file
// computes the branch at claim time and stores it nowhere, while pf_ship,
// pf_pr, pf_push and pf_wrap all read `git rev-parse --abbrev-ref HEAD` out of
// the worktree. So the lock guarded a name no attempt ever checked out, and two
// agents on one branch of one repo could not collide on it.
//
// ─── Why the assertions read the wire and not a Go field ────────────────────
//
// keyedBranch below decodes the recorded claim body as plain JSON and mirrors
// the server's fallback rather than importing domain.ClaimRequest. That is
// deliberate: it keeps this file COMPILING AND RUNNING against a tree without
// the fix, which is the difference between a negative control and a build
// error, and it makes the failure message name the key such a tree would really
// insert. The server half — that the reported branch reaches the lock key — is
// internal/domain/claim_branch_lock_test.go, and both sides anchor on the wire
// literal "task_branches", so a rename on either goes red.
//
// ─── Scope ─────────────────────────────────────────────────────────────────
//
// Nothing here asserts what a branch is NAMED. aihub#322 owns the naming rule
// and aihub#356 does not touch it; every name in this file is measured by
// running a claim, never written down, so a future change to that rule cannot
// make these tests assert against a fiction.
//
// Run: go test ./internal/mcp/ -run TestClaim -v   (no database needed)

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registerClaimFixture answers the two calls a claim makes: the pre-claim GET
// that supplies project/seq/goal/declared_resources, and the claim itself.
//
// declaredBranch goes into every repo entry's task_branch, so it is the key the
// server derives for any repo the client reports nothing about.
func registerClaimFixture(f *fakeAihub, wiID, slug, goal, declaredBranch string, extraRepos ...string) {
	decl := []any{}
	for _, repo := range append([]string{"aihub"}, extraRepos...) {
		decl = append(decl, map[string]any{
			"type": "repo", "uri": "repo:" + repo,
			"task_branch": declaredBranch, "intent": "write",
		})
	}
	f.on("/v1/work_items/"+wiID, func(map[string]any) (int, any) {
		return 200, map[string]any{
			"id": wiID, "slug": slug, "project": "aihub", "goal": goal,
			"declared_resources": decl,
		}
	})
	f.on("/v1/work_items/"+wiID+"/claim", func(map[string]any) (int, any) {
		return 200, claimResponse(wiID, slug, "aihub", goal)
	})
}

// claimOnce drives pf_claim_work_item and returns the branch the worktree came
// out on. wtDir is the pf.<project>-<seq> directory the claim is expected to
// create.
func claimOnce(t *testing.T, f *fakeAihub, root, wtDir, wiID, idemKey string) string {
	t.Helper()
	result, isErr := callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": idemKey,
	})
	if isErr {
		t.Fatalf("pf_claim_work_item failed: %v", result)
	}
	wt := filepath.Join(root, wtDir, "aihub")
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("the claim created no worktree at %s: %v (response: %v)", wt, err, result)
	}
	// Every claim in this file is healthy, so the aihub#356 post-claim check —
	// "the branch I keyed the lock on is the branch the worktree came out on" —
	// must stay silent. An alarm that fires on ordinary traffic is worse than no
	// alarm: it teaches the reader to skip the field where the real warning
	// would appear.
	if p, present := result["worktree_problems"]; present {
		t.Errorf("a healthy claim reported worktree_problems: %v", p)
	}
	return wsGit(t, wt, "rev-parse", "--abbrev-ref", "HEAD")
}

// keyedBranch is the branch value the SERVER will key this repo's git_branch
// lock on, read off the claim request the MCP tool actually sent.
//
// The fallback mirrors domain.ClaimRequest.EffectiveDeclaredResource: an absent
// or empty report leaves the declaration in force. So on a tree without the fix
// this returns `declared` — the ieops#996 key — and the failure message is true
// rather than a report of a missing field.
func keyedBranch(t *testing.T, f *fakeAihub, wiID, repo, declared string) string {
	t.Helper()
	for _, c := range f.recorded() {
		if c.Method != http.MethodPost || c.Path != "/v1/work_items/"+wiID+"/claim" {
			continue
		}
		if tb, ok := c.Body["task_branches"].(map[string]any); ok {
			if b, _ := tb[repo].(string); b != "" {
				return b
			}
		}
		return declared
	}
	t.Fatalf("no claim request reached the server, so nothing keyed a lock")
	return ""
}

// TestClaimKeysTheBranchLockOnTheBranchItCreates is the negative control for the
// reported defect: a claim that has to CREATE its branch.
//
// ⚠️ THE CREATE IS THE REPRODUCTION CONDITION, not incidental colour. ieops#994
// is the recorded control where the branch already existed before the claim and
// the two names agreed, so a fixture that handed the claim a ready-made branch
// would pass on a broken tree. The guard below refuses to assert anything until
// it has confirmed the worktree did not land on the declared name.
//
// MUTANT: drop the `body["task_branches"] = taskBranches` line from the claim
// handler, or have claimTaskBranches return nil. This goes red naming both
// branches; every test in claim_worktree_test.go and claim_handler_wiring_test.go
// stays green, because the worktree is still created correctly — only the lock
// is pointed at a fiction.
func TestClaimKeysTheBranchLockOnTheBranchItCreates(t *testing.T) {
	const (
		wiID     = "wi_01JBRANCHLOCK001"
		slug     = "aihub#356"
		goal     = "key the git branch lock on the branch that is really checked out"
		declared = "polyforge/pin-bump-token" // the shape ieops#996 declared
	)

	w := newClaimWorkspace(t) // the clone holds main and nothing else
	f := newFakeAihub(t)
	registerClaimFixture(f, wiID, slug, goal, declared)

	actual := claimOnce(t, f, w.root, "pf.aihub-356", wiID, "idem-branchlock-created")

	if actual == declared {
		t.Fatalf("fixture: the worktree came out on the declared branch %q, so this test cannot "+
			"tell a fixed tree from a broken one — the claim must have had to CREATE a branch", declared)
	}
	if !strings.HasPrefix(actual, "polyforge/") {
		t.Fatalf("fixture: the claim created no task branch (HEAD is %q)", actual)
	}

	if keyed := keyedBranch(t, f, wiID, "aihub", declared); keyed != actual {
		t.Errorf("the claim had its git_branch lock keyed on %q while the worktree it created is on %q. "+
			"That lock protects a branch nobody is working on, so a second agent takes %q unopposed. "+
			"(declared task_branch: %q)", keyed, actual, actual, declared)
	}
}

// TestClaimKeysTheBranchLockOnTheBranchItAttachesTo is the SECOND direction, and
// the fresh-claim case above cannot reach it.
//
// resolveClaimBranch attaches to a branch that already exists — here through its
// last tier, the <stem>-* glob, which is what catches a work item whose goal has
// been edited since the branch was made. Then "the branch git checks out" and
// "the name this claim computes today" are two DIFFERENT strings, and a fix that
// keyed the lock on the freshly computed name would look correct in every
// fresh-claim test and still be wrong here.
//
// Both names are measured by running real claims rather than written down: a
// literal would go stale the day aihub#322's naming rule changes and would then
// be asserting against a fiction.
//
// MUTANT: make claimBranchForRepo return n.Branch unconditionally (i.e. delete
// its resolveClaimBranch call). The fresh-claim test stays green; this one goes
// red on the second assertion, naming the computed name it followed instead.
func TestClaimKeysTheBranchLockOnTheBranchItAttachesTo(t *testing.T) {
	const (
		wiID     = "wi_01JBRANCHLOCK002"
		slug     = "aihub#356"
		oldGoal  = "an older goal from before somebody edited it"
		newGoal  = "the goal as it reads today after being rewritten"
		declared = "polyforge/pin-bump-token"
	)

	// ── measure what each goal produces, using the real claim path ──────────
	nameForOldGoal := branchAFreshClaimCreates(t, wiID, slug, oldGoal, declared, "idem-measure-old")
	nameForNewGoal := branchAFreshClaimCreates(t, wiID, slug, newGoal, declared, "idem-measure-new")
	if nameForOldGoal == nameForNewGoal {
		t.Fatalf("fixture: both goals derive %q, so this test cannot tell 'the branch git checked out' "+
			"from 'the name this claim computes today'", nameForOldGoal)
	}

	// ── the arm: the old branch exists, the goal has since been rewritten ───
	w := newClaimWorkspace(t)
	w.legacyBranchWithMarker(t, nameForOldGoal, "prior-agent-work.txt")

	f := newFakeAihub(t)
	registerClaimFixture(f, wiID, slug, newGoal, declared)

	actual := claimOnce(t, f, w.root, "pf.aihub-356", wiID, "idem-branchlock-attached")
	wt := filepath.Join(w.root, "pf.aihub-356", "aihub")

	if actual != nameForOldGoal {
		t.Fatalf("fixture: the claim did not attach to the pre-existing branch — HEAD is %q, wanted %q",
			actual, nameForOldGoal)
	}
	// The name alone would also be satisfied by a brand-new branch created under
	// the same name off origin/main; the prior agent's commit is what proves this
	// is the same branch and that attaching, rather than creating, is under test.
	if _, err := os.Stat(filepath.Join(wt, "prior-agent-work.txt")); err != nil {
		t.Fatalf("fixture: the worktree does not carry the prior agent's commit, so the claim "+
			"created a fresh branch rather than attaching: %v", err)
	}

	keyed := keyedBranch(t, f, wiID, "aihub", declared)
	if keyed != actual {
		t.Errorf("the claim keyed its git_branch lock on %q while git checked out %q", keyed, actual)
	}
	if keyed == nameForNewGoal {
		t.Errorf("the key followed the name this claim COMPUTES today (%q) instead of the branch git "+
			"actually checked out (%q) — the two differ only on the attach path, which is why the "+
			"fresh-claim case cannot catch this", nameForNewGoal, actual)
	}
}

// branchAFreshClaimCreates runs one complete claim in a throwaway workspace
// whose clone holds nothing but main, and returns the branch it created.
func branchAFreshClaimCreates(t *testing.T, wiID, slug, goal, declared, idemKey string) string {
	t.Helper()
	w := newClaimWorkspace(t)
	f := newFakeAihub(t)
	registerClaimFixture(f, wiID, slug, goal, declared)
	return claimOnce(t, f, w.root, "pf.aihub-356", wiID, idemKey)
}

// TestClaimLeavesTheDeclarationInForceWithNoWorktree is the containment control:
// the substitution has to be confined to repos this claim really puts a worktree
// on.
//
// A declared repo the workspace config does not list gets no prediction, so
// nothing is on any branch there and declared_resources is the only statement
// anybody has made about it. Reporting a branch for it would key the lock on a
// name as fictional as the one aihub#356 is removing — and worse, it would stop
// the lock colliding with a task_branch a human deliberately declared. That is
// the shape in which "fix the key" degenerates into "the lock no longer protects
// anything".
//
// ⚠️ THE CONTAINMENT IS ON THE PREDICTION, NOT ON THE WORKTREE, and this test
// asserts only the first. "No prediction" implies no worktree; the converse is
// false, and TestClaimReportsAKeyedBranchNoWorktreeConfirms below owns the gap
// between them — a repo predicted for whose `worktree add` fails IS re-keyed,
// which is why that case has to be reported rather than merely documented.
func TestClaimLeavesTheDeclarationInForceWithNoWorktree(t *testing.T) {
	const (
		wiID     = "wi_01JBRANCHLOCK003"
		slug     = "aihub#356"
		goal     = "confine the substitution to repos this claim really checks out"
		declared = "polyforge/hand-declared-branch"
		absent   = "not-in-this-workspace"
	)

	w := newClaimWorkspace(t)
	f := newFakeAihub(t)
	registerClaimFixture(f, wiID, slug, goal, declared, absent)

	actual := claimOnce(t, f, w.root, "pf.aihub-356", wiID, "idem-branchlock-absent")

	if keyed := keyedBranch(t, f, wiID, absent, declared); keyed != declared {
		t.Errorf("a repo with no worktree had its lock re-keyed to %q; with nothing checked out "+
			"anywhere for it, the declared %q is the only branch anyone has named", keyed, declared)
	}
	// The same claim, same call: the repo that IS here must still be corrected,
	// or this control could be satisfied by a fix that does nothing at all.
	if keyed := keyedBranch(t, f, wiID, "aihub", declared); keyed != actual {
		t.Errorf("the repo that does have a worktree was keyed on %q rather than its actual branch %q", keyed, actual)
	}
}

// problemsFrom returns the worktree_problems strings on a claim response.
func problemsFrom(t *testing.T, result map[string]any) []string {
	t.Helper()
	raw, ok := result["worktree_problems"]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("worktree_problems is %T, want a list: %v", raw, raw)
	}
	var out []string
	for _, it := range items {
		s, _ := it.(string)
		out = append(out, s)
	}
	return out
}

// TestClaimReportsAKeyedBranchNoWorktreeConfirms is the aihub#356 review's
// finding 2, and it is the ONE case where this change does not degrade to
// pre-aihub#356 behaviour.
//
// Everywhere else, a claim that cannot predict a branch reports nothing and the
// declaration stands. Here the prediction was made — .repo/<repo> is a directory
// so the guard passes — and the `worktree add` afterwards FAILED. The lock is
// therefore keyed on a branch nothing is on, the declared task_branch has been
// overridden, and the check that exists to notice exactly this used to range
// over the worktrees that succeeded, so it could not see the repo that did not.
//
// ⚠️ THE ORDERING IS WHY THIS CANNOT BE FIXED BY PREDICTING MORE CAREFULLY. The
// key must be chosen before the claim so a real collision can still refuse it
// (claimTaskBranches); the worktree only exists after. So the honest remedy is
// to TELL the caller, which is what this asserts.
//
// MUTANT: in keyedBranchProblems, range over `worktrees` instead of over
// `taskBranches` (the pre-review shape). This goes red where it reads
// worktree_problems back — measured as `worktree_problems = []` — while every
// other test in this file stays green, because on a healthy claim the two sets
// are identical.
func TestClaimReportsAKeyedBranchNoWorktreeConfirms(t *testing.T) {
	const (
		wiID     = "wi_01JBRANCHLOCK004"
		slug     = "aihub#356"
		goal     = "tell the caller when a keyed branch has no worktree behind it"
		declared = "polyforge/hand-declared-branch"
	)

	w := newClaimWorkspace(t)
	// The reviewer's measured probe. .repo/aihub stays a DIRECTORY, so
	// claimBranchForRepo's os.Stat guard passes and a branch is predicted and
	// keyed — but it is no longer a git repository, so `worktree add` fails.
	// This is the shape of a stale or half-deleted clone.
	if err := os.RemoveAll(filepath.Join(w.src, ".git")); err != nil {
		t.Fatalf("break the clone: %v", err)
	}

	f := newFakeAihub(t)
	registerClaimFixture(f, wiID, slug, goal, declared)

	result, isErr := callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": "idem-branchlock-noworktree",
	})
	// The claim itself must still succeed. A broken clone is not a reason to
	// refuse a claim, and turning this into an error would be a far worse
	// regression than the missing warning. Green on both trees.
	if isErr {
		t.Fatalf("pf_claim_work_item failed on a repo with a broken clone: %v", result)
	}
	if ok, _ := result["ok"].(bool); !ok {
		t.Fatalf("claim response is not ok:true: %v", result)
	}

	// ── the two fixture guards, without which this test cannot discriminate ──
	if _, err := os.Stat(filepath.Join(w.root, "pf.aihub-356", "aihub")); err == nil {
		t.Fatalf("fixture: the claim created a worktree after all, so the case under test — " +
			"a branch keyed with no worktree behind it — was never reached")
	}
	keyed := keyedBranch(t, f, wiID, "aihub", declared)
	if keyed == declared {
		t.Fatalf("fixture: the claim reported no branch for aihub, so the declared %q is still in "+
			"force and there is nothing to warn about", declared)
	}

	// ── the assertion ───────────────────────────────────────────────────────
	problems := problemsFrom(t, result)
	found := ""
	for _, p := range problems {
		if strings.HasPrefix(p, "aihub:") && strings.Contains(p, keyed) {
			found = p
		}
	}
	if found == "" {
		t.Fatalf("the claim keyed aihub's git_branch lock on %q, created no worktree for aihub, and "+
			"said nothing about it. That lock names a branch nobody is on AND has overridden the "+
			"declared %q, so a hand-made branch is no longer the one protected — and the only place "+
			"this is observable reported nothing. worktree_problems = %q",
			keyed, declared, problems)
	}
	// The warning has to say the worktree is MISSING, not merely that two names
	// disagree: the remedy is different, and "came out on" would be a lie here.
	if !strings.Contains(found, "NO worktree") {
		t.Errorf("the warning does not say the worktree is missing, so the reader is pointed at the "+
			"wrong remedy: %q", found)
	}
}
