package domain

// aihub#684 — the database halves of the no-steps-recorded gate in
// FnCompleteAttempt: a wrap refused when a work item produced code
// (commit/push/pr_opened) but never had a single step opened for it
// (wi_step_state.version == 0), the escape hatch that un-refuses it, the
// gate's known blind spot held deliberately open, and the two structural
// guarantees (it reads wi_step_state directly rather than through the list
// path, and the escape hatch cannot half-land between the row and the
// timeline).
//
// AIHUB_TEST_DB-gated, in the AIHUB_TEST_DB style of every other DB test in
// this package (complete_attempt_derived_db_test.go is the direct model):
//
//	AIHUB_TEST_DB=postgres://postgres@127.0.0.1:5684/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestNoStepsRecordedGate|TestGateCannotSeeAStepBypassThatProducedNoCode' -count=1 -v

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// seedStepVersion inserts (or updates) a bare wi_step_state row for wiID at
// the given version, leaving current_step_status at its default 'idle' — the
// shape a work item that never had fnForceTerminateStep touch it has.
// version is the one column aihub#684's gate reads (run_attempts.go, the
// `stepVersion == 0` conjunct); leaving every other column at its migration
// 0005 default keeps this fixture from accidentally exercising the unrelated
// in_progress force-terminate branch a few lines above the gate.
func seedStepVersion(t *testing.T, pool *pgxpool.Pool, wiID string, version int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wi_step_state (work_item_id, wi_type, graph_source, version)
		VALUES ($1, 'feature', 'scenario_config', $2)
		ON CONFLICT (work_item_id) DO UPDATE SET version=$2`, wiID, version)
	require.NoError(t, err)
}

// seedCodeEvent inserts a bare commit/push/pr_opened event for wiID — the
// minimal row the gate's own EXISTS query (run_attempts.go, "SELECT
// EXISTS(SELECT 1 FROM agent_events WHERE work_item_id=$1 AND event_type =
// ANY($2))") reads. No run_attempt_id or payload: the gate's query does not
// scope by either, and AC9's own file is what pins that the work_item_id
// scoping (not run_attempt_id) is load-bearing.
func seedCodeEvent(t *testing.T, pool *pgxpool.Pool, wiID, eventType string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO agent_events (id, work_item_id, event_type)
		VALUES ($1, $2, $3)`, NewID("evt"), wiID, eventType)
	require.NoError(t, err)
}

// noStepsReasonColumn reads run_attempts.no_steps_reason, nil meaning NULL —
// the distinction migration 0042 exists to keep (mirrors readDerivedColumn
// one field over).
func noStepsReasonColumn(t *testing.T, pool *pgxpool.Pool, attemptID string) *string {
	t.Helper()
	var v *string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT no_steps_reason FROM run_attempts WHERE id=$1`, attemptID).Scan(&v))
	return v
}

// attemptCompletedPayload reads the payload of the most recent
// attempt_completed event for wiID.
func attemptCompletedPayload(t *testing.T, pool *pgxpool.Pool, wiID string) map[string]any {
	t.Helper()
	var raw []byte
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT payload FROM agent_events
		WHERE work_item_id=$1 AND event_type='attempt_completed'
		ORDER BY created_at DESC LIMIT 1`, wiID).Scan(&raw))
	var evt map[string]any
	require.NoError(t, json.Unmarshal(raw, &evt))
	return evt
}

