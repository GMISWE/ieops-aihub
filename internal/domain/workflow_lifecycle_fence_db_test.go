package domain

// workflow_lifecycle_fence_db_test.go — the aihub#708 Batch 2A re-review
// blockers B1-B4 (memory mem_FT9zbvSN), as live-PostgreSQL tests:
//
//	B1  recording one of several outstanding episode-bound invocations must
//	    RECORD successfully and leave the episode open — the close scan read a
//	    nullable result created_at into time.Time before checking for missing,
//	    so the whole result transaction rolled back instead
//	    (TestWorkflowEpisodeOutstandingResultRecordsAndStaysOpen).
//	B2  StartWorkflowStep / RecordWorkflowResult / AuthorizeWorkflowRepair lock
//	    the work item row BEFORE the attempt-credential check and hold it
//	    through the mutation — the same order the claim/pause/takeover
//	    transitions serialize on — so a pause or takeover that lands inside the
//	    check-to-write window makes the loser leave NO invocation, result or
//	    authorization behind (TestWorkflowLifecycleFenceBlocksLoserMutations,
//	    TestWorkflowRevisionRaceLeavesNoLoserMutation).
//	B3  an open invocation left by a paused/taken-over attempt is fenced by the
//	    EXPLICIT controller-reconcile transition, ordinary and episode-bound
//	    alike; the live attempt can then start a replacement, and a stale old
//	    result is rejected. No implicit trust: the named attempt's own row is
//	    read under the work item lock (TestWorkflowReconcileFencesDeadAttempt-
//	    Invocations).
//	B4  a start mints no invocation past the human gate: every step whose
//	    effective RHS (WI rhs AND step rhs) holds must have its LATEST
//	    recorded result's exact artifact approved — a missing decision, a
//	    rejection and a stale decision on an older artifact each refuse, and a
//	    machine credential's approval never exists to satisfy the gate
//	    (TestWorkflowStartRequiresExactArtifactApproval).
//
// Gated exactly like the other workflow DB tests (setupWorkflowDB): skipped
// unless AIHUB_TEST_DB is set. Run against an isolated database so a shared
// one cannot see half-finished fixtures:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5433/aihub_wf708b2a?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run 'TestWorkflow' -count=1 -v
//
// The B2 arms hold a raw pgx transaction open on the lifecycle transition's
// work-items write — the same discipline force_takeover_commit_window_db_test.go
// established (a real transition cannot be paused mid-transaction, and a bug in
// the function under test must not be able to break the fixture meant to expose
// it). The writes mirror what FnForceTakeover and the FnCompleteAttempt paused
// branch commit, restricted to the rows this fence reads.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	wf "github.com/GMISWE/ieops-aihub/internal/workflow"
)

// ─── B1: the nullable-scan regression ────────────────────────────────────────

