package mcp_test

// aihub#366 — the WIRING hop of the commit-time lock gate.
//
// Three layers have to agree for this feature to do anything, and each is
// tested where it lives:
//
//	internal/coding      the change set and the refusal (commit_gate_test.go)
//	internal/domain      coverage, acquisition and the 409 (commit_locks*.go)
//	HERE                 that pf_commit/pf_ship actually CALL it, send the right
//	                     body, and turn its answers into what the caller reads
//
// The middle two can both be perfect while nothing is wired together, and that
// failure is invisible from either end. So these tests drive real MCP tool calls
// against a fake aihub and a real git worktree, and assert on the HTTP request
// that went out and the JSON that came back.
//
// Run: go test ./internal/mcp/ -run TestCommitGateWire -v   (no database needed)

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

const gateWIID = "wi_GateWire"

// gateRepo builds a git worktree plus the state file pf_commit resolves it
// through, and returns the workspace root and the worktree path.
func gateRepo(t *testing.T) (wsRoot, wt string) {
	t.Helper()
	wsRoot = t.TempDir()
	wt = filepath.Join(wsRoot, "pf.aihub-366", "aihub")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "polyforge/aihub-366-gate", wt},
		{"-C", wt, "config", "user.email", "t@t.com"},
		{"-C", wt, "config", "user.name", "t"},
		{"-C", wt, "commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	t.Setenv("POLYFORGE_WORKSPACE_ROOT", wsRoot)
	sf := &config.StateFile{
		WIID:          gateWIID,
		Slug:          "aihub#366",
		Project:       "aihub",
		AttemptID:     "ra_gatewire",
		ClaimEpoch:    7,
		SessionSecret: "gate-wire-secret",
		Worktrees:     map[string]string{"aihub": wt},
	}
	if err := config.WriteStateFile(sf); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
	return wsRoot, wt
}

func gateWrite(t *testing.T, wt, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func gateHEAD(t *testing.T, wt string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

const commitLocksPath = "/v1/work_items/" + gateWIID + "/commit_locks"

// TestCommitGateWire_SendsTheStagedPathsAndTheCredentials pins what goes over
// the wire.
//
// Every field asserted here is one the server cannot do its job without, and
// each has a distinct way of being wrong: paths from the wrong diff range,
// a repo the caller forgot to forward (which the server rejects, because an
// unqualified probe over-reports coverage), or attempt credentials read from
// somewhere other than the state file.
func TestCommitGateWire_SendsTheStagedPathsAndTheCredentials(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)

	f.on(commitLocksPath, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{
			"checked": 2, "covered": []string{"a.txt"}, "probed": 1,
			"acquired_paths": []string{"b.txt"},
		}
	})

	gateWrite(t, wt, "a.txt", "a")
	gateWrite(t, wt, "b.txt", "b")

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot,
		"work_item_id":   gateWIID,
		"repo":           "aihub",
		"message":        "feat: two files",
	})
	if isErr {
		t.Fatalf("pf_commit failed: %v", out)
	}

	var body map[string]any
	for _, c := range f.recorded() {
		if c.Path == commitLocksPath {
			body = c.Body
		}
	}
	if body == nil {
		t.Fatalf("pf_commit never called %s; the gate is not wired in at all. Paths seen: %v",
			commitLocksPath, f.paths())
	}

	if body["repo"] != "aihub" {
		t.Errorf("repo = %v, want \"aihub\"; the server rejects an empty repo because an "+
			"unqualified probe reports another repo's lock as coverage", body["repo"])
	}
	if body["attempt_id"] != "ra_gatewire" || body["session_secret"] != "gate-wire-secret" {
		t.Errorf("credentials = %v / %v, want the state file's", body["attempt_id"], body["session_secret"])
	}
	var got []string
	for _, p := range body["paths"].([]any) {
		got = append(got, p.(string))
	}
	// Both files, from the INDEX. The default diff range would send an empty
	// list here and the gate would pass everything, forever, in silence.
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "b.txt" {
		t.Errorf("paths = %v, want [a.txt b.txt] — the staged set of the pending commit", got)
	}

	// The auto-acquisition must be visible where the call was made.
	if out["lock_gate"] != "acquired" {
		t.Errorf("lock_gate = %v, want \"acquired\"; a call that silently widened the "+
			"attempt's lock set is the very shape this feature exists to remove", out["lock_gate"])
	}
	acq, _ := out["locks_acquired_for"].([]any)
	if len(acq) != 1 || acq[0] != "b.txt" {
		t.Errorf("locks_acquired_for = %v, want [b.txt]", out["locks_acquired_for"])
	}
}