// TestNoStepsRecordedGateRefusesWrapWithCodeAndNoStep is AC1: a work item
// with a code-produced event and wi_step_state.version==0 is refused on
// status="wrapped", a whitespace-only no_steps_reason is refused the same
// way an absent one is, and a real one both un-refuses the wrap and lands,
// byte-identical, in the column and in the attempt_completed event.
//
// Mutant (plan §2.1): delete the `stepVersion == 0` conjunct from the gate's
// condition. Not red here — AC1's own fixture already has version==0, so the
// gate still fires correctly under that mutant. It is AC3's fixture
// (version>0) that goes red, which is the point: this mutant widens the
// gate rather than narrowing it, and only a normal-shaped work item can show
// that.
func TestNoStepsRecordedGateRefusesWrapWithCodeAndNoStep(t *testing.T) {
	pool := setupLatestTestDB(t)

	t.Run("refused_409_with_no_reason_and_nothing_moves", func(t *testing.T) {
		_, wiID, attemptID, secret := wrapReadyFixture(t, pool)
		seedCodeEvent(t, pool, wiID, "commit")
		// No wi_step_state row at all: version stays at its Go zero value (0)
		// per the gate's own comment on the read below it.

		aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
			AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
			Status: "wrapped", Derived: []string{},
		}, nil, "")
		require.NotNil(t, aerr, "a commit event with no step ever opened wrapped anyway")
		require.Equal(t, ErrConflictNoStepsRecorded, aerr.Code)

		var attemptStatus, wiStatus string
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT status FROM run_attempts WHERE id=$1`, attemptID).Scan(&attemptStatus))
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&wiStatus))
		require.Equal(t, "running", attemptStatus, "a refused wrap must not have completed the attempt")
		require.Equal(t, "running", wiStatus, "a refused wrap must not have touched the work item")

		var completions int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT count(*) FROM agent_events WHERE work_item_id=$1 AND event_type='attempt_completed'`,
			wiID).Scan(&completions))
		require.Zero(t, completions, "a refused wrap left an attempt_completed event behind")
	})

	t.Run("whitespace_only_reason_is_refused_the_same_way_an_absent_one_is", func(t *testing.T) {
		_, wiID, attemptID, secret := wrapReadyFixture(t, pool)
		seedCodeEvent(t, pool, wiID, "push")
		reason := "   \t  \n  "

		aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
			AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
			Status: "wrapped", Derived: []string{}, NoStepsReason: &reason,
		}, nil, "")
		require.NotNil(t, aerr, "a whitespace-only no_steps_reason satisfied the gate")
		require.Equal(t, ErrConflictNoStepsRecorded, aerr.Code)
	})

	t.Run("a_real_reason_succeeds_and_is_readable_in_both_the_row_and_the_event", func(t *testing.T) {
		_, wiID, attemptID, secret := wrapReadyFixture(t, pool)
		seedCodeEvent(t, pool, wiID, "pr_opened")
		reason := "raw git push landed this; no step graph was ever opened for it"

		aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
			AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
			Status: "wrapped", Derived: []string{}, NoStepsReason: &reason,
		}, nil, "")
		require.Nil(t, aerr, "a non-empty no_steps_reason must let the wrap through")

		stored := noStepsReasonColumn(t, pool, attemptID)
		require.NotNil(t, stored, "the wrap carried a reason and the column is NULL")
		require.Equal(t, reason, *stored)

		evt := attemptCompletedPayload(t, pool, wiID)
		require.Equal(t, "wrapped", evt["status"])
		require.Equal(t, reason, evt["no_steps_reason"],
			"the attempt_completed payload and the column disagree about no_steps_reason")
	})
}

// TestGateCannotSeeAStepBypassThatProducedNoCode is AC2 — the predicate's
// known blind spot, held deliberately open, named for what it is rather than
// as a precision virtue: a work item that produced NO code at all is not
// gated on step count, but that is because the gate can only see
// commit/push/pr_opened events, not because a stepless completion is
// correct in general. aihub#680 (a deploy work item with a 3-step graph
// that bypassed it) is the counter-example that rules out naming or
// commenting this as deploy-shaped work items being naturally exempt.
//
// Mutant (plan §2.1): reduce the predicate to `stepVersion == 0` alone
// (drop the hasCodeEvent conjunct) → this work item, which never opened a
// step, gets refused → red.
func TestGateCannotSeeAStepBypassThatProducedNoCode(t *testing.T) {
	pool := setupLatestTestDB(t)
	_, wiID, attemptID, secret := wrapReadyFixture(t, pool)
	// Deliberately no seedCodeEvent call and no wi_step_state row: this is
	// the AC1 fixture's step shape (version==0) with the one difference the
	// blind spot is about — zero commit/push/pr_opened events.

	aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
		Status: "wrapped", Derived: []string{},
	}, nil, "")
	require.Nil(t, aerr, "a work item that produced no code at all must not be gated on step count")
}

// TestNoStepsRecordedGateAllowsNormalShape is AC3: a work item with code
// events AND a real step opened (version>0) must not be gated.
//
// Mutant (plan §2.1): invert the step conjunct to `stepVersion > 0` → red.
// Shares the AC1/AC3/AC5 mutant applied at the read site (hardcoding
// stepVersion to 0 immediately after the Scan) with
// TestNoStepsRecordedGateReadsStepStateDirectlyNotThroughListPath below —
// both fixtures set version>0, so both go red under it, which is the
// documented cross-arm effect (plan §2, AC5 row).
func TestNoStepsRecordedGateAllowsNormalShape(t *testing.T) {
	pool := setupLatestTestDB(t)
	_, wiID, attemptID, secret := wrapReadyFixture(t, pool)
	seedCodeEvent(t, pool, wiID, "commit")
	seedStepVersion(t, pool, wiID, 3)

	aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
		Status: "wrapped", Derived: []string{},
	}, nil, "")
	require.Nil(t, aerr,
		"a work item that opened at least one real step must not be gated just because it also produced code")
}

