package mcp_test

// DB-gated invariant tests for aihub#421, flow `terminal_without_claim` — the
// largest lifecycle class in the aihub#412 corpus, and the one whose name is
// misleading.
//
// Provenance. docs/audits/aihub-412-corpus-facts/sequence-inventory.md finds 258
// groups (9.36% of 2756) matching "complete_attempt/wrap present with no claim
// in this transcript". Its own detector description already reads it correctly —
// "the other half of a cross-transcript flow" — and it pairs with
// `claim_without_terminal` at 110 groups, "work that spans sessions, the
// commonest real shape". The three printed traces are all transcript
// d297e63472ef:
//
//	4 calls: create_work_item -> get_work_item -> update_work_item -> complete_attempt
//	9 calls: create_work_item -> update_work_item -> update_work_item -> get_work_item ->
//	         update_work_item -> update_work_item ->
//	         cancel_work_item(!CONFLICT_WI_ALREADY_CLAIMED) -> complete_attempt -> cancel_work_item
//	7 calls: create_work_item -> update_work_item x4 -> get_work_item -> complete_attempt
//
// ─── The finding that shaped this file ──────────────────────────────────────
//
// error-taxonomy.md records 626 failed calls across 14090, and NOT ONE of them
// is a terminal transition refused for want of a claim. Every one of the 258
// groups succeeded. That is a fact about the corpus, and it is the answer, not a
// gap: the state file outlives the transcript, so "no claim in this transcript"
// is a property of the TRANSCRIPT, never of the authorisation. Writing a test
// that expected these to be refused would have encoded a defect as a contract.
//
// So the invariant is about what the gate is keyed on:
//
//	A terminal transition requires the credential triple (attempt_id,
//	claim_epoch, session_secret) to name a live, running, CURRENT attempt. It
//	does not require — and must not require — that the caller performed the
//	claim.
//
// verifyAttemptCredential (internal/domain/run_attempts.go:1463) is the whole
// gate, and it reads no actor_user_id, no api_key_id and no machine_id. The
// plaintext session_secret is a bearer capability, not an identity. That is why
// the 258 groups are legitimate, and it is also the exposure: whoever holds the
// state file can wrap the work item.
//
// The three arms are therefore one control and two refusals, and the control is
// the important one — it is the arm that says the corpus's dominant shape is
// supported rather than tolerated.
//
// ─── Why the real stack and not the domain layer ────────────────────────────
//
// The credential does not travel as an argument. It is read off disk by each MCP
// handler and injected into the body, so "a second session with no claim of its
// own" is only expressible where a state file and a real server both exist.
// internal/domain/attempt_paused_terminal_dbgated_test.go covers the same
// verifyAttemptCredential from the other side, where the triple is passed in
// directly; neither test can stand in for the other.
//
// ⚠️ ONE RUN AT A TIME PER DATABASE. Like every other claimStack test, these
// share the fixed project p_echo_e2e, and newE2EStack DELETES every work item in
// it on construction. Sequential runs are fine — that wipe is what makes them
// repeatable — but two concurrent `go test ./internal/mcp/` processes pointed at
// one database will destroy each other's fixtures and fail in ways that look
// like real defects. Measured while developing this file: a mutation matrix run
// concurrently with a second test process produced a self-contradictory result
// table (mutants reddening arms in files they do not touch), and re-running it
// against a dedicated database made it coherent. CI is safe because the DB steps
// run sequentially in one job and nothing here calls t.Parallel; a developer
// running two shells is not.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15421/aihub_test?sslmode=disable \
//	go test ./internal/mcp/ -run 'TestE2ETerminalWithoutClaim' -v -count=1

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
)

// newDetachedSession stands up a SECOND MCP server and client session over the
// same aihub client, and returns a caller for it.
//
// This is the mechanical model of "a different transcript": a fresh MCP process
// that shares the database and the workspace state directory with the first, and
// shares nothing else. It has no memory of the claim, holds no in-process
// credential, and has never called pf_claim_work_item. Everything it knows about
// the attempt it will read off disk — which is exactly the situation of all 258
// corpus groups.
func newDetachedSession(t *testing.T, s *e2eStack) func(tool string, args map[string]any) (string, bool) {
	t.Helper()
	ctx := context.Background()

	server := mcp.New(nil, s.client)
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		sess, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = sess.Wait()
	}()

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "aihub421-detached", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("detached mcp session connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return func(tool string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("detached call %s: transport error: %v", tool, err)
		}
		text, ok := res.Content[0].(*sdkmcp.TextContent)
		if !ok {
			t.Fatalf("detached call %s returned %T, want TextContent", tool, res.Content[0])
		}
		return text.Text, res.IsError
	}
}

// claimThrough performs a real claim and returns the state file the claim wrote,
// which is the only thing a later session inherits.
func claimThrough(t *testing.T, s *e2eStack, wiID, idemKey string) *config.StateFile {
	t.Helper()
	// claimStack already creates the work item with wi_type=fix_bug, which
	// FnClaimWorkItem requires (C-R9-6, run_attempts.go:514).
	s.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id":    wiID,
		"idempotency_key": idemKey,
	})
	// persistedSecret fatals on an empty secret, which would make every
	// assertion below vacuous.
	_ = persistedSecret(t, wiID)
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		t.Fatalf("no state file after a successful claim: %v", err)
	}
	return sf
}

