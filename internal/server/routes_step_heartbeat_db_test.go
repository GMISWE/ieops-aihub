package server

// aihub#442 — the heartbeat branch of handleUpdateStep.
//
// The branch had two silences, and these tests are about the difference between
// them. It bumped step_started_at with `_, _ = pool.Exec(...)`, so a failed
// write was indistinguishable from a successful one; and it returns early, so a
// step_id or status sent in the same request went nowhere with nothing said.
//
// Both are the aihub#290 shape ("a parameter we do not act on must not be
// accepted in silence") applied to a write rather than to a parameter, and
// neither could be observed from outside — which is why the fix has to be
// pinned by tests rather than by a comment. Gated on AIHUB_TEST_DB like every
// other test in this package:
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:15440/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleUpdateStep_Heartbeat' -v -count=1

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readStepStartedAt returns wi_step_state.step_started_at, or nil when the wi
// has no wi_step_state row at all. The two cases are distinguished by the
// second return value, because "no row" is exactly the state one arm below is
// about and a nil timestamp alone cannot express it.
func readStepStartedAt(t *testing.T, pool *pgxpool.Pool, wiID string) (*time.Time, bool) {
	t.Helper()
	var ts *time.Time
	err := pool.QueryRow(context.Background(),
		`SELECT step_started_at FROM wi_step_state WHERE work_item_id=$1`, wiID).Scan(&ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false
	}
	require.NoError(t, err)
	return ts, true
}

// TestHandleUpdateStep_HeartbeatReportsWhetherItRefreshedAnything pins the
// half of the defect that has nothing to do with an error: the UPDATE is keyed
// on work_item_id, so on a wi with no wi_step_state row it matches ZERO rows,
// returns no error, and used to be answered "heartbeat_ok" — a response that
// says the timestamp was refreshed when nothing was written at all.
//
// That state is not hypothetical. The fixture of
// TestHandleUpdateStep_NextStepRejectedUnlessCompleted ends by asserting there
// is no wi_step_state row for its wi, and its "plain heartbeat still works"
// case is answered 200 in exactly that state. It is also reachable in
// production: FnClaimWorkItem's wi_step_state upsert is deliberately non-fatal
// ("Non-fatal but log"), so a claim can succeed leaving no row behind, after
// which every heartbeat for that attempt bumps nothing forever.
//
// Both arms are asserted from the DATABASE as well as from the response, since
// a response field that merely echoes a constant would satisfy the response
// half of this test on its own.
func TestHandleUpdateStep_HeartbeatReportsWhetherItRefreshedAnything(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := fusedTestUser(uid, project)

	// Arm 1: no wi_step_state row. Still a 200 — the caller cannot act on a
	// missing row — but the body must not claim a refresh that did not happen.
	_, exists := readStepStartedAt(t, pool, wi.ID)
	require.False(t, exists,
		"fixture check: this arm is about a wi with NO wi_step_state row; if the seed now creates one "+
			"the assertion below would be testing the opposite case and still pass")

	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "heartbeat": true,
	})
	require.Equal(t, 200, code, "a heartbeat with no step state must stay a 200; body: %v", body)
	assert.Equal(t, "heartbeat_ok", body["status"])
	assert.Equal(t, false, body["step_started_at_refreshed"],
		"nothing was bumped — the UPDATE matched no row — so the response must not report a refresh")

	// Arm 2: with a row, the bump happens and is reported, and the timestamp
	// really moves. `before` is read after the in_progress transition wrote it,
	// so a heartbeat that did nothing would leave the two values equal.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "in_progress", "step": "implement",
		"step_attempt_id": "sa_hb_arm2",
	})
	require.Equal(t, 200, code, "seeding step state via in_progress failed; body: %v", body)
	before, exists := readStepStartedAt(t, pool, wi.ID)
	require.True(t, exists)
	require.NotNil(t, before)

	time.Sleep(5 * time.Millisecond) // clock_timestamp() is per-statement, not per-transaction
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "heartbeat": true,
	})
	require.Equal(t, 200, code, "body: %v", body)
	assert.Equal(t, "heartbeat_ok", body["status"])
	assert.Equal(t, true, body["step_started_at_refreshed"])

	after, exists := readStepStartedAt(t, pool, wi.ID)
	require.True(t, exists)
	require.NotNil(t, after)
	assert.True(t, after.After(*before),
		"step_started_at must have moved forward (%v -> %v); a reported refresh that did not "+
			"change the column would make the field a decoration", before, after)
}

