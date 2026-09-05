package server

// Integration tests for aihub#290 B1: PATCH /v1/work_items/:id/step accepts
// next_step, completing one step and starting its successor in a single
// transaction.
//
// A step-graph walk brackets every step with a "completed" call immediately
// followed by an "in_progress" call for the next step, and the second one reads
// nothing out of the first one's response — 350 measured adjacent pairs, 0.358%
// of billed input, all of it the cost of the round-trip rather than of the
// few-hundred-byte confirmation.
//
// The governing property is not "the fused call works" but "the fused call is
// indistinguishable from the two calls it replaces", which is what
// TestHandleUpdateStep_FusedEqualsTwoCalls asserts directly. Gated by
// AIHUB_TEST_DB, same as routes_step_test.go, whose seed helpers these reuse.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:15440/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleUpdateStep_(NextStep|FusedEquals)' -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// stepStateRow is the part of wi_step_state these tests compare on.
type stepStateRow struct {
	CurrentStep    *string
	Status         string
	CurrentAttempt *string
	Version        int64
}

func readStepState(t *testing.T, pool *pgxpool.Pool, wiID string) stepStateRow {
	t.Helper()
	var s stepStateRow
	err := pool.QueryRow(context.Background(), `
		SELECT current_step, current_step_status, current_step_attempt, version
		FROM wi_step_state WHERE work_item_id = $1`, wiID,
	).Scan(&s.CurrentStep, &s.Status, &s.CurrentAttempt, &s.Version)
	require.NoError(t, err, "wi_step_state row for %s", wiID)
	return s
}

// readStepEvents returns (event_type, payload->>'step') in insertion order.
func readStepEvents(t *testing.T, pool *pgxpool.Pool, wiID string) [][2]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT event_type, COALESCE(payload->>'step','')
		FROM agent_events
		WHERE work_item_id = $1 AND event_type LIKE 'step_%'
		ORDER BY created_at, id`, wiID)
	require.NoError(t, err)
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var et, step string
		require.NoError(t, rows.Scan(&et, &step))
		out = append(out, [2]string{et, step})
	}
	require.NoError(t, rows.Err())
	return out
}

// readStepCompletions returns (step_id, status) per completion row, in insertion
// order. Deliberately NOT including step_attempt_id: the two arms of the
// differential test must use different attempt ids (the unique index on that
// column is global), so the ids are the one thing that legitimately differs.
func readStepCompletions(t *testing.T, pool *pgxpool.Pool, wiID string) [][2]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT step_id, status FROM wi_step_completions
		WHERE work_item_id = $1 ORDER BY completed_at, id`, wiID)
	require.NoError(t, err)
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var step, status string
		require.NoError(t, rows.Scan(&step, &status))
		out = append(out, [2]string{step, status})
	}
	require.NoError(t, rows.Err())
	return out
}

// patchStep issues one PATCH against handleUpdateStep and returns the recorder.
func patchStep(t *testing.T, pool *pgxpool.Pool, wiID string, uc *UserContext, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	c, rec := newStepUpdateRequest(t, wiID, string(raw), uc)
	require.NoError(t, handleUpdateStep(pool)(c))
	var decoded map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	}
	return rec.Code, decoded
}

func fusedTestUser(uid, project string) *UserContext {
	return &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "writer"},
	}
}