func wiStatus(t *testing.T, s *e2eStack, wiID string) string {
	t.Helper()
	var status string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&status); err != nil {
		t.Fatalf("read work item status: %v", err)
	}
	return status
}

// TestE2ETerminalWithoutClaim_ADetachedSessionWithTheLiveCredentialWraps is the
// CONTROL arm, and it is the point of the whole file: the corpus's dominant
// lifecycle shape must work.
//
// A session that never claimed, holding nothing but the state file the earlier
// claim left on disk, wraps the work item. If this ever goes red, 258 groups'
// worth of real cross-session work — every wi picked up the day after it was
// started — stops being completable, and the two refusal arms below would go on
// passing while it happened. So this is not a smoke test; it is the arm that
// makes the other two safe to have.
func TestE2ETerminalWithoutClaim_ADetachedSessionWithTheLiveCredentialWraps(t *testing.T) {
	s, wiID := claimStack(t, "wrap a work item from a session that never claimed it")
	sf := claimThrough(t, s, wiID, "aihub421-control")

	detached := newDetachedSession(t, s)
	text, isErr := detached("pf_complete_attempt", map[string]any{
		"work_item_id": wiID,
		"status":       "wrapped",
		"note":         "wrapped from a detached session (aihub#421 control arm)",
	})
	if isErr {
		t.Fatalf("a detached session holding the live credential must be able to wrap — this is the "+
			"shape of 258 of the corpus's 2756 groups, i.e. all cross-session work. Refusing it would "+
			"strand every work item whose claim happened in an earlier transcript.\ngot: %s", text)
	}

	if got := wiStatus(t, s, wiID); got != "wrapped" {
		t.Errorf("work item status = %q, want \"wrapped\"; the call reported success, so a status that "+
			"did not move would mean the success was cosmetic", got)
	}

	// Terminal statuses delete the state file (tools_lifecycle.go:1243). Assert
	// it, because the next session's refusal depends on it being gone.
	statePath := filepath.Join(config.StateDir(), sf.WIID+".json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("state file %q still present after a terminal completion (err=%v); the wrap deletes it "+
			"precisely so the next call cannot re-send a dead credential", statePath, err)
	}
}

// TestE2ETerminalWithoutClaim_ATamperedSecretIsRefused is the first refusal arm:
// a state file that exists but whose secret does not match.
//
// This is the failure the corpus does NOT contain, and that is why it needs a
// test rather than a citation. Its absence from 14090 calls means the path has
// never been exercised in production, so nothing but a test says what it does.
//
// 401 UNAUTHORIZED is the contract, and the status matters as much as the code:
// pkg/client renders the error as "aihub <status> <CODE>: <message>", and
// internal/mcp's stale-credential classifier keys on the code text. A refusal
// that came back as, say, 500 would be retried by callers that treat 5xx as
// transient, forever.
func TestE2ETerminalWithoutClaim_ATamperedSecretIsRefused(t *testing.T) {
	s, wiID := claimStack(t, "refuse a terminal transition carrying the wrong session secret")
	sf := claimThrough(t, s, wiID, "aihub421-tampered")

	// Rewrite the credential in place. Nothing else changes: same attempt_id,
	// same epoch, same file, same path — so the only thing under test is the
	// secret comparison.
	tampered := *sf
	tampered.SessionSecret = strings.Repeat("0", len(sf.SessionSecret))
	if tampered.SessionSecret == sf.SessionSecret {
		t.Fatalf("the tampered secret is identical to the real one; the arm would be vacuous")
	}
	if err := config.WriteStateFile(&tampered); err != nil {
		t.Fatalf("rewrite state file: %v", err)
	}

	detached := newDetachedSession(t, s)
	text, isErr := detached("pf_complete_attempt", map[string]any{
		"work_item_id": wiID,
		"status":       "wrapped",
	})
	if !isErr {
		t.Fatalf("a terminal transition with a wrong session_secret must be refused, got success: %s", text)
	}
	// The whole rendering, not the pieces. pkg/client emits
	// `aihub <status> <CODE>: <message>` (client.go:109), so matching the joined
	// string is strictly tighter than matching "401" and "UNAUTHORIZED"
	// separately — a bare "401" can be satisfied by digits inside an id or a
	// timestamp, which makes the status half no evidence at all.
	if !strings.Contains(text, "aihub 401 UNAUTHORIZED") {
		t.Errorf("error = %q\nwant it to name 401 UNAUTHORIZED. The code and the status are both part "+
			"of the contract: pkg/client renders \"aihub <status> <CODE>: ...\" and callers classify on "+
			"it, so a 5xx here would be retried indefinitely instead of prompting a re-claim", text)
	}
	if !strings.Contains(text, "invalid session_secret") {
		t.Errorf("error = %q, want it to say which credential failed — the operator's next action "+
			"differs entirely between a bad secret and a superseded attempt", text)
	}

	if got := wiStatus(t, s, wiID); got != "running" {
		t.Errorf("work item status = %q, want it still \"running\": a refused terminal transition must "+
			"not half-wrap the work item the legitimate holder is still working on", got)
	}
}