// TestNoStepsRecordedGateReadsStepStateDirectlyNotThroughListPath is AC5:
// the gate reads wi_step_state.version directly, never
// pf_list_work_items' step_state projection — domain.WorkItemStepState
// (work_items.go) carries no completed_steps field, so a gate reimplemented
// against that projection would see every work item as stepless regardless
// of its real version.
//
// The premise is pinned by driving the actual list path (ListWorkItems with
// IncludeStepState) and asserting its JSON has no completed_steps key,
// rather than only inspecting the Go struct — a json tag rename that
// quietly added the key would be caught the same way a caller reading the
// wire format would notice it.
//
// Mutant (plan §2.1): reimplement the step half as "completed_steps is
// empty" read from the list-path struct — the concrete, revertable stand-in
// for that is hardcoding stepVersion to 0 immediately after the Scan in
// FnCompleteAttempt, since there is no real completed_steps field to read
// from and every work item's list-path projection is equally empty of one.
// Every work item then looks stepless → this test and
// TestNoStepsRecordedGateAllowsNormalShape both go red.
func TestNoStepsRecordedGateReadsStepStateDirectlyNotThroughListPath(t *testing.T) {
	pool := setupLatestTestDB(t)
	project, wiID, attemptID, secret := wrapReadyFixture(t, pool)
	seedCodeEvent(t, pool, wiID, "commit")
	seedStepVersion(t, pool, wiID, 5)

	listed, aerr := ListWorkItems(context.Background(), pool, project, ListWorkItemsFilter{
		IDs: []string{wiID}, IncludeStepState: true,
	})
	require.Nil(t, aerr)
	require.Len(t, listed.Items, 1)
	require.NotNil(t, listed.Items[0].StepState,
		"the list path's own step_state did not populate, so the premise below would be vacuous")
	rawStepState, err := json.Marshal(listed.Items[0].StepState)
	require.NoError(t, err)
	require.NotContains(t, string(rawStepState), "completed_steps",
		"domain.WorkItemStepState now serializes a completed_steps key. The gate's own comment "+
			"(run_attempts.go, aihub#684) records that it deliberately reads wi_step_state.version "+
			"directly rather than through this projection BECAUSE that key did not exist; if it now "+
			"does, that comment and this test are both stale and the gate should be re-examined "+
			"rather than this assertion just deleted")

	aerr = FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
		Status: "wrapped", Derived: []string{},
	}, nil, "")
	require.Nil(t, aerr, "a work item with a real step opened (version=5) was refused by the gate")
}

// TestNoStepsRecordedGateOnlyGatesWrapped is AC4's DB half: the gate's own
// condition tests req.Status == "wrapped" itself, distinct from (and in
// addition to) the pre-BeginTx guard in
// complete_attempt_no_steps_reason_guard_test.go that refuses a stray
// no_steps_reason sent with the wrong status. A paused or failed completion
// of a work item shaped exactly like AC1's fixture (a commit event present,
// wi_step_state.version==0, no reason supplied) must pass straight through —
// only a wrap is gated on step count.
//
// Mutant (plan §2.1): delete the `req.Status == "wrapped" &&` conjunct from
// the gate's outer condition (run_attempts.go) — a paused or failed
// completion of this exact fixture is refused → red.
func TestNoStepsRecordedGateOnlyGatesWrapped(t *testing.T) {
	pool := setupLatestTestDB(t)
	for _, status := range []string{"paused", "failed"} {
		t.Run(status, func(t *testing.T) {
			_, wiID, attemptID, secret := wrapReadyFixture(t, pool)
			seedCodeEvent(t, pool, wiID, "commit")

			aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
				AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
				Status: status,
			}, nil, "")
			require.Nil(t, aerr, "a %s completion of a work item that produced code but never opened "+
				"a step must pass through untouched — only a wrap is gated on step count", status)
		})
	}
}

// TestNoStepsRecordedGateReasonCannotHalfLand is AC7: the column write and
// the attempt_completed payload are driven from the SAME normalized value,
// so a whitespace-only no_steps_reason cannot land in one place and not the
// other.
//
// Deliberately on a work item with NO code event at all (the gate never
// fires here) — AC7 is about the normalization discipline in the final
// write, not about the refusal, and isolating it from the gate is what
// keeps this arm from being redundant with AC1's third subtest, which
// already covers the positive (both sides carry a REAL reason) direction.
//
// Mutant (plan §2.1): gate the payload key on `req.NoStepsReason != nil` /
// `*req.NoStepsReason` instead of `noStepsReasonToStore` — the column still
// normalizes the whitespace-only value to NULL, but the event now carries
// the raw, untrimmed string, and the two rows disagree → red.
func TestNoStepsRecordedGateReasonCannotHalfLand(t *testing.T) {
	pool := setupLatestTestDB(t)
	_, wiID, attemptID, secret := wrapReadyFixture(t, pool)
	whitespace := "   \t\n  "

	aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
		Status: "wrapped", Derived: []string{}, NoStepsReason: &whitespace,
	}, nil, "")
	require.Nil(t, aerr,
		"a whitespace-only no_steps_reason on a work item the gate never touched must still let the wrap through")

	require.Nil(t, noStepsReasonColumn(t, pool, attemptID),
		"a whitespace-only no_steps_reason must normalize to NULL in the column, same as an absent one")

	evt := attemptCompletedPayload(t, pool, wiID)
	_, present := evt["no_steps_reason"]
	require.False(t, present,
		"the column normalized a whitespace-only no_steps_reason to NULL but the attempt_completed "+
			"event still carries the key — the row and the timeline have half-landed the same input "+
			"differently (AC7)")
}
