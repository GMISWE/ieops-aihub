package server

// aihub#398, step 1 of two — DB-gated gates for the step-IDENTITY predicate on
// PATCH /v1/work_items/:id/step.
//
// The defect. handleUpdateStep files the wi_step_completions row under the
// STORED current_step (`derefStr(currentStep)`, read from the table), never
// under the request's step_id, and until this change nothing required the two to
// agree. A completed/failed transition naming any other step therefore produced
// three records that contradicted each other and a 200:
//
//	history row      -> filed under the step the SERVER had open
//	step_completed   -> emitted naming the step the CALLER sent
//	wi_step_state    -> current_step overwritten with the CALLER's value
//
// pf_get_step's completed_steps is what aihub#265 tells a resuming agent to
// treat as the full done-set, so the result is a resumer that skips a step which
// never ran and redoes the one that did — in the same walk. And on a fused
// advance the damage is larger than one row: a stale actor completing the step
// it thinks it is on overwrites an in_progress successor, so the step that was
// actually running is recorded as finished and then forgotten.
//
// This is reachable without malformed input. The interactive loop's own
// documented "skip" path (plugins/polyforge/skills/pf-execute/references/
// engine-native-details.md) tells the agent to skip a step by making NO
// pf_update_step call, leaving the skipped step as current_step, and asserts
// that "the next step you actually complete reports itself and advances from
// there" — which is precisely the shape above.
//
// What this file does NOT gate, deliberately: current_step_status. Requiring
// 'in_progress' on a terminal transition is a separate predicate and a separate
// work item, because ~17% of measured completed calls have no prior in_progress
// for that (wi, step) in the same transcript and would be refused today. The
// consequence is not left implicit — TestHandleUpdateStep_DoubleCompleteIsCaught
// ByWhicheverGuardApplies measures the hole and names its owner, so the boundary
// between the two steps is a fact in the suite rather than a sentence in a
// commit message.
//
// Why a database. Every claim here is a disagreement BETWEEN records — the
// history row versus the event versus wi_step_state — and the unfixed handler
// answered 200 for all of them. There is nothing to observe without the real
// schema; the DB-free half (TestValidateStepIdentity, no database) can only
// check the predicate function in isolation, and a probe that deletes the
// handler's call site leaves it green.
//
// Gated by AIHUB_TEST_DB like routes_step_test.go, whose seed helpers these
// reuse, plus patchStep / readStepState / readStepCompletions from
// routes_step_fused_test.go, requireHistoryMatchesEvents /
// requireNothingCommitted / readRecordedOutcomes from
// routes_step_history_row_db_test.go, and stepEventCount / historyRowsFor /
// historyRowsForWI from routes_step_outcome_records_db_test.go.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleUpdateStep_(TerminalStepIdMustMatchTheStoredCurrentStep|DoubleCompleteIsCaughtByWhicheverGuardApplies)' -v -count=1

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// stepStateRows counts wi_step_state rows for one work item. Needed because
// readStepState requires the row to exist, and the first arm below is about a
// work item that has none — the case where the unfixed handler filed a
// completion under the empty string.
func stepStateRows(t *testing.T, pool *pgxpool.Pool, wiID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_step_state WHERE work_item_id = $1`, wiID).Scan(&n))
	return n
}

// completionsForStep counts the history rows filed under one step_id, which is
// the question "was this recorded twice" reduces to.
func completionsForStep(t *testing.T, pool *pgxpool.Pool, wiID, stepID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_step_completions WHERE work_item_id = $1 AND step_id = $2`,
		wiID, stepID).Scan(&n))
	return n
}

