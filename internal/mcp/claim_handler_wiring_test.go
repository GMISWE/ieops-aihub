package mcp_test

// The claim HANDLER hop for the task-branch work (aihub#322).
//
// WHY THIS FILE EXISTS, given claim_worktree_test.go already has fifteen tests:
// all of those call addClaimWorktree directly. Removing the `mode` parameter
// made the bug unrepresentable INSIDE that function — but it says nothing about
// the caller. Re-gating one level up,
//
//	if mode == "resume" { addClaimWorktree(ctx, srcPath, wtPath, branchNames) }
//
// would leave every one of those fifteen green while reproducing exactly the
// defect they were written for. A contract with N hops needs N assertions, and
// this is the hop that had none.
//
// It drives the real MCP tool over the in-memory transport against a fake aihub
// (the harness in tools_fusion_test.go), a real .polyforge.yaml, and a real git
// clone — so what is asserted is the observable outcome of pf_claim_work_item,
// not the shape of the code that produces it.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// claimWorkspace is a workspace root the claim handler can actually resolve:
// a .polyforge.yaml naming one project with one repo, and a .repo/<repo> clone
// of a bare origin.
type claimWorkspace struct {
	root string
	src  string
}

func newClaimWorkspace(t *testing.T) *claimWorkspace {
	t.Helper()
	root := t.TempDir()
	// ⚠️ Isolation. Without this, config.StateDir() walks up for .polyforge.yaml
	// and lands on the live workspace, whose state directory holds every claimed
	// work item's credentials — and this test's claim WRITES a state file.
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)

	yaml := "version: 1\n" +
		"projects:\n" +
		"    aihub:\n" +
		"        repos:\n" +
		"            - name: aihub\n" +
		"              url: git@example.com:test/aihub.git\n"
	if err := os.WriteFile(filepath.Join(root, ".polyforge.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("write .polyforge.yaml: %v", err)
	}

	w := &claimWorkspace{root: root, src: filepath.Join(root, ".repo", "aihub")}
	bare := filepath.Join(root, "origin.git")
	wsGit(t, "", "init", "--bare", "-q", "-b", "main", bare)
	wsGit(t, "", "clone", "-q", bare, w.src)
	wsGit(t, w.src, "config", "user.email", "t@t.test")
	wsGit(t, w.src, "config", "user.name", "t")
	wsWrite(t, w.src, "main.txt", "base")
	wsGit(t, w.src, "add", "main.txt")
	wsGit(t, w.src, "commit", "-q", "-m", "base")
	wsGit(t, w.src, "push", "-q", "-u", "origin", "main")
	return w
}

// legacyBranchWithMarker lays down a pre-1.1.18 branch carrying a commit that
// exists nowhere else, then returns the clone to main.
func (w *claimWorkspace) legacyBranchWithMarker(t *testing.T, branch, marker string) {
	t.Helper()
	wsGit(t, w.src, "checkout", "-q", "-b", branch, "main")
	wsWrite(t, w.src, marker, marker)
	wsGit(t, w.src, "add", marker)
	wsGit(t, w.src, "commit", "-q", "-m", "prior work")
	wsGit(t, w.src, "checkout", "-q", "main")
}

func wsWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func wsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// claimResponse is what the fake aihub answers with. `goal` is the field
// aihub#322 added to domain.ClaimResponse; the branch name is derived from it.
func claimResponse(wiID, slug, project, goal string) map[string]any {
	return map[string]any{
		"attempt_id":            "ra_wiring",
		"claim_epoch":           1,
		"acquired_locks":        []any{},
		"current_attempt_epoch": 1,
		"id":                    wiID,
		"slug":                  slug,
		"project":               project,
		"goal":                  goal,
	}
}

