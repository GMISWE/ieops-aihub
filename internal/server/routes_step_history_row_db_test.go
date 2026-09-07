package server

// aihub#390 — DB-gated gates for the step-history row that PATCH
// /v1/work_items/:id/step appends on every completed / failed transition.
//
// The defect. pf_get_step(aihub#383) answered six completed_steps with
// completed_steps_truncated=false while the same attempt had seven
// step_completed events, and the tool's own description tells a resuming agent
// to treat that list as the full done-set. Not a query bug — completedStepsQuery
// reads wi_step_completions by work_item_id and nothing else — the row was
// never written: prepare_context's completion carried a 4,243-character
// artifact_summary, the table has CHECK (length(artifact_summary) <= 4096)
// (internal/db/migrations/0005_step_state.sql), and the INSERT ran inside a
// savepoint whose ROLLBACK swallowed the violation. The wi_step_state UPDATE,
// the step_completed event (agent_events has no such cap) and the 200 all went
// through.
//
// Why each arm needs a database: the defect lives between the handler and that
// CHECK, and the invariant is between two tables. No DB-free test can see
// either; routes_step_authority_test.go pins the handler's constant to the
// migration's number and stops there.
//
//   - EveryRecordedStepOutcomeHasAHistoryRow drives the fix_bug graph the way
//     pf-execute does — first step started by a bare in_progress, the rest
//     fused, one retry — with the measured 4,243-character summary, and after
//     EVERY request asserts the invariant itself: the step_completed /
//     step_failed events for the attempt and pf_get_step's completed_steps are
//     the same list. Not "prepare_context is present": a fix that special-cased
//     the one length and left the swallow in place would satisfy that.
//   - ArtifactSummaryAtTheCapIsRecordedAndOverItIsRefused is the boundary, in
//     CHARACTERS: 4,096 CJK characters (12,288 bytes) must be recorded — that
//     is what kills a byte-counting check — and 4,097 must be refused before
//     anything is written, on both the completed and the failed branch.
//   - DuplicateStepAttemptIsAConflictNotASilentDrop is the rest of the class.
//     The savepoint swallowed EVERY error, not just this CHECK, so a fix that
//     only added a length pre-check would leave a duplicate step_attempt_id
//     answering 200 while writing nothing. This arm requires 409 with nothing
//     committed, on both branches.
//
// Gated by AIHUB_TEST_DB like routes_step_test.go, whose seed helpers these
// reuse (plus patchStep / readStepState from routes_step_fused_test.go).
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleUpdateStep_(EveryRecordedStepOutcomeHasAHistoryRow|ArtifactSummaryAtTheCapIsRecordedAndOverItIsRefused|DuplicateStepAttemptIsAConflictNotASilentDrop)' -v -count=1

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// The cap is written as a literal here on purpose, so that this file compiles
// against a tree WITHOUT the fix (which has no maxArtifactSummaryChars) — the
// FAIL-on-unfixed measurement depends on that. The literal is the migration's
// number; TestArtifactSummaryCapMatchesTheMigration keeps the handler's constant
// equal to it.
const (
	historyRowCap = 4096
	// historyRowMeasuredOverflow is the artifact_summary length of the
	// prepare_context completion on aihub#383 (evt_t07fUne2), the one that went
	// missing. Measured, not chosen.
	historyRowMeasuredOverflow = 4243
)

// stepOutcome is the projection both sides of the invariant are compared on.
// Summary is compared for completed outcomes only: step_failed events do not
// carry artifact_summary (handleUpdateStep adds it to the payload for
// step_completed alone), so on that side there is nothing to compare against.
type stepOutcome struct {
	Step    string
	Status  string
	Summary string // summaryDigest of the artifact_summary; "" for failed outcomes and for completed ones with no summary
}

// summaryDigest keeps the comparison exact (length AND content) while keeping
// a failure message readable: "<characters>:<sha256 prefix>" instead of 4,096
// characters of fixture printed twice.
func summaryDigest(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%d:%x", utf8.RuneCountInString(s), sum[:6])
}

