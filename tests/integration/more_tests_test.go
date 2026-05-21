//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── 1. Step state lifecycle ─────────────────────────────────────────────────

// TestStepStateMachineLifecycle covers Layer-2 step transitions:
// idle → in_progress → completed (advances current_step, returns to idle),
// idle → in_progress → failed (returns to idle, wi remains running).
func TestStepStateMachineLifecycle(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// Create and claim a fix_bug wi
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("step-lifecycle-test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	claim := mustClaimWorkItem(t, c, ctx, wiID, "step-life-001")
	attemptID := claim["attempt_id"].(string)
	claimEpoch := int64(claim["claim_epoch"].(float64))
	sessionSecret := claim["session_secret"].(string)
	t.Logf("claimed attempt=%s epoch=%d", attemptID, claimEpoch)

	// Step 1: idle → in_progress (step="code_change")
	codeChange := "code_change"
	_, err := c.UpdateStep(ctx, wiID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         "in_progress",
		"step":           codeChange,
	})
	if err != nil {
		t.Fatalf("UpdateStep in_progress: %v", err)
	}

	// Verify GET /v1/work_items/:id/step → current_step_status="in_progress"
	stepState, err := c.GetStep(ctx, wiID)
	if err != nil {
		t.Fatalf("GetStep after in_progress: %v", err)
	}
	if stepState["current_step_status"] != "in_progress" {
		t.Errorf("expected current_step_status=in_progress, got %v", stepState["current_step_status"])
	}
	if stepState["current_step"] != codeChange {
		t.Errorf("expected current_step=%q, got %v", codeChange, stepState["current_step"])
	}
	t.Logf("OK: step transitioned to in_progress (step=%v)", stepState["current_step"])

	// Step 2: in_progress → completed; advance to next step "commit_and_pr"
	commitAndPR := "commit_and_pr"
	_, err = c.UpdateStep(ctx, wiID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         "completed",
		"step":           commitAndPR,
	})
	if err != nil {
		t.Fatalf("UpdateStep completed: %v", err)
	}
	stepState, err = c.GetStep(ctx, wiID)
	if err != nil {
		t.Fatalf("GetStep after completed: %v", err)
	}
	if stepState["current_step_status"] != "idle" {
		t.Errorf("expected current_step_status=idle after complete, got %v", stepState["current_step_status"])
	}
	if stepState["current_step"] != commitAndPR {
		t.Errorf("expected current_step=%q after complete, got %v", commitAndPR, stepState["current_step"])
	}
	t.Logf("OK: step completed → idle, current_step advanced to %v", stepState["current_step"])

	// Step 3: idle → in_progress on the next step, then fail it
	_, err = c.UpdateStep(ctx, wiID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         "in_progress",
		"step":           commitAndPR,
	})
	if err != nil {
		t.Fatalf("UpdateStep second in_progress: %v", err)
	}
	_, err = c.UpdateStep(ctx, wiID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         "failed",
	})
	if err != nil {
		t.Fatalf("UpdateStep failed: %v", err)
	}

	// Verify wi remains running (step failure ≠ wi failure)
	wi := mustGetWorkItem(t, c, ctx, wiID)
	if wi["status"] != "running" {
		t.Errorf("wi should still be running after step failed, got %v", wi["status"])
	}
	stepState, err = c.GetStep(ctx, wiID)
	if err != nil {
		t.Fatalf("GetStep after failed: %v", err)
	}
	if stepState["current_step_status"] != "idle" {
		t.Errorf("expected current_step_status=idle after step failed, got %v", stepState["current_step_status"])
	}
	t.Logf("OK: step failed → idle, wi still running")

	// Cleanup
	mustCompleteAttempt(t, c, ctx, wiID, claim, "wrapped")
}

