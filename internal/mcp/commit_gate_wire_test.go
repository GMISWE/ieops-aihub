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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
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

// gatePathsSent returns the `paths` array of every gate request made so far, in
// order — one entry per call.
func gatePathsSent(t *testing.T, f *fakeAihub) [][]string {
	t.Helper()
	var out [][]string
	for _, c := range f.recorded() {
		if c.Path != commitLocksPath {
			continue
		}
		raw, ok := c.Body["paths"].([]any)
		if !ok {
			t.Fatalf("gate request carried paths=%v (%T), want an array", c.Body["paths"], c.Body["paths"])
		}
		var one []string
		for _, p := range raw {
			one = append(one, p.(string))
		}
		out = append(out, one)
	}
	return out
}

// gateIndex returns what the worktree's index holds against HEAD.
func gateIndex(t *testing.T, wt string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", wt, "diff", "--cached", "--name-only", "HEAD").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	return strings.Fields(string(out))
}

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

// TestCommitGateWire_NarrowingPathsDoesNotNarrowTheStagedSet is the measurement
// underneath the refusal's advice, kept as a test so the advice cannot drift
// back to a move that does not work.
//
// The obvious reaction to CONFLICT_LOCK_TAKEN is to retry with
// pf_commit(paths=[<the ones that are mine>]) — and the refusal used to say
// exactly that. It cannot work, and it fails in the silent direction: staging
// succeeds, the gate re-runs, and the server is handed the SAME set. Three facts
// compose into that. coding.GitStage only ever ADDS (`git add -- <paths>`); the
// first call's `git add -A` already put everything in the index and a refusal
// leaves it there; and GitStagedPaths reads INDEX vs HEAD, so the width of the
// change set lives in the index, which `paths` never resets.
//
// The assertion is therefore that call 2 sent byte-identical paths to call 1.
// 🔴 If this ever goes red because pf_commit learned to reset the index, that is
// a real widening of a shared tool's contract and an owner decision — and
// domain.CommitLockRefusalAdvice becomes revisitable at the same moment.
// TestCommitGateWire_RefusalAdviceIsExecutableAndWorks is the other end of the
// pair; the two are meant to be changed together or not at all.
//
// Note what this test does NOT say. It shows that narrowing `paths` alone
// changes nothing — not that narrowing is useless. After the index has been
// narrowed by `git restore --staged`, `paths` is exactly what keeps the plain
// `git add -A` from putting the blocked file back, which is why the advice's
// remedy is two steps and why that one is second.
func TestCommitGateWire_NarrowingPathsDoesNotNarrowTheStagedSet(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)

	f.on(commitLocksPath, func(map[string]any) (int, any) {
		return http.StatusConflict, map[string]any{
			"code":    "CONFLICT_LOCK_TAKEN",
			"message": "this commit changes 1 file(s) locked by another attempt",
			"details": map[string]any{
				"blocked_paths": []string{"contested.txt"},
				"conflicts": []map[string]any{{
					"path": "contested.txt", "attempt_id": "ra_theirs",
					"actor_display": "someone-else", "work_item_slug": "aihub#999",
				}},
			},
		}
	})

	gateWrite(t, wt, "contested.txt", "somebody else owns this")
	gateWrite(t, wt, "mine.txt", "this one is mine")

	// Step 1 — the ordinary refused commit.
	if _, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: both files",
	}); !isErr {
		t.Fatal("the first pf_commit was not refused; this test's premise is a refusal")
	}

	// The refusal left the index fully populated. That is the whole mechanism,
	// so it is asserted rather than assumed.
	if idx := gateIndex(t, wt); len(idx) != 2 {
		t.Fatalf("index after the refusal = %v, want both files. If a refusal now cleans "+
			"up after itself the rest of this test is measuring nothing", idx)
	}

	// Step 2 — the "obvious" narrowed retry.
	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: just mine",
		"paths": []any{"mine.txt"},
	})
	if !isErr {
		t.Fatalf("the narrowed retry COMMITTED. paths= now drops files from the pending "+
			"commit, so the refusal's advice can be rewritten — read this test's doc "+
			"comment before doing so: %v", out)
	}

	sent := gatePathsSent(t, f)
	if len(sent) != 2 {
		t.Fatalf("the gate ran %d time(s), want 2 (once per call): %v", len(sent), sent)
	}
	if strings.Join(sent[0], "\x00") != strings.Join(sent[1], "\x00") {
		t.Fatalf("call 1 sent %v and the narrowed call 2 sent %v. They differ, so paths= DOES "+
			"narrow what the gate sees — see this test's doc comment", sent[0], sent[1])
	}
	if len(sent[1]) != 2 {
		t.Fatalf("the narrowed retry sent %v; the premise is that it sends both files", sent[1])
	}
	if got := gateHEAD(t, wt); got != before {
		t.Errorf("HEAD moved to %s; neither call was supposed to commit", got)
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
//
// 🔴 AND THE OBJECT HAS TO AGREE WITH ITSELF. An earlier version of this test
// asserted stage/committed/pushed/code and never looked at `lock_gate`, so the
// field was free to be wrong and was: report() keyed on a single !ran flag that
// is true for all three failure facts, and printed "no staged changes, so no
// files needed locking" next to an `error` naming CONFLICT_LOCK_TAKEN and a
// `side_effects` entry saying files WERE staged. A caller reading fields in a
// different order gets a different answer out of one response. So lock_gate is
// asserted here, and asserted for the specific value — "not not_run" would pass
// on a version that called every failure could_not_run.
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

	if out["lock_gate"] != "refused" {
		t.Errorf("lock_gate = %v, want \"refused\". The gate DID run and it DID refuse; "+
			"any other value contradicts the error and the side effects in this same object: %s",
			out["lock_gate"], raw)
	}
	detail, _ := out["lock_gate_detail"].(string)
	if strings.Contains(detail, "no staged changes") {
		t.Errorf("lock_gate_detail = %q, but side_effects in the same response says the files "+
			"WERE staged. One response, two answers", detail)
	}
}