// TestE2ETerminalWithoutClaim_ASupersededAttemptIsRefusedWithWhoTookOver is the
// second refusal arm: the credential was valid, and is not any more.
//
// This is the case a long-lived agent actually hits — it is holding a state file
// from before someone took the work item over — and the assertion that matters
// is not the refusal but the DETAILS. verifyAttemptCredential enriches this one
// 409 with superseded_by{actor_display, at} (run_attempts.go:1467, :1525), and
// pkg/client appends details to the error text, so the losing session can tell a
// human who has it now instead of reporting an opaque conflict.
//
// ⚠️ Contrast this deliberately with the paused case in
// internal/domain/attempt_paused_terminal_dbgated_test.go. Both answer
// CONFLICT_EPOCH_MISMATCH. Only this one carries superseded_by, because
// supersededByDetails returns nil unless the caller's attempt row is literally
// 'superseded' — and resuming a PAUSED work item does not supersede anything.
// Same code, two situations, and the details are the only thing that separates
// "you were taken over" from "you paused and then resumed". A test asserting
// only the code would let that distinction be lost.
func TestE2ETerminalWithoutClaim_ASupersededAttemptIsRefusedWithWhoTookOver(t *testing.T) {
	s, wiID := claimStack(t, "refuse a terminal transition from an attempt that was superseded")
	stale := claimThrough(t, s, wiID, "aihub421-stale")

	// Claiming again as the same user on a running work item is the implicit
	// force-takeover path (C-R9-12, run_attempts.go:522): it supersedes the
	// prior attempt and mints a new one at epoch+1.
	s.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id":    wiID,
		"idempotency_key": "aihub421-stale-takeover",
	})
	fresh, err := config.ResolveStateFile(wiID)
	if err != nil {
		t.Fatalf("no state file after the second claim: %v", err)
	}
	if fresh.AttemptID == stale.AttemptID {
		t.Fatalf("the second claim reused attempt %s, so there is nothing superseded to test; the "+
			"implicit-takeover path must have been reached", stale.AttemptID)
	}

	// Put the STALE credential back on disk — this is the long-lived agent that
	// never learned it had been taken over.
	if err := config.WriteStateFile(stale); err != nil {
		t.Fatalf("restore stale state file: %v", err)
	}

	detached := newDetachedSession(t, s)
	text, isErr := detached("pf_complete_attempt", map[string]any{
		"work_item_id": wiID,
		"status":       "wrapped",
	})
	if !isErr {
		t.Fatalf("a superseded attempt must not be able to wrap the work item, got success: %s", text)
	}
	// Joined, for the same reason as the arm above — and here it matters more:
	// this error appends `details=<json>` carrying an RFC3339 timestamp, so a
	// bare "409" would match the fractional seconds inside superseded_by.at.
	if !strings.Contains(text, "aihub 409 CONFLICT_EPOCH_MISMATCH") {
		t.Errorf("error = %q, want the rendering to name 409 CONFLICT_EPOCH_MISMATCH", text)
	}
	if !strings.Contains(text, "superseded_by") {
		t.Errorf("error = %q\nwant it to carry superseded_by. This is the ONLY signal distinguishing a "+
			"takeover from an ordinary paused-then-resumed epoch mismatch (the domain-level companion "+
			"test asserts the same code with NO details for that case)", text)
	}
	// ...and the details must actually NAME someone. supersededByDetails builds
	// `sb := map[string]any{}` and fills actor_display only if its second query
	// succeeds, so `details={"superseded_by":{}}` satisfies the Contains above
	// while telling the losing session nothing. "Report who holds it now" is
	// this arm's stated point, so assert the who.
	if !strings.Contains(text, "u_echo_e2e") {
		t.Errorf("error = %q\nwant superseded_by to name the actor that took over (u_echo_e2e). An "+
			"empty superseded_by object passes a substring check for the key and still leaves a human "+
			"with no one to go and ask", text)
	}

	if got := wiStatus(t, s, wiID); got != "running" {
		t.Errorf("work item status = %q, want still \"running\" — the new holder's attempt must survive "+
			"the stale actor's terminal call", got)
	}

	// And the live credential still works: the refusal is about WHICH attempt,
	// not about the work item having become uncompletable.
	if err := config.WriteStateFile(fresh); err != nil {
		t.Fatalf("restore fresh state file: %v", err)
	}
	text, isErr = detached("pf_complete_attempt", map[string]any{
		"work_item_id": wiID,
		"status":       "wrapped",
	})
	if isErr {
		t.Fatalf("the CURRENT attempt must still be able to wrap after a stale actor was refused: %s", text)
	}
	if got := wiStatus(t, s, wiID); got != "wrapped" {
		t.Errorf("work item status = %q, want \"wrapped\"", got)
	}
}