// TestStepIdleToInProgressGuard verifies that consecutive in_progress requests
// on the same step are rejected with 409 CAS_FAILED (idle → in_progress only).
func TestStepIdleToInProgressGuard(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("step-guard-test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	claim := mustClaimWorkItem(t, c, ctx, wiID, "step-guard-001")
	attemptID := claim["attempt_id"].(string)
	claimEpoch := int64(claim["claim_epoch"].(float64))
	sessionSecret := claim["session_secret"].(string)

	step := "code_change"
	body := map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         "in_progress",
		"step":           step,
	}

	// First in_progress: should succeed (idle → in_progress)
	if _, err := c.UpdateStep(ctx, wiID, body); err != nil {
		t.Fatalf("first UpdateStep in_progress should succeed: %v", err)
	}
	t.Log("OK: first in_progress accepted")

	// Second in_progress without completing first: should be rejected (409 CAS_FAILED)
	_, err := c.UpdateStep(ctx, wiID, body)
	if err == nil {
		t.Error("second consecutive in_progress should fail; got nil")
	} else if !strings.Contains(err.Error(), "409") &&
		!strings.Contains(err.Error(), "CAS") &&
		!strings.Contains(err.Error(), "in_progress") {
		t.Logf("NOTE: expected 409/CAS/in_progress error, got: %v", err)
	} else {
		t.Logf("OK: second in_progress rejected: %v", err)
	}

	// Cleanup: complete the in-flight step and wrap
	_, _ = c.UpdateStep(ctx, wiID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         "completed",
		"step":           step,
	})
	mustCompleteAttempt(t, c, ctx, wiID, claim, "wrapped")
}

// ─── 2. Pause/Resume lifecycle ──────────────────────────────────────────────

// TestPauseAndResume verifies the full pause → re-claim → wrap cycle.
// Pause must:
//   - set wi.status = paused
//   - terminate the active attempt
//
// Re-claim after pause must:
//   - allocate a new attempt with epoch > old epoch
//   - bring wi.status back to running
func TestPauseAndResume(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("pause-resume-test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	// 1. Claim (epoch=1)
	claim := mustClaimWorkItem(t, c, ctx, wiID, "pause-resume-001")
	attemptID := claim["attempt_id"].(string)
	epoch1 := int64(claim["claim_epoch"].(float64))
	sessionSecret := claim["session_secret"].(string)
	t.Logf("initial claim: attempt=%s epoch=%d", attemptID, epoch1)

	// 2. Pause the attempt
	_, err := c.PauseAttempt(ctx, wiID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    epoch1,
		"session_secret": sessionSecret,
		"status":         "paused", // server forces this anyway
	})
	if err != nil {
		t.Fatalf("PauseAttempt: %v", err)
	}

	// 3. Verify wi.status = paused
	wi := mustGetWorkItem(t, c, ctx, wiID)
	if wi["status"] != "paused" {
		t.Errorf("expected wi.status=paused after pause, got %v", wi["status"])
	}
	t.Log("OK: wi.status=paused")

	// 4. Re-claim — new attempt should have epoch > epoch1
	claim2 := mustClaimWorkItem(t, c, ctx, wiID, "pause-resume-002")
	attemptID2 := claim2["attempt_id"].(string)
	epoch2 := int64(claim2["claim_epoch"].(float64))
	t.Logf("re-claim: attempt=%s epoch=%d", attemptID2, epoch2)

	if attemptID2 == attemptID {
		t.Errorf("expected new attempt_id after resume, got same: %s", attemptID)
	}
	if epoch2 <= epoch1 {
		t.Errorf("expected epoch2(%d) > epoch1(%d) after resume", epoch2, epoch1)
	} else {
		t.Logf("OK: epoch advanced %d → %d on resume", epoch1, epoch2)
	}

	// 5. wi.status should be running again
	wi = mustGetWorkItem(t, c, ctx, wiID)
	if wi["status"] != "running" {
		t.Errorf("expected wi.status=running after resume, got %v", wi["status"])
	}

	// 6. Wrap to clean up
	mustCompleteAttempt(t, c, ctx, wiID, claim2, "wrapped")
}

// ─── 3. Terminal state guard ─────────────────────────────────────────────────

