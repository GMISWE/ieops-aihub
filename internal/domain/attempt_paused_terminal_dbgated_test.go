package domain

// DB-gated invariant tests for aihub#421, flow `attempt_paused_then_complete`
// — one of the four abnormal flows the aihub#412 corpus mining turned up.
//
// Provenance. docs/audits/aihub-412-corpus-facts/sequence-inventory.md classifies
// 2756 (transcript, work item) groups; 10 of them (0.36%) match the detector "a
// call fails 409 ATTEMPT_PAUSED and a later pf_complete_attempt/pf_wrap
// succeeds". The three anonymized traces it prints are:
//
//	transcript 509409fe6a59, 11 calls:
//	  get_work_item -> claim_work_item -> recall -> recall -> recall -> pr ->
//	  pause_attempt -> complete_attempt(!ATTEMPT_PAUSED) -> claim_work_item ->
//	  complete_attempt -> update_work_item
//	transcript 509409fe6a59, 7 calls:
//	  claim_work_item -> recall -> pr -> pause_attempt ->
//	  complete_attempt(!ATTEMPT_PAUSED) -> claim_work_item -> complete_attempt
//	transcript d297e63472ef, 11 calls:
//	  create_work_item -> update_work_item -> emit_event(!CLIENT_UNCLASSIFIED) ->
//	  ... -> complete_attempt(!ATTEMPT_PAUSED) -> claim_work_item -> complete_attempt
//
// and error-taxonomy.md records the wire message those failures carried, twice
// over, at 5 calls each:
//
//	pf_complete_attempt | 409 | ATTEMPT_PAUSED | attempt is paused; resume it before continuing
//	pf_complete_attempt | 409 | ATTEMPT_PAUSED | attempt is paused; resume it before continuing
//	                                             (the closing note WAS already recorded; retrying
//	                                             this call will record it a second time)
//
// The invariant. `pause` and `complete` are the SAME domain function —
// handlePauseAttempt (internal/server/routes_step.go:1110) just sets
// req.Status="paused" and delegates — so the refusal is not a separate code path
// with its own guard. It is verifyAttemptCredential step 5
// (run_attempts.go:1508) returning before FnCompleteAttempt writes anything, out
// of a SERIALIZABLE transaction whose rollback is deferred. That is what makes
// the strong claim testable: the refusal must be TOTAL, across every record the
// success path touches, not merely "the status did not flip".
//
// Why this file exists at all. Both explore sweeps of the tree agreed:
// FnCompleteAttempt's credential rejection paths had NO test anywhere before
// this work item. ATTEMPT_PAUSED was referenced only by
// internal/mcp/tools_step_test.go, which classifies the message string on the
// client, and by the negative gate in routes_step_authority_test.go — neither
// executes the emitter. And internal/domain/errors_test.go does not help: its
// code->status table is a claim about the map rather than about any emitter, and
// ErrAttemptPaused is not even in it — neither TestCodeToHTTPStatus nor
// TestAllErrCodesMapped names it — so nothing anywhere touched this code.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15421/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestPausedAttemptTerminal' -v -count=1

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pausedAttemptFixture seeds a work item with a running attempt and then pauses
// it through the real FnCompleteAttempt path, returning the credential triple
// the corpus's failing call was holding.
//
// The pause goes through the production function rather than an UPDATE on
// purpose: ended_at, the work-item status transition and the attempt_completed
// timeline event are all written by FnCompleteAttempt, and a hand-written row
// would have none of them — so the refusal below would have nothing real to
// leave alone.
//
// ⚠️ It does NOT produce locks. seedRunAttempt INSERTs the attempt row directly
// and the seeded work item declares no resources, so the snapshot's LockRows is
// 0 before and after. That field cannot tell "the retained git_branch lock
// survived" from "no lock ever existed". Said here because an earlier draft of
// this comment claimed the pause-time lock split as part of the fixture's
// value, and it is not.
func pausedAttemptFixture(t *testing.T, pool *pgxpool.Pool) (wiID, attemptID, secret string) {
	t.Helper()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWI(t, pool, project, u)

	// C-R9-6 (run_attempts.go:514): a claim refuses a work item whose wi_type is
	// unset, and the recovery arm below resumes through the real claim path. The
	// seed helper does not set one, so set it here rather than discover it as a
	// WI_TYPE_MISMATCH two tests later.
	mustExec(t, pool, `UPDATE work_items SET wi_type='fix_bug' WHERE id='`+wi.ID+`'`)

	secret = "aihub421-paused-0123456789abcdef0123456789abcdef0123456789ab"
	attemptID = seedRunAttempt(t, pool, wi.ID, u, secret)

	reason := "aihub#421 fixture pause"
	aerr := FnCompleteAttempt(context.Background(), pool, wi.ID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "paused",
		PauseReason:   &reason,
	}, nil, "")
	require.Nil(t, aerr, "precondition: pausing the seeded attempt must succeed")

	return wi.ID, attemptID, secret
}

