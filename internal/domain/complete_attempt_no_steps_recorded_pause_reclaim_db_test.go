package domain

// aihub#684 — AC9: the no-steps-recorded gate is scoped to the WORK ITEM, not
// to the run attempt doing the wrapping. Pausing and re-claiming mints a
// second, structurally UNLINKED attempt — FnClaimWorkItem's
// wi.Status=="paused" branch takes the "normal claim" path (no isTakeover, no
// priorAttemptID; see run_attempts.go line ~605) rather than the takeover
// branch that would carry a parent_attempt_id forward. If the gate's
// EXISTS query were scoped to run_attempt_id instead of work_item_id, a
// commit made under attempt A would become invisible the moment attempt B
// wraps — the exact shape measured in 10 of 2756 transcript groups: pause,
// re-claim, wrap, with the code that was actually produced sitting on an
// attempt nobody is completing anymore.
//
// Own file, separate from complete_attempt_no_steps_recorded_db_test.go,
// because this is the one arm that needs a SECOND attempt (a pause and a
// re-claim) rather than a single wrapReadyFixture; AIHUB_TEST_DB-gated like
// every other DB-backed test in this package.
//
//	AIHUB_TEST_DB=postgres://postgres@127.0.0.1:5684/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestNoStepsRecordedGateSurvivesPauseAndReclaim -count=1 -v

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// seedCodeEventOnAttempt is seedCodeEvent (complete_attempt_no_steps_recorded_db_test.go)
// with run_attempt_id also populated, needed here specifically: the mutant
// this file pins (scope the gate's query by run_attempt_id instead of
// work_item_id) is only distinguishable from the correct query when the
// event's run_attempt_id and the WRAPPING attempt's id are two different
// values — seedCodeEvent alone leaves run_attempt_id NULL, which would make
// that mutant fail for every fixture in this package, not for the specific
// cross-attempt reason AC9 is about.
func seedCodeEventOnAttempt(t *testing.T, pool *pgxpool.Pool, wiID, attemptID, eventType string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type)
		VALUES ($1, $2, $3, $4)`, NewID("evt"), wiID, attemptID, eventType)
	require.NoError(t, err)
}

// TestNoStepsRecordedGateSurvivesPauseAndReclaim is AC9.
//
// Mutant (plan §2.1): change the gate's EXISTS query from
// `WHERE work_item_id=$1 AND event_type = ANY($2)` to
// `WHERE run_attempt_id=$1 AND event_type = ANY($2)` (querying by the
// WRAPPING attempt's id, attempt B, in place of the work item's id) → the
// commit event recorded under attempt A no longer matches → hasCodeEvent
// flips false → the wrap under attempt B, with wi_step_state.version still
// genuinely 0, succeeds with no no_steps_reason where it must be refused →
// this test's first assertion (require.NotNil(aerr)) goes red.
func TestNoStepsRecordedGateSurvivesPauseAndReclaim(t *testing.T) {
	pool := setupLatestTestDB(t)
	_, wiID, attemptA, secretA := wrapReadyFixture(t, pool)
	// FnClaimWorkItem refuses WI_TYPE_MISMATCH on a wi_type-less work item
	// (wrapReadyFixture's seedWI never sets one; wrapping doesn't care, but a
	// re-claim does) - set it directly, same as a caller would via
	// pf_update_work_item before the re-claim this test needs.
	mustExec(t, pool, `UPDATE work_items SET wi_type='feature' WHERE id='`+wiID+`'`)

	// Code produced under attempt A, before anything is paused. No step is
	// ever opened for this work item under attempt A or attempt B — the
	// premise is that the whole wi, across its entire attempt lineage, never
	// had wi_step_state.version move off 0.
	seedCodeEventOnAttempt(t, pool, wiID, attemptA, "commit")

	pauseErr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID: attemptA, ClaimEpoch: 1, SessionSecret: secretA, Status: "paused",
	}, nil, "")
	require.Nil(t, pauseErr, "pausing attempt A must succeed before it can be re-claimed")

	// testUser is idempotent and keyed on t.Name(), so this returns the SAME
	// user id wrapReadyFixture already created (and used as run_attempts'
	// actor_user_id FK owner) rather than a second, unrelated one.
	u := testUser(t, pool)
	secretB := "aihub684-reclaim-0123456789abcdef0123456789abcdef0123456789ab"
	claimResp, claimErr := FnClaimWorkItem(context.Background(), pool, wiID, &ClaimRequest{
		IdempotencyKey: "aihub684-ac9-reclaim-" + wiID,
		SessionInfo: SessionInfo{
			MachineID:     "m_aihub684_ac9",
			SessionSecret: secretB,
		},
	}, u, "", "tester")
	require.Nil(t, claimErr, "re-claiming a paused work item must succeed")
	require.NotEqual(t, attemptA, claimResp.AttemptID,
		"the re-claim minted the SAME attempt id back; this test needs two genuinely different attempts")
	require.Equal(t, int64(2), claimResp.ClaimEpoch,
		"a paused work item's re-claim takes the \"normal claim\" branch (run_attempts.go ~605), which "+
			"increments the epoch same as any other claim; a re-claim that inherited epoch 1 would mean "+
			"this fixture built a takeover instead of the unlinked re-claim AC9 is about")

	t.Run("wrapping_attempt_B_without_a_reason_is_still_refused", func(t *testing.T) {
		aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
			AttemptID: claimResp.AttemptID, ClaimEpoch: claimResp.ClaimEpoch, SessionSecret: secretB,
			Status: "wrapped", Derived: []string{},
		}, nil, "")
		require.NotNil(t, aerr, "attempt B wrapped a work item that produced code under attempt A "+
			"and never opened a single step under EITHER attempt, with no no_steps_reason, and it "+
			"succeeded — the gate is scoped to the run attempt instead of the work item")
		require.Equal(t, ErrConflictNoStepsRecorded, aerr.Code)
	})

	t.Run("a_reason_on_attempt_B_still_unblocks_it", func(t *testing.T) {
		reason := "attempt A pushed this and was paused before any step graph was opened; " +
			"re-claimed as attempt B to close it out"
		aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
			AttemptID: claimResp.AttemptID, ClaimEpoch: claimResp.ClaimEpoch, SessionSecret: secretB,
			Status: "wrapped", Derived: []string{}, NoStepsReason: &reason,
		}, nil, "")
		require.Nil(t, aerr, "a non-empty no_steps_reason on attempt B must still unblock the wrap "+
			"the gate correctly refused above")
		stored := noStepsReasonColumn(t, pool, claimResp.AttemptID)
		require.NotNil(t, stored)
		require.Equal(t, reason, *stored)
	})
}
