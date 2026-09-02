package mcp_test

// aihub#323: pf_claim_work_item and pf_force_takeover write this machine's state
// file AFTER the server transaction has committed. When that write fails the
// error used to say only "write state file: <errno>", and a caller reads that as
// "the whole call failed" — while on the server the attempt is live, holds this
// work item's locks, and the previous holder (if there was one) has already been
// evicted. The agent that was displaced is dead and the agent that displaced it
// believes nothing happened.
//
// RETURNING THE ERROR IS NOT THE DEFECT and must not be "fixed" by swallowing
// it: aihub#319 settled that twice. A best-effort `_ = WriteStateFile(...)`
// answers ok:true with a new_attempt_id and then every later tool dies on "state
// file not found", a whole diagnosis away from the cause.
//
// HOW THE FAILURE IS INJECTED. config.WriteStateFile does MkdirAll(StateDir())
// then WriteFile. Putting a regular FILE at <root>/.polyforge makes the MkdirAll
// fail with ENOTDIR. Chmod would not do: these tests run as root in CI and in
// the dev container, and root ignores the permission bits — the failure would
// simply not happen and both tests would pass vacuously.
//
// ⚠️ ASSERTION CHOICE. The substrings asserted below must not appear in the
// PRE-FIX message, which was exactly "write state file: %v" / "update state
// file: %v" — aihub#316 shipped an assertion on "upstream" that matched the
// correct message's own "not an upstream dependency". "NOT A NO-OP",
// "ALREADY SUCCEEDED" and "RECOVERY:" appear in neither the old text nor any
// errno, so each of them fails on the old build and passes on the new one.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// breakStateDir makes every subsequent config.WriteStateFile fail, by putting a
// regular file where the .polyforge directory has to be.
//
// ⚠️ IT RETURNS AN ERROR RATHER THAN CALLING t.Fatalf, because one of its two
// callers runs INSIDE the fake aihub's HTTP handler, i.e. on a goroutine that is
// not the test's. testing.T.FailNow may only be called from the test goroutine;
// from the handler it would runtime.Goexit, the response would never be written,
// and the test would report an opaque transport error instead of "the injection
// stopped working" — the exact opposite of what the probe below is for.
func breakStateDir(root string) error {
	dot := filepath.Join(root, ".polyforge")
	if err := os.RemoveAll(dot); err != nil {
		return fmt.Errorf("remove %s: %w", dot, err)
	}
	if err := os.WriteFile(dot, []byte("not a directory\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dot, err)
	}
	// Prove the injection works before relying on it: a test whose failure
	// injection silently stopped working passes for the wrong reason.
	if err := config.WriteStateFile(&config.StateFile{WIID: "probe"}); err == nil {
		return fmt.Errorf("injection failed: config.WriteStateFile still succeeds with a file at %s", dot)
	}
	return nil
}

// mustBreakStateDir is the test-goroutine-only wrapper.
func mustBreakStateDir(t *testing.T, root string) {
	t.Helper()
	if err := breakStateDir(root); err != nil {
		t.Fatalf("%v", err)
	}
}

// sandboxedWorkspace is a temp workspace root with POLYFORGE_WORKSPACE_ROOT
// pointed at it, so nothing here can reach the live credential directory.
func sandboxedWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)
	if dir := config.StateDir(); !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		t.Fatalf("config.StateDir() = %q, outside the temp root %q — refusing to run", dir, root)
	}
	return root
}

func errorText(t *testing.T, result map[string]any) string {
	t.Helper()
	raw, ok := result["_raw"].(string)
	if !ok {
		t.Fatalf("expected a bare error string, got a JSON result: %v", result)
	}
	return raw
}

// assertDisclosesCommittedSideEffect checks the three things a caller needs, in
// the order it needs them. They are asserted separately rather than as one
// golden string so a failure names WHICH of the three went missing — the
// filesystem sentence is the one most likely to be dropped as boilerplate, and
// it is the one that stops the "re-run the call" advice from becoming a retry
// loop against a full disk.
func assertDisclosesCommittedSideEffect(t *testing.T, msg string, attemptID string) {
	t.Helper()
	for _, want := range []string{
		"NOT A NO-OP",       // 1. the server-side effect landed
		"ALREADY SUCCEEDED", // 1, restated so a caller skimming one line still sees it
		"RECOVERY:",         // 2. what to do about it
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error text does not disclose the committed side effect (%q missing) — a caller reads it as a failed call while the server says otherwise; got %q", want, msg)
		}
	}
	if !strings.Contains(msg, attemptID) {
		t.Errorf("error text never names attempt %s, which is the only identifier of the thing that is now running unrecorded; got %q", attemptID, msg)
	}
	// 3. Retrying is useless against a broken filesystem, and both failure modes
	//    of the write (MkdirAll, WriteFile) are exactly that kind.
	if !strings.Contains(msg, "filesystem") {
		t.Errorf("error text does not say that a repeated failure is this machine's filesystem rather than the server, so the recovery advice reads as an unbounded retry; got %q", msg)
	}
}