// TestCommitGateWire_AutoAcquireDoesNotInterrupt is requirement 2(a) at the tool
// boundary: when the missing locks WERE taken, the commit completes. Not "the
// message was gentle" — the commit exists.
func TestCommitGateWire_AutoAcquireDoesNotInterrupt(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)

	f.on(commitLocksPath, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{
			"checked": 1, "covered": []string{}, "probed": 1,
			"acquired_paths": []string{"undeclared.txt"},
		}
	})
	gateWrite(t, wt, "undeclared.txt", "never declared anywhere")

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: undeclared but acquired",
	})
	if isErr {
		t.Fatalf("the commit was interrupted even though the lock was acquired: %v", out)
	}
	sha, _ := out["sha"].(string)
	if sha == "" || sha == before {
		t.Fatalf("sha = %q, HEAD was %q; the commit did not happen", sha, before)
	}
	if got := gateHEAD(t, wt); got != sha {
		t.Errorf("worktree HEAD = %s, response sha = %s", got, sha)
	}
}

// TestCommitGateWire_ConflictRefusesTheCommit is requirement 2(b): a 409 from
// the server must stop the commit dead and carry the holder through to the
// caller.
func TestCommitGateWire_ConflictRefusesTheCommit(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)

	f.on(commitLocksPath, func(map[string]any) (int, any) {
		return http.StatusConflict, map[string]any{
			"code":    "CONFLICT_LOCK_TAKEN",
			"message": "this commit changes 1 file(s) locked by another attempt",
			"details": map[string]any{
				"conflicts": []map[string]any{{
					"path": "contested.txt", "attempt_id": "ra_theirs",
					"actor_display": "someone-else", "work_item_slug": "aihub#999",
				}},
			},
		}
	})
	gateWrite(t, wt, "contested.txt", "somebody else owns this")

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: should not land",
	})
	if !isErr {
		t.Fatalf("pf_commit succeeded over a file another attempt holds: %v", out)
	}
	if got := gateHEAD(t, wt); got != before {
		t.Fatalf("HEAD moved to %s; the refusal did not stop the commit", got)
	}

	// The holder has to survive the trip. Without it the caller's next move is
	// a pf_read_events lookup — the round-trip this design exists to avoid.
	text, _ := out["_raw"].(string)
	for _, want := range []string{"CONFLICT_LOCK_TAKEN", "contested.txt", "someone-else", "aihub#999"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal reaching the caller never mentions %q: %s", want, text)
		}
	}
}

// TestCommitGateWire_UnreachableServerFailsClosed pins the deliberate trade.
//
// "The gate could not run" is not "the gate found nothing", and folding the
// first into the second is how a gate becomes decoration without anything going
// red. The message must also say which of the two happened, or the caller
// retries a conflict or escalates a network blip.
func TestCommitGateWire_UnreachableServerFailsClosed(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)

	f.on(commitLocksPath, func(map[string]any) (int, any) {
		return http.StatusBadGateway, map[string]any{"code": "UPSTREAM", "message": "boom"}
	})
	gateWrite(t, wt, "unchecked.txt", "x")

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: unchecked",
	})
	if !isErr {
		t.Fatalf("pf_commit committed although the lock check never completed: %v", out)
	}
	if got := gateHEAD(t, wt); got != before {
		t.Errorf("HEAD moved to %s", got)
	}
	text, _ := out["_raw"].(string)
	if !strings.Contains(text, "could not be completed") {
		t.Errorf("the error does not distinguish a failed CHECK from a real conflict: %s", text)
	}
	if strings.Contains(text, "CONFLICT_LOCK_TAKEN") {
		t.Errorf("a transport failure was reported as a lock conflict: %s", text)
	}
}