// TestHandleUpdateStep_HeartbeatDBFailureIsReportedNotDiscarded is the test the
// work item exists for. The write is not decoration: step_recovery_hint is
// recomputed from current_step_status plus step_started_at on every claim of an
// already-claimed wi (internal/domain/run_attempts.go, both the fresh and the
// idempotent path) and calls a step whose timestamp is older than 15s
// `crashed_in_progress`. A bump that fails in silence therefore makes a live
// agent read as crashed to whoever takes the work item over.
//
// The failure is injected with a trigger rather than by closing the pool,
// because closing it would break the handler EARLIER — GetWorkItem uses the
// same pool — and the test would then pass without the heartbeat's own error
// ever being reached. The trigger fires only for this test's work_item_id, so
// it cannot disturb a test running concurrently in another package against the
// same database.
//
// The two controls are the point. Without arm 1 the test cannot distinguish
// "the error is now checked" from "this request was always a 500"; without
// arm 3 it cannot distinguish it from "heartbeats are broken".
func TestHandleUpdateStep_HeartbeatDBFailureIsReportedNotDiscarded(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := fusedTestUser(uid, project)
	ctx := context.Background()

	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "in_progress", "step": "implement",
		"step_attempt_id": "sa_hb_fail",
	})
	require.Equal(t, 200, code, "seeding step state via in_progress failed; body: %v", body)

	// Arm 1 (negative control): the same request succeeds before the failure is
	// injected.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "heartbeat": true,
	})
	require.Equal(t, 200, code, "control: the heartbeat must succeed before injection; body: %v", body)
	require.Equal(t, true, body["step_started_at_refreshed"])

	_, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION aihub442_break_step_bump() RETURNS trigger AS $fn$
		BEGIN
			IF NEW.work_item_id = `+quoteLiteral(wi.ID)+` THEN
				RAISE EXCEPTION 'aihub442 injected failure';
			END IF;
			RETURN NEW;
		END
		$fn$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DROP TRIGGER IF EXISTS aihub442_break_step_bump ON wi_step_state`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		CREATE TRIGGER aihub442_break_step_bump BEFORE UPDATE ON wi_step_state
		FOR EACH ROW EXECUTE FUNCTION aihub442_break_step_bump()`)
	require.NoError(t, err)
	dropTrigger := func() {
		_, dErr := pool.Exec(ctx, `DROP TRIGGER IF EXISTS aihub442_break_step_bump ON wi_step_state`)
		assert.NoError(t, dErr)
		_, dErr = pool.Exec(ctx, `DROP FUNCTION IF EXISTS aihub442_break_step_bump()`)
		assert.NoError(t, dErr)
	}
	t.Cleanup(dropTrigger)

	// Arm 2: the write fails, and the caller is told.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "heartbeat": true,
	})
	assert.Equal(t, 500, code,
		"a heartbeat whose UPDATE failed must not be answered 200 heartbeat_ok — that is a guard "+
			"failing with nobody learning, which is what aihub#442 removed; body: %v", body)
	raw := string(mustJSON(t, body))
	assert.NotContains(t, raw, "heartbeat_ok",
		"a failed bump must not carry the success token")
	assert.Contains(t, raw, "step_started_at",
		"the error has to name the write that failed, or the 500 is unactionable: %s", raw)

	// Arm 3 (negative control): with the injection removed the heartbeat works
	// again, so arm 2 measured the injected failure and not a broken branch.
	dropTrigger()
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "heartbeat": true,
	})
	require.Equal(t, 200, code, "body: %v", body)
	assert.Equal(t, "heartbeat_ok", body["status"])
	assert.Equal(t, true, body["step_started_at_refreshed"])
}