// TestHandleUpdateStep_TerminalStepIdMustMatchTheStoredCurrentStep is the
// predicate proper.
//
// Assertion ORDER is load-bearing throughout, and it is the aihub#390 ordering
// for the same reason: on the unfixed tree these requests answer 200 and the
// thing that is wrong is not the status code but the disagreement between the
// records. So requireHistoryMatchesEvents goes FIRST wherever it applies —
// otherwise a future reader would think this test is about a 409, and a change
// that returned 409 while still filing the wrong row would pass it.
func TestHandleUpdateStep_TerminalStepIdMustMatchTheStoredCurrentStep(t *testing.T) {
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

	// --- nothing open at all -----------------------------------------------
	// FIRST, before any step is started, because it is the one arm that needs a
	// work item with no wi_step_state row, and nothing here creates one but
	// startStep.
	//
	// 🔴 This arm is ACCEPTED, and the assertion is about WHERE the row lands.
	// Read the two reasons together, because either alone gives the wrong
	// design:
	//
	//   - It must not be refused. "No step open" is "no prior in_progress",
	//     which is exactly the population the STATE predicate was deferred over
	//     (~17% of measured completed calls). Refusing here was the first
	//     implementation and it turned three landed tests red —
	//     TestHandleUpdateStep_ArtifactSummary, _EscalatedStall and
	//     _MandatoryRecordGate all complete a step they never started — so the
	//     shape is live in this codebase, not hypothetical.
	//   - It must not file the row under the empty string, which is what the
	//     unfixed handler did: the UPDATE matches no row, currentStep stays nil,
	//     and derefStr(nil) was the key. So the completion was recorded as a
	//     step with no name, and pf_get_step's completed_steps carried a "" that
	//     matches no step_id a resuming agent could compare against.
	//
	// Both are satisfied by taking the row's step_id from req.Step. That is the
	// half of this change that makes the invariant hold by construction rather
	// than by a predicate having run first, and this arm is the only place it is
	// observable on its own — everywhere else the two values are equal.
	require.Equal(t, 0, stepStateRows(t, pool, wi.ID), "the seeded wi must start with no step state")
	saNone := domain.NewID("sa")
	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": saNone,
		"artifact_summary": "completed a step that was never started",
	})
	require.Equal(t, http.StatusOK, code,
		"completing a step that was never started must keep working — that is the deferred state predicate's "+
			"subject, not this one's; body: %v", body)
	require.Equal(t, 1, historyRowsFor(t, pool, saNone))
	require.Equal(t, 0, historyRowsFor(t, pool, ""),
		"the row must NOT be filed under the empty string: with no current_step the old code keyed it on "+
			"derefStr(nil), so completed_steps carried a nameless step")
	require.Equal(t, 1, completionsForStep(t, pool, wi.ID, "implement"),
		"it must be filed under the step the caller named, which is the only value available when none is stored")
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// --- completed naming another step -------------------------------------
	sa1 := domain.NewID("sa")
	start("implement", sa1)
	before, eventsBefore := readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	sa2 := domain.NewID("sa")
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "verify", "step_attempt_id": sa2,
		"artifact_summary": "verified",
	})
	// Invariant first: on the unfixed tree this is a 200 whose history row says
	// "implement" and whose event says "verify".
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusConflict, code,
		`completing "verify" while "implement" is the open step must be refused: the row would be filed under `+
			`"implement", recording a step that did not finish and omitting the one that did; body: %v`, body)
	assert.Equal(t, string(domain.ErrConflictCASFailed), body["code"])
	for _, want := range []string{"verify", "implement"} {
		assert.Contains(t, body["message"], want,
			"the refusal must name BOTH steps or the caller cannot tell which one to act on")
	}
	assert.Contains(t, body["message"], "Nothing was committed")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, 0, historyRowsFor(t, pool, sa2), "the refused step attempt must have no history row")
	require.Equal(t, 1, completionsForStep(t, pool, wi.ID, "implement"),
		`and nothing NEW may be filed under "implement" either — the open step did not finish (the one row `+
			`present is the never-started arm above)`)
	require.Equal(t, "in_progress", readStepState(t, pool, wi.ID).Status,
		"the open step must still be in progress after the refusal")

	// --- failed naming another step ----------------------------------------
	// A separate shape rather than a rewording: the failed UPDATE does not touch
	// current_step, so on the unfixed tree it left the state alone while still
	// filing the row under the wrong step. Same refusal, different write path.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "verify", "step_attempt_id": sa2,
		"error_type": "verify_fail",
	})
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusConflict, code, "failed naming another step; body: %v", body)
	assert.Equal(t, string(domain.ErrConflictCASFailed), body["code"])
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, 0, historyRowsFor(t, pool, sa2))

	// --- a fused advance is refused whole ----------------------------------
	// The predicate runs before the switch, so the successor is never started.
	// Worth its own arm because next_step is the one argument whose silent
	// mishandling aihub#290 already had to fix once: a refusal that started the
	// successor anyway would leave a step in_progress that no completion will
	// ever match.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "verify", "step_attempt_id": sa2,
		"artifact_summary": "verified", "next_step": "ship", "next_step_attempt_id": domain.NewID("sa"),
	})
	require.Equal(t, http.StatusConflict, code, "fused advance from the wrong step; body: %v", body)
	require.Nil(t, body["next_step"], "a refused fused call must not report a successor")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, "implement", derefStr(readStepState(t, pool, wi.ID).CurrentStep),
		"the successor must NOT have been started")

	// --- a heartbeat is untouched ------------------------------------------
	// heartbeat is selected by its own flag, may legitimately carry
	// status="completed", and returns before any write — so the predicate must
	// sit AFTER the heartbeat return, exactly where aihub#390 put the
	// artifact_summary cap and aihub#399 the step_attempt_id requirement. A
	// mismatched step_id here is the argument the heartbeat branch discards.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "heartbeat": true, "status": "completed", "step": "verify",
	})
	require.Equal(t, http.StatusOK, code,
		"a heartbeat carrying status=completed for another step must stay heartbeat_ok; body: %v", body)
	assert.Equal(t, "heartbeat_ok", body["status"])
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)

	// --- the control: the same request, naming the open step ---------------
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": sa1,
		"artifact_summary": "implemented",
	})
	require.Equal(t, http.StatusOK, code, "completing the open step must still work; body: %v", body)
	require.Equal(t, 1, historyRowsFor(t, pool, sa1))
	require.Equal(t, 2, completionsForStep(t, pool, wi.ID, "implement"),
		"the row must be filed under the step the caller named, which is now also the stored one (2 = this "+
			"control plus the never-started arm at the top)")
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// --- in_progress is NOT caught by this predicate -----------------------
	// Starting a step whose name differs from the last one IS how a graph
	// advances. If the predicate ever leaked onto in_progress, no work item
	// could reach its second step — so this arm is the negative control for the
	// exemption, not a courtesy.
	sa3 := domain.NewID("sa")
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "verify", "step_attempt_id": sa3,
	})
	require.Equal(t, http.StatusOK, code,
		`starting "verify" after "implement" completed must work — this is how every step graph advances; body: %v`, body)
	require.Equal(t, "verify", derefStr(readStepState(t, pool, wi.ID).CurrentStep))

	// --- and the failed branch's control -----------------------------------
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "verify", "step_attempt_id": sa3,
		"error_type": "verify_fail",
	})
	require.Equal(t, http.StatusOK, code, "failing the open step must still work; body: %v", body)
	require.Equal(t, 1, historyRowsFor(t, pool, sa3))
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// The whole walk, as pf_get_step would report it: exactly the two steps that
	// really finished, each under its own name. Every refusal above added
	// nothing.
	require.Equal(t, [][2]string{{"implement", "completed"}, {"implement", "completed"}, {"verify", "failed"}},
		readStepCompletions(t, pool, wi.ID),
		"the history must be exactly the steps that finished, under the names their callers claimed — the two "+
			"\"implement\" rows are the never-started arm and the control, and NOT ONE of them is the empty "+
			"string the old code would have written for the first")
}

