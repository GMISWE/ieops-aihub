package server

// Integration tests for aihub#206 (C1, spec A-1): handleUpdateStep used to
// silently drop artifact_summary / error_type / escalated from the request
// body. These run handleUpdateStep directly against a live DB (gated by
// AIHUB_TEST_DB), following the setUser/UserContext injection pattern from
// router_auth_test.go and the direct-seed pattern from
// internal/domain/memory_latest_test.go.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:15440/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleUpdateStep' -v -count=1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// setupStepTestDB connects to AIHUB_TEST_DB, skipping the test if unset.
// Mirrors internal/domain/memory_latest_test.go's setupLatestTestDB — server
// package tests have no equivalent live-DB helper yet.
func setupStepTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// sanitizeStepTestName mirrors sanitizeTestName in the domain package
// (unexported there, so duplicated here rather than exported cross-package
// for a two-file need).
func sanitizeStepTestName(name string) string {
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, byte(r))
		case r >= 'A' && r <= 'Z':
			out = append(out, byte(r-'A'+'a'))
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 37 {
		out = out[:37]
	}
	return string(out)
}

// seedStepTestUserAndProject creates a real users row + projects row (both
// required by FK constraints on work_items.reporter_user_id and
// agent_events.actor_user_id) and returns (userID, project).
func seedStepTestUserAndProject(t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	uid := "u_" + sanitizeStepTestName(t.Name())
	proj := "p_" + sanitizeStepTestName(t.Name())
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users(id,email,display_name) VALUES($1,$1||'@test.local',$1) ON CONFLICT (id) DO NOTHING`, uid)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`, proj, uid)
	require.NoError(t, err)
	return uid, proj
}

// seedStepTestWI creates a minimal work item via the real CreateWorkItem path.
// CreateWorkItem runs a goal-similarity dedup check against existing
// queued/running/paused/blocked work items in the same project, so a leftover
// wi from a prior run of this same test (same deterministic project name,
// same goal string) would be seen as 100% similar and reject the new create.
// Clear any prior run's work items (and FK-dependent rows) for this project
// first, in child-to-parent order.
func seedStepTestWI(t *testing.T, pool *pgxpool.Pool, project, userID string) *domain.WorkItem {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`, project)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`, project)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`, project)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`, project)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`, project)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM work_items WHERE project=$1`, project)
	require.NoError(t, err)
	wi, aerr := domain.CreateWorkItem(context.Background(), pool, &domain.CreateWorkItemRequest{
		Project: project,
		Goal:    "seed wi for " + t.Name(),
		Source:  "human",
	}, userID, userID)
	require.Nil(t, aerr)
	return wi
}

// seedStepTestAttempt inserts a run_attempts row directly and points
// work_items.current_attempt_id at it, mirroring
// internal/domain/step_pause_stall_test.go's seedRunAttempt (duplicated here
// since it is unexported in the domain package).
func seedStepTestAttempt(t *testing.T, pool *pgxpool.Pool, wiID, userID string) string {
	t.Helper()
	attemptID := domain.NewID("ra")
	_, err := pool.Exec(context.Background(), `
		INSERT INTO run_attempts (id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, actor_display, machine_id, session_secret_hash)
		VALUES ($1, $2, 'running', 1, $3, $4, $4, 'm_test', 'unused_hash')`,
		attemptID, wiID, "idem_"+attemptID, userID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		UPDATE work_items SET status='running', current_attempt_id=$1, current_attempt_epoch=1 WHERE id=$2`,
		attemptID, wiID)
	require.NoError(t, err)
	return attemptID
}

// seedStepTestMemory inserts a minimal active memory row of the given type
// for wiID directly via SQL. The gate is existence-only (never reads
// content), so a minimal row is sufficient.
func seedStepTestMemory(t *testing.T, pool *pgxpool.Pool, wiID, project, userID, memType string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, author_user_id, work_item_id, type, content, status)
		VALUES ($1, $2, $3, $4, $5, 'seed content', 'active')`,
		domain.NewID("mem"), project, userID, wiID, memType)
	require.NoError(t, err)
}