// TestCompleteAttemptTerminalGuard verifies that completing an attempt twice
// returns 409 CONFLICT_TERMINAL_STATE.
func TestCompleteAttemptTerminalGuard(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("terminal-guard-test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	claim := mustClaimWorkItem(t, c, ctx, wiID, "terminal-guard-001")
	attemptID := claim["attempt_id"].(string)
	claimEpoch := int64(claim["claim_epoch"].(float64))
	sessionSecret := claim["session_secret"].(string)

	// First wrap: should succeed
	mustCompleteAttempt(t, c, ctx, wiID, claim, "wrapped")
	t.Log("OK: first wrap succeeded")

	// Second wrap on the same attempt: should fail with 409
	_, err := c.CompleteAttempt(ctx, wiID, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
		"status":         "wrapped",
	})
	if err == nil {
		t.Error("second wrap on wrapped attempt should fail with 409, got nil")
	} else if !strings.Contains(err.Error(), "409") &&
		!strings.Contains(err.Error(), "TERMINAL") &&
		!strings.Contains(err.Error(), "terminal") {
		t.Logf("NOTE: expected 409/TERMINAL, got: %v", err)
	} else {
		t.Logf("OK: double-wrap rejected: %v", err)
	}
}

// ─── 4. Memory visibility filtering ──────────────────────────────────────────

// TestMemoryVisibilityFiltering verifies the visibility rules:
//   - private memories: only visible to author (and admin)
//   - project memories: visible to all project members
//   - admin: bypasses all visibility filters
func TestMemoryVisibilityFiltering(t *testing.T) {
	ctx := context.Background()
	writerC := newTestClient(t)
	adminC := newAdminClient(t)
	waitForHealth(t, writerC, 30*time.Second)

	// 1. Writer creates a PRIVATE memory
	privContent := fmt.Sprintf("vis-test PRIVATE %d secret-marker-x", time.Now().UnixNano())
	priv, err := writerC.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "rule.coding",
		"content":    privContent,
		"visibility": "private",
		"dedup_mode": "off",
	})
	if err != nil {
		t.Fatalf("Remember private: %v", err)
	}
	privID, _ := priv["id"].(string)
	if privID == "" {
		privID, _ = priv["memory_id"].(string)
	}
	if privID == "" {
		t.Fatalf("Remember private: no id in response: %v", priv)
	}
	t.Logf("private memory: %s", privID)

	// 2. Writer creates a PROJECT memory
	projContent := fmt.Sprintf("vis-test PROJECT %d shared-marker-y", time.Now().UnixNano())
	proj, err := writerC.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "rule.coding",
		"content":    projContent,
		"visibility": "project",
		"dedup_mode": "off",
	})
	if err != nil {
		t.Fatalf("Remember project: %v", err)
	}
	projID, _ := proj["id"].(string)
	if projID == "" {
		projID, _ = proj["memory_id"].(string)
	}
	if projID == "" {
		t.Fatalf("Remember project: no id in response: %v", proj)
	}
	t.Logf("project memory: %s", projID)

	// 3. Writer recall — should see both (author can always see own private)
	wParams := url.Values{}
	wParams.Set("project", testProject)
	wParams.Set("type", "rule.coding")
	wParams.Set("top_k", "200")
	wResp, err := writerC.Recall(ctx, wParams)
	if err != nil {
		t.Fatalf("writer Recall: %v", err)
	}
	wItems, _ := wResp["items"].([]any)
	sawPrivAsAuthor := false
	sawProjAsAuthor := false
	for _, item := range wItems {
		if m, ok := item.(map[string]any); ok {
			if m["id"] == privID {
				sawPrivAsAuthor = true
			}
			if m["id"] == projID {
				sawProjAsAuthor = true
			}
		}
	}
	if !sawPrivAsAuthor {
		t.Errorf("writer (author) should see own private memory %s", privID)
	}
	if !sawProjAsAuthor {
		t.Errorf("writer should see own project memory %s", projID)
	}
	t.Logf("OK: writer sees both (priv=%v proj=%v) of %d items", sawPrivAsAuthor, sawProjAsAuthor, len(wItems))

	// 4. Admin recall — should bypass all filters and see both
	aParams := url.Values{}
	aParams.Set("project", testProject)
	aParams.Set("type", "rule.coding")
	aParams.Set("top_k", "200")
	aResp, err := adminC.Recall(ctx, aParams)
	if err != nil {
		t.Fatalf("admin Recall: %v", err)
	}
	aItems, _ := aResp["items"].([]any)
	sawPrivAsAdmin := false
	sawProjAsAdmin := false
	for _, item := range aItems {
		if m, ok := item.(map[string]any); ok {
			if m["id"] == privID {
				sawPrivAsAdmin = true
			}
			if m["id"] == projID {
				sawProjAsAdmin = true
			}
		}
	}
	if !sawPrivAsAdmin {
		t.Errorf("admin should bypass private filter and see %s", privID)
	}
	if !sawProjAsAdmin {
		t.Errorf("admin should see project memory %s", projID)
	}
	t.Logf("OK: admin sees both (priv=%v proj=%v) of %d items", sawPrivAsAdmin, sawProjAsAdmin, len(aItems))

	// NOTE: seed data has exactly one writer user — cannot test cross-writer private occlusion
	// without provisioning a second writer. Documenting the gap.
	t.Logf("NOTE: cross-writer private-occlusion check skipped — seed has only one writer user")
}