// TestWorkflowEpisodeOutstandingResultRecordsAndStaysOpen is the B1 DB
// regression: with several episode-bound invocations outstanding, recording
// one of them must land and leave the episode open. The old
// episodeRoleSetComplete scanned the LEFT JOIN's nullable result created_at
// into a time.Time BEFORE the missing-result check, so the scan itself failed,
// the failure was classified as a DB error, and the caller's whole result
// transaction rolled back — the result was never recorded at all.
func TestWorkflowEpisodeOutstandingResultRecordsAndStaysOpen(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	// Two producers feed the failed gate, so the episode's repair role can
	// hold TWO bound invocations at once. The episode's role order gate
	// (aihub#708 re-review blocker 1) refuses to mint a dependent role before
	// its predecessors recorded, so the verification and fresh review roles
	// can no longer be bound while a producer is outstanding — two
	// simultaneously outstanding PRODUCER invocations are what keeps this
	// regression's substance: several outstanding bound invocations, one of
	// which records while the other has no result yet.
	fixa := wfSkill(t, ctx, pool, caller, "fix-a-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	fixb := wfSkill(t, ctx, pool, caller, "fix-b-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapReview}, []string{"change_a", "change_b"}, []string{"approval"}))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapVerification}, []string{"change"}, []string{"evidence"}))

	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true,
		wfTwoProducerGateFlowSpec(fixa, fixb, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

	for _, seed := range []struct{ stepID, artifact string }{
		{"fixa", "art-fixa"}, {"fixb", "art-fixb"},
	} {
		start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, seed.stepID)
		_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
			AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
			Result: wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", seed.artifact),
		})
		require.Nil(t, aerr)
	}

	reviewStart := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "review")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, reviewStart, wf.StatusCompleted, wf.ReviewFail, "art-review-1"),
	})
	require.Nil(t, aerr)

	attempt2 := wfAttempt(t, pool, wi.ID, owner, "s3cret", 2)
	auth, aerr := AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: reviewStart.StepAttemptID, Kind: "episode",
		Reason: "owner-authorized recovery from the failing review",
	})
	require.Nil(t, aerr)

	// TWO outstanding bound invocations — both repair producers, the role
	// with no predecessors. None has a recorded result yet.
	fixaRepair := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "fixa", auth.ID)
	fixbRepair := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "fixb", auth.ID)

	episodeStatus := func() string {
		var status string
		require.Nil(t, pool.QueryRow(ctx,
			`SELECT status FROM wi_workflow_repair_episodes WHERE id=$1`, auth.ID).Scan(&status))
		return status
	}
	resultCount := func(stepAttemptID string) int {
		var n int
		require.Nil(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM wi_workflow_results WHERE step_attempt_id=$1`, stepAttemptID).Scan(&n))
		return n
	}

	// THE regression: recording one of several outstanding bound invocations.
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, fixaRepair, wf.StatusCompleted, "", "art-fixa-2"),
	})
	require.Nil(t, aerr, "recording one of several outstanding bound invocations must succeed, not roll back on the close scan")
	require.False(t, rec.Paused)
	require.Equal(t, 1, resultCount(fixaRepair.StepAttemptID), "the result must actually be recorded")
	require.Equal(t, "open", episodeStatus(), "the episode must stay open while the other outstanding invocation has no result")

	// The second outstanding producer records too, and the episode still
	// stays open: the verification and review roles are not bound yet.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, fixbRepair, wf.StatusCompleted, "", "art-fixb-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "open", episodeStatus(), "the repair role recorded must leave the episode open")

	// The chain resumes in the episode's role order (aihub#708 re-review
	// blocker 1): the verification role mints now that every bound producer
	// recorded a completed result, and records.
	verif := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "verify", auth.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, verif, wf.StatusCompleted, "", "art-verify-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "open", episodeStatus(), "two of three roles recorded must leave the episode open")

	// The complete role set closes it — the machinery the nullable scan broke
	// still works when every role has recorded.
	fresh := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "review", auth.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, fresh, wf.StatusCompleted, wf.ReviewPass, "art-review-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "closed", episodeStatus(), "the complete role set must close the episode")
}

// ─── B2: the lifecycle fence ────────────────────────────────────────────────

// wfFenceHolder is one raw open transaction holding a lifecycle transition's
// work-items write, so the loser's locked read can park inside the window
// between the transition's commit and its own.
type wfFenceHolder struct {
	tx interface {
		Commit(context.Context) error
		Rollback(context.Context) error
	}
	committed bool
}

func (h *wfFenceHolder) commit(t *testing.T) {
	t.Helper()
	require.NoError(t, h.tx.Commit(context.Background()))
	h.committed = true
}

// wfFenceTakeover mirrors FnForceTakeover's committed writes for the fence: the
// prior attempt superseded and the work item's current attempt moved to the
// pre-seeded successor. Raw SQL for the reason force_takeover_commit_window_
// db_test.go states: a real takeover cannot be paused mid-transaction, and a
// bug in the function under test must not be able to break the fixture.
func wfFenceTakeover(t *testing.T, pool *pgxpool.Pool, wiID, deadAttempt, newAttempt string, newEpoch int64) *wfFenceHolder {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	h := &wfFenceHolder{tx: tx}
	t.Cleanup(func() {
		if !h.committed {
			_ = tx.Rollback(ctx)
		}
		conn.Release()
	})
	_, err = tx.Exec(ctx, `
		UPDATE run_attempts SET status='superseded', ended_at=clock_timestamp() WHERE id=$1`, deadAttempt)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		UPDATE work_items SET current_attempt_id=$1, current_attempt_epoch=$2 WHERE id=$3`,
		newAttempt, newEpoch, wiID)
	require.NoError(t, err)
	return h
}