// TestCommitGateWire_ShipCheckThatCouldNotRunIsNotReportedAsARefusal is the
// third of the three facts !g.ran used to collapse into one.
//
// "Another attempt holds this file" and "I could not find out" lead to opposite
// next moves — wait for a human/the other attempt, versus retry in ten seconds —
// so a response that cannot tell them apart is worse than one that says nothing.
// Both stop the ship identically, which is exactly why nothing else in the
// object distinguishes them.
func TestCommitGateWire_ShipCheckThatCouldNotRunIsNotReportedAsARefusal(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)

	f.on(commitLocksPath, func(map[string]any) (int, any) {
		return http.StatusBadGateway, map[string]any{"code": "UPSTREAM", "message": "boom"}
	})
	gateWrite(t, wt, "unchecked.txt", "x")

	out, isErr := callTool(t, f, "pf_ship", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: unchecked",
		"pr_title": "t", "pr_body": "b",
	})
	if isErr {
		t.Fatalf("pf_ship returned a bare error; its failure contract is a JSON object: %v", out)
	}
	if got := gateHEAD(t, wt); got != before {
		t.Fatalf("HEAD moved to %s although the check never completed", got)
	}
	if out["lock_gate"] != "could_not_run" {
		t.Errorf("lock_gate = %v, want \"could_not_run\": the check failed, it did not find "+
			"a conflict and it did not find nothing to do", out["lock_gate"])
	}
	detail, _ := out["lock_gate_detail"].(string)
	if strings.Contains(detail, "no staged changes") {
		t.Errorf("lock_gate_detail = %q claims nothing was staged; one file was, and it is "+
			"still sitting in the index", detail)
	}
}

// adviceRecipe splits an advice string into the ordered steps it numbers
// "(1)", "(2)", … — everything before "(1)" is prose and is dropped, and each
// step runs to the next marker or to the end of the string.
//
// This exists because the previous guard on that advice was a substring
// assertion, and a substring assertion cannot answer the only question that
// matters about a remedy: does performing it work? Turning the advice into an
// ordered program that a test can actually RUN is what closes that gap, so the
// numbering is part of domain.CommitLockRefusalAdvice's contract rather than
// formatting. An advice with no numbered steps returns nothing here, and the
// test that calls this fails loudly rather than silently verifying nothing.
func adviceRecipe(advice string) []string {
	var steps []string
	for n := 1; ; n++ {
		i := strings.Index(advice, fmt.Sprintf("(%d) ", n))
		if i < 0 {
			return steps
		}
		rest := advice[i+len(fmt.Sprintf("(%d) ", n)):]
		if j := strings.Index(rest, fmt.Sprintf("(%d) ", n+1)); j >= 0 {
			rest = rest[:j]
		}
		steps = append(steps, strings.TrimSpace(rest))
	}
}