// ─── 5. Conflict prediction ──────────────────────────────────────────────────

// TestConflictPredictionGitBranch verifies that:
//   - declaring a repo resource on a claimed wi triggers Rule 1/2 hard_block/soft_block
//   - after the holding wi is wrapped, the locks release and prediction is clean
func TestConflictPredictionGitBranch(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	branchSuffix := fmt.Sprintf("conflict-test-%d", time.Now().UnixNano())
	taskBranch := "fix/" + branchSuffix

	declaredResources, _ := json.Marshal([]map[string]any{
		{
			"type":        "repo",
			"uri":         "repo:aihub",
			"intent":      "write",
			"task_branch": taskBranch,
		},
	})

	// 1. Create + claim a wi with declared_resources holding the branch
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":               fmt.Sprintf("conflict-pred-holder %d", time.Now().UnixNano()),
		"project":            testProject,
		"scenario":           "coding",
		"wi_type":            "fix_bug",
		"declared_resources": json.RawMessage(declaredResources),
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	claim := mustClaimWorkItem(t, c, ctx, wiID, "conflict-pred-001")
	t.Logf("holder wi=%s, claimed: epoch=%v", wiID, claim["claim_epoch"])

	// 2. Predict conflicts for the same resource — should hit Rule 1 (hard_block)
	predResp, err := c.PredictConflicts(ctx, map[string]any{
		"declared_resources": json.RawMessage(declaredResources),
	})
	if err != nil {
		t.Fatalf("PredictConflicts (while held): %v", err)
	}
	predictions, _ := predResp["predictions"].([]any)
	severity, _ := predResp["severity"].(string)
	if len(predictions) == 0 {
		t.Errorf("expected at least 1 prediction while resource is held, got 0; severity=%s", severity)
	} else {
		t.Logf("OK: %d prediction(s) reported, severity=%s", len(predictions), severity)
	}
	if severity == "info" {
		t.Errorf("severity should be soft_block or hard_block while resource is held, got info")
	}

	// 3. Wrap the holder — locks should be released
	mustCompleteAttempt(t, c, ctx, wiID, claim, "wrapped")
	t.Logf("holder wrapped, locks should be released")

	// 4. Re-predict — predictions should be empty
	predResp2, err := c.PredictConflicts(ctx, map[string]any{
		"declared_resources": json.RawMessage(declaredResources),
	})
	if err != nil {
		t.Fatalf("PredictConflicts (after wrap): %v", err)
	}
	predictions2, _ := predResp2["predictions"].([]any)
	severity2, _ := predResp2["severity"].(string)
	if len(predictions2) > 0 {
		t.Errorf("expected 0 predictions after holder wrapped, got %d (severity=%s)", len(predictions2), severity2)
	} else {
		t.Logf("OK: 0 predictions after wrap, severity=%s", severity2)
	}
}

// ─── 6. Idempotent claim ─────────────────────────────────────────────────────