// newStepUpdateRequest builds an authenticated echo.Context for
// PATCH /v1/work_items/:id/step with the given body, using the setUser
// pattern from router_auth_test.go.
func newStepUpdateRequest(t *testing.T, wiID string, body string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/work_items/"+wiID+"/step", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(wiID)
	setUser(c, uc)
	return c, rec
}

// TestHandleUpdateStep_ArtifactSummary is the AC-1 regression test: a
// completed step with artifact_summary must (a) land in the step_completed
// event payload and (b) be written onto the wi_step_completions row —
// previously both were silently dropped since UpdateStepRequest had no field
// for it.
func TestHandleUpdateStep_ArtifactSummary(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "writer"},
	}

	const summary = "ran the migration, all rows backfilled"
	stepAttemptID := domain.NewID("sa")
	body, err := json.Marshal(map[string]any{
		"attempt_id":       attemptID,
		"claim_epoch":      0,
		"status":           "completed",
		"step":             "implement",
		"step_attempt_id":  stepAttemptID,
		"artifact_summary": summary,
	})
	require.NoError(t, err)

	c, rec := newStepUpdateRequest(t, wi.ID, string(body), uc)
	handler := handleUpdateStep(pool)
	herr := handler(c)
	require.NoError(t, herr)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	// (a) wi_step_completions row carries artifact_summary.
	var gotSummary *string
	err = pool.QueryRow(context.Background(),
		`SELECT artifact_summary FROM wi_step_completions WHERE step_attempt_id=$1`, stepAttemptID,
	).Scan(&gotSummary)
	require.NoError(t, err)
	require.NotNil(t, gotSummary)
	assert.Equal(t, summary, *gotSummary)

	// (b) step_completed event payload contains artifact_summary.
	var payloadRaw []byte
	err = pool.QueryRow(context.Background(),
		`SELECT payload FROM agent_events WHERE work_item_id=$1 AND event_type='step_completed' ORDER BY created_at DESC LIMIT 1`,
		wi.ID,
	).Scan(&payloadRaw)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadRaw, &payload))
	assert.Equal(t, summary, payload["artifact_summary"])
}

// TestHandleUpdateStep_EscalatedStall is the AC-3 regression test: a failed
// step with escalated=true must (a) move the work item to status=blocked and
// (b) emit a wi_stalled agent_event — distinct from a dependency block, which
// carries no such event and instead has a wi_dependencies row.
func TestHandleUpdateStep_EscalatedStall(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "writer"},
	}

	stepAttemptID := domain.NewID("sa")
	body, err := json.Marshal(map[string]any{
		"attempt_id":      attemptID,
		"claim_epoch":     0,
		"status":          "failed",
		"step":            "implement",
		"step_attempt_id": stepAttemptID,
		"error_type":      "compile_error",
		"escalated":       true,
	})
	require.NoError(t, err)

	c, rec := newStepUpdateRequest(t, wi.ID, string(body), uc)
	handler := handleUpdateStep(pool)
	herr := handler(c)
	require.NoError(t, herr)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	// (a) work_items.status == 'blocked'.
	var status string
	err = pool.QueryRow(context.Background(), `SELECT status FROM work_items WHERE id=$1`, wi.ID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "blocked", status)

	// (b) a wi_stalled event exists carrying error_type.
	var payloadRaw []byte
	err = pool.QueryRow(context.Background(),
		`SELECT payload FROM agent_events WHERE work_item_id=$1 AND event_type='wi_stalled' ORDER BY created_at DESC LIMIT 1`,
		wi.ID,
	).Scan(&payloadRaw)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadRaw, &payload))
	assert.Equal(t, "compile_error", payload["error_type"])

	// (c) also surfaced in the ready-queue stalled[] segment, with a non-empty
	//     StallReason equal to the error_type (the emitter writes stall_reason,
	//     which the stalled query reads into StalledItem.StallReason).
	rq, aerr := domain.GetReadyQueue(context.Background(), pool, project, 50)
	require.Nil(t, aerr)
	var stalledItem *domain.StalledItem
	for i := range rq.Stalled {
		if rq.Stalled[i].ID == wi.ID {
			stalledItem = &rq.Stalled[i]
		}
	}
	require.NotNil(t, stalledItem, "wi must appear in ready-queue stalled[] segment")
	assert.Equal(t, "compile_error", stalledItem.StallReason, "StallReason must be populated from the wi_stalled event's stall_reason")

	// wi_step_completions.error_type also persisted.
	var gotErrType *string
	err = pool.QueryRow(context.Background(),
		`SELECT error_type FROM wi_step_completions WHERE step_attempt_id=$1`, stepAttemptID,
	).Scan(&gotErrType)
	require.NoError(t, err)
	require.NotNil(t, gotErrType)
	assert.Equal(t, "compile_error", *gotErrType)
}