// seedFusedArm seeds an independent user/project/wi/attempt under an explicit
// short name.
//
// The shared seedStepTestUserAndProject derives its names from t.Name() and
// truncates to 37 chars, so two arms of one differential test collapse onto the
// same project — and since seedStepTestWI deletes the project's work items
// first, the second arm silently destroys the first. Explicit names, not
// subtests, are what actually keep the two arms apart here.
func seedFusedArm(t *testing.T, pool *pgxpool.Pool, arm string) (*domain.WorkItem, string, *UserContext) {
	t.Helper()
	ctx := context.Background()
	uid := "u_fusedarm_" + arm
	proj := "p_fusedarm_" + arm

	_, err := pool.Exec(ctx,
		`INSERT INTO users(id,email,display_name) VALUES($1,$1||'@test.local',$1) ON CONFLICT (id) DO NOTHING`, uid)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`, proj, uid)
	require.NoError(t, err)

	// Same child-to-parent cleanup as seedStepTestWI: a leftover wi from a prior
	// run would be 100% goal-similar and fail the dedup check on create.
	for _, q := range []string{
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		_, err = pool.Exec(ctx, q, proj)
		require.NoError(t, err)
	}

	wi, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
		Project: proj,
		Goal:    "seed wi for fused arm " + arm,
		Source:  "human",
	}, uid, uid, nil, "")
	require.Nil(t, aerr)

	return wi, seedStepTestAttempt(t, pool, wi.ID, uid), fusedTestUser(uid, proj)
}

// TestHandleUpdateStep_NextStepFusesCompleteAndStart is the core behaviour: one
// request leaves the successor in_progress, files the completion row under the
// step that actually completed, and emits BOTH timeline events.
func TestHandleUpdateStep_NextStepFusesCompleteAndStart(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := fusedTestUser(uid, project)

	saImplement := domain.NewID("sa")
	saVerify := domain.NewID("sa")

	code, _ := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "in_progress",
		"step": "implement", "step_attempt_id": saImplement,
	})
	require.Equal(t, http.StatusOK, code)

	// The fused call: finish "implement", start "verify".
	code, resp := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "completed",
		"step": "implement", "step_attempt_id": saImplement,
		"artifact_summary":     "wrote the thing",
		"next_step":            "verify",
		"next_step_attempt_id": saVerify,
	})
	require.Equal(t, http.StatusOK, code)

	// The response names the successor: a fused caller makes no follow-up call,
	// so anything it cannot infer from its own request has to come back here.
	assert.Equal(t, "verify", resp["next_step"])
	assert.Equal(t, "in_progress", resp["next_step_status"])

	st := readStepState(t, pool, wi.ID)
	require.NotNil(t, st.CurrentStep)
	assert.Equal(t, "verify", *st.CurrentStep, "successor must be the current step")
	assert.Equal(t, "in_progress", st.Status)
	require.NotNil(t, st.CurrentAttempt)
	assert.Equal(t, saVerify, *st.CurrentAttempt,
		"current_step_attempt must be the SUCCESSOR's attempt id, not the completed step's")
	assert.Equal(t, int64(3), st.Version,
		"one fused call must perform BOTH version bumps (INSERT=1, completion=2, successor start=3)")

	// The completion row belongs to the step that completed. Conflating the two
	// attempt ids would file this history row under "verify", which has not
	// finished anything.
	var completedStep, completedSA string
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT step_id, step_attempt_id FROM wi_step_completions
		WHERE work_item_id = $1 AND status = 'completed'`, wi.ID,
	).Scan(&completedStep, &completedSA))
	assert.Equal(t, "implement", completedStep)
	assert.Equal(t, saImplement, completedSA)

	assert.Equal(t, [][2]string{
		{"step_started", "implement"},
		{"step_completed", "implement"},
		{"step_started", "verify"},
	}, readStepEvents(t, pool, wi.ID),
		"a fused call must leave the same timeline two calls would")
}