// TestHandleUpdateStep_HeartbeatDisclosesTheArgumentsItDrops covers the second
// half of aihub#442: the branch returns early, so a step_id or status in the
// same request is not acted on.
//
// It is DISCLOSED rather than rejected, and that is a decision, not an
// oversight. Rejecting it would refuse the taught producer — _common/
// lifecycle.md says "add heartbeat=true to an in_progress call every ~5 min",
// and 49 of the 50 heartbeat calls in the measured transcript corpus carry
// status="in_progress" plus a step_id — and heartbeat+status="completed"
// answering 200 heartbeat_ok is separately pinned as intended by three tests in
// this package (routes_step_outcome_records_db_test.go,
// routes_step_identity_db_test.go, routes_step_history_row_db_test.go) and by
// aihub#398's owner decision to document the drop rather than change it. So the
// values are reported back through aihub#314's
// request_adjusted list, whose meaning is exactly this: what the server did to
// a value it held.
//
// The last arm is the one that keeps the field honest. A disclosure attached to
// every heartbeat would be noise on the branch the producer calls every five
// minutes, so an untouched heartbeat must carry no request_adjusted key at all.
func TestHandleUpdateStep_HeartbeatDisclosesTheArgumentsItDrops(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := fusedTestUser(uid, project)

	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "in_progress", "step": "implement",
		"step_attempt_id": "sa_hb_disclose",
	})
	require.Equal(t, 200, code, "seeding step state via in_progress failed; body: %v", body)

	// The dangerous shape: the caller believes it completed "other", and the
	// server refreshed "implement" and completed nothing. Both facts are now in
	// the response.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "heartbeat": true,
		"status": "completed", "step": "other",
	})
	require.Equal(t, 200, code,
		"heartbeat+status=completed stays a 200 heartbeat_ok (pinned by aihub#398); body: %v", body)
	require.Equal(t, "heartbeat_ok", body["status"])

	adjusted := requestAdjustedByParam(t, body)
	require.Contains(t, adjusted, "status",
		"status was sent and completed nothing; saying nothing about it is the silent drop "+
			"aihub#442 removed. body: %v", body)
	assert.Equal(t, "completed", adjusted["status"]["requested"])
	assert.Nil(t, adjusted["status"]["applied"],
		"nothing was applied from the status — the branch completes no step")

	require.Contains(t, adjusted, "step_id", "body: %v", body)
	assert.Equal(t, "other", adjusted["step_id"]["requested"])
	assert.Equal(t, "implement", adjusted["step_id"]["applied"],
		"the bump keys on work_item_id alone, so it refreshed the OPEN step rather than the one "+
			"named — which is the fact the caller needs and can get nowhere else")

	// And the drop is real: no completion row was filed for either step.
	var completions int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_step_completions WHERE work_item_id=$1`, wi.ID).Scan(&completions))
	assert.Zero(t, completions,
		"fixture check: if a heartbeat DID complete the step there would be nothing to disclose "+
			"and this test would be asserting the wrong thing")

	// An untouched heartbeat says nothing, so the key's presence stays a signal.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "heartbeat": true,
	})
	require.Equal(t, 200, code, "body: %v", body)
	assert.Equal(t, "heartbeat_ok", body["status"])
	_, present := body["request_adjusted"]
	assert.False(t, present,
		"a heartbeat that dropped nothing must carry no request_adjusted key; the taught producer "+
			"calls this branch every ~5 min and an always-present list would be pure noise")
}

// requestAdjustedByParam indexes a response's request_adjusted list by param.
func requestAdjustedByParam(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	list, ok := body["request_adjusted"].([]any)
	if !ok {
		return out
	}
	for _, e := range list {
		entry, ok := e.(map[string]any)
		require.True(t, ok, "request_adjusted entry is not an object: %v", e)
		param, ok := entry["param"].(string)
		require.True(t, ok, "request_adjusted entry has no param: %v", e)
		out[param] = entry
	}
	return out
}

// quoteLiteral wraps s as a single-quoted SQL literal for the one place that
// cannot use a bind parameter: the body of a plpgsql function passed to CREATE
// FUNCTION, which is parsed as text at definition time. Test-only, and the
// input is a generated work item id.
func quoteLiteral(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += "''"
			continue
		}
		out += string(r)
	}
	return out + "'"
}

// TestHeartbeatGuardMessagesDoNotResurrectTheWordLease is the server-side half
// of the owner ruling recorded in aihub#416: aihub implemented leases early and
// abandoned them deliberately, migration 0004 removed run_attempts.expires_at,
// pf_renew_lease answers 410, and the word must not come back.
//
// internal/mcp's TestUpdateStepSchemaDoesNotPromiseALease already guards every
// string pf_update_step PUBLISHES. Neither that test nor this one is the whole
// guard, and the split matters: this covers the AUTHORITY's copy of the message
// — the one this handler produces — while its sibling
// TestValidateNextStepArgsDoesNotResurrectTheWordLease covers the MCP mirror,
// which is the copy that actually drifted (aihub#398 found "refreshes the lease"
// there while this server-side twin already said step_started_at). A guard on
// only this end would be a guard on the end that never broke.
//
// The negative assertion is paired with a positive one because a message
// emptied of all content would satisfy the negative half by itself.
func TestHeartbeatGuardMessagesDoNotResurrectTheWordLease(t *testing.T) {
	aerr := validateNextStepArgs("verify", "", "in_progress", true)
	require.NotNil(t, aerr, "heartbeat+next_step must still be refused")
	assert.NotContains(t, aerr.Message, "lease",
		"there is no lease: a claim is permanent ownership and pf_renew_lease answers 410. "+
			"Telling a caller a heartbeat refreshes one is what aihub#398 removed and "+
			"aihub#416's owner ruling keeps out. Got: %s", aerr.Message)
	assert.Contains(t, aerr.Message, "step_started_at",
		"the message has to say what a heartbeat DOES, or emptying it would pass the check above")
}