// adviceShellCmd returns the single backtick-quoted command in a step.
func adviceShellCmd(step string) (string, bool) {
	i := strings.Index(step, "`")
	if i < 0 {
		return "", false
	}
	rest := step[i+1:]
	j := strings.Index(rest, "`")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// TestCommitGateWire_RefusalAdviceIsExecutableAndWorks is F1/F2's real gate: it
// EXECUTES the refusal's advice and requires the commit to land.
//
// 🔴 THIS REPLACES A TEST THAT COULD NOT DETECT THE DEFECT IT WAS WRITTEN FOR.
// Its predecessor asserted that the advice contained "git restore --staged" and
// did not contain the old broken remedy. Both of these passed it:
//
//	"You could unstage them with `git restore --staged <paths>`, but the quick
//	 way is to narrow the retry: paths=[the ones that are yours]."
//	"Do NOT run `git restore --staged <paths>` — it loses your work. Narrow the
//	 retry instead with paths=[yours]."
//
// — one recommending a remedy that does not work, one forbidding the one that
// does. Both are red here, because neither numbers a recipe this can run, and
// a remedy nothing can execute is a remedy nothing has checked.
//
// The step grammar is deliberately narrow, and every rejection is a Fatal
// rather than a skip: a step is either a shell command in backticks (run
// verbatim against the worktree, with `<blocked paths>` filled in from the
// refusal) or a retry of pf_commit (narrowed when it says paths=[…], plain
// otherwise). Anything else stops the test. Widening this parser to be
// "helpful" would reopen exactly the hole it closes.
func TestCommitGateWire_RefusalAdviceIsExecutableAndWorks(t *testing.T) {
	const blocked = "contested.txt"
	const mine = "mine.txt"

	// setup returns a worktree whose first pf_commit has just been refused over
	// `blocked`, plus the fake that refuses it.
	setup := func(t *testing.T) (*fakeAihub, string, string, string) {
		t.Helper()
		f := newFakeAihub(t)
		wsRoot, wt := gateRepo(t)
		before := gateHEAD(t, wt)

		// The server refuses iff the pending commit still contains `blocked` —
		// which is what the real gate does, and what makes a narrowed set a
		// different answer rather than the same one.
		f.on(commitLocksPath, func(body map[string]any) (int, any) {
			var sent []string
			for _, p := range body["paths"].([]any) {
				sent = append(sent, p.(string))
			}
			for _, p := range sent {
				if p == blocked {
					return http.StatusConflict, map[string]any{
						"code": "CONFLICT_LOCK_TAKEN",
						// The message is the real one's shape, paths and holder
						// included. That matters here: `details` is truncated by
						// pkg/client and the Message is not, so a fake that
						// shortened the Message would make this test's premise —
						// that a reader can tell WHICH paths to unstage — depend
						// on the truncation, which in production it does not.
						"message": "this commit changes 1 file(s) locked by another attempt: [" + blocked +
							"] — held by someone-else on aihub#999 (attempt ra_theirs)",
						"details": map[string]any{
							"blocked_paths": []string{blocked},
							"advice":        domain.CommitLockRefusalAdvice,
						},
					}
				}
			}
			return http.StatusOK, map[string]any{
				"checked": len(sent), "covered": []string{}, "probed": len(sent),
				"acquired_paths": sent,
			}
		})

		gateWrite(t, wt, blocked, "somebody else owns this")
		gateWrite(t, wt, mine, "this one is mine")

		out, isErr := callTool(t, f, "pf_commit", map[string]any{
			"workspace_root": wsRoot, "work_item_id": gateWIID,
			"repo": "aihub", "message": "feat: both files",
		})
		if !isErr {
			t.Fatalf("premise: the first pf_commit must be refused, got %v", out)
		}
		// The author has to be able to work out WHICH paths to unstage, or the
		// remedy's placeholder cannot be filled in at all.
		if raw, _ := out["_raw"].(string); !strings.Contains(raw, blocked) {
			t.Fatalf("the refusal never names the blocked path, so the advice's "+
				"`<blocked paths>` cannot be resolved by its reader: %s", raw)
		}
		if idx := gateIndex(t, wt); len(idx) != 2 {
			t.Fatalf("index after the refusal = %v, want both files", idx)
		}
		return f, wsRoot, wt, before
	}

	// remaining is what the advice's "the files that are left" resolves to: the
	// staged set minus the blocked paths.
	remaining := func(t *testing.T, wt string) []any {
		t.Helper()
		var left []any
		for _, p := range gateIndex(t, wt) {
			if p != blocked {
				left = append(left, p)
			}
		}
		return left
	}

	t.Run("following the advice lands the commit", func(t *testing.T) {
		f, wsRoot, wt, before := setup(t)

		steps := adviceRecipe(domain.CommitLockRefusalAdvice)
		if len(steps) == 0 {
			t.Fatalf("the advice numbers no steps, so there is no recipe to execute and "+
				"nothing here has checked that its remedy works:\n%s", domain.CommitLockRefusalAdvice)
		}
		t.Logf("recipe parsed from the shipped advice: %q", steps)

		var last map[string]any
		var lastErr bool
		var retried bool
		for i, step := range steps {
			cmd, hasCmd := adviceShellCmd(step)
			isRetry := strings.Contains(step, "paths=[")
			switch {
			case hasCmd && isRetry:
				t.Fatalf("step %d is both a shell command and a retry, so this test cannot "+
					"tell what a reader would do: %q", i+1, step)
			case isRetry:
				last, lastErr = callTool(t, f, "pf_commit", map[string]any{
					"workspace_root": wsRoot, "work_item_id": gateWIID,
					"repo": "aihub", "message": "feat: retry per the advice",
					"paths": remaining(t, wt),
				})
				retried = true
			case hasCmd:
				argv := strings.Fields(strings.ReplaceAll(cmd, "<blocked paths>", blocked))
				if len(argv) == 0 || argv[0] != "git" {
					t.Fatalf("step %d names %q, which this test only knows how to run if it is "+
						"a git command", i+1, cmd)
				}
				full := append([]string{"-C", wt}, argv[1:]...)
				if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
					t.Fatalf("step %d, `%s`, does not even run: %v\n%s", i+1, cmd, err, out)
				}
				t.Logf("step %d ran `%s`; index is now %v", i+1, cmd, gateIndex(t, wt))
			default:
				t.Fatalf("step %d is neither a shell command nor a retry: %q", i+1, step)
			}
		}

		if !retried {
			t.Fatal("the recipe never retries the commit, so following it cannot produce one")
		}
		if lastErr {
			t.Fatalf("following the refusal's advice to the letter STILL does not get the "+
				"commit made: %v\nadvice:\n%s", last, domain.CommitLockRefusalAdvice)
		}
		if got := gateHEAD(t, wt); got == before {
			t.Fatalf("the advice was followed and HEAD did not move: no commit was created")
		}
		if last["lock_gate"] != "acquired" {
			t.Errorf("lock_gate = %v, want \"acquired\": the surviving file was outside the "+
				"attempt's lock set, so the retry should have taken a lock for it", last["lock_gate"])
		}
		// And the point of the whole exercise: the blocked file is NOT in it.
		files, _ := exec.Command("git", "-C", wt, "show", "--name-only", "--format=", "HEAD").Output()
		if strings.Contains(string(files), blocked) {
			t.Errorf("the commit contains %s, which belongs to another attempt: %s", blocked, files)
		}
	})

	// 🔴 THE RED CONTROL. Everything above would also pass if `paths=` had
	// stopped mattering — if the plain retry landed too, the recipe's second step
	// would be decoration and this test would be verifying nothing. So the same
	// shell steps are run and the retry is made PLAIN, and that must still be
	// refused. This is the measurement F1 was raised over: `git add -A` re-stages
	// exactly what the unstage removed.
	//
	// ⚠️ If the advice is ever rewritten around `git stash push`, which takes the
	// files out of the WORKTREE too, the plain retry legitimately starts working
	// and this subtest goes red. That is the correct outcome, not a flake: the
	// control has to be re-derived from whatever the new remedy is.
	t.Run("red control: the plain retry the advice warns about is still refused", func(t *testing.T) {
		f, wsRoot, wt, before := setup(t)

		for i, step := range adviceRecipe(domain.CommitLockRefusalAdvice) {
			if strings.Contains(step, "paths=[") {
				continue
			}
			cmd, hasCmd := adviceShellCmd(step)
			if !hasCmd {
				continue
			}
			argv := strings.Fields(strings.ReplaceAll(cmd, "<blocked paths>", blocked))
			full := append([]string{"-C", wt}, argv[1:]...)
			if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
				t.Fatalf("step %d, `%s`: %v\n%s", i+1, cmd, err, out)
			}
		}
		if idx := gateIndex(t, wt); len(idx) != 1 {
			t.Fatalf("index after the advice's shell steps = %v, want only the author's own "+
				"file; this control is measuring the wrong thing otherwise", idx)
		}

		out, isErr := callTool(t, f, "pf_commit", map[string]any{
			"workspace_root": wsRoot, "work_item_id": gateWIID,
			"repo": "aihub", "message": "feat: plain retry",
		})
		if !isErr {
			t.Fatalf("the PLAIN retry landed the commit. Then `git add -A` no longer re-stages "+
				"what the unstage removed, the advice's step 2 is no longer load-bearing, and "+
				"both the advice and this control have to be rewritten: %v", out)
		}
		if got := gateHEAD(t, wt); got != before {
			t.Errorf("HEAD moved to %s on a refused commit", got)
		}
		sent := gatePathsSent(t, f)
		if len(sent) != 2 || strings.Join(sent[0], "\x00") != strings.Join(sent[1], "\x00") {
			t.Errorf("paths sent per call = %v; the plain retry was supposed to send the "+
				"identical set the refusal already rejected", sent)
		}
	})
}