// TestHandleUpdateStep_FusedEqualsTwoCalls is the property that matters most:
// fusing is a transport optimisation, so the observable end state must be
// identical to the sequence it replaces. Two work items, same step walk, one
// driven with three requests and one with two.
func TestHandleUpdateStep_FusedEqualsTwoCalls(t *testing.T) {
	pool := setupStepTestDB(t)
	wiFused, attFused, uc := seedFusedArm(t, pool, "a")
	wiSplit, attSplit, ucB := seedFusedArm(t, pool, "b")

	// Each arm gets its OWN step-attempt ids. Sharing them looks harmless and is
	// not: wi_step_completions has a GLOBAL unique index on step_attempt_id
	// (0005_step_state.sql), and the completion insert is deliberately wrapped in
	// a savepoint that swallows the violation. Two arms sharing an id therefore
	// produce ONE completion row between them, the second arm's silently
	// discarded — and a comparison that skipped that table would still pass,
	// while asserting nothing about the very row this fusion could file under the
	// wrong step.
	saFused1, saFused2 := domain.NewID("sa"), domain.NewID("sa")
	saSplit1, saSplit2 := domain.NewID("sa"), domain.NewID("sa")

	// Arm A — fused: start, then complete+advance in one call.
	code, _ := patchStep(t, pool, wiFused.ID, uc, map[string]any{
		"attempt_id": attFused, "status": "in_progress", "step": "code_change", "step_attempt_id": saFused1,
	})
	require.Equal(t, http.StatusOK, code)
	code, _ = patchStep(t, pool, wiFused.ID, uc, map[string]any{
		"attempt_id": attFused, "status": "completed", "step": "code_change", "step_attempt_id": saFused1,
		"artifact_summary": "same summary", "next_step": "commit_and_pr", "next_step_attempt_id": saFused2,
	})
	require.Equal(t, http.StatusOK, code)

	// Arm B — the two calls it replaces.
	code, _ = patchStep(t, pool, wiSplit.ID, ucB, map[string]any{
		"attempt_id": attSplit, "status": "in_progress", "step": "code_change", "step_attempt_id": saSplit1,
	})
	require.Equal(t, http.StatusOK, code)
	code, _ = patchStep(t, pool, wiSplit.ID, ucB, map[string]any{
		"attempt_id": attSplit, "status": "completed", "step": "code_change", "step_attempt_id": saSplit1,
		"artifact_summary": "same summary",
	})
	require.Equal(t, http.StatusOK, code)
	code, _ = patchStep(t, pool, wiSplit.ID, ucB, map[string]any{
		"attempt_id": attSplit, "status": "in_progress", "step": "commit_and_pr", "step_attempt_id": saSplit2,
	})
	require.Equal(t, http.StatusOK, code)

	// Compare the state rows with each arm's own successor-attempt id substituted
	// for a fixed marker: those ids differ by construction (see above), and they
	// are the ONLY field allowed to differ. Everything else — which step is
	// current, its status, and the version, which must have been incremented
	// twice on both arms — has to match exactly.
	fusedState := readStepState(t, pool, wiFused.ID)
	splitState := readStepState(t, pool, wiSplit.ID)
	require.NotNil(t, fusedState.CurrentAttempt)
	require.NotNil(t, splitState.CurrentAttempt)
	assert.Equal(t, saFused2, *fusedState.CurrentAttempt,
		"the fused arm must record the successor's attempt id, not the completed step's")
	assert.Equal(t, saSplit2, *splitState.CurrentAttempt)
	// Pin the version ABSOLUTELY, on both arms, before comparing them.
	//
	// A relative comparison is blind to anything the two arms share, and they
	// share startStep: break its `version = wi_step_state.version + 1` into
	// `= wi_step_state.version` and both arms degrade together, so the comparison
	// below still passes. Nothing else in this package asserts on version at all.
	// 3 = the INSERT (1), the completion (+1), the successor's start (+1).
	assert.Equal(t, int64(3), fusedState.Version,
		"fused arm: INSERT + completion + successor start must each bump version")
	assert.Equal(t, int64(3), splitState.Version,
		"split arm must reach the same version through three separate calls")

	marker := "<successor-attempt>"
	fusedState.CurrentAttempt = &marker
	splitState.CurrentAttempt = &marker

	assert.Equal(t, splitState, fusedState,
		"fused and split arms must leave identical wi_step_state")
	assert.Equal(t, readStepEvents(t, pool, wiSplit.ID), readStepEvents(t, pool, wiFused.ID),
		"fused and split arms must leave identical step timelines")

	// The completion history is the table the fusion could most plausibly get
	// wrong — it carries two attempt ids in one request now — so compare it too,
	// normalised to (step_id, status) since the ids differ by construction.
	assert.Equal(t, readStepCompletions(t, pool, wiSplit.ID), readStepCompletions(t, pool, wiFused.ID),
		"fused and split arms must file identical wi_step_completions rows")
	assert.Equal(t, [][2]string{{"code_change", "completed"}}, readStepCompletions(t, pool, wiFused.ID),
		"exactly one completion row, for the step that completed — not for its successor")
}