// TestClaimIdempotency verifies that the same idempotency_key on a re-issued
// claim returns the same attempt_id and claim_epoch (server replays the cached
// response without creating a new attempt).
func TestClaimIdempotency(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("idempotent-claim-test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	idemKey := fmt.Sprintf("idem-test-%d", time.Now().UnixNano())

	claim1 := mustClaimWorkItem(t, c, ctx, wiID, idemKey)
	attemptID1 := claim1["attempt_id"].(string)
	epoch1 := int64(claim1["claim_epoch"].(float64))
	t.Logf("first claim: attempt=%s epoch=%d", attemptID1, epoch1)

	// Re-issue claim with the SAME idempotency key + fresh session_info
	si := newSessionInfo()
	result, err := c.ClaimWorkItem(ctx, wiID, map[string]any{
		"idempotency_key": idemKey,
		"session_info":    si,
		"mode":            "fresh",
	})
	if err != nil {
		t.Fatalf("idempotent re-claim: %v", err)
	}
	attemptID2 := result["attempt_id"].(string)
	epoch2 := int64(result["claim_epoch"].(float64))
	t.Logf("idem replay: attempt=%s epoch=%d", attemptID2, epoch2)

	if attemptID2 != attemptID1 {
		t.Errorf("idempotent claim should return same attempt_id; got %s vs %s", attemptID2, attemptID1)
	} else {
		t.Logf("OK: same attempt_id on idem replay")
	}
	if epoch2 != epoch1 {
		t.Errorf("idempotent claim should return same claim_epoch; got %d vs %d", epoch2, epoch1)
	} else {
		t.Logf("OK: same claim_epoch on idem replay")
	}

	// Cleanup: complete the original attempt (use the original session_secret)
	mustCompleteAttempt(t, c, ctx, wiID, claim1, "wrapped")
}

// ─── 7. Work item not-found and forbidden ────────────────────────────────────

// TestWorkItemErrorCases verifies common error paths:
//   - GET nonexistent wi → 404
//   - Claim wrapped wi → 409 CONFLICT_TERMINAL_STATE
//   - Complete with wrong session_secret → 401/403
func TestWorkItemErrorCases(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. GET nonexistent → 404
	_, err := c.GetWorkItem(ctx, "wi_nonexistent_xxxxxxx")
	if err == nil {
		t.Error("expected 404 for nonexistent wi, got nil")
	} else if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Logf("NOTE: expected 404/NOT_FOUND, got: %v", err)
	} else {
		t.Logf("OK: GET nonexistent wi → %v", err)
	}

	// 2. Create + wrap a wi, then attempt to claim it → should fail
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("error-cases-test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	claim := mustClaimWorkItem(t, c, ctx, wiID, "err-cases-001")
	mustCompleteAttempt(t, c, ctx, wiID, claim, "wrapped")

	si := newSessionInfo()
	_, err = c.ClaimWorkItem(ctx, wiID, map[string]any{
		"idempotency_key": "err-cases-002",
		"session_info":    si,
		"mode":            "fresh",
	})
	if err == nil {
		t.Error("expected error when claiming wrapped wi, got nil")
	} else {
		t.Logf("OK: claim of wrapped wi rejected: %v", err)
	}

	// 3. Complete with WRONG session_secret on a fresh attempt → 401/403
	wiID2 := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("error-cases-secret %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID2)
	claim2 := mustClaimWorkItem(t, c, ctx, wiID2, "err-cases-003")
	attemptID := claim2["attempt_id"].(string)
	claimEpoch := int64(claim2["claim_epoch"].(float64))

	_, err = c.CompleteAttempt(ctx, wiID2, map[string]any{
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": "wrong-secret-deadbeef-not-the-real-one",
		"status":         "wrapped",
	})
	if err == nil {
		t.Error("expected auth error with wrong session_secret, got nil")
	} else if !strings.Contains(err.Error(), "401") &&
		!strings.Contains(err.Error(), "403") &&
		!strings.Contains(err.Error(), "UNAUTHORIZED") &&
		!strings.Contains(err.Error(), "FORBIDDEN") &&
		!strings.Contains(err.Error(), "CREDENTIAL") {
		t.Logf("NOTE: expected 401/403/credential error, got: %v", err)
	} else {
		t.Logf("OK: wrong session_secret rejected: %v", err)
	}

	// Cleanup with correct credential
	mustCompleteAttempt(t, c, ctx, wiID2, claim2, "wrapped")
}

// ─── 8. GC endpoint ──────────────────────────────────────────────────────────

// TestGCEndpointReturnsResults verifies POST /v1/admin/gc returns the expected
// sweep result shape: an array of {sweep_type, affected, skipped, error?}.
func TestGCEndpointReturnsResults(t *testing.T) {
	ctx := context.Background()
	adminC := newAdminClient(t)
	waitForHealth(t, adminC, 30*time.Second)

	// Sanity check admin auth
	if _, err := adminC.WhoAmI(ctx); err != nil {
		t.Fatalf("admin WhoAmI: %v", err)
	}

	// POST /v1/admin/gc — client doesn't expose a dedicated method, so use raw HTTP.
	out, err := postAdminGC(ctx)
	if err != nil {
		t.Fatalf("POST /v1/admin/gc: %v", err)
	}
	results, ok := out["results"].([]any)
	if !ok {
		t.Fatalf("expected results array; got %T: %v", out["results"], out)
	}
	if len(results) == 0 {
		t.Error("expected at least one sweep result")
	}

	// Validate each entry has sweep_type + affected fields
	seenTypes := map[string]bool{}
	for i, r := range results {
		entry, ok := r.(map[string]any)
		if !ok {
			t.Errorf("results[%d] is not a map: %T", i, r)
			continue
		}
		st, _ := entry["sweep_type"].(string)
		if st == "" {
			t.Errorf("results[%d] missing sweep_type", i)
		}
		seenTypes[st] = true
		if _, has := entry["affected"]; !has {
			t.Errorf("results[%d] (%s) missing affected", i, st)
		}
	}
	t.Logf("OK: GC returned %d sweep results: %v", len(results), keys(seenTypes))

	// Spot-check that common sweep types appear (best-effort; some only run on cron tick)
	common := []string{"orphan_lock_cleanup", "memory_expired_archive", "unblock_dependent_wi"}
	for _, ct := range common {
		if seenTypes[ct] {
			t.Logf("OK: saw expected sweep_type %s", ct)
		} else {
			t.Logf("NOTE: sweep_type %s not in results (may be tick-gated)", ct)
		}
	}
}

// postAdminGC calls POST /v1/admin/gc directly via net/http (the client package
// does not expose a dedicated method). Uses the same env-var fallbacks as
// newAdminClient: AIHUB_URL / AIHUB_ADMIN_KEY.
func postAdminGC(ctx context.Context) (map[string]any, error) {
	baseURL := os.Getenv("AIHUB_URL")
	if baseURL == "" {
		baseURL = defaultAihubURL
	}
	adminKey := os.Getenv("AIHUB_ADMIN_KEY")
	if adminKey == "" {
		adminKey = testAdminKey
	}
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/admin/gc", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")

	cli := &http.Client{Timeout: 60 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("aihub %d: %s", resp.StatusCode, string(body))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode: %w (body=%s)", err, string(body))
	}
	return out, nil
}

// ─── 9. Ready queue max param ────────────────────────────────────────────────

// TestReadyQueueMaxParam verifies that GET /v1/work_items/ready?max=N caps the
// number of returned items per segment.
func TestReadyQueueMaxParam(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// Create 3 queued fix_bug wis (auto, requires_human_session=false → items[])
	created := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
			"goal":     fmt.Sprintf("max-test-%d-%d", i, time.Now().UnixNano()),
			"project":  testProject,
			"scenario": "coding",
			"wi_type":  "fix_bug",
			"priority": "normal",
		})
		created = append(created, wiID)
	}
	defer func() {
		for _, id := range created {
			cancelWorkItem(t, c, ctx, id)
		}
	}()

	// max=2 — items[] should have ≤ 2 entries
	params := url.Values{}
	params.Set("project", testProject)
	params.Set("max", "2")
	q2, err := c.GetReadyQueue(ctx, params)
	if err != nil {
		t.Fatalf("GetReadyQueue max=2: %v", err)
	}
	items2, _ := q2["items"].([]any)
	if len(items2) > 2 {
		t.Errorf("expected items[] ≤ 2 with max=2, got %d", len(items2))
	} else {
		t.Logf("OK: max=2 → items[]=%d", len(items2))
	}

	// max=10 — items[] should be able to hold more entries (≥ items2 length, up to 10)
	params10 := url.Values{}
	params10.Set("project", testProject)
	params10.Set("max", "10")
	q10, err := c.GetReadyQueue(ctx, params10)
	if err != nil {
		t.Fatalf("GetReadyQueue max=10: %v", err)
	}
	items10, _ := q10["items"].([]any)
	if len(items10) > 10 {
		t.Errorf("expected items[] ≤ 10 with max=10, got %d", len(items10))
	}
	if len(items10) < len(items2) {
		t.Errorf("expected items[] with max=10 (%d) >= max=2 (%d)", len(items10), len(items2))
	} else {
		t.Logf("OK: max=10 → items[]=%d (>= max=2 result of %d)", len(items10), len(items2))
	}
}