// wfFencePause mirrors the FnCompleteAttempt paused branch's committed writes:
// the attempt paused (still the current one) and the work item paused with it.
func wfFencePause(t *testing.T, pool *pgxpool.Pool, wiID, attempt string) *wfFenceHolder {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	h := &wfFenceHolder{tx: tx}
	t.Cleanup(func() {
		if !h.committed {
			_ = tx.Rollback(ctx)
		}
		conn.Release()
	})
	_, err = tx.Exec(ctx, `
		UPDATE run_attempts SET status='paused', ended_at=clock_timestamp(),
			pause_reason='fence fixture pause' WHERE id=$1`, attempt)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		UPDATE work_items SET status='paused' WHERE id=$1`, wiID)
	require.NoError(t, err)
	return h
}

// wfFenceSeedAttempt inserts a RUNNING attempt row without touching the work
// item pointer — the takeover's successor, committed before the window opens.
func wfFenceSeedAttempt(t *testing.T, pool *pgxpool.Pool, wiID, userID, secret string, epoch int64) string {
	t.Helper()
	attemptID := NewID("ra")
	_, err := pool.Exec(context.Background(), `
		INSERT INTO run_attempts (id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, actor_display, machine_id, session_secret_hash)
		VALUES ($1, $2, 'running', $3, $4, $5, $5, 'm_fence', $6)`,
		attemptID, wiID, epoch, "idem_fence_"+attemptID, userID, HashSecret(secret))
	require.NoError(t, err)
	return attemptID
}

// wfFenceQueryLike is the statement every attempt-credential workflow route now
// opens with: getWorkflowWIOnTx's locked work-item read. Parking on THIS
// statement is what proves the loser's transaction began before the transition
// committed and reached its mutation after — the exact window B2 closes.
// Match the stable clause only: pg_stat_activity normalizes/formats SQL
// whitespace, so the literal query layout is not reliable there.
const wfFenceQueryLike = `%FOR UPDATE%`

// TestWorkflowLifecycleFenceBlocksLoserMutations is B2: while a lifecycle
// transition (takeover or pause) holds its work-items write uncommitted, the
// loser's workflow call parks on the locked row; when the transition commits,
// the loser re-reads the moved row and its credential check fails — leaving no
// invocation, result or authorization from the dead attempt. Without the
// lock-first fence the loser read the pre-transition row, passed the check and
// mutated; waitForLockWaiter would then never see a parked backend and the
// arms fail loudly instead of vacuously passing.
func TestWorkflowLifecycleFenceBlocksLoserMutations(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()

	type arm struct {
		name       string
		transition string // "takeover" | "pause"
		loser      string // "start" | "result" | "repair"
		wantCode   ErrCode
	}

	arms := []arm{
		{"takeover_while_start_mints_invocation", "takeover", "start", ErrConflictEpochMismatch},
		{"takeover_while_result_records", "takeover", "result", ErrConflictEpochMismatch},
		{"takeover_while_repair_authorizes", "takeover", "repair", ErrConflictEpochMismatch},
		{"pause_while_start_mints_invocation", "pause", "start", ErrAttemptPaused},
		{"pause_while_result_records", "pause", "result", ErrAttemptPaused},
		{"pause_while_repair_authorizes", "pause", "repair", ErrAttemptPaused},
	}

	for _, a := range arms {
		t.Run(a.name, func(t *testing.T) {
			owner := wfUser(t, pool, "owner")
			project := wfProjectName(t)
			wfCleanup(t, pool, project, owner)
			testSeedProject(t, pool, project, owner)
			caller := &UserRecord{ID: owner, Role: "writer"}

			spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t),
				wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
			review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t),
				wfSchemasContract([]skillregistry.Capability{skillregistry.CapReview}, []string{"change"}, []string{"approval"}))
			verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t),
				wfSchemasContract([]skillregistry.Capability{skillregistry.CapVerification}, []string{"change"}, []string{"evidence"}))

			wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfGateFlowSpec(spec, review, verify))
			require.Nil(t, aerr)
			attemptA := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

			// The pre-race state each loser shape needs: an open invocation
			// for the result loser, a provider_error result for the repair
			// loser.
			var specStart *StartWorkflowStepResponse
			if a.loser == "result" || a.loser == "repair" {
				specStart = wfStart(t, ctx, pool, wi.ID, attemptA, "s3cret", 1, "spec")
			}
			if a.loser == "repair" {
				_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
					AttemptID: attemptA, ClaimEpoch: 1, SessionSecret: "s3cret",
					Result: wfStepResult(t, ctx, pool, specStart, wf.StatusProviderError, "", "art-fence"),
				})
				require.Nil(t, aerr)
			}

			counts := func() (inv, res, rep int) {
				require.Nil(t, pool.QueryRow(ctx,
					`SELECT count(*) FROM wi_workflow_invocations WHERE work_item_id=$1 AND run_attempt_id=$2`,
					wi.ID, attemptA).Scan(&inv))
				require.Nil(t, pool.QueryRow(ctx,
					`SELECT count(*) FROM wi_workflow_results WHERE work_item_id=$1 AND run_attempt_id=$2`,
					wi.ID, attemptA).Scan(&res))
				require.Nil(t, pool.QueryRow(ctx,
					`SELECT count(*) FROM wi_workflow_repair_episodes WHERE work_item_id=$1 AND run_attempt_id=$2`,
					wi.ID, attemptA).Scan(&rep))
				return
			}
			preInv, preRes, preRep := counts()

			// A completed result needs a real artifact row. Seed it before the
			// lifecycle holder opens so the goroutine reaches RecordWorkflowResult's
			// locked work-item read without doing unrelated database setup first.
			var resultPayload []byte
			if a.loser == "result" {
				resultPayload = wfStepResult(t, ctx, pool, specStart, wf.StatusCompleted, "", "art-fence-2")
			}

			// Open the window: the transition's writes held uncommitted.
			var holder *wfFenceHolder
			if a.transition == "takeover" {
				successor := wfFenceSeedAttempt(t, pool, wi.ID, owner, "succ", 2)
				holder = wfFenceTakeover(t, pool, wi.ID, attemptA, successor, 2)
			} else {
				holder = wfFencePause(t, pool, wi.ID, attemptA)
			}

			// The loser: the workflow call with the attempt's credentials.
			got := make(chan *AihubError, 1)
			go func() {
				var aerr *AihubError
				switch a.loser {
				case "start":
					_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
						AttemptID: attemptA, ClaimEpoch: 1, SessionSecret: "s3cret", StepID: "spec"})
				case "result":
					_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
						AttemptID: attemptA, ClaimEpoch: 1, SessionSecret: "s3cret",
						Result: resultPayload,
					})
				case "repair":
					_, aerr = AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
						AttemptID: attemptA, ClaimEpoch: 1, SessionSecret: "s3cret",
						FailedStepAttemptID: specStart.StepAttemptID, Kind: "retry",
						Reason: "provider channel died inside the fence window",
					})
				}
				got <- aerr
			}()

			// Prove the loser really parked inside the window on the locked
			// work-item row, then let the transition commit.
			waitForLockWaiter(t, pool, wfFenceQueryLike,
				"the loser's locked work-item read (getWorkflowWIOnTx FOR UPDATE)")
			holder.commit(t)

			select {
			case aerr := <-got:
				require.NotNil(t, aerr, "the losing request must be refused, not silently succeed")
				require.Equal(t, a.wantCode, aerr.Code,
					"the loser's refusal code: %s", aerr.Message)
			case <-time.After(60 * time.Second):
				t.Fatal("the blocked loser never returned after the transition committed")
			}

			// THE assertion: the loser left no mutation behind. Whatever rows
			// existed before the window are unchanged; nothing new exists.
			postInv, postRes, postRep := counts()
			require.Equal(t, preInv, postInv, "no invocation may be minted from the dead attempt")
			require.Equal(t, preRes, postRes, "no result may be recorded from the dead attempt")
			require.Equal(t, preRep, postRep, "no repair authorization may be opened from the dead attempt")

			// The recoverable state is intact: a pre-existing open invocation
			// stays open (nothing half-closed it), and the work item is where
			// the transition left it.
			if a.loser == "result" {
				var status string
				require.Nil(t, pool.QueryRow(ctx,
					`SELECT status FROM wi_workflow_invocations WHERE step_attempt_id=$1`,
					specStart.StepAttemptID).Scan(&status))
				require.Equal(t, "open", status, "a refused result must leave the invocation open")
			}
			var wiStatus string
			require.Nil(t, pool.QueryRow(ctx,
				`SELECT status FROM work_items WHERE id=$1`, wi.ID).Scan(&wiStatus))
			if a.transition == "pause" {
				require.Equal(t, "paused", wiStatus)
			} else {
				require.Equal(t, "running", wiStatus)
			}
			wfCleanup(t, pool, project, owner)
		})
	}
}

// TestWorkflowRevisionRaceLeavesNoLoserMutation is B2's revision arm and its
// lock-order half: a workflow revision and a step start on the same RUNNING
// work item take the same first lock (the work item row), so every
// interleaving serializes — no deadlock, the revision (illegal while a worker
// is in progress) always loses and always leaves nothing, and the start wins
// exactly once. The revision's own gate is not new; what this pins is that the
// two transactions' order is CONSISTENT with each other and with the lifecycle
// transitions, which is the property B2's "preserve lock ordering" names.
func TestWorkflowRevisionRaceLeavesNoLoserMutation(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t), wfContract(skillregistry.CapAuthoring))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))

	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

	rhs := true
	revise := func() *AihubError {
		_, aerr := UpdateWorkItemWorkflow(ctx, pool, wi.ID, caller, owner, "writer", nil,
			UpdateWorkItemWorkflowRequest{ExpectedStepsVersion: 1, RequiresHumanSession: &rhs,
				Steps: wfFlowSpec(spec, review, verify)})
		return aerr
	}
	start := func() *AihubError {
		_, aerr := StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
			AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret", StepID: "spec"})
		return aerr
	}

	startsWon := 0
	for round := 0; round < 10; round++ {
		revErrs := make(chan *AihubError, 1)
		startErrs := make(chan *AihubError, 1)
		go func() { revErrs <- revise() }()
		go func() { startErrs <- start() }()

		var revErr, startErr *AihubError
		select {
		case revErr = <-revErrs:
		case <-time.After(60 * time.Second):
			t.Fatalf("round %d: the racing revision never returned — a lock-order inversion deadlocked it", round)
		}
		select {
		case startErr = <-startErrs:
		case <-time.After(60 * time.Second):
			t.Fatalf("round %d: the racing start never returned — a lock-order inversion deadlocked it", round)
		}

		// The revision is the loser every time: refused on the live status,
		// and nothing of it survives.
		require.NotNil(t, revErr, "round %d: a revision of a RUNNING work item must never succeed", round)
		require.Equal(t, ErrConflictWIAlreadyClaimed, revErr.Code,
			"round %d: the revision refusal code drifted: %s", round, revErr.Message)

		if startErr == nil {
			startsWon++
		} else {
			// Rounds after the winning start hit the one-open-invocation
			// fence — an ordinary typed refusal, never a deadlock or a 500.
			require.Equal(t, ErrConflictStepInProgress, startErr.Code,
				"round %d: unexpected start refusal: %s", round, startErr.Message)
		}

		var generations int
		require.Nil(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM wi_workflow_generations WHERE work_item_id=$1`, wi.ID).Scan(&generations))
		require.Equal(t, 1, generations, "round %d: the refused revision left a generation behind", round)
	}

	require.Equal(t, 1, startsWon, "exactly one start may win across the race rounds")
	var openInvocations int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_workflow_invocations WHERE work_item_id=$1 AND status='open'`, wi.ID).Scan(&openInvocations))
	require.Equal(t, 1, openInvocations)
}

// ─── B3: the explicit controller-reconcile transition ───────────────────────

// TestWorkflowReconcileFencesDeadAttemptInvocations is B3: an open invocation
// left by a paused/taken-over attempt is superseded by the explicit reconcile
// transition — ordinary, retry-bound and episode-bound alike — after which the
// live attempt can start replacements, a stale result for the superseded
// invocation is refused, and an episode completes through its replacement
// bindings. The no-implicit-trust refusals are pinned first: the dead attempt
// cannot call it, the current attempt cannot name itself, a foreign work
// item's attempt cannot be fenced, an unknown id answers 404, and a legacy
// no-workflow work item has nothing to reconcile.
func TestWorkflowReconcileFencesDeadAttemptInvocations(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapReview}, []string{"change"}, []string{"approval"}))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapVerification}, []string{"change"}, []string{"evidence"}))

	reconcile := func(wiID, attemptID, secret string, epoch int64, supersede string) (*WorkflowInvocationReconcile, *AihubError) {
		return ReconcileWorkflowInvocations(ctx, pool, wiID, ReconcileWorkflowInvocationsRequest{
			AttemptID: attemptID, ClaimEpoch: epoch, SessionSecret: secret, SupersedeAttemptID: supersede,
		})
	}

	// ── Ordinary invocations ─────────────────────────────────────────────────
	wi0, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfGateFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	attemptA := wfAttempt(t, pool, wi0.ID, owner, "s3cret", 1)
	staleStart := wfStart(t, ctx, pool, wi0.ID, attemptA, "s3cret", 1, "spec")

	// The takeover: attempt B inherits.
	attemptB := wfAttempt(t, pool, wi0.ID, owner, "res3med", 2)

	// The dead attempt's leftover blocks the step, loudly, pointing at the
	// reconcile transition rather than pretending the step is merely busy.
	_, aerr = StartWorkflowStep(ctx, pool, wi0.ID, StartWorkflowStepRequest{
		AttemptID: attemptB, ClaimEpoch: 2, SessionSecret: "res3med", StepID: "spec"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "reconcile", "the refusal must name the transition: %s", aerr.Message)

	// No implicit trust: each refusal shape.
	_, aerr = reconcile(wi0.ID, attemptA, "s3cret", 1, attemptA)
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictEpochMismatch, aerr.Code, "a dead attempt cannot drive the reconcile")

	_, aerr = reconcile(wi0.ID, attemptB, "res3med", 2, attemptB)
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code, "the current attempt cannot name itself")
	require.Contains(t, aerr.Message, "CURRENT attempt")

	foreignWI, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfGateFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	foreignAttempt := wfAttempt(t, pool, foreignWI.ID, owner, "f0reign", 1)
	_, aerr = reconcile(wi0.ID, attemptB, "res3med", 2, foreignAttempt)
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code, "another work item's attempt cannot be fenced here")

	_, aerr = reconcile(wi0.ID, attemptB, "res3med", 2, "ra_does_not_exist")
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code)

	// A legacy no-workflow work item has nothing to reconcile.
	legacy, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "legacy reconcile refusal " + wfHash(project)[:12], Source: "human",
	}, owner, owner, nil, "writer")
	require.Nil(t, aerr)
	legacyAttempt := wfAttempt(t, pool, legacy.ID, owner, "leg4cy", 1)
	_, aerr = ReconcileWorkflowInvocations(ctx, pool, legacy.ID, ReconcileWorkflowInvocationsRequest{
		AttemptID: legacyAttempt, ClaimEpoch: 1, SessionSecret: "leg4cy", SupersedeAttemptID: "ra_whatever",
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code)

	// The transition: B fences A's ordinary leftover.
	rec, aerr := reconcile(wi0.ID, attemptB, "res3med", 2, attemptA)
	require.Nil(t, aerr)
	require.Equal(t, 1, rec.Superseded)
	require.Equal(t, wi0.ID, rec.WorkItemID)
	require.Len(t, rec.InvocationIDs, 1)

	var invStatus string
	var supersededAt *time.Time
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status, superseded_at FROM wi_workflow_invocations WHERE id=$1`, rec.InvocationIDs[0]).
		Scan(&invStatus, &supersededAt))
	require.Equal(t, "superseded", invStatus)
	require.NotNil(t, supersededAt, "the abandon must be timestamped")
	var events int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_events WHERE work_item_id=$1 AND event_type='workflow_invocation_superseded'
		  AND payload->>'invocation_id'=$2`, wi0.ID, rec.InvocationIDs[0]).Scan(&events))
	require.Equal(t, 1, events, "the abandon must leave its timeline event")

	// Idempotent: nothing left open, the answer is an ordinary 0.
	rec, aerr = reconcile(wi0.ID, attemptB, "res3med", 2, attemptA)
	require.Nil(t, aerr)
	require.Equal(t, 0, rec.Superseded)

	// The replacement: B starts spec again and its result records.
	replacement := wfStart(t, ctx, pool, wi0.ID, attemptB, "res3med", 2, "spec")
	require.NotEqual(t, staleStart.StepAttemptID, replacement.StepAttemptID)

	// The stale old result is rejected — the superseded invocation answers
	// with its distinct stale refusal before any attempt-identity comparison;
	// from the dead attempt itself it never authenticates at all.
	_, aerr = RecordWorkflowResult(ctx, pool, wi0.ID, RecordWorkflowResultRequest{
		AttemptID: attemptB, ClaimEpoch: 2, SessionSecret: "res3med",
		Result: wfStepResult(t, ctx, pool, staleStart, wf.StatusCompleted, "", "art-stale"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)
	require.Contains(t, aerr.Message, "superseded", "the stale refusal must name the superseded invocation: %s", aerr.Message)
	_, aerr = RecordWorkflowResult(ctx, pool, wi0.ID, RecordWorkflowResultRequest{
		AttemptID: attemptA, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, staleStart, wf.StatusCompleted, "", "art-stale"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictEpochMismatch, aerr.Code, "the dead attempt cannot authenticate to record anything")
	var staleResults int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_workflow_results WHERE step_attempt_id=$1`, staleStart.StepAttemptID).Scan(&staleResults))
	require.Equal(t, 0, staleResults, "nothing may record against the superseded invocation")

	_, aerr = RecordWorkflowResult(ctx, pool, wi0.ID, RecordWorkflowResultRequest{
		AttemptID: attemptB, ClaimEpoch: 2, SessionSecret: "res3med",
		Result: wfStepResult(t, ctx, pool, replacement, wf.StatusCompleted, "", "art-replacement"),
	})
	require.Nil(t, aerr)

	// ── Episode-bound invocations ────────────────────────────────────────────
	// A's flow runs to a review FAIL; B resumes and opens an episode; B binds
	// the repair producer; then a SECOND takeover (C) leaves B's episode-bound
	// invocation open. C must reconcile B before it can re-bind the role, and
	// the episode must complete through the replacement bindings.
	wi1, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfGateFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	epA := wfAttempt(t, pool, wi1.ID, owner, "s3cret", 1)
	epSpec := wfStart(t, ctx, pool, wi1.ID, epA, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi1.ID, RecordWorkflowResultRequest{
		AttemptID: epA, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, epSpec, wf.StatusCompleted, "", "art-ep-1"),
	})
	require.Nil(t, aerr)
	epReview := wfStart(t, ctx, pool, wi1.ID, epA, "s3cret", 1, "review")
	_, aerr = RecordWorkflowResult(ctx, pool, wi1.ID, RecordWorkflowResultRequest{
		AttemptID: epA, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, epReview, wf.StatusCompleted, wf.ReviewFail, "art-ep-r1"),
	})
	require.Nil(t, aerr)

	epB := wfAttempt(t, pool, wi1.ID, owner, "res3med", 2)
	epAuth, aerr := AuthorizeWorkflowRepair(ctx, pool, wi1.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: epB, ClaimEpoch: 2, SessionSecret: "res3med",
		FailedStepAttemptID: epReview.StepAttemptID, Kind: "episode",
		Reason: "resume-authorized recovery from the failing review",
	})
	require.Nil(t, aerr)
	deadRepair := wfStartBound(t, ctx, pool, wi1.ID, epB, "res3med", 2, "spec", epAuth.ID)

	// The second takeover: C inherits mid-episode.
	epC := wfAttempt(t, pool, wi1.ID, owner, "th1rd", 3)

	// The dead episode binding blocks the role, pointing at the reconcile.
	_, aerr = StartWorkflowStep(ctx, pool, wi1.ID, StartWorkflowStepRequest{
		AttemptID: epC, ClaimEpoch: 3, SessionSecret: "th1rd", StepID: "spec", RepairEpisodeID: epAuth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "reconcile")

	rec, aerr = ReconcileWorkflowInvocations(ctx, pool, wi1.ID, ReconcileWorkflowInvocationsRequest{
		AttemptID: epC, ClaimEpoch: 3, SessionSecret: "th1rd", SupersedeAttemptID: epB,
	})
	require.Nil(t, aerr)
	require.Equal(t, 1, rec.Superseded, "the episode-bound invocation is fenced like an ordinary one")

	// The replacement binding takes the role the dead one occupied...
	liveRepair := wfStartBound(t, ctx, pool, wi1.ID, epC, "th1rd", 3, "spec", epAuth.ID)
	require.NotEqual(t, deadRepair.StepAttemptID, liveRepair.StepAttemptID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi1.ID, RecordWorkflowResultRequest{
		AttemptID: epC, ClaimEpoch: 3, SessionSecret: "th1rd",
		Result: wfStepResult(t, ctx, pool, liveRepair, wf.StatusCompleted, "", "art-ep-2"),
	})
	require.Nil(t, aerr)

	// ...and the episode completes through the replacement set: the
	// superseded row neither keeps the episode open nor steals a role.
	epVerif := wfStartBound(t, ctx, pool, wi1.ID, epC, "th1rd", 3, "verify", epAuth.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi1.ID, RecordWorkflowResultRequest{
		AttemptID: epC, ClaimEpoch: 3, SessionSecret: "th1rd",
		Result: wfStepResult(t, ctx, pool, epVerif, wf.StatusCompleted, "", "art-ep-v"),
	})
	require.Nil(t, aerr)
	epFresh := wfStartBound(t, ctx, pool, wi1.ID, epC, "th1rd", 3, "review", epAuth.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi1.ID, RecordWorkflowResultRequest{
		AttemptID: epC, ClaimEpoch: 3, SessionSecret: "th1rd",
		Result: wfStepResult(t, ctx, pool, epFresh, wf.StatusCompleted, wf.ReviewPass, "art-ep-r2"),
	})
	require.Nil(t, aerr)
	var episodeStatus string
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_repair_episodes WHERE id=$1`, epAuth.ID).Scan(&episodeStatus))
	require.Equal(t, "closed", episodeStatus, "the episode must close on its replacement bindings")

	// ── Retry-bound invocations ─────────────────────────────────────────────
	wi2, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfGateFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	rtA := wfAttempt(t, pool, wi2.ID, owner, "s3cret", 1)
	rtStart := wfStart(t, ctx, pool, wi2.ID, rtA, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi2.ID, RecordWorkflowResultRequest{
		AttemptID: rtA, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, rtStart, wf.StatusProviderError, "", "art-rt-1"),
	})
	require.Nil(t, aerr)
	rtAuth, aerr := AuthorizeWorkflowRepair(ctx, pool, wi2.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: rtA, ClaimEpoch: 1, SessionSecret: "s3cret",
		FailedStepAttemptID: rtStart.StepAttemptID, Kind: "retry",
		Reason: "provider channel died before the takeover",
	})
	require.Nil(t, aerr)
	wfStartBound(t, ctx, pool, wi2.ID, rtA, "s3cret", 1, "spec", rtAuth.ID)

	rtB := wfAttempt(t, pool, wi2.ID, owner, "res3med", 2)
	_, aerr = StartWorkflowStep(ctx, pool, wi2.ID, StartWorkflowStepRequest{
		AttemptID: rtB, ClaimEpoch: 2, SessionSecret: "res3med", StepID: "spec", RepairEpisodeID: rtAuth.ID})
	require.NotNil(t, aerr)
	require.Contains(t, aerr.Message, "reconcile")

	_, aerr = ReconcileWorkflowInvocations(ctx, pool, wi2.ID, ReconcileWorkflowInvocationsRequest{
		AttemptID: rtB, ClaimEpoch: 2, SessionSecret: "res3med", SupersedeAttemptID: rtA,
	})
	require.Nil(t, aerr)
	rtRetry := wfStartBound(t, ctx, pool, wi2.ID, rtB, "res3med", 2, "spec", rtAuth.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi2.ID, RecordWorkflowResultRequest{
		AttemptID: rtB, ClaimEpoch: 2, SessionSecret: "res3med",
		Result: wfStepResult(t, ctx, pool, rtRetry, wf.StatusCompleted, "", "art-rt-2"),
	})
	require.Nil(t, aerr)
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_repair_episodes WHERE id=$1`, rtAuth.ID).Scan(&episodeStatus))
	require.Equal(t, "closed", episodeStatus, "the retry authorization closes on its replacement binding")
}