// attemptStateSnapshot is every record FnCompleteAttempt writes on the success
// path, read back in one place so the refusal can be compared against all of it
// rather than against whichever field the test author happened to remember.
//
// Honest accounting of what it can and cannot see against the fixture above.
// FIVE fields carry a non-trivial value and would move if the refusal wrote
// anything: WIStatus and AttemptStatus are "paused", AttemptEndedAtIsSet is
// true, WIClosedAtIsNull is true, AttemptCompletedEvt is 1 from the pause
// itself. TWO — StepCompletionRows and LockRows — are 0 both before and after,
// because this fixture starts no step and holds no lock. Those two are
// must-stay-zero guards, not discriminators: they would catch a refusal that
// force-terminated a step or took a lock, and they say nothing about retention.
// Do not read the struct equality below as "seven fields verified".
type attemptStateSnapshot struct {
	WIStatus            string
	WIClosedAtIsNull    bool
	AttemptStatus       string
	AttemptEndedAtIsSet bool
	AttemptCompletedEvt int
	StepCompletionRows  int
	LockRows            int
}

func snapshotAttemptState(t *testing.T, pool *pgxpool.Pool, wiID, attemptID string) attemptStateSnapshot {
	t.Helper()
	ctx := context.Background()
	var s attemptStateSnapshot
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, closed_at IS NULL FROM work_items WHERE id=$1`, wiID,
	).Scan(&s.WIStatus, &s.WIClosedAtIsNull))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, ended_at IS NOT NULL FROM run_attempts WHERE id=$1`, attemptID,
	).Scan(&s.AttemptStatus, &s.AttemptEndedAtIsSet))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_events WHERE work_item_id=$1 AND event_type='attempt_completed'`, wiID,
	).Scan(&s.AttemptCompletedEvt))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_step_completions WHERE work_item_id=$1`, wiID,
	).Scan(&s.StepCompletionRows))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM resource_locks WHERE owner_attempt_id=$1`, attemptID,
	).Scan(&s.LockRows))
	return s
}

// TestPausedAttemptTerminal_CompleteOnAPausedAttemptChangesNothing is the
// refusal arm: the exact call the 10 corpus groups made, and the assertion that
// it was a no-op rather than a partial wrap.
//
// The code assertion and the state assertions are BOTH load-bearing and they
// fail for different reasons. A server that returned ATTEMPT_MISMATCH here
// (which is what verifyAttemptCredential's next branch would answer) is wrong in
// a way the client can see: internal/mcp's classifyStepUpdateErr substring-
// matches "ATTEMPT_MISMATCH" and DELETES the local state file, so a paused
// agent that is supposed to be resumable would instead be told to re-claim, and
// the retained git_branch lock would have no holder that can release it. That is
// aihub#209, and the distinct code is the whole mitigation.
func TestPausedAttemptTerminal_CompleteOnAPausedAttemptChangesNothing(t *testing.T) {
	pool := setupLatestTestDB(t)
	wiID, attemptID, secret := pausedAttemptFixture(t, pool)

	before := snapshotAttemptState(t, pool, wiID, attemptID)
	require.Equal(t, "paused", before.WIStatus, "precondition: the work item is paused")
	require.Equal(t, "paused", before.AttemptStatus, "precondition: the attempt row is paused")

	aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "wrapped",
		Derived:       []string{},
	}, nil, "")

	require.NotNil(t, aerr, "completing a paused attempt must be refused")
	assert.Equal(t, ErrAttemptPaused, aerr.Code,
		"a paused attempt must answer its own code: the client keys on it to KEEP the state file and "+
			"point the user at resume, and any code containing ATTEMPT_MISMATCH makes internal/mcp "+
			"delete the credential instead (aihub#209)")
	assert.Equal(t, 409, aerr.HTTPStatus)
	assert.Equal(t, "attempt is paused; resume it before continuing", aerr.Message,
		"the wire message the corpus recorded for all 10 groups; it is what the client's "+
			"classifier and the operator both read")

	after := snapshotAttemptState(t, pool, wiID, attemptID)
	assert.Equal(t, before, after,
		"the refusal must be TOTAL. verifyAttemptCredential returns before the first write and "+
			"FnCompleteAttempt's transaction rolls back, so every record the success path touches — "+
			"work_items.status/closed_at, run_attempts.status/ended_at, the attempt_completed event, "+
			"the step-history rows and the attempt's locks — must be byte-identical. A refusal that "+
			"moved any one of them would leave a wi that is neither resumable nor wrapped")
}

// TestPausedAttemptTerminal_ResumeThenCompleteSucceeds is the recovery arm — the
// second half of every corpus trace, where the agent re-claims and the same
// terminal call goes through.
//
// It is also the control that keeps the arm above honest. A CAS predicate or a
// guard that refused EVERY complete would satisfy the refusal test perfectly;
// only a passing success path shows the refusal is selective.
func TestPausedAttemptTerminal_ResumeThenCompleteSucceeds(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	wiID, attemptID, _ := pausedAttemptFixture(t, pool)

	resumed, aerr := FnClaimWorkItem(ctx, pool, wiID, &ClaimRequest{
		IdempotencyKey: "aihub421-resume",
		SessionInfo: SessionInfo{
			MachineID:     "m_aihub421",
			SessionSecret: "aihub421-resumed-fedcba9876543210fedcba9876543210fedcba98",
		},
	}, testUser(t, pool), "", "tester")
	require.Nil(t, aerr, "re-claiming a paused work item is the documented resume path")

	// Resume does not reuse the attempt: this claim INSERTs a new row at epoch+1
	// with a hash of the freshly minted secret. Assert it, because the negative
	// control below depends on it — a resume that revived the old row would make
	// that test vacuous.
	//
	// ⚠️ "FnClaimWorkItem always inserts" would be FALSE: it has an
	// idempotent-replay exit that returns the EXISTING attempt untouched, secret
	// included (aihub#392). What makes this a real insert is the FRESH
	// IdempotencyKey above, because replay is keyed on
	// (work_item_id, idempotency_key). The guard holds because of the fixture,
	// not because of the function.
	require.NotEqual(t, attemptID, resumed.AttemptID,
		"resume must mint a NEW attempt row rather than revive this one")
	require.Equal(t, int64(2), resumed.ClaimEpoch, "the new attempt sits at the prior epoch + 1")

	aerr = FnCompleteAttempt(ctx, pool, wiID, &CompleteAttemptRequest{
		AttemptID:     resumed.AttemptID,
		ClaimEpoch:    resumed.ClaimEpoch,
		SessionSecret: "aihub421-resumed-fedcba9876543210fedcba9876543210fedcba98",
		Status:        "wrapped",
		Derived:       []string{},
	}, nil, "")
	require.Nil(t, aerr, "after a resume the terminal transition must go through: %+v", aerr)

	var wiStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&wiStatus))
	assert.Equal(t, "wrapped", wiStatus, "the recovery path must actually reach the terminal state")
}

// TestPausedAttemptTerminal_ResumeDoesNotRevivateTheOldCredential is the
// discriminating negative control, and it is the reason the two tests above are
// not enough.
//
// "After resume the same call succeeds" is true at the MCP layer and false at
// this one, and the difference is exactly what a reader gets wrong. The claim
// rewrites the state file, so the TOOL invocation is unchanged; the CREDENTIAL
// is not. If recovery were implemented by clearing a paused flag on the existing
// row, the pre-pause credential would start working again — a stale actor that
// had been sitting on a 409 for an hour would silently wrap a work item someone
// else had since resumed and moved on. It is not, and this pins that.
//
// ⚠️ The code here is CONFLICT_EPOCH_MISMATCH with NO superseded_by details, and
// both halves are deliberate. Resuming a paused work item does not supersede the
// old attempt: FnClaimWorkItem sets isTakeover only when wi.Status=="running"
// (run_attempts.go:523), so the old row keeps status='paused' rather than
// becoming 'superseded', and supersededByDetails (:1525) returns nil for
// anything that is not literally superseded. The presence or absence of those
// details is therefore the only thing that distinguishes "you paused and someone
// resumed" from "you were force-taken-over" — same code, different situation,
// and an agent reporting to a human needs to tell them apart.
func TestPausedAttemptTerminal_ResumeDoesNotRevivateTheOldCredential(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	wiID, oldAttemptID, oldSecret := pausedAttemptFixture(t, pool)

	_, aerr := FnClaimWorkItem(ctx, pool, wiID, &ClaimRequest{
		IdempotencyKey: "aihub421-resume-negctl",
		SessionInfo: SessionInfo{
			MachineID:     "m_aihub421",
			SessionSecret: "aihub421-negctl-0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c",
		},
	}, testUser(t, pool), "", "tester")
	require.Nil(t, aerr)

	aerr = FnCompleteAttempt(ctx, pool, wiID, &CompleteAttemptRequest{
		AttemptID:     oldAttemptID,
		ClaimEpoch:    1,
		SessionSecret: oldSecret,
		Status:        "wrapped",
		Derived:       []string{},
	}, nil, "")

	require.NotNil(t, aerr,
		"the pre-pause credential must stay dead after a resume; recovery mints a new attempt, "+
			"it does not un-pause the old one")
	assert.Equal(t, ErrConflictEpochMismatch, aerr.Code,
		"the old attempt is no longer current_attempt_id, so this is an epoch mismatch — NOT "+
			"ATTEMPT_PAUSED, which would tell the stale actor to resume something already resumed")
	assert.Equal(t, 409, aerr.HTTPStatus)
	assert.Nil(t, aerr.Details,
		"a resumed-past attempt carries NO superseded_by: its row is still 'paused', not "+
			"'superseded'. Those details are the only signal separating this from a force-takeover, "+
			"so inventing them here would make a takeover indistinguishable from an ordinary resume")

	var wiStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&wiStatus))
	assert.Equal(t, "running", wiStatus,
		"the refused stale call must not have wrapped the work item the new attempt is holding")
}

// TestPausedAttemptTerminal_AnEndedAttemptAnswersMismatchNotPaused is the other
// side of the same `if`, and it exists because a review of this file found the
// CI step's comment claiming coverage of run_attempts.go:1512 that no arm here
// delivered. Rather than delete the claim, here is the arm.
//
// verifyAttemptCredential step 5 is one branch with two exits:
//
//	if storedStatus != "running" {
//	    if storedStatus == "paused" { return ATTEMPT_PAUSED }
//	    return ATTEMPT_MISMATCH
//	}
//
// and the two exits mean OPPOSITE things to the client. internal/mcp's
// classifyStepUpdateErr substring-matches "ATTEMPT_MISMATCH" and DELETES the
// local state file ("please re-claim this work item"); it matches
// "ATTEMPT_PAUSED" and keeps it. Both are correct for their own state and
// catastrophic for the other's: deleting a paused agent's credential strands the
// git_branch lock it legitimately retains, and keeping a dead attempt's
// credential leaves the caller retrying a 403 forever.
//
// Nothing else in the tree reaches :1512. The two arms above that hold a
// non-current attempt short-circuit at step 1 before status is examined, so
// getting here needs an attempt that is still current_attempt_id, with a
// matching epoch AND a matching secret, whose own row has ended. That is a real
// state — it is what a wrapped-then-reopened work item looks like — and it is
// reachable only by ending the attempt row without moving current_attempt_id,
// which is what the UPDATE below does.
func TestPausedAttemptTerminal_AnEndedAttemptAnswersMismatchNotPaused(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWI(t, pool, project, u)

	secret := "aihub421-ended-0123456789abcdef0123456789abcdef0123456789ab"
	attemptID := seedRunAttempt(t, pool, wi.ID, u, secret)

	// End the attempt WITHOUT moving current_attempt_id, so the caller's
	// credential still passes steps 1, 3 and 4 and the status check is the only
	// thing left to fail. 'superseded' rather than 'wrapped' keeps the work item
	// itself non-terminal, which matters: the wi-level CONFLICT_TERMINAL_STATE
	// check at run_attempts.go:908 runs BEFORE the credential check and would
	// otherwise answer first and hide this branch entirely.
	mustExec(t, pool, `UPDATE run_attempts SET status='superseded', ended_at=clock_timestamp() WHERE id='`+attemptID+`'`)

	var wiStatus, attemptStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT w.status, r.status FROM work_items w JOIN run_attempts r ON r.id=w.current_attempt_id
		 WHERE w.id=$1`, wi.ID).Scan(&wiStatus, &attemptStatus))
	require.Equal(t, "running", wiStatus,
		"precondition: the WORK ITEM must still be non-terminal, or the wi-level terminal check answers first")
	require.Equal(t, "superseded", attemptStatus, "precondition: the attempt row has ended")

	aerr := FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "wrapped",
		Derived:       []string{},
	}, nil, "")

	require.NotNil(t, aerr, "an ended attempt must not be able to wrap")
	assert.Equal(t, ErrAttemptMismatch, aerr.Code,
		"an ended-but-still-current attempt is a DEAD credential, not a paused one: the client must be "+
			"told to re-claim (which is what internal/mcp does on ATTEMPT_MISMATCH), not told to resume")
	assert.Equal(t, 403, aerr.HTTPStatus,
		"403, not the paused branch's 409 — the two codes are mapped to different statuses and callers "+
			"classify on both")
	assert.NotContains(t, string(aerr.Code), "PAUSED",
		"this must not answer ATTEMPT_PAUSED. internal/mcp's classifier keys on the substring, so a "+
			"paused-shaped code here would make the client KEEP a credential that can never work again")
	assert.Contains(t, aerr.Message, `attempt status is "superseded"`,
		"the message must name the status it found, because \"only running attempts can be used\" alone "+
			"does not tell an operator whether they were superseded, wrapped or failed")
}
