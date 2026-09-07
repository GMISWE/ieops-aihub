package server

// aihub#399 (with aihub#403 folded in) — DB-gated gates for the OTHER two ways
// PATCH /v1/work_items/:id/step could answer 200 while the two records a step
// outcome owes disagree.
//
// aihub#390 made the wi_step_completions INSERT strict: its errors are the
// request's errors, and the artifact_summary cap is pre-validated. It left two
// halves of the same invariant open, and recorded both:
//
//   - (a) the INSERT is still reached only `if req.StepAttemptID != nil`, so a
//     completed/failed request that omits step_attempt_id files no history row
//     at all — while the wi_step_state UPDATE, the step_completed/step_failed
//     event and the 200 all go ahead. The MCP schema has published the field as
//     "required for completed/failed" since aihub#265 and the server never
//     enforced it. An explicitly-sent "" was worse than omitting it: non-nil, so
//     it filed a row keyed on the empty string, and the GLOBAL unique index
//     idx_wsc_attempt then answered 409 on the NEXT such request about an id the
//     caller never chose.
//   - (b) the events side. #390's note says the completions side is now strict
//     while the events side can still fail silently, so the invariant can break
//     from the other direction: every step event was emitted inside its own
//     `SAVEPOINT bp` whose ROLLBACK discarded the error, leaving a history row
//     with no timeline entry and a 200.
//
// The invariant this file holds, in both directions: a 200 from a
// completed/failed transition implies BOTH the wi_step_completions row for that
// step attempt AND the matching step_completed/step_failed event; a request that
// cannot deliver both is refused and commits nothing.
//
// Why each arm needs a database. (a) is only observable as a difference between
// two tables. (b)'s two reachable causes are constraints, measured on
// pgvector/pgvector:pg18 (PostgreSQL 18.6, the CI image) with this repo's
// migrations applied:
//
//	run_attempt_id = ''   -> 23503, violates agent_events_run_attempt_id_fkey
//	run_attempt_id = NULL -> accepted (the column is nullable by design, 0006)
//	payload::jsonb with a NUL byte -> 22P05, "unsupported Unicode escape
//	                                 sequence ... cannot be converted to text"
//
// Neither is visible without the real schema, which is the same reason the
// aihub#390 defect was invisible.
//
// The three functions are three independent claims, not arms of one guard:
// refusing a terminal transition with no step_attempt_id; refusing one whose
// timeline event cannot be written; and RECORDING — positively, not merely
// "no longer silently dropping" — the event of an attempt-less transition.
//
// Gated by AIHUB_TEST_DB like routes_step_test.go, whose seed helpers these
// reuse (plus patchStep / readStepState from routes_step_fused_test.go and
// requireHistoryMatchesEvents / requireNothingCommitted from
// routes_step_history_row_db_test.go).
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleUpdateStep_(TerminalWithoutStepAttemptIDIsRefused|TimelineEventIsRecordedOrTheRequestIsRefused|AttemptlessStepEventIsRecordedWithANullAttempt)' -v -count=1

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// stepEventCount counts step_* events for one work item, whatever attempt they
// are filed under. readRecordedOutcomes cannot stand in for it: that one selects
// by run_attempt_id, and two arms here deliberately send an attempt_id that is
// empty or names nothing, which is exactly the row an attempt-keyed query cannot
// see.
func stepEventCount(t *testing.T, pool *pgxpool.Pool, wiID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_events
		WHERE work_item_id = $1 AND event_type LIKE 'step\_%'`, wiID).Scan(&n))
	return n
}

// historyRowsFor counts wi_step_completions rows for one step_attempt_id. Used
// with "" as the argument it answers the question the empty-string arm is about:
// whether a blank id got filed as if it were an id.
func historyRowsFor(t *testing.T, pool *pgxpool.Pool, stepAttemptID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_step_completions WHERE step_attempt_id = $1`, stepAttemptID).Scan(&n))
	return n
}