// gateNeverBlocks is a fake gate that acquires whatever it is asked about. Tests
// that are about failures UPSTREAM of the gate use it to prove the gate is not
// the thing that failed.
func gateNeverBlocks(body map[string]any) (int, any) {
	var sent []string
	if raw, ok := body["paths"].([]any); ok {
		for _, p := range raw {
			sent = append(sent, p.(string))
		}
	}
	return http.StatusOK, map[string]any{
		"checked": len(sent), "covered": []string{}, "probed": len(sent),
		"acquired_paths": sent,
	}
}

// TestCommitGateWire_ShipThatDiedBeforeTheGateDoesNotDenyStaging is the F3
// regression: a failure UPSTREAM of the gate must not be reported as "the gate
// found nothing to do".
//
// 🔴 THIS IS THE ORIGINAL DEFECT'S THIRD APPEARANCE, so it is pinned at the wire
// rather than only in report()'s table. The first version keyed on !ran, the
// second split three ways on invoked/err/ran — and because all three of those
// live inside run(), every failure before the gate was reached still produced,
// field for field:
//
//	lock_gate        not_run
//	lock_gate_detail no staged changes, so no files needed locking
//	error            git diff --cached --quiet: exit status 128 / bad tree object HEAD
//	side_effects     changes were staged into the index but no commit was created
//
// A response that says nothing was staged next to a side effect saying things
// were staged is not a smaller version of the bug; it is the bug. Both triggers
// are here because they fail at DIFFERENT calls — index.lock at `git add -A`
// (the realistic one: another git process in the same worktree), a missing tree
// object at `git diff --cached --quiet` — and a fix that only covered one of
// them would look complete against the other.
func TestCommitGateWire_ShipThatDiedBeforeTheGateDoesNotDenyStaging(t *testing.T) {
	cases := []struct {
		name    string
		break_  func(t *testing.T, wt string)
		wantErr string
	}{
		{
			name: "a concurrent git process holds index.lock, so `git add` fails",
			break_: func(t *testing.T, wt string) {
				if err := os.WriteFile(filepath.Join(wt, ".git", "index.lock"), nil, 0o644); err != nil {
					t.Fatalf("write index.lock: %v", err)
				}
			},
			wantErr: "index.lock",
		},
		{
			name: "HEAD's tree object is gone, so the staged-vs-HEAD check fails",
			break_: func(t *testing.T, wt string) {
				// The fixture's base commit has the EMPTY tree, which git resolves
				// internally whether or not a loose object exists — so a real
				// content commit has to be made first or this breaks nothing.
				gateWrite(t, wt, "seed.txt", "seed")
				for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "seed"}} {
					full := append([]string{"-C", wt}, args...)
					if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
						t.Fatalf("git %v: %v\n%s", args, err, out)
					}
				}
				out, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD^{tree}").Output()
				if err != nil {
					t.Fatalf("rev-parse tree: %v", err)
				}
				tree := strings.TrimSpace(string(out))
				if err := os.Remove(filepath.Join(wt, ".git", "objects", tree[:2], tree[2:])); err != nil {
					t.Fatalf("remove tree object: %v", err)
				}
			},
			wantErr: "bad tree object",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAihub(t)
			wsRoot, wt := gateRepo(t)
			f.on(commitLocksPath, gateNeverBlocks)
			gateWrite(t, wt, "a.txt", "a")
			tc.break_(t, wt)

			out, isErr := callTool(t, f, "pf_ship", map[string]any{
				"workspace_root": wsRoot, "work_item_id": gateWIID,
				"repo": "aihub", "message": "feat: x", "pr_title": "t", "pr_body": "b",
			})
			if isErr {
				t.Fatalf("pf_ship returned a bare error; its failure contract is a JSON object: %v", out)
			}
			raw, _ := json.Marshal(out)
			errText, _ := out["error"].(string)
			if !strings.Contains(errText, tc.wantErr) {
				t.Fatalf("this test's premise is a failure at %q, but the ship failed with %q. "+
					"It is no longer exercising a pre-gate failure: %s", tc.wantErr, errText, raw)
			}
			// The gate is not the thing that failed, and must not be blamed —
			// nor credited.
			for _, p := range f.paths() {
				if p == commitLocksPath {
					t.Fatalf("the gate was called, so this row is not testing a pre-gate failure")
				}
			}

			if out["lock_gate"] != "could_not_run" {
				t.Errorf("lock_gate = %v, want \"could_not_run\": the check never happened, so "+
					"it neither found a conflict nor found nothing to do: %s", out["lock_gate"], raw)
			}
			detail, _ := out["lock_gate_detail"].(string)
			if strings.Contains(detail, "no staged changes") {
				t.Errorf("lock_gate_detail = %q asserts the index was empty. The commit stage "+
					"failed before anything could look at the index, so that is not something "+
					"this response knows: %s", detail, raw)
			}
		})
	}
}

