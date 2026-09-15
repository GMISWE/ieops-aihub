package mcp_test

// aihub#684 — AC8: pf_wrap converges on the exact same no-steps-recorded gate
// as pf_complete_attempt, because it forwards to the identical HTTP endpoint
// (FnCompleteAttempt) rather than carrying a second copy of the check.
//
// internal/domain's DB-gated tests
// (complete_attempt_no_steps_recorded_db_test.go and its pause/reclaim sibling)
// already prove the gate itself against every shape it has to tell apart. None
// of them can prove THIS: that pf_wrap — a different tool, a different code
// path, reachable only over MCP — actually reaches that same gate rather than
// answering out of a copy of the check baked into tools_coding.go. The two
// tools' completion bodies are built by hand in two different files
// (tools_lifecycle.go for pf_complete_attempt, tools_coding.go for pf_wrap);
// nothing forces them to agree except both POSTing to
// /v1/work_items/<id>/complete, and that is exactly the fact this test drives
// rather than reads off the source.
//
// ─── Why the real stack, and why real git ───────────────────────────────────
//
// pf_wrap's own push+PR half (internal/coding.Wrap) is what produces the
// commit/push/pr_opened event the gate looks for — nothing here seeds one by
// hand, unlike every domain-layer arm above. That is deliberate: a fake HTTP
// stack (state_resolve_wiring_test.go's fakeAihub, which wrap_completion_shape_test.go
// itself uses) can assert pf_wrap SENT status="wrapped" in a body, but it
// cannot show what a REAL server does with that body — and a hand-rolled fake
// gate would just be this test grading its own homework. The push and the PR
// still run against a throwaway local git repo and a stub `gh` on PATH (same
// technique as wrap_completion_shape_test.go's wrapFixture) so nothing here
// touches a real remote; only the completion call crosses to the real
// httptest router and the real Postgres database.
//
//	AIHUB_TEST_DB='postgres://postgres:test@127.0.0.1:5433/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2EWrapConvergesOnTheSameNoStepsRecordedGate -count=1 -v
//
// ⚠️ Shares claimStack's fixed project p_echo_e2e with every other e2eStack
// test (see attempt_terminal_credential_dbgated_test.go's header) — one run at
// a time per database.

import (
	"context"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// wrapChokePointFixture points wiID's just-claimed state file at a real,
// throwaway local git repo (newResolveRepo, state_resolve_wiring_test.go) with
// a stub `gh` on PATH answering "no PR exists yet" (fakeGHForResolve(t, "[]")),
// so a real pf_wrap call's push+PR half is a plain local git operation rather
// than a network call.
//
// Overwriting sf.Worktrees directly, rather than relying on the claim's own
// worktree-creation pass, is deliberate: that pass only fires when the DB
// project name is a key of the local .polyforge.yaml's `projects:` map
// (internal/lifecycle/claim.go, `effectiveCfg.Projects[sf.Project]`), and
// e2eStack's fixed project is "p_echo_e2e" while newClaimWorkspace's yaml
// names "aihub" — a mismatch AC8 has no reason to route around. coding.
// WorktreePath's own doc comment names sf.Worktrees as its PRIMARY lookup,
// checked before any project/slug reconstruction, so writing it directly is
// not a bypass of anything pf_wrap depends on.
func wrapChokePointFixture(t *testing.T, wiID string) {
	t.Helper()
	root := t.TempDir()
	r := newResolveRepo(t, root)
	fakeGHForResolve(t, "[]")

	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		t.Fatalf("no state file for %s after a real claim: %v", wiID, err)
	}
	sf.Worktrees = map[string]string{"aihub": r.wt}
	if err := config.WriteStateFile(sf); err != nil {
		t.Fatalf("point the claimed state file at the fixture repo: %v", err)
	}
}