// TestHandleUpdateStep_DoubleCompleteIsCaughtByWhicheverGuardApplies is the
// "two agents complete the same step" shape, and it exists to say WHICH guard
// catches WHICH variant — including the one that is still not caught.
//
// The three variants are not degrees of the same thing; they hit three
// different mechanisms, and lumping them together is how "the double-complete
// is handled now" would become a false summary of this change:
//
//	A  retry, SAME step_attempt_id     -> 409 CONFLICT_DUPLICATE (aihub#399's
//	                                      global unique index). Green before and
//	                                      after this change: a CONTROL, asserted
//	                                      so that the new predicate is shown not
//	                                      to have swallowed it — a caller
//	                                      resending after a lost response must
//	                                      keep getting "do not resend" rather
//	                                      than a mismatch conflict.
//	B  late actor, DIFFERENT id, after
//	   the graph advanced               -> 409 CONFLICT_CAS_FAILED (this change).
//	                                      RED before it: a 200 that filed the
//	                                      successor's name as completed and
//	                                      destroyed the in_progress successor.
//	C  second actor, DIFFERENT id,
//	   name still matches               -> STILL 200, and a second row is filed.
//	                                      Not closed here and not closable here:
//	                                      only current_step_status distinguishes
//	                                      it, which is the state predicate, i.e.
//	                                      aihub#398 step 2.
//
// 🔴 Variant C asserts the CURRENT behaviour, defect and all. That is
// deliberate: an unasserted comment about a known hole rots silently, and this
// one is the boundary between two work items. When the state predicate lands,
// this arm goes red — read its failure message, which says so, and flip it.
func TestHandleUpdateStep_DoubleCompleteIsCaughtByWhicheverGuardApplies(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	att := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := stepWriter(uid, project)

	// --- A: the retry, same step_attempt_id --------------------------------
	saA := domain.NewID("sa")
	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "implement", "step_attempt_id": saA,
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": saA,
		"artifact_summary": "implemented",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": saA,
		"artifact_summary": "implemented",
	})
	require.Equal(t, http.StatusConflict, code, "a resent completion must be a conflict; body: %v", body)
	require.Equal(t, string(domain.ErrConflictDuplicate), body["code"],
		"the guard that fires on a RESEND must remain aihub#399's duplicate index, not the identity predicate: "+
			"the caller needs \"this already landed, do not resend\", which a mismatch conflict does not say")
	assert.Contains(t, body["message"], "already has a step-history row")
	require.Equal(t, 1, historyRowsFor(t, pool, saA), "nothing may be recorded twice")
	require.Equal(t, 1, completionsForStep(t, pool, wi.ID, "implement"))
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// --- B: the late actor, after a fused advance --------------------------
	// The graph moves on: "one" completes and starts "two" in one transaction.
	// A stale actor then completes "one" with its own step_attempt_id. Before
	// this change that answered 200, filed the row under "two" — the step that
	// was RUNNING — and overwrote current_step with "one", so the successor was
	// recorded as finished and then forgotten. This is the corruption, not a
	// tidy-up.
	saB1, saB2, saStale := domain.NewID("sa"), domain.NewID("sa"), domain.NewID("sa")
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "one", "step_attempt_id": saB1,
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "one", "step_attempt_id": saB1,
		"artifact_summary": "one done", "next_step": "two", "next_step_attempt_id": saB2,
	})
	require.Equal(t, http.StatusOK, code, "fused advance one->two: body: %v", body)
	require.Equal(t, "two", derefStr(readStepState(t, pool, wi.ID).CurrentStep))

	before, eventsBefore := readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "one", "step_attempt_id": saStale,
		"artifact_summary": "one done (stale actor)",
	})
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusConflict, code,
		`a stale actor completing "one" while "two" is open must be refused: the row would be filed under "two", `+
			`recording the RUNNING step as finished and then overwriting current_step; body: %v`, body)
	require.Equal(t, string(domain.ErrConflictCASFailed), body["code"],
		"a different step_attempt_id means aihub#399's duplicate index cannot fire, so this variant is the "+
			"identity predicate's alone")
	for _, want := range []string{"one", "two"} {
		assert.Contains(t, body["message"], want, "the refusal must name both the requested and the open step")
	}
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, 0, historyRowsFor(t, pool, saStale))
	require.Equal(t, 0, completionsForStep(t, pool, wi.ID, "two"),
		`"two" is still running and must not appear in the history`)
	require.Equal(t, "in_progress", readStepState(t, pool, wi.ID).Status,
		"the successor must still be running — the whole point is that a stale actor cannot end it")

	// "two" then completes normally, under its own name.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "two", "step_attempt_id": saB2,
		"artifact_summary": "two done",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 1, completionsForStep(t, pool, wi.ID, "two"))
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// --- C: the hole this work item does NOT close -------------------------
	saC1, saC2 := domain.NewID("sa"), domain.NewID("sa")
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "three", "step_attempt_id": saC1,
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "three", "step_attempt_id": saC1,
		"artifact_summary": "three done",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, "idle", readStepState(t, pool, wi.ID).Status)

	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "three", "step_attempt_id": saC2,
		"artifact_summary": "three done again",
	})
	t.Logf("variant C — second completion of an idle step with a fresh step_attempt_id -> %d %v", code, body)
	require.Equal(t, http.StatusOK, code,
		"MEASURED RESIDUAL, not a wish: with the step name still matching and a fresh step_attempt_id, neither the "+
			"identity predicate (the name agrees) nor aihub#399's duplicate index (the id is new) fires, so the "+
			"second completion still lands. Only current_step_status='idle' distinguishes it, and that is the STATE "+
			"predicate — aihub#398 step 2. If this now returns 409, the state predicate has landed: delete this arm "+
			"and assert the refusal instead. body: %v", body)
	require.Equal(t, 2, completionsForStep(t, pool, wi.ID, "three"),
		"and the residual's cost is a duplicate history row, which is exactly what the state predicate will remove")
}