// ─── B4: the start-side human-approval gate ────────────────────────────────

// TestWorkflowStartRequiresExactArtifactApproval is B4: a start mints no
// invocation past the human gate. Every step whose effective RHS (WI rhs AND
// step rhs) holds must have its LATEST recorded result's exact artifact
// approved: no decision refuses, a machine credential's non-approval refuses
// (approvals are human-only where they are recorded), a rejection refuses, a
// decision gone stale on an older artifact refuses, and only the exact
// approval lets the successor mint — on the ordinary path and inside a repair
// episode alike.
func TestWorkflowStartRequiresExactArtifactApproval(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapReview}, []string{"change"}, []string{"approval"}))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapVerification}, []string{"change"}, []string{"evidence"}))

	// rhsFlow is the gate flow with the spec step gated on the human.
	rhsFlow := func() []WorkflowStepSpec {
		steps := wfGateFlowSpec(spec, review, verify)
		steps[0].RHS = &[]bool{true}[0]
		return steps
	}
	startStep := func(wiID, attempt string, epoch int64, secret, stepID string) *AihubError {
		_, aerr := StartWorkflowStep(ctx, pool, wiID, StartWorkflowStepRequest{
			AttemptID: attempt, ClaimEpoch: epoch, SessionSecret: secret, StepID: stepID})
		return aerr
	}
	approve := func(wiID, stepID string, artifact wf.ArtifactRef, decision, actorType string) *AihubError {
		_, aerr := ApproveWorkflowStep(ctx, pool, wiID, caller, owner, actorType,
			ApproveWorkflowRequest{StepsVersion: 1, StepID: stepID, Artifact: artifact, Decision: decision})
		return aerr
	}

	// ── Missing, machine-forged and rejected decisions all refuse ──────────
	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, rhsFlow())
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)
	gated := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, gated, wf.StatusCompleted, "", "art-gate-1"),
	})
	require.Nil(t, aerr)

	// No decision yet: the successor cannot mint.
	aerr = startStep(wi.ID, attempt, 1, "s3cret", "review")
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "approves", "the refusal must name the missing human approval: %s", aerr.Message)
	var reviewInvocations int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_workflow_invocations WHERE work_item_id=$1 AND step_id='review'`,
		wi.ID).Scan(&reviewInvocations))
	require.Equal(t, 0, reviewInvocations, "no invocation may be minted past the gate")

	// A machine credential cannot approve, so its attempt satisfies nothing.
	aerr = approve(wi.ID, "spec", wfLatestArtifactRef(t, pool, wi.ID, "spec"), "approved", "machine")
	require.NotNil(t, aerr)
	require.Equal(t, ErrForbidden, aerr.Code)
	aerr = startStep(wi.ID, attempt, 1, "s3cret", "review")
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code, "the gate must still hold after the machine's refused approval")

	// A human REJECTION holds the gate closed too.
	aerr = approve(wi.ID, "spec", wfLatestArtifactRef(t, pool, wi.ID, "spec"), "rejected", "human")
	require.Nil(t, aerr)
	aerr = startStep(wi.ID, attempt, 1, "s3cret", "review")
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "REJECTED", "the refusal must name the rejection: %s", aerr.Message)

	// ── The exact approval opens the gate ───────────────────────────────────
	wi2, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, rhsFlow())
	require.Nil(t, aerr)
	attempt2 := wfAttempt(t, pool, wi2.ID, owner, "s3cret", 1)
	gated2 := wfStart(t, ctx, pool, wi2.ID, attempt2, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi2.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, gated2, wf.StatusCompleted, "", "art-gate-2"),
	})
	require.Nil(t, aerr)

	// An approval for a DIFFERENT artifact does not open the gate: the exact
	// latest artifact is the only one a decision can bind.
	aerr = approve(wi2.ID, "spec", wf.ArtifactRef{ID: "not-the-artifact", Version: 1, Hash: "sha256:" + wfHash("not-the-artifact")}, "approved", "human")
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)
	aerr = startStep(wi2.ID, attempt2, 1, "s3cret", "review")
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)

	aerr = approve(wi2.ID, "spec", wfLatestArtifactRef(t, pool, wi2.ID, "spec"), "approved", "human")
	require.Nil(t, aerr)
	aerr = startStep(wi2.ID, attempt2, 1, "s3cret", "review")
	require.Nil(t, aerr, "the exact approval must open the gate")

	// ── Stale mismatch inside a repair episode ─────────────────────────────
	// The gated step re-records through the episode path; the approval of the
	// OLD artifact must not carry, the verification role's start refuses with
	// the stale-mismatch refusal, and approving the NEW artifact lets the
	// episode complete.
	wi3, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, rhsFlow())
	require.Nil(t, aerr)
	attempt3 := wfAttempt(t, pool, wi3.ID, owner, "s3cret", 1)
	g3 := wfStart(t, ctx, pool, wi3.ID, attempt3, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi3.ID, RecordWorkflowResultRequest{
		AttemptID: attempt3, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, g3, wf.StatusCompleted, "", "art-ep3-1"),
	})
	require.Nil(t, aerr)
	require.Nil(t, approve(wi3.ID, "spec", wfLatestArtifactRef(t, pool, wi3.ID, "spec"), "approved", "human"))
	r3 := wfStart(t, ctx, pool, wi3.ID, attempt3, "s3cret", 1, "review")
	_, aerr = RecordWorkflowResult(ctx, pool, wi3.ID, RecordWorkflowResultRequest{
		AttemptID: attempt3, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, r3, wf.StatusCompleted, wf.ReviewFail, "art-ep3-r1"),
	})
	require.Nil(t, aerr)

	attempt4 := wfAttempt(t, pool, wi3.ID, owner, "res3med", 2)
	ep, aerr := AuthorizeWorkflowRepair(ctx, pool, wi3.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt4, ClaimEpoch: 2, SessionSecret: "res3med",
		FailedStepAttemptID: r3.StepAttemptID, Kind: "episode",
		Reason: "recover the failed review through the episode path",
	})
	require.Nil(t, aerr)

	// The repair producer re-runs the gated step. Its start passes the gate
	// (the failed review is excluded, the gated step IS the target), and its
	// NEW artifact is what the successor roles will be judged against.
	repairStart := wfStartBound(t, ctx, pool, wi3.ID, attempt4, "res3med", 2, "spec", ep.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi3.ID, RecordWorkflowResultRequest{
		AttemptID: attempt4, ClaimEpoch: 2, SessionSecret: "res3med",
		Result: wfStepResult(t, ctx, pool, repairStart, wf.StatusCompleted, "", "art-ep3-2"),
	})
	require.Nil(t, aerr)

	// THE stale mismatch: the verification role cannot start while the gated
	// step's LATEST artifact (art-ep3-2) carries no decision, even though an
	// older artifact of the same step carries an approval.
	_, aerr = StartWorkflowStep(ctx, pool, wi3.ID, StartWorkflowStepRequest{
		AttemptID: attempt4, ClaimEpoch: 2, SessionSecret: "res3med", StepID: "verify", RepairEpisodeID: ep.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "LATEST recorded artifact",
		"the stale-mismatch refusal must distinguish an old decision from none: %s", aerr.Message)

	// Approving the new artifact lets the episode finish.
	require.Nil(t, approve(wi3.ID, "spec", wfLatestArtifactRef(t, pool, wi3.ID, "spec"), "approved", "human"))
	v3 := wfStartBound(t, ctx, pool, wi3.ID, attempt4, "res3med", 2, "verify", ep.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi3.ID, RecordWorkflowResultRequest{
		AttemptID: attempt4, ClaimEpoch: 2, SessionSecret: "res3med",
		Result: wfStepResult(t, ctx, pool, v3, wf.StatusCompleted, "", "art-ep3-v"),
	})
	require.Nil(t, aerr)
	f3 := wfStartBound(t, ctx, pool, wi3.ID, attempt4, "res3med", 2, "review", ep.ID)
	_, aerr = RecordWorkflowResult(ctx, pool, wi3.ID, RecordWorkflowResultRequest{
		AttemptID: attempt4, ClaimEpoch: 2, SessionSecret: "res3med",
		Result: wfStepResult(t, ctx, pool, f3, wf.StatusCompleted, wf.ReviewPass, "art-ep3-r2"),
	})
	require.Nil(t, aerr)
	var epStatus string
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_repair_episodes WHERE id=$1`, ep.ID).Scan(&epStatus))
	require.Equal(t, "closed", epStatus)

	// The gate is tri-state by EFFECTIVE rhs: the same flow with the step's
	// rhs off (WI rhs still true) starts freely with no approval recorded.
	noRHS := wfGateFlowSpec(spec, review, verify)
	wi4, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, noRHS)
	require.Nil(t, aerr)
	attempt5 := wfAttempt(t, pool, wi4.ID, owner, "s3cret", 1)
	g5 := wfStart(t, ctx, pool, wi4.ID, attempt5, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi4.ID, RecordWorkflowResultRequest{
		AttemptID: attempt5, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, g5, wf.StatusCompleted, "", "art-norhs"),
	})
	require.Nil(t, aerr)
	require.Nil(t, startStep(wi4.ID, attempt5, 1, "s3cret", "review"),
		"a step whose EFFECTIVE rhs is false must not gate on an approval")

}