// readRecordedOutcomes returns the step_completed / step_failed events for ONE
// run attempt, in insertion order, as outcomes.
func readRecordedOutcomes(t *testing.T, pool *pgxpool.Pool, runAttemptID string) []stepOutcome {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT event_type, COALESCE(payload->>'step',''), COALESCE(payload->>'artifact_summary','')
		FROM agent_events
		WHERE run_attempt_id = $1 AND event_type IN ('step_completed','step_failed')
		ORDER BY created_at, id`, runAttemptID)
	require.NoError(t, err)
	defer rows.Close()
	out := []stepOutcome{}
	for rows.Next() {
		var et, step, summary string
		require.NoError(t, rows.Scan(&et, &step, &summary))
		o := stepOutcome{Step: step, Status: "completed"}
		if et == "step_failed" {
			o.Status = "failed"
		} else {
			o.Summary = summaryDigest(summary)
		}
		out = append(out, o)
	}
	require.NoError(t, rows.Err())
	return out
}

// readHistoryViaGet reads completed_steps the way a resuming agent does — through
// handleGetStep, not the table — and projects it onto outcomes. Every entry must
// belong to runAttemptID and the response must not claim truncation.
func readHistoryViaGet(t *testing.T, pool *pgxpool.Pool, wiID, runAttemptID string, uc *UserContext) []stepOutcome {
	t.Helper()
	c, rec := newStepGetRequest(t, wiID, uc)
	require.NoError(t, handleGetStep(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, "GET step body: %s", rec.Body.String())
	var st StepState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.False(t, st.CompletedStepsTruncated, "a walk this short must never be reported truncated")
	out := []stepOutcome{}
	for _, cs := range st.CompletedSteps {
		require.NotNil(t, cs.RunAttemptID, "history row for %s carries no run_attempt_id", cs.StepID)
		require.Equal(t, runAttemptID, *cs.RunAttemptID, "history row for %s is filed under another attempt", cs.StepID)
		o := stepOutcome{Step: cs.StepID, Status: cs.Status}
		if cs.Status == "completed" && cs.ArtifactSummary != nil {
			o.Summary = summaryDigest(*cs.ArtifactSummary)
		}
		out = append(out, o)
	}
	return out
}

// requireHistoryMatchesEvents is the aihub#390 invariant: for one run attempt,
// the terminal step events and pf_get_step's completed_steps are the same list.
// The failure message names what the history is missing, because that is the
// shape the defect took — never an extra row, always a missing one.
func requireHistoryMatchesEvents(t *testing.T, pool *pgxpool.Pool, wiID, runAttemptID string, uc *UserContext) {
	t.Helper()
	events := readRecordedOutcomes(t, pool, runAttemptID)
	history := readHistoryViaGet(t, pool, wiID, runAttemptID, uc)

	seen := map[stepOutcome]int{}
	for _, h := range history {
		seen[h]++
	}
	var missing []string
	for _, e := range events {
		if seen[e] > 0 {
			seen[e]--
			continue
		}
		missing = append(missing, fmt.Sprintf("%s(%s, summary %s)", e.Step, e.Status, e.Summary))
	}
	require.Equal(t, events, history,
		"aihub#390 invariant violated for attempt %s: completed_steps must equal the step_completed/step_failed "+
			"events recorded for the same attempt (%d events, %d history rows); missing from completed_steps: %v",
		runAttemptID, len(events), len(history), missing)
}

// requireNothingCommitted pins the "nothing was recorded" half of a refusal:
// the state row, the version and the event count are exactly what they were.
func requireNothingCommitted(t *testing.T, pool *pgxpool.Pool, wiID, runAttemptID string, before stepStateRow, eventsBefore int) {
	t.Helper()
	after := readStepState(t, pool, wiID)
	require.Equal(t, before, after, "a refused request must leave wi_step_state untouched")
	require.Equal(t, eventsBefore, len(readRecordedOutcomes(t, pool, runAttemptID)),
		"a refused request must emit no step_completed/step_failed event")
}

func historyRowSummary(n int) string { return strings.Repeat("字", n) }

// TestHandleUpdateStep_EveryRecordedStepOutcomeHasAHistoryRow walks
// fix_bug.aihub.md's seven steps exactly as the measured run did and holds the
// invariant after every request.
func TestHandleUpdateStep_EveryRecordedStepOutcomeHasAHistoryRow(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	att := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := stepWriter(uid, project)

	graph := []string{"prepare_context", "code_change", "code_review", "review_fix", "test", "commit_and_pr", "await_ci"}
	sa := map[string]string{}
	for _, s := range graph {
		sa[s] = domain.NewID("sa")
	}

	// Step 1 starts BARE — no step_attempt_id — which is how pf-execute's loop
	// starts the first step (engine.native.md); every later start is fused.
	code, _ := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": graph[0],
	})
	require.Equal(t, http.StatusOK, code)
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// The measured request: prepare_context completes with a summary 147
	// characters over the cap, fused into code_change.
	before, eventsBefore := readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": graph[0], "step_attempt_id": sa[graph[0]],
		"artifact_summary": historyRowSummary(historyRowMeasuredOverflow),
		"next_step":        graph[1], "next_step_attempt_id": sa[graph[1]],
	})
	// The invariant is checked BEFORE the status code on purpose: on the
	// unfixed tree this request answers 200, and the finding is that the
	// history is now one step short of the events — not that 200 was the
	// wrong code.
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusRequestEntityTooLarge, code,
		"an over-cap artifact_summary must be refused, not recorded without its history row; body: %v", body)
	assert.Equal(t, string(domain.ErrPayloadTooLarge), body["code"])
	assert.Contains(t, body["message"], fmt.Sprint(historyRowMeasuredOverflow), "the refusal must name the actual length")
	assert.Contains(t, body["message"], fmt.Sprint(historyRowCap), "the refusal must name the cap")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	st := readStepState(t, pool, wi.ID)
	require.Equal(t, "in_progress", st.Status, "the refused step must still be in progress")
	require.Equal(t, graph[0], *st.CurrentStep, "the refused step must still be current — the successor must NOT have started")

	// The agent shortens the summary to exactly the cap and resends.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": graph[0], "step_attempt_id": sa[graph[0]],
		"artifact_summary": historyRowSummary(historyRowCap),
		"next_step":        graph[1], "next_step_attempt_id": sa[graph[1]],
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, graph[1], body["next_step"], "the fused advance must be honoured")
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// code_change -> code_review -> review_fix, fused, ordinary summaries.
	for i := 1; i <= 3; i++ {
		code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
			"attempt_id": att, "status": "completed", "step": graph[i], "step_attempt_id": sa[graph[i]],
			"artifact_summary": "summary for " + graph[i],
			"next_step":        graph[i+1], "next_step_attempt_id": sa[graph[i+1]],
		})
		require.Equal(t, http.StatusOK, code, "%s: body: %v", graph[i], body)
		requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	}

	// test FAILS once, first with an over-cap summary (same refusal on the failed
	// branch), then for real; the retry is a fresh step attempt. "retries
	// included" is part of completed_steps' contract, so the failed row must be
	// in the history alongside the later completion.
	before, eventsBefore = readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "test", "step_attempt_id": sa["test"],
		"error_type": "test_fail", "artifact_summary": historyRowSummary(historyRowMeasuredOverflow),
	})
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	require.Equal(t, http.StatusRequestEntityTooLarge, code, "the failed branch shares the cap; body: %v", body)
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)

	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "test", "step_attempt_id": sa["test"],
		"error_type": "test_fail",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	saTestRetry := domain.NewID("sa")
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "in_progress", "step": "test", "step_attempt_id": saTestRetry,
	})
	require.Equal(t, http.StatusOK, code, "restarting a failed step: body: %v", body)
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "test", "step_attempt_id": saTestRetry,
		"artifact_summary": "tests green on retry",
		"next_step":        "commit_and_pr", "next_step_attempt_id": sa["commit_and_pr"],
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// commit_and_pr -> await_ci (fused), then await_ci completes with no successor.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "commit_and_pr", "step_attempt_id": sa["commit_and_pr"],
		"artifact_summary": "pushed", "next_step": "await_ci", "next_step_attempt_id": sa["await_ci"],
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "await_ci", "step_attempt_id": sa["await_ci"],
		"artifact_summary": "green",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)

	// End state, stated absolutely rather than relatively: seven completions and
	// one failure, every step of the graph completed exactly once, and the first
	// step's row carrying the summary that was actually accepted.
	history := readHistoryViaGet(t, pool, wi.ID, att, uc)
	require.Len(t, history, len(graph)+1, "7 completed + 1 failed: %+v", history)
	completedSteps := map[string]int{}
	for _, h := range history {
		if h.Status == "completed" {
			completedSteps[h.Step]++
		}
	}
	for _, s := range graph {
		assert.Equal(t, 1, completedSteps[s], "step %s must appear as completed exactly once", s)
	}
	assert.Equal(t, stepOutcome{Step: "prepare_context", Status: "completed", Summary: summaryDigest(historyRowSummary(historyRowCap))}, history[0])
	assert.Equal(t, stepOutcome{Step: "test", Status: "failed"}, history[4])
}

// TestHandleUpdateStep_ArtifactSummaryAtTheCapIsRecordedAndOverItIsRefused pins
// the boundary in characters, on both branches that persist the field.
func TestHandleUpdateStep_ArtifactSummaryAtTheCapIsRecordedAndOverItIsRefused(t *testing.T) {
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
	rowSummary := func(sa string) (string, int) {
		t.Helper()
		var summary string
		var pgLen int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT artifact_summary, length(artifact_summary) FROM wi_step_completions WHERE step_attempt_id = $1`, sa,
		).Scan(&summary, &pgLen), "no history row for step attempt %s", sa)
		return summary, pgLen
	}

	// Exactly at the cap, in multi-byte characters: 4,096 characters is 12,288
	// bytes. Postgres' length() counts characters and accepts this row; a
	// handler that counted bytes would refuse it, and this is the arm that
	// notices.
	atCap := historyRowSummary(historyRowCap)
	require.Equal(t, 3*historyRowCap, len(atCap), "the fixture must be multi-byte for the arm to mean anything")
	sa1 := domain.NewID("sa")
	start("implement", sa1)
	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": sa1, "artifact_summary": atCap,
	})
	require.Equal(t, http.StatusOK, code, "a summary of exactly %d CHARACTERS must be accepted; body: %v", historyRowCap, body)
	got, pgLen := rowSummary(sa1)
	assert.Equal(t, atCap, got)
	assert.Equal(t, historyRowCap, pgLen)

	// One over, completed branch: refused before anything is written.
	sa2 := domain.NewID("sa")
	start("verify", sa2)
	before, eventsBefore := readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "verify", "step_attempt_id": sa2,
		"artifact_summary": historyRowSummary(historyRowCap + 1),
	})
	require.Equal(t, http.StatusRequestEntityTooLarge, code,
		"%d characters is over the cap and must be refused, not answered 200 with no history row; body: %v", historyRowCap+1, body)
	assert.Equal(t, string(domain.ErrPayloadTooLarge), body["code"])
	assert.Contains(t, body["message"], fmt.Sprint(historyRowCap+1))
	assert.Contains(t, body["message"], fmt.Sprint(historyRowCap))
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_step_completions WHERE step_attempt_id = $1`, sa2).Scan(&n))
	assert.Equal(t, 0, n, "a refused completion must leave no history row")
	// A heartbeat is selected by its flag, may carry status="completed", and
	// must not be caught by the cap check, which sits after the heartbeat
	// return: it is answered heartbeat_ok and commits nothing, as before.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "heartbeat": true, "status": "completed", "step": "verify", "step_attempt_id": sa2,
		"artifact_summary": historyRowSummary(historyRowCap + 1),
	})
	require.Equal(t, http.StatusOK, code, "a heartbeat carrying an oversize summary must stay heartbeat_ok; body: %v", body)
	assert.Equal(t, "heartbeat_ok", body["status"])
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	// ...and the same step then completes at the cap.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "verify", "step_attempt_id": sa2, "artifact_summary": atCap,
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	_, pgLen = rowSummary(sa2)
	assert.Equal(t, historyRowCap, pgLen)

	// One over, failed branch: the same refusal; at the cap, the failed row
	// carries the summary.
	sa3 := domain.NewID("sa")
	start("ship", sa3)
	before, eventsBefore = readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "ship", "step_attempt_id": sa3,
		"error_type": "ship_fail", "artifact_summary": historyRowSummary(historyRowCap + 1),
	})
	require.Equal(t, http.StatusRequestEntityTooLarge, code, "failed branch, over the cap; body: %v", body)
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_step_completions WHERE step_attempt_id = $1`, sa3).Scan(&n))
	assert.Equal(t, 0, n, "a refused failure must leave no history row either")
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "ship", "step_attempt_id": sa3,
		"error_type": "ship_fail", "artifact_summary": atCap,
	})
	require.Equal(t, http.StatusOK, code, "failed branch, at the cap; body: %v", body)
	got, pgLen = rowSummary(sa3)
	assert.Equal(t, atCap, got)
	assert.Equal(t, historyRowCap, pgLen)

	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
}