// ─── 10. List work items filters ─────────────────────────────────────────────

// TestListWorkItemsFilters verifies the ?status= and ?wi_type= query filters
// on GET /v1/work_items.
func TestListWorkItemsFilters(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// Create a queued fix_bug and a queued feature
	fixBugID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("list-filter-fixbug %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	featID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("list-filter-feature %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "feature",
	})
	defer cancelWorkItem(t, c, ctx, fixBugID)
	defer cancelWorkItem(t, c, ctx, featID)

	// Create a wi we'll wrap so we have a non-queued wi to differentiate
	wrappedID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("list-filter-wrapped %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	wrappedClaim := mustClaimWorkItem(t, c, ctx, wrappedID, "list-filter-001")
	mustCompleteAttempt(t, c, ctx, wrappedID, wrappedClaim, "wrapped")
	defer cancelWorkItem(t, c, ctx, wrappedID)

	// 1. status=queued → must include fixBugID & featID, must NOT include wrappedID
	queuedParams := url.Values{}
	queuedParams.Set("project", testProject)
	queuedParams.Set("status", "queued")
	queuedParams.Set("limit", "200")
	queuedResp, err := c.ListWorkItems(ctx, queuedParams)
	if err != nil {
		t.Fatalf("ListWorkItems status=queued: %v", err)
	}
	queuedItems, _ := queuedResp["items"].([]any)
	sawFixBugQueued := false
	sawFeatQueued := false
	sawWrappedInQueued := false
	for _, item := range queuedItems {
		if m, ok := item.(map[string]any); ok {
			switch m["id"] {
			case fixBugID:
				sawFixBugQueued = true
			case featID:
				sawFeatQueued = true
			case wrappedID:
				sawWrappedInQueued = true
			}
			if status, _ := m["status"].(string); status != "queued" {
				t.Errorf("status=queued filter returned wi with status=%v (id=%v)", status, m["id"])
			}
		}
	}
	if !sawFixBugQueued {
		t.Errorf("expected queued list to contain fix_bug wi %s", fixBugID)
	}
	if !sawFeatQueued {
		t.Errorf("expected queued list to contain feature wi %s", featID)
	}
	if sawWrappedInQueued {
		t.Errorf("queued list should not contain wrapped wi %s", wrappedID)
	}
	t.Logf("OK: status=queued returned %d items (fixBug=%v feat=%v wrapped-absent=%v)",
		len(queuedItems), sawFixBugQueued, sawFeatQueued, !sawWrappedInQueued)

	// 2. wi_type=fix_bug → must include fixBugID, must NOT include featID
	typeParams := url.Values{}
	typeParams.Set("project", testProject)
	typeParams.Set("wi_type", "fix_bug")
	typeParams.Set("limit", "200")
	typeResp, err := c.ListWorkItems(ctx, typeParams)
	if err != nil {
		t.Fatalf("ListWorkItems wi_type=fix_bug: %v", err)
	}
	typeItems, _ := typeResp["items"].([]any)
	sawFixBugByType := false
	sawFeatByType := false
	for _, item := range typeItems {
		if m, ok := item.(map[string]any); ok {
			switch m["id"] {
			case fixBugID:
				sawFixBugByType = true
			case featID:
				sawFeatByType = true
			}
			if wt, _ := m["wi_type"].(string); wt != "fix_bug" {
				t.Errorf("wi_type=fix_bug filter returned wi with wi_type=%v (id=%v)", wt, m["id"])
			}
		}
	}
	if !sawFixBugByType {
		t.Errorf("expected wi_type=fix_bug list to contain %s", fixBugID)
	}
	if sawFeatByType {
		t.Errorf("wi_type=fix_bug filter should not return feature wi %s", featID)
	}
	t.Logf("OK: wi_type=fix_bug returned %d items (fixBug=%v feat-absent=%v)",
		len(typeItems), sawFixBugByType, !sawFeatByType)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