// historyRowsForWI counts every wi_step_completions row of one work item, for
// the arms that assert "no row was filed" without knowing which id it would
// have been filed under.
func historyRowsForWI(t *testing.T, pool *pgxpool.Pool, wiID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_step_completions WHERE work_item_id = $1`, wiID).Scan(&n))
	return n
}

// TestHandleUpdateStep_TerminalWithoutStepAttemptIDIsRefused is claim (a).
//
// On the unfixed tree the omitted-id request answers 200, bumps the version,
// emits step_completed and files nothing — so the assertion that goes red first
// is the invariant, not the status code, and the two are ordered accordingly
// (same ordering, for the same reason, as aihub#390's first test).
func TestHandleUpdateStep_TerminalWithoutStepAttemptIDIsRefused(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	att := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := stepWriter(uid, project)

	start := func(step, sa string) {
		t.Helper()
		code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
			"attempt_id": att, "status": "in_progress", "step": step, "step_attempt_id": sa,
		})
		require.Equal(t, http.StatusOK, code, "start %s: body: %v", step, body)
	}

	// --- completed, step_attempt_id absent entirely -------------------------
	sa1 := domain.NewID("sa")
	start("implement", sa1)
	before, eventsBefore := readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "artifact_summary": "done, no id",
	})
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusBadRequest, code,
		"a completed transition with no step_attempt_id cannot file the history row completed_steps is read from, "+
			"so it must be refused rather than answered 200 with the step missing from the history; body: %v", body)
	assert.Equal(t, string(domain.ErrBadRequest), body["code"])
	assert.Contains(t, body["message"], "step_attempt_id", "the refusal must name the field the schema calls required")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, "in_progress", readStepState(t, pool, wi.ID).Status,
		"the refused step must still be in progress")

	// --- completed, step_attempt_id present but blank -----------------------
	// A distinct shape, not a rewording of the one above: "" is NON-nil, so the
	// unfixed tree reaches the INSERT and files a row keyed on the empty string.
	// The invariant HOLDS there (row and event both exist), which is precisely
	// why this arm asserts on the status code and on the absence of a row filed
	// under "" instead.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": "",
		"artifact_summary": "done, blank id",
	})
	require.Equal(t, http.StatusBadRequest, code,
		`step_attempt_id:"" is not an id and must be refused, not filed under the empty string `+
			`(the global unique index would then answer 409 on the next such request about an id nobody chose); body: %v`, body)
	assert.Contains(t, body["message"], "step_attempt_id")
	require.Equal(t, 0, historyRowsFor(t, pool, ""),
		"no history row may be filed under an empty step_attempt_id")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)

	// --- a heartbeat is untouched ------------------------------------------
	// heartbeat is selected by its own flag and may legitimately carry
	// status="completed" (see validateNextStepArgs), and it completes no step, so
	// the new requirement must sit AFTER the heartbeat return — exactly where
	// aihub#390 put the artifact_summary cap, and for the same reason.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "heartbeat": true, "status": "completed", "step": "implement",
	})
	require.Equal(t, http.StatusOK, code, "a heartbeat carrying status=completed must stay heartbeat_ok; body: %v", body)
	assert.Equal(t, "heartbeat_ok", body["status"])
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)

	// --- and the same step completes once an id is supplied -----------------
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": sa1,
		"artifact_summary": "done",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 1, historyRowsFor(t, pool, sa1))
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// --- failed branch, both shapes ----------------------------------------
	sa2 := domain.NewID("sa")
	start("verify", sa2)
	before, eventsBefore = readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "verify", "error_type": "verify_fail",
	})
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusBadRequest, code, "failed branch, no step_attempt_id; body: %v", body)
	assert.Contains(t, body["message"], "step_attempt_id")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)

	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "verify", "step_attempt_id": "", "error_type": "verify_fail",
	})
	require.Equal(t, http.StatusBadRequest, code, "failed branch, blank step_attempt_id; body: %v", body)
	require.Equal(t, 0, historyRowsFor(t, pool, ""))
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)

	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "verify", "step_attempt_id": sa2, "error_type": "verify_fail",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// in_progress never files a history row, so the requirement must NOT reach
	// it. A start with no step_attempt_id is how pf-execute's loop begins a graph
	// (engine.native.md) and has to keep working.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "ship",
	})
	require.Equal(t, http.StatusOK, code,
		"a bare in_progress start files no history row, so it must not be caught by the terminal requirement; body: %v", body)
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
}

// TestHandleUpdateStep_TimelineEventIsRecordedOrTheRequestIsRefused is claim
// (b): the step event is no longer best-effort.
//
// Two arms, two different constraints, both reached from the request body alone
// — no schema mutation and no fault-injection harness, because the swallow is
// wide enough that ordinary inputs reach it:
//
//   - 22P05 on the FAILED branch. `step` is copied verbatim into the event
//     payload, and payload::jsonb cannot store a NUL byte. The failed branch's
//     UPDATE does not touch current_step and the history row's step_id comes
//     from wi_step_state, so the row is written and only the event fails: the
//     invariant broken from the events side, which is the shape #390 predicted.
//     (The completed branch escapes this one by accident — its own
//     `UPDATE ... current_step = $2` fails first — so it is not the arm to use.)
//   - 23503 on an in_progress start whose attempt_id names no run attempt. The
//     credential check is skipped when session_secret is empty, so nothing
//     upstream notices, and the FK is the same one insertStepCompletion already
//     answers 400 for on a terminal transition.
func TestHandleUpdateStep_TimelineEventIsRecordedOrTheRequestIsRefused(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	att := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := stepWriter(uid, project)

	// --- 22P05: the event payload cannot hold the step name ----------------
	sa1 := domain.NewID("sa")
	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "ship", "step_attempt_id": sa1,
	})
	require.Equal(t, http.StatusOK, code, "start ship: body: %v", body)

	before, eventsBefore := readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "ship\x00x", "step_attempt_id": sa1,
		"error_type": "ship_fail",
	})
	// Invariant first: on the unfixed tree this answers 200 with the history row
	// written and no step_failed event, and THAT is the finding.
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusBadRequest, code,
		"a terminal transition whose step_failed event cannot be written must be refused, not answered 200 "+
			"with a history row and no timeline entry; body: %v", body)
	assert.Equal(t, string(domain.ErrBadRequest), body["code"])
	assert.Contains(t, body["message"], "step_failed", "the refusal must name the event that could not be recorded")
	assert.Contains(t, body["message"], "nothing was committed")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, 0, historyRowsFor(t, pool, sa1),
		"the history row must have rolled back with the failed event")

	// The same step then fails with a storable name, and both records land.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "ship", "step_attempt_id": sa1,
		"error_type": "ship_fail",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 1, historyRowsFor(t, pool, sa1))
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// --- the completed branch, same unstorable step name -------------------
	// The review of this change asserted from reading the code that the
	// completed branch "escapes the 22P05 by accident, because its own
	// UPDATE ... current_step = $2 fails first". That was an inference, and an
	// inference about which statement fails first is exactly the kind that is
	// wrong for free. Measured instead, 2026-09-07: the request answers 500
	// with the driver's own text — `ERROR: invalid byte sequence for encoding
	// "UTF8": 0x00 (SQLSTATE 22021)` — and commits nothing.
	//
	// So what is asserted here is the INVARIANT, not that code: never a 200,
	// and no record of either kind. The status is deliberately not pinned to
	// 500. It is a pre-existing raw-driver leak on this branch's generic
	// `execErr.Error()` handler, not something this change introduced, and
	// tidying it into a 400 that names `step` would be an improvement — one
	// this arm must not turn red. Why it is NOT tidied here: the obvious clean
	// fix, validating `step` up front, would refuse the request before the
	// event INSERT and so would remove the only input-reachable way to make
	// that INSERT fail — destroying the gate above, which is the whole
	// events-side half of this work item. That tension is real and is recorded
	// rather than resolved silently.
	histAll := historyRowsForWI(t, pool, wi.ID)
	sa2 := domain.NewID("sa")
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "tag", "step_attempt_id": sa2,
	})
	require.Equal(t, http.StatusOK, code, "start tag: body: %v", body)
	// Snapshot AFTER the start, so the comparison isolates the refused
	// completion rather than including the start's own transition.
	before, eventsAll := readStepState(t, pool, wi.ID), stepEventCount(t, pool, wi.ID)
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "tag\x00x", "step_attempt_id": sa2,
		"artifact_summary": "tagged",
	})
	t.Logf("completed with an unstorable step name -> %d %v", code, body)
	require.NotEqual(t, http.StatusOK, code,
		"a completed transition whose step name cannot be stored must not answer 200; body: %v", body)
	require.NotEmpty(t, body["code"], "the refusal must carry an error code, not an empty body")
	require.Equal(t, before, readStepState(t, pool, wi.ID),
		"a refused completion must leave wi_step_state untouched")
	require.Equal(t, eventsAll, stepEventCount(t, pool, wi.ID),
		"a refused completion must emit no step event")
	require.Equal(t, histAll, historyRowsForWI(t, pool, wi.ID),
		"a refused completion must file no history row")

	// tag then completes with a storable name, which also returns the step
	// state to idle — the next arm is an in_progress start and the idle
	// predicate would otherwise refuse it 409 before the events loop is
	// reached, quietly turning that arm into a test of something else.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "tag", "step_attempt_id": sa2,
		"artifact_summary": "tagged",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 1, historyRowsFor(t, pool, sa2))
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// --- 23503: attempt_id names no run attempt ----------------------------
	before, eventsAll = readStepState(t, pool, wi.ID), stepEventCount(t, pool, wi.ID)
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": "ra_thisAttemptDoesNotExist", "status": "in_progress", "step": "probe",
		"step_attempt_id": domain.NewID("sa"),
	})
	require.Equal(t, http.StatusBadRequest, code,
		"an attempt_id that names no run attempt cannot be filed on the step_started event, so the start must be "+
			"refused rather than answered 200 with the step in progress and no timeline entry; body: %v", body)
	assert.Equal(t, string(domain.ErrBadRequest), body["code"])
	assert.Contains(t, body["message"], "attempt_id")
	require.Equal(t, before, readStepState(t, pool, wi.ID),
		"a refused start must leave wi_step_state untouched")
	require.Equal(t, eventsAll, stepEventCount(t, pool, wi.ID),
		"a refused start must emit no step event")
}