// TestHandleUpdateStep_NextStepRejectedUnlessCompleted: the parameter is
// refused where it cannot be honoured rather than ignored. Silently dropping a
// declared parameter is the defect aihub#290 exists to remove; reintroducing it
// on the new parameter would be a poor joke.
func TestHandleUpdateStep_NextStepRejectedUnlessCompleted(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := fusedTestUser(uid, project)

	for _, status := range []string{"in_progress", "failed"} {
		t.Run(status, func(t *testing.T) {
			code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
				"attempt_id": attemptID, "status": status,
				"step": "implement", "next_step": "verify",
			})
			assert.Equal(t, http.StatusBadRequest, code,
				"next_step with status=%q must be rejected, not ignored", status)
			assert.Contains(t, string(mustJSON(t, body)), "next_step")
		})
	}

	// A heartbeat returns early and only bumps step_started_at, so it must reject
	// next_step rather than answering "heartbeat_ok" while quietly not starting
	// the successor.
	//
	// The second case is the one a status-only guard misses, and it is why this
	// is a table rather than a single call: `heartbeat` is selected by its own
	// flag, NOT by the status, so a heartbeat can carry status="completed" and
	// sail straight past a `status != "completed"` check into the early return.
	// A test that only sends heartbeats without a status proves nothing about it.
	for _, hb := range []map[string]any{
		{"attempt_id": attemptID, "heartbeat": true, "next_step": "verify"},
		{"attempt_id": attemptID, "heartbeat": true, "status": "completed", "step": "implement", "next_step": "verify"},
	} {
		t.Run("heartbeat status="+fmt.Sprint(hb["status"]), func(t *testing.T) {
			code, body := patchStep(t, pool, wi.ID, uc, hb)
			assert.Equal(t, http.StatusBadRequest, code,
				"a heartbeat carrying next_step must be rejected, not answered heartbeat_ok")
			assert.NotContains(t, string(mustJSON(t, body)), "heartbeat_ok")
		})
	}

	// next_step_attempt_id names the attempt of the step being STARTED. With no
	// next_step there is no such step, so nothing would ever read it — accepting
	// it would be the expected_version defect on the parameter that replaced it.
	t.Run("next_step_attempt_id without next_step", func(t *testing.T) {
		code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
			"attempt_id": attemptID, "status": "completed", "step": "implement",
			"next_step_attempt_id": "sa_orphan",
		})
		assert.Equal(t, http.StatusBadRequest, code,
			"next_step_attempt_id without next_step must be rejected, not silently ignored")
		assert.Contains(t, string(mustJSON(t, body)), "next_step_attempt_id")
	})

	// An ordinary heartbeat is untouched by the new check.
	t.Run("plain heartbeat still works", func(t *testing.T) {
		code, body := patchStep(t, pool, wi.ID, uc, map[string]any{
			"attempt_id": attemptID, "heartbeat": true,
		})
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "heartbeat_ok", body["status"])
	})

	// And nothing was written: a rejected request must not have half-applied.
	var exists bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM wi_step_state WHERE work_item_id=$1)`, wi.ID).Scan(&exists))
	assert.False(t, exists, "a rejected fused request must not create step state")
}

// TestHandleUpdateStep_FusedRespectsMandatoryRecordGate: the fusion must not
// become a way around the aihub#221 gate. A spec step that cannot complete must
// not start its successor either — otherwise adding next_step would have quietly
// converted a hard gate into a step the walk sails past.
func TestHandleUpdateStep_FusedRespectsMandatoryRecordGate(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := fusedTestUser(uid, project)

	sa := domain.NewID("sa")
	code, _ := patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "in_progress", "step": "spec", "step_attempt_id": sa,
	})
	require.Equal(t, http.StatusOK, code)

	// No methodology.spec artifact recorded -> the completion must fail...
	code, _ = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "completed", "step": "spec", "step_attempt_id": sa,
		"next_step": "plan", "next_step_attempt_id": domain.NewID("sa"),
	})
	require.Equal(t, http.StatusBadRequest, code)

	// ...and "plan" must NOT have been started.
	st := readStepState(t, pool, wi.ID)
	require.NotNil(t, st.CurrentStep)
	assert.Equal(t, "spec", *st.CurrentStep, "the gated step must still be current")
	assert.Equal(t, "in_progress", st.Status, "the gated step must still be in_progress")

	// With the artifact present, the same fused call goes through.
	seedStepTestMemory(t, pool, wi.ID, project, uid, "methodology.spec")
	saPlan := domain.NewID("sa")
	code, _ = patchStep(t, pool, wi.ID, uc, map[string]any{
		"attempt_id": attemptID, "status": "completed", "step": "spec", "step_attempt_id": sa,
		"next_step": "plan", "next_step_attempt_id": saPlan,
	})
	require.Equal(t, http.StatusOK, code)
	st = readStepState(t, pool, wi.ID)
	require.NotNil(t, st.CurrentStep)
	assert.Equal(t, "plan", *st.CurrentStep)
	assert.Equal(t, "in_progress", st.Status)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