// TestClaimHandlerCreatesTheWorktreeOnANonResumeClaim is the wiring assertion.
//
// `mode` is deliberately absent from the arguments — that is the third of the
// three shapes that send a non-resume claim (the others are an explicit
// mode="fresh" and a force takeover), and the one that arrives as "" rather than
// as any word a conditional would obviously be testing. A legacy branch holds
// the work and the worktree directory does not exist, so the handler's os.Stat
// reuse cannot fire and the branch lookup is genuinely reached.
//
// MUTANT: wrap the addClaimWorktree call in the claim handler in
// `if strArg(args, "mode") == "resume" { ... }`. Every test in
// claim_worktree_test.go stays green; this one goes red on both assertions —
// there is no worktrees key in the response at all, and the prior agent's commit
// is nowhere.
func TestClaimHandlerCreatesTheWorktreeOnANonResumeClaim(t *testing.T) {
	const wiID = "wi_01JWIRINGABCDEFGH"
	const legacy = "polyforge/ABCDEFGH" // last 8 of the id, the pre-1.1.18 name

	w := newClaimWorkspace(t)
	w.legacyBranchWithMarker(t, legacy, "prior-agent-work.txt")

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+wiID+"/claim", func(map[string]any) (int, any) {
		return 200, claimResponse(wiID, "aihub#322", "aihub", "readable task branch names")
	})

	result, isErr := callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id":    wiID,
		"idempotency_key": "idem-wiring-1",
		// no "mode" — this is the shape that arrives as ""
	})
	if isErr {
		t.Fatalf("pf_claim_work_item failed: %v", result)
	}

	worktrees, ok := result["worktrees"].(map[string]any)
	if !ok || worktrees["aihub"] == nil {
		t.Fatalf("the claim returned no worktree for repo aihub: %v", result)
	}
	wt := fmt.Sprint(worktrees["aihub"])

	if want := filepath.Join(w.root, "pf.aihub-322", "aihub"); wt != want {
		t.Errorf("worktree is at %q, want %q", wt, want)
	}
	if got := wsGit(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != legacy {
		t.Errorf("worktree is on %q, want the legacy branch %q", got, legacy)
	}
	// The load-bearing assertion: the prior agent's commit, not the branch name.
	if _, err := os.Stat(filepath.Join(wt, "prior-agent-work.txt")); err != nil {
		t.Errorf("prior-agent-work.txt is not in the worktree — the claim started over instead of attaching: %v", err)
	}
}

// TestClaimHandlerNamesTheBranchAfterTheGoal is the other half of the same hop:
// the goal has to travel all the way from the claim response into the branch
// name. domain.ClaimResponse.Goal, the JSON field, the handler's
// result["goal"] read and newClaimBranchNames are four separate places where it
// can be dropped, and dropping it is silent — the claim still succeeds, on
// polyforge/<project>-<seq>, which is a perfectly legal name.
//
// MUTANT: delete the `wiGoal, _ := result["goal"].(string)` line and pass "".
// The branch becomes polyforge/aihub-322 and this goes red, while every
// derivation test in branchname_test.go stays green because they call
// newClaimBranchNames directly.
func TestClaimHandlerNamesTheBranchAfterTheGoal(t *testing.T) {
	const wiID = "wi_01JGOALWIREDXYZ99"

	newClaimWorkspace(t) // sets POLYFORGE_WORKSPACE_ROOT and lays down the clone

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+wiID+"/claim", func(map[string]any) (int, any) {
		return 200, claimResponse(wiID, "aihub#322", "aihub", "Readable TASK/branch: names!!")
	})

	result, isErr := callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id":    wiID,
		"idempotency_key": "idem-wiring-2",
		"mode":            "fresh",
	})
	if isErr {
		t.Fatalf("pf_claim_work_item failed: %v", result)
	}

	worktrees, ok := result["worktrees"].(map[string]any)
	if !ok || worktrees["aihub"] == nil {
		t.Fatalf("the claim returned no worktree for repo aihub: %v", result)
	}
	wt := fmt.Sprint(worktrees["aihub"])

	const want = "polyforge/aihub-322-readable-task-branch-names"
	if got := wsGit(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != want {
		t.Errorf("worktree is on %q, want %q — the goal did not reach the branch name", got, want)
	}
}