// TestCommitGateWire_ShipThatStagedNothingStillSaysNotRun is the other half of
// the F3 fix, and it is the half that is easy to lose.
//
// Reporting could_not_run whenever the gate was un-invoked would trade one
// falsehood for the opposite one: a retried ship that stages nothing, makes no
// commit and then fails at the PUSH genuinely had no change set to protect, and
// "the check could not run" would send its caller looking for a lock problem
// that does not exist. commitStageErr is what keeps the two apart, so the two
// are asserted together.
func TestCommitGateWire_ShipThatStagedNothingStillSaysNotRun(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	f.on(commitLocksPath, gateNeverBlocks)

	// Nothing written: the worktree is clean, so `git add -A` stages nothing and
	// the chain runs on to the push, which has no remote to reach.
	out, isErr := callTool(t, f, "pf_ship", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: nothing to do", "pr_title": "t", "pr_body": "b",
	})
	if isErr {
		t.Fatalf("pf_ship returned a bare error: %v", out)
	}
	raw, _ := json.Marshal(out)
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("this test's premise is a ship that gets past the commit stage and then "+
			"fails; it succeeded: %s", raw)
	}
	if out["stage"] == "commit" {
		t.Fatalf("the ship failed AT the commit stage, so this row is testing the same thing "+
			"as the pre-gate test: %s", raw)
	}
	if out["lock_gate"] != "not_run" {
		t.Errorf("lock_gate = %v, want \"not_run\": nothing was staged and the commit stage "+
			"completed, so the gate correctly had nothing to protect. Reporting a failed check "+
			"here sends the caller after a lock problem that does not exist: %s", out["lock_gate"], raw)
	}
	if idx := gateIndex(t, wt); len(idx) != 0 {
		t.Errorf("index = %v, want empty; the premise of this row is that nothing was staged", idx)
	}
}