// TestHandleUpdateStep_MandatoryRecordGate is the aihub#221 regression test:
// spec/plan steps must not complete without their corresponding
// methodology.spec/methodology.plan artifact already recorded. Other steps
// (e.g. code_change) are unaffected.
func TestHandleUpdateStep_MandatoryRecordGate(t *testing.T) {
	pool := setupStepTestDB(t)

	// setup returns a fresh wi + attempt + authenticated UserContext, keyed by
	// the subtest name so each case is isolated (no cross-subtest state leak).
	setup := func(t *testing.T) (*domain.WorkItem, string, *UserContext, string) {
		t.Helper()
		uid, project := seedStepTestUserAndProject(t, pool)
		wi := seedStepTestWI(t, pool, project, uid)
		attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
		uc := &UserContext{
			UserID:       uid,
			DisplayName:  uid,
			Role:         "writer",
			ProjectRoles: map[string]string{project: "writer"},
		}
		return wi, attemptID, uc, uid
	}

	complete := func(t *testing.T, wiID, attemptID string, uc *UserContext, step string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"attempt_id":  attemptID,
			"claim_epoch": 0,
			"status":      "completed",
			"step":        step,
		})
		require.NoError(t, err)
		c, rec := newStepUpdateRequest(t, wiID, string(body), uc)
		herr := handleUpdateStep(pool)(c)
		require.NoError(t, herr)
		return rec
	}

	t.Run("spec step rejected without methodology.spec", func(t *testing.T) {
		wi, attemptID, uc, _ := setup(t)
		rec := complete(t, wi.ID, attemptID, uc, "spec")
		require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		assert.Contains(t, rec.Body.String(), "methodology.spec")
	})

	t.Run("spec step succeeds with methodology.spec recorded", func(t *testing.T) {
		wi, attemptID, uc, uid := setup(t)
		seedStepTestMemory(t, pool, wi.ID, wi.Project, uid, "methodology.spec")
		rec := complete(t, wi.ID, attemptID, uc, "spec")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("plan step rejected without methodology.plan", func(t *testing.T) {
		wi, attemptID, uc, _ := setup(t)
		rec := complete(t, wi.ID, attemptID, uc, "plan")
		require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		assert.Contains(t, rec.Body.String(), "methodology.plan")
	})

	t.Run("plan step succeeds with methodology.plan recorded", func(t *testing.T) {
		wi, attemptID, uc, uid := setup(t)
		seedStepTestMemory(t, pool, wi.ID, wi.Project, uid, "methodology.plan")
		rec := complete(t, wi.ID, attemptID, uc, "plan")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("non-gated step succeeds with no artifact", func(t *testing.T) {
		wi, attemptID, uc, _ := setup(t)
		rec := complete(t, wi.ID, attemptID, uc, "code_change")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})
}