// TestCommitGateWire_DeletingTheStateFileIsNotABypass forecloses the obvious
// escape hatch, and records a measured fact that an earlier version of the gate
// got wrong.
//
// That version PASSED the commit when the state file was missing, reasoning
// that no state file means no attempt, hence no lock set, hence nothing to
// check — and that refusing would break pf_commit in an already-wrapped work
// item's worktree. This test disproved the second half: both tools resolve the
// worktree through coding.WorktreePath, which reads the SAME state file and
// fails before the gate is ever reached. So the branch conceded nothing to a
// real case; it was a way to switch the gate off with `rm`.
//
// The assertion is therefore that the commit FAILS, that nothing was committed,
// and — the load-bearing part — that it failed before the gate, so no wording
// change in the gate can accidentally reopen the hatch.
func TestCommitGateWire_DeletingTheStateFileIsNotABypass(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)
	if err := os.Remove(filepath.Join(wsRoot, ".polyforge", "state", gateWIID+".json")); err != nil {
		t.Fatalf("remove state file: %v", err)
	}
	gateWrite(t, wt, "orphan.txt", "x")

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: no attempt",
	})
	if !isErr {
		t.Fatalf("pf_commit succeeded with no state file, so deleting one file turns the "+
			"lock gate off: %v", out)
	}
	if got := gateHEAD(t, wt); got != before {
		t.Errorf("HEAD moved to %s", got)
	}
	if raw, _ := out["_raw"].(string); !strings.Contains(raw, "state file") {
		t.Errorf("the failure does not name the missing state file: %s", raw)
	}
	for _, p := range f.paths() {
		if p == commitLocksPath {
			t.Error("the gate called the server with no attempt credentials to send")
		}
	}
}

// TestCommitGateWire_ShipRefusalReportsNothingHappened is the pf_ship arm.
//
// pf_ship answers with a JSON object even on failure, and its whole point is
// that the object says what already happened. A gate refusal must therefore
// land as stage="commit" with committed=false and no push — not as a bare error
// string that leaves the caller unable to tell whether a commit is sitting in
// the worktree.
func TestCommitGateWire_ShipRefusalReportsNothingHappened(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)

	f.on(commitLocksPath, func(map[string]any) (int, any) {
		return http.StatusConflict, map[string]any{
			"code": "CONFLICT_LOCK_TAKEN", "message": "locked",
			"details": map[string]any{"blocked_paths": []string{"contested.txt"}},
		}
	})
	gateWrite(t, wt, "contested.txt", "x")

	out, isErr := callTool(t, f, "pf_ship", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: blocked",
		"pr_title": "t", "pr_body": "b",
	})
	if isErr {
		t.Fatalf("pf_ship returned a bare error; its failure contract is a JSON object: %v", out)
	}
	if ok, _ := out["ok"].(bool); ok {
		t.Fatal("ok=true for a ship whose commit was refused")
	}
	if out["stage"] != "commit" {
		t.Errorf("stage = %v, want \"commit\"", out["stage"])
	}
	if committed, _ := out["committed"].(bool); committed {
		t.Error("committed=true for a refused commit")
	}
	if pushed, ok := out["pushed"].(bool); ok && pushed {
		t.Error("pushed=true; a refused commit must never reach the remote")
	}
	if got := gateHEAD(t, wt); got != before {
		t.Errorf("HEAD moved to %s", got)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "CONFLICT_LOCK_TAKEN") {
		t.Errorf("the ship response does not carry the conflict code: %s", raw)
	}
}