// TestForceTakeoverErrorDisclosesTheEvictionItAlreadyPerformed is the scenario
// aihub#323 was filed for: agent B force-takes a work item from agent A, B's
// local write fails, B reports "takeover failed" — and A is already gone.
//
// MUTANT (the pre-aihub#323 build): restore
//
//	return errResult(fmt.Errorf("write state file: %w", err))
//
// Every assertion in assertDisclosesCommittedSideEffect goes red, and the
// message the test prints is the whole of what the caller used to be told.
func TestForceTakeoverErrorDisclosesTheEvictionItAlreadyPerformed(t *testing.T) {
	const wiID = "wi_01JTAKEOVERCOMMIT"
	const newAttempt = "ra_after_takeover"
	root := sandboxedWorkspace(t)

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+wiID+"/force_takeover", func(map[string]any) (int, any) {
		return 200, map[string]any{
			"ok":                  true,
			"id":                  wiID,
			"slug":                "aihub#323",
			"project":             "aihub",
			"prior_attempt_id":    "ra_the_evicted_one",
			"prior_actor_display": "agent-a",
			"new_attempt_id":      newAttempt,
			"new_claim_epoch":     7,
		}
	})

	mustBreakStateDir(t, root)

	result, isErr := callTool(t, f, "pf_force_takeover", map[string]any{
		"work_item_id": wiID,
		"reason":       "the previous holder is idle",
	})
	if !isErr {
		t.Fatalf("pf_force_takeover reported success despite the state write failing: %v", result)
	}
	msg := errorText(t, result)

	// The takeover really did reach the server — otherwise this test would be
	// asserting disclosure of a side effect that never happened.
	if paths := f.paths(); len(paths) == 0 || !strings.Contains(strings.Join(paths, ","), "/force_takeover") {
		t.Fatalf("the takeover never reached the server, so there is no committed side effect to disclose: %v", paths)
	}

	assertDisclosesCommittedSideEffect(t, msg, newAttempt)
	// Who was evicted is the actionable half for a human: the message is the only
	// place it survives, because the caller never gets a successful response.
	if !strings.Contains(msg, "agent-a") {
		t.Errorf("error text does not name the holder that was evicted, so nobody learns whose session just died; got %q", msg)
	}
	// ⚠️ THE DIRECTION OF THE ADVICE, not just its presence. Asserting only
	// "RECOVERY:" plus the tool name is satisfied by
	// "RECOVERY: do NOT re-run pf_force_takeover — escalate to an operator",
	// which contains every other substring here while inverting the one thing
	// this message exists to say. "self-takeover" is the server's own reason the
	// re-run is admitted (FnForceTakeover's isSelf branch) and appears in no
	// plausible don't-re-run wording.
	if !strings.Contains(msg, "re-run this exact pf_force_takeover call") {
		t.Errorf("error text does not tell the caller to re-run the same call; got %q", msg)
	}
	if !strings.Contains(msg, "self-takeover") {
		t.Errorf("error text does not say WHY the re-run is admitted (the caller now owns the attempt, so the server takes it as a self-takeover), which is what makes the advice checkable rather than a suggestion; got %q", msg)
	}
}

// TestClaimErrorDisclosesTheClaimItAlreadyMade is the same defect on the claim
// path. The state write here is the SECOND one — the C6-2 pre-claim partial
// write at the top of the handler has to succeed first, or the server is never
// called at all and there is no committed side effect — so the injection runs
// inside the fake aihub's handler, i.e. after the partial write and before the
// post-claim one.
//
// ⚠️ THE RECOVERY IS A NEW idempotency_key, NOT A REPLAY. Read off
// internal/domain/run_attempts.go rather than assumed: a claim carrying an
// already-used key takes the idempotency branch, which returns the EXISTING
// attempt and never touches session_secret_hash, while this handler mints a
// fresh session_secret on every call — so a replay would write a state file
// holding a secret the server has never seen, and every later call would 401
// "invalid session_secret". That is a second silent failure stacked on the
// first, which is why the message has to be specific and why this test asserts
// on it.
//
// MUTANT (the pre-aihub#323 build): restore
//
//	return errResult(fmt.Errorf("update state file: %w", err))
func TestClaimErrorDisclosesTheClaimItAlreadyMade(t *testing.T) {
	const wiID = "wi_01JCLAIMCOMMITTED"
	const attempt = "ra_claim_committed"
	root := sandboxedWorkspace(t)

	// Written on the handler goroutine, read on the test goroutine only after
	// callTool has returned — the MCP call is fully synchronous, so the HTTP
	// round trip has completed by then and there is no race (confirmed under
	// -race).
	var injectErr error

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+wiID+"/claim", func(map[string]any) (int, any) {
		// The server transaction has committed by the time a response exists.
		// Break the state directory here so the NEXT local write is the one that
		// fails — the partial write before the call must still have succeeded.
		//
		// Runs on the httptest handler's goroutine, hence breakStateDir's
		// error return rather than t.Fatalf; the error is carried out through
		// injectErr and asserted on the test goroutine below.
		injectErr = breakStateDir(root)
		return 200, map[string]any{
			"ok":          true,
			"id":          wiID,
			"slug":        "aihub#323",
			"project":     "aihub",
			"attempt_id":  attempt,
			"claim_epoch": 3,
		}
	})

	result, isErr := callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id":    wiID,
		"idempotency_key": "idem-claim-committed",
	})
	if injectErr != nil {
		t.Fatalf("the failure injection did not work, so this test proves nothing: %v", injectErr)
	}
	if !isErr {
		t.Fatalf("pf_claim_work_item reported success despite the state write failing: %v", result)
	}
	msg := errorText(t, result)

	if paths := f.paths(); !strings.Contains(strings.Join(paths, ","), "/claim") {
		t.Fatalf("the claim never reached the server, so there is no committed side effect to disclose: %v", paths)
	}

	assertDisclosesCommittedSideEffect(t, msg, attempt)
	if !strings.Contains(msg, "NEW idempotency_key") {
		t.Errorf("error text does not say the retry needs a NEW idempotency_key. Replaying the same one returns this attempt from the idempotency branch without registering the fresh session_secret this handler generates, so every later call 401s; got %q", msg)
	}
}