// TestE2EWrapConvergesOnTheSameNoStepsRecordedGateAsCompleteAttempt is AC8.
//
// First call: no no_steps_reason. The work item has never had a step opened
// (pf_update_step is never called anywhere in this test, so
// wi_step_state.version stays at its default 0 for the whole run), and
// pf_wrap's own push+PR produces the commit-shaped code event the gate reads
// — so the real server must refuse it with 409 CONFLICT_NO_STEPS_RECORDED,
// the identical code the domain-layer tests pin, reached over the identical
// tool-independent endpoint.
//
// Second call: the identical request plus no_steps_reason. The escape hatch
// must unblock pf_wrap exactly as it unblocks pf_complete_attempt, and the
// reason must land in the same run_attempts.no_steps_reason column either
// tool writes to.
//
// Mutants (plan §2.1), both applied to the tree and reverted:
//
//	M1  disable the shared gate itself (run_attempts.go: `if false &&
//	    req.Status == "wrapped" && stepVersion == 0`) — proves CONVERGENCE:
//	    if pf_wrap answered this refusal from anywhere other than the one
//	    function pf_complete_attempt's own DB tests pin, disabling that
//	    function's condition would leave this test green while every
//	    domain-layer arm went red. It does not: the first tryCall's
//	    `require refused` assertion goes red here too.                    RED
//	M2  cut pf_wrap's own forwarding (tools_coding.go: `false &&
//	    noStepsReason != ""` in place of `noStepsReason != ""`) — proves the
//	    escape-hatch forwarding CONTRACT specific to this tool: the second
//	    tryCall stays refused with the reason present and sent, because the
//	    server never receives it.                                        RED
func TestE2EWrapConvergesOnTheSameNoStepsRecordedGateAsCompleteAttempt(t *testing.T) {
	s, wiID := claimStack(t, "aihub#684 AC8: pf_wrap must hit the same choke point as pf_complete_attempt")

	_, claimed := s.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": "aihub684-ac8-wrap",
	})
	attemptID, _ := claimed["attempt_id"].(string)
	if attemptID == "" {
		t.Fatalf("pf_claim_work_item returned no attempt_id: %v", claimed)
	}

	wrapChokePointFixture(t, wiID)

	wrapArgs := map[string]any{
		"work_item_id": wiID, "repo": "aihub",
		"pr_title": "aihub#684 AC8 probe", "pr_body": "body",
		"derived": []any{},
	}
	text, isErr := tryCall(t, s, "pf_wrap", wrapArgs)
	if !isErr {
		t.Fatalf("pf_wrap succeeded on a work item that produced code (via its own real push+PR) but "+
			"never opened a single step, with no no_steps_reason sent: %s\n"+
			"If pf_wrap answered this from a copy of the check instead of forwarding to the same "+
			"FnCompleteAttempt choke point pf_complete_attempt uses, this is exactly the shape that "+
			"copy could get wrong without anything here noticing", text)
	}
	if !strings.Contains(text, string(domain.ErrConflictNoStepsRecorded)) {
		t.Fatalf("pf_wrap was refused, but not with %s: %s", domain.ErrConflictNoStepsRecorded, text)
	}

	if got := wiStatus(t, s, wiID); got != "running" {
		t.Errorf("work item status = %q after a refused wrap, want still \"running\" — a refused "+
			"completion must not have half-wrapped the work item", got)
	}

	// The escape hatch, through the exact same tool and the exact same
	// otherwise-identical arguments.
	const reason = "aihub#684 AC8 probe: pf_wrap's own push+PR produced code; no step graph was " +
		"ever opened for this work item"
	wrapArgs["no_steps_reason"] = reason
	text, isErr = tryCall(t, s, "pf_wrap", wrapArgs)
	if isErr {
		t.Fatalf("pf_wrap with a non-empty no_steps_reason must be let through, got: %s", text)
	}

	if got := wiStatus(t, s, wiID); got != "wrapped" {
		t.Errorf("work item status = %q, want \"wrapped\" once the escape hatch let the wrap through", got)
	}

	var stored *string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT no_steps_reason FROM run_attempts WHERE id=$1`, attemptID).Scan(&stored); err != nil {
		t.Fatalf("read run_attempts.no_steps_reason: %v", err)
	}
	if stored == nil || *stored != reason {
		t.Errorf("run_attempts.no_steps_reason = %v, want %q — pf_wrap's own no_steps_reason parameter "+
			"must reach the same column pf_complete_attempt writes it to", stored, reason)
	}
}