// TestHandleUpdateStep_DuplicateStepAttemptIsAConflictNotASilentDrop: the
// savepoint swallowed every INSERT error, and the CHECK is only the one that
// was measured. The unique index on step_attempt_id is the other constraint on
// this table, and a fix that merely pre-validates the length leaves it
// answering 200 with no row written and the state machine advanced.
func TestHandleUpdateStep_DuplicateStepAttemptIsAConflictNotASilentDrop(t *testing.T) {
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
	historyRows := func() int {
		t.Helper()
		var n int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT count(*) FROM wi_step_completions WHERE work_item_id = $1`, wi.ID).Scan(&n))
		return n
	}

	sa1 := domain.NewID("sa")
	start("implement", sa1)
	code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "implement", "step_attempt_id": sa1, "artifact_summary": "first",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 1, historyRows())

	// verify is completed under implement's step_attempt_id.
	sa2 := domain.NewID("sa")
	start("verify", sa2)
	before, eventsBefore := readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "verify", "step_attempt_id": sa1, "artifact_summary": "second, same id",
	})
	require.Equal(t, http.StatusConflict, code,
		"a step_attempt_id that already has a history row must be a conflict, not a 200 that writes nothing; body: %v", body)
	assert.Equal(t, string(domain.ErrConflictDuplicate), body["code"])
	assert.Contains(t, body["message"], sa1)
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, 1, historyRows(), "the duplicate must not have replaced or added a row")
	var firstSummary, firstStep string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT step_id, artifact_summary FROM wi_step_completions WHERE step_attempt_id = $1`, sa1).Scan(&firstStep, &firstSummary))
	assert.Equal(t, "implement", firstStep)
	assert.Equal(t, "first", firstSummary, "the existing row must be untouched")
	// The correct id goes through.
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "verify", "step_attempt_id": sa2, "artifact_summary": "second",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 2, historyRows())

	// Same on the failed branch.
	sa3 := domain.NewID("sa")
	start("ship", sa3)
	before, eventsBefore = readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "ship", "step_attempt_id": sa2, "error_type": "ship_fail",
	})
	require.Equal(t, http.StatusConflict, code, "failed branch, duplicate id; body: %v", body)
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, 2, historyRows())
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "failed", "step": "ship", "step_attempt_id": sa3, "error_type": "ship_fail",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 3, historyRows())

	// The third constraint on the table is the run_attempt_id foreign key. The
	// credential check is skipped when attempt_id is empty, so a writer's
	// completion with no attempt_id used to reach the INSERT, fail the FK and be
	// swallowed — 200, state advanced, no row. It must now be refused with the
	// field named and nothing committed.
	sa4 := domain.NewID("sa")
	start("tag", sa4)
	before, eventsBefore = readStepState(t, pool, wi.ID), len(readRecordedOutcomes(t, pool, att))
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"status": "completed", "step": "tag", "step_attempt_id": sa4, "artifact_summary": "no attempt",
	})
	require.Equal(t, http.StatusBadRequest, code, "a completion with no attempt_id cannot file a history row and must say so; body: %v", body)
	assert.Equal(t, string(domain.ErrBadRequest), body["code"])
	assert.Contains(t, body["message"], "attempt_id")
	requireNothingCommitted(t, pool, wi.ID, att, before, eventsBefore)
	require.Equal(t, 3, historyRows())
	code, body = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": att, "status": "completed", "step": "tag", "step_attempt_id": sa4, "artifact_summary": "tagged",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Equal(t, 4, historyRows())

	requireHistoryMatchesEvents(t, pool, wi.ID, att, uc)
}