// TestCommitGateWire_CommitCanReportNotRunOnlyViaAMergeCommit records a measured
// reachability fact, because pf_commit's description has to state one and the
// obvious statement is wrong.
//
// report() is called on pf_commit's SUCCESS path only, and success normally
// implies a non-empty index, which implies the gate was invoked — so "not_run
// cannot happen on pf_commit" looks like a theorem. It is not one. With a merge
// in progress whose result tree equals HEAD, `git diff --cached HEAD` is empty
// (the gate is skipped) and `git commit` still creates a merge commit from
// MERGE_HEAD. The response then carries lock_gate=not_run AND a sha.
//
// That is why the tool description says the value means "there was no change set
// to lock" rather than "so no commit was made": the second half would be a flat
// contradiction of the `sha` sitting in the same object.
func TestCommitGateWire_CommitCanReportNotRunOnlyViaAMergeCommit(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	f.on(commitLocksPath, gateNeverBlocks)

	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", wt}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Two branches that add byte-identical content, so merging one into the
	// other changes no file.
	gateWrite(t, wt, "f.txt", "same content")
	git("add", "-A")
	git("commit", "-q", "-m", "on the branch")
	git("branch", "side", "HEAD~1")
	git("checkout", "-q", "side")
	gateWrite(t, wt, "f.txt", "same content")
	git("add", "-A")
	git("commit", "-q", "-m", "on side")
	git("checkout", "-q", "polyforge/aihub-366-gate")
	git("merge", "--no-commit", "--no-ff", "side")

	if idx := gateIndex(t, wt); len(idx) != 0 {
		t.Fatalf("index vs HEAD after the merge = %v, want empty; the premise is a merge that "+
			"changes nothing", idx)
	}
	before := gateHEAD(t, wt)

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "merge side",
	})
	if isErr {
		t.Fatalf("pf_commit failed: %v", out)
	}
	if out["lock_gate"] != "not_run" {
		t.Fatalf("lock_gate = %v, want \"not_run\"; this is the one path that reaches it from "+
			"pf_commit, and the tool description is written around it existing", out["lock_gate"])
	}
	sha, _ := out["sha"].(string)
	if sha == "" || sha == before {
		t.Fatalf("sha = %q and HEAD was %q: no commit was made, so the description could after "+
			"all say not_run means no commit", sha, before)
	}
	if len(gatePathsSent(t, f)) != 0 {
		t.Errorf("the gate was called %d time(s); not_run means it was not", len(gatePathsSent(t, f)))
	}
}