// TestHandleUpdateStep_AttemptlessStepEventIsRecordedWithANullAttempt is the
// positive half of claim (b), and it is a separate claim because a fix that
// merely stopped swallowing would satisfy the test above while making every
// attempt-less transition newly 400 — a shape that answered 200 before.
//
// agent_events.run_attempt_id is nullable by design (0006: "global system
// events"), and '' is not a run attempt id. The handler passed req.AttemptID —
// a non-pointer string — straight through, so EVERY step transition PATCHed
// without an attempt_id violated the FK and lost its event silently. Passing
// SQL NULL records it instead: "this event belongs to no attempt" is a fact the
// column can hold.
func TestHandleUpdateStep_AttemptlessStepEventIsRecordedWithANullAttempt(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	uc := stepWriter(uid, project)

	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"status": "in_progress", "step": "prepare_context", "step_attempt_id": domain.NewID("sa"),
	})
	require.Equal(t, http.StatusOK, code,
		"a start with no attempt_id was accepted before this change and must stay accepted; body: %v", body)
	require.Equal(t, "in_progress", readStepState(t, pool, wi.ID).Status)

	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_events
		WHERE work_item_id = $1 AND event_type = 'step_started'
		  AND payload->>'step' = 'prepare_context' AND run_attempt_id IS NULL`, wi.ID).Scan(&n))
	require.Equal(t, 1, n,
		"the step_started event of an attempt-less start must be RECORDED with a NULL run_attempt_id; "+
			"the empty string it used to be given violates agent_events_run_attempt_id_fkey (23503), and that "+
			"violation was swallowed by the per-event savepoint")
	require.Equal(t, 1, stepEventCount(t, pool, wi.ID), "exactly one step event, not a duplicate")
}