// TestEveryPostCommitStateWriteDisclosesIt is aihub#323's first acceptance
// criterion as a test instead of a grep somebody has to remember to run.
//
// The criterion is explicitly NOT "I changed two places": it is that every point
// where a committed server transaction is followed by a local state write that
// can FAIL THE CALL discloses the commit. config.WriteClaimState is that shape —
// both of its call sites run after the server has answered — so a third site
// added later goes red rather than silently reintroducing the defect.
//
// ⚠️ SCOPE: this reads tools_lifecycle.go ONLY. Both call sites live there
// today (verified by grepping the whole repo for config.WriteClaimState), but a
// call added in another file would be invisible to this guard.
//
// It parses the AST rather than measuring a byte distance. The first draft took
// a fixed 1600-byte window after each call site, which is a proximity check
// wearing a correctness costume: adding six lines of comment inside one of the
// two blocks pushed the disclosure out of the window and turned this red with a
// message about a defect that was not there. Measured, not imagined — it
// happened during this change. The if-statement's own body is the honest scope.
//
// The other post-commit local writes in this package are deliberately not
// covered: internal/mcp/tools_lifecycle.go's worktree-map update and the
// DeleteStateFile calls in tools_lifecycle.go / tools_coding.go / tools_step.go
// are all `_ =` best-effort and return ok:true, so they cannot mislead a caller
// into believing the call failed. They have their own problems; this is not one.
func TestEveryPostCommitStateWriteDisclosesIt(t *testing.T) {
	b, err := os.ReadFile("tools_lifecycle.go")
	if err != nil {
		t.Fatalf("read tools_lifecycle.go: %v", err)
	}
	src := string(b)

	// A textual count first, so a call in a shape the AST walk below does not
	// look for (a bare statement, an assignment to a named err) cannot pass by
	// simply not being found. A count, not a floor of one: deleting a site must
	// go red rather than pass on the survivor.
	const call = "config.WriteClaimState("
	if n := strings.Count(src, call); n != 2 {
		t.Fatalf("found %d occurrences of %s in tools_lifecycle.go, expected 2 (pf_claim_work_item and pf_force_takeover) — update this guard deliberately, and disclose the committed side effect at the new site", n, call)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "tools_lifecycle.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse tools_lifecycle.go: %v", err)
	}

	checked := 0
	ast.Inspect(file, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || ifs.Init == nil {
			return true
		}
		initSrc := src[fset.Position(ifs.Init.Pos()).Offset:fset.Position(ifs.Init.End()).Offset]
		if !strings.Contains(initSrc, call) {
			return true
		}
		checked++
		body := src[fset.Position(ifs.Body.Pos()).Offset:fset.Position(ifs.Body.End()).Offset]
		if !strings.Contains(body, "NOT A NO-OP") {
			t.Errorf("the config.WriteClaimState error branch at %s does not disclose the committed server-side effect. The server transaction has already committed by the time this line runs, so a bare \"write state file: <errno>\" reads as \"nothing happened\" while the attempt is live and the previous holder is already evicted (aihub#323). Branch:\n%s",
				fset.Position(ifs.Pos()), firstLines(body, 8))
		}
		return true
	})
	if checked != 2 {
		t.Errorf("only %d of the 2 config.WriteClaimState calls sit in an `if err := ...; err != nil` whose body this guard could read. A call whose error is handled some other way is not covered by this test — either restore that shape or extend the guard", checked)
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