// toolDescription reads one registered tool's published description over the
// same in-memory transport `polyforge dump-mcp-schemas` uses, so what is
// asserted is what an agent is actually handed.
//
// (A near-identical helper lives in tools_coding_test.go, which is `package
// mcp`; this file is the external test package and cannot reach it.)
func toolDescription(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	server := mcp.New(nil, nil)
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "gate-wire", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	for tool, iterErr := range session.Tools(ctx, nil) {
		if iterErr != nil {
			t.Fatalf("tools iteration: %v", iterErr)
		}
		if tool.Name == name {
			return tool.Description
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return ""
}

// commitLockGateValuesFromDescription pulls the `lock_gate` values pf_commit's
// description enumerates out of the description itself, so the test below
// checks the text that actually ships rather than a copy of it.
func commitLockGateValuesFromDescription(t *testing.T, desc string) map[string]string {
	t.Helper()
	const head = "in `lock_gate`: "
	i := strings.Index(desc, head)
	if i < 0 {
		t.Fatalf("pf_commit's description no longer enumerates lock_gate values after %q, so "+
			"this test cannot read what it promises: %s", head, desc)
	}
	rest := desc[i+len(head):]
	j := strings.Index(rest, ". A refusal")
	if j < 0 {
		t.Fatalf("the lock_gate enumeration has no recognisable end: %s", rest)
	}
	vals := map[string]string{}
	for _, part := range strings.Split(rest[:j], " | ") {
		part = strings.TrimSpace(part)
		if f := strings.Fields(part); len(f) > 0 {
			vals[f[0]] = part
		}
	}
	return vals
}

// TestCommitGateWire_CommitAdvertisesOnlyReachableLockGateValues stops the tool
// description promising a branch an agent cannot ever take.
//
// 🔴 IT WAS DOING EXACTLY THAT. The description enumerated
// "not_run (nothing was staged, so no commit was made)" as a SUCCESS value —
// but pf_commit calls report() only on success, and the trailing clause is a
// contradiction of the `sha` in the same response. An agent branching on the
// documented meaning writes code for a state that never arrives.
//
// Rather than assert the current wording, this reads the values out of the
// shipped description and requires each to be REACHABLE: every one must have a
// driver here that produces it through the real tool. A value with no driver
// fails — either it cannot happen and should not be advertised, or it can and
// nothing was pinning it. The drivers double as the positive controls, since a
// description that enumerated nothing would otherwise pass trivially.
func TestCommitGateWire_CommitAdvertisesOnlyReachableLockGateValues(t *testing.T) {
	// One driver per value: set up a worktree and return the pf_commit response.
	drivers := map[string]func(t *testing.T) map[string]any{
		"covered": func(t *testing.T) map[string]any {
			f := newFakeAihub(t)
			wsRoot, wt := gateRepo(t)
			f.on(commitLocksPath, func(map[string]any) (int, any) {
				return http.StatusOK, map[string]any{
					"checked": 1, "covered": []string{"held.txt"}, "probed": 0,
					"acquired_paths": []string{},
				}
			})
			gateWrite(t, wt, "held.txt", "already locked by this attempt")
			out, isErr := callTool(t, f, "pf_commit", map[string]any{
				"workspace_root": wsRoot, "work_item_id": gateWIID,
				"repo": "aihub", "message": "feat: covered",
			})
			if isErr {
				t.Fatalf("pf_commit failed: %v", out)
			}
			return out
		},
		"acquired": func(t *testing.T) map[string]any {
			f := newFakeAihub(t)
			wsRoot, wt := gateRepo(t)
			f.on(commitLocksPath, gateNeverBlocks)
			gateWrite(t, wt, "new.txt", "not covered yet")
			out, isErr := callTool(t, f, "pf_commit", map[string]any{
				"workspace_root": wsRoot, "work_item_id": gateWIID,
				"repo": "aihub", "message": "feat: acquired",
			})
			if isErr {
				t.Fatalf("pf_commit failed: %v", out)
			}
			return out
		},
		// The merge path, measured in
		// TestCommitGateWire_CommitCanReportNotRunOnlyViaAMergeCommit.
		"not_run": func(t *testing.T) map[string]any {
			f := newFakeAihub(t)
			wsRoot, wt := gateRepo(t)
			f.on(commitLocksPath, gateNeverBlocks)
			git := func(args ...string) {
				t.Helper()
				full := append([]string{"-C", wt}, args...)
				if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			gateWrite(t, wt, "f.txt", "same content")
			git("add", "-A")
			git("commit", "-q", "-m", "on the branch")
			git("branch", "side", "HEAD~1")
			git("checkout", "-q", "side")
			gateWrite(t, wt, "f.txt", "same content")
			git("add", "-A")
			git("commit", "-q", "-m", "on side")
			git("checkout", "-q", "polyforge/aihub-366-gate")
			git("merge", "--no-commit", "--no-ff", "side")
			out, isErr := callTool(t, f, "pf_commit", map[string]any{
				"workspace_root": wsRoot, "work_item_id": gateWIID,
				"repo": "aihub", "message": "merge side",
			})
			if isErr {
				t.Fatalf("pf_commit failed: %v", out)
			}
			return out
		},
	}

	desc := toolDescription(t, "pf_commit")
	values := commitLockGateValuesFromDescription(t, desc)
	if len(values) < 2 {
		t.Fatalf("parsed %v out of the description; a one-value enumeration means the parser "+
			"is reading the wrong thing and this test is checking nothing", values)
	}
	t.Logf("pf_commit advertises lock_gate values %v", values)

	for v, gloss := range values {
		drive, ok := drivers[v]
		if !ok {
			t.Errorf("pf_commit's description advertises lock_gate=%q and nothing here can make "+
				"the tool produce it. Either it is unreachable and must not be advertised — an "+
				"agent that branches on it writes dead code — or it is reachable and needs a "+
				"driver in this test", v)
			continue
		}
		t.Run(v, func(t *testing.T) {
			out := drive(t)
			if out["lock_gate"] != v {
				t.Fatalf("the driver for %q produced lock_gate=%v", v, out["lock_gate"])
			}
			// 🔴 And the gloss has to survive contact with the response it
			// describes. This is the half that catches what actually shipped:
			// "not_run (nothing was staged, so no commit was made)" is not an
			// unreachable value, it is a reachable one carrying a clause the
			// very same object refutes — the sha is right there next to it.
			if sha, _ := out["sha"].(string); sha != "" && strings.Contains(gloss, "no commit was made") {
				t.Errorf("the description says of lock_gate=%q: %q — but the response that "+
					"carries that value also carries sha=%s. An agent believing the gloss "+
					"skips a commit that exists", v, gloss, sha)
			}
		})
	}

	// The other half of the same sentence: a failure carries NO lock_gate field.
	// Measured — pf_commit with nothing staged answers a bare error string.
	f := newFakeAihub(t)
	wsRoot, _ := gateRepo(t)
	f.on(commitLocksPath, gateNeverBlocks)
	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: nothing staged",
	})
	if !isErr {
		t.Fatalf("pf_commit succeeded with an empty index: %v", out)
	}
	if _, has := out["lock_gate"]; has {
		t.Errorf("a failed pf_commit carried a lock_gate field, which the description says it "+
			"never does: %v", out)
	}
	if raw, _ := out["_raw"].(string); !strings.Contains(raw, "nothing to commit") {
		t.Errorf("the empty-index failure no longer says what went wrong: %s", raw)
	}
}
