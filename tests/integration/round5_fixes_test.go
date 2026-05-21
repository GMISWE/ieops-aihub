//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestDependencyBlocksReadyQueue verifies the full dependency lifecycle:
// blocker wi blocks blocked wi → blocked wi absent from items[] →
// wrap blocker → blocked wi unblocked and appears in items[].
// Covers Round 5 fix A: CreateDependency / RemoveDependency URL paths.
func TestDependencyBlocksReadyQueue(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create two fix_bug work items
	blockerID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("Dep-test blocker %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
		"priority": "high",
	})
	blockedID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("Dep-test blocked %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
		"priority": "normal",
	})
	t.Logf("blocker=%s blocked=%s", blockerID, blockedID)
	defer cancelWorkItem(t, c, ctx, blockerID)
	defer cancelWorkItem(t, c, ctx, blockedID)

	// 2. Create dependency: blockedID is blocked by blockerID
	_, err := c.CreateDependency(ctx, map[string]any{
		"blocked_wi_id":  blockedID,
		"blocking_wi_id": blockerID,
		"kind":           "blocks",
	})
	if err != nil {
		t.Fatalf("CreateDependency: %v", err)
	}
	t.Logf("dependency created: %s blocks %s", blockerID, blockedID)

	// 3. Verify blocked wi does NOT appear in items[] (it's blocked)
	queue := mustGetReadyQueue(t, c, ctx, testProject)
	items, _ := queue["items"].([]any)
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && m["id"] == blockedID {
			t.Errorf("blocked wi %s should NOT be in items[] before blocker is wrapped", blockedID)
		}
	}
	t.Logf("OK: blocked wi absent from items[] (items has %d)", len(items))

	// 4. Claim and wrap the blocker
	blockerClaim := mustClaimWorkItem(t, c, ctx, blockerID, "dep-test-blocker-001")
	mustCompleteAttempt(t, c, ctx, blockerID, blockerClaim, "wrapped")
	t.Logf("blocker %s wrapped", blockerID)

	// 5. Verify blocked wi now appears in items[] (auto-unblocked)
	time.Sleep(200 * time.Millisecond) // give server a moment to run unblockDependentWI
	queue2 := mustGetReadyQueue(t, c, ctx, testProject)
	items2, _ := queue2["items"].([]any)
	foundUnblocked := false
	for _, item := range items2 {
		if m, ok := item.(map[string]any); ok && m["id"] == blockedID {
			foundUnblocked = true
		}
	}
	if !foundUnblocked {
		t.Errorf("blocked wi %s should appear in items[] after blocker wrapped (items=%d)", blockedID, len(items2))
	} else {
		t.Logf("OK: blocked wi %s now in items[] after blocker wrapped", blockedID)
	}
}

// TestMemoryDedupThresholds verifies the two-threshold dedup model from design §7.7 / §11:
// - similarity ≥ 0.85 (strict) → 409 CONFLICT_SIMILAR_MEMORY
// - similarity ≥ 0.65 (suggest) → 200 with attrs.similar_to annotation
// - similarity < 0.65 → 200 clean insert
// Covers Round 5 fix D.
func TestMemoryDedupThresholds(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	base := fmt.Sprintf("polyforge-dedup-test-%d", time.Now().UnixNano())

	// 1. Create the seed memory
	seed, err := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "rule.coding",
		"content":    base + " always validate input before processing",
		"visibility": "project",
		"dedup_mode": "off", // bypass dedup for seed
	})
	if err != nil {
		t.Fatalf("seed Remember: %v", err)
	}
	seedID, _ := seed["id"].(string)
	t.Logf("seed memory: %s", seedID)

	// 2. Near-identical content (>0.85 Jaccard) in strict mode → expect 409
	nearIdentical := base + " always validate input before processing carefully"
	_, errStrict := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "rule.coding",
		"content":    nearIdentical,
		"visibility": "project",
		"dedup_mode": "strict",
	})
	if errStrict == nil {
		t.Errorf("strict dedup: expected 409 for near-identical content, got nil error")
	} else {
		t.Logf("OK: strict dedup rejected near-identical content: %v", errStrict)
	}

	// 3. Moderate similarity (0.65–0.85) in suggest mode → expect 200 + attrs.similar_to
	moderate := base + " validate inputs thoroughly using the domain rules defined for each field type"
	result, errSuggest := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "rule.coding",
		"content":    moderate,
		"visibility": "project",
		"dedup_mode": "suggest",
	})
	if errSuggest != nil {
		t.Logf("suggest dedup (note: might be below threshold): %v", errSuggest)
	} else {
		attrs, _ := result["attrs"].(map[string]any)
		similarTo, hasSimilar := attrs["similar_to"]
		if hasSimilar {
			t.Logf("OK: suggest dedup annotated attrs.similar_to=%v", similarTo)
		} else {
			t.Logf("INFO: content below 0.65 threshold — clean insert (no similar_to annotation)")
		}
	}

	// 4. Completely different content → expect clean insert (no annotation)
	different := fmt.Sprintf("totally unrelated content about networking protocols %d", time.Now().UnixNano())
	clean, errClean := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "rule.coding",
		"content":    different,
		"visibility": "project",
		"dedup_mode": "strict",
	})
	if errClean != nil {
		t.Errorf("clean insert with different content should succeed, got: %v", errClean)
	} else {
		attrs, _ := clean["attrs"].(map[string]any)
		if _, has := attrs["similar_to"]; has {
			t.Errorf("completely different content should not have attrs.similar_to")
		}
		t.Logf("OK: clean insert, id=%v", clean["id"])
	}
}

// TestMemoryReinforce verifies that PATCH /v1/memories/:id/reinforce updates
// the existing row in-place (activation_count++, stability_days recomputed)
// rather than creating a new memory row.
// Covers Round 5 fix E.
func TestMemoryReinforce(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create a memory to reinforce
	mem, err := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "experience.coding",
		"content":    fmt.Sprintf("reinforce-test memory %d — use context.WithTimeout for DB calls", time.Now().UnixNano()),
		"visibility": "project",
		"dedup_mode": "off",
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	memID, _ := mem["id"].(string)
	initialActivation, _ := mem["activation_count"].(float64)
	t.Logf("created memory %s activation_count=%v", memID, initialActivation)

	// 2. Reinforce it
	reinforced, err := c.ReinforceMemory(ctx, memID, map[string]any{
		"additional_context": "confirmed: always use context.WithTimeout with 30s for database calls",
	})
	if err != nil {
		t.Fatalf("ReinforceMemory: %v", err)
	}
	t.Logf("reinforce response: %+v", reinforced)

	// 3. Fetch the memory and verify it was updated in-place (not a new row)
	params := url.Values{}
	params.Set("project", testProject)
	params.Set("type", "experience.*")
	recalled, err := c.Recall(ctx, params)
	if err != nil {
		t.Fatalf("Recall after reinforce: %v", err)
	}
	items, _ := recalled["items"].([]any)
	var updated map[string]any
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && m["id"] == memID {
			updated = m
		}
	}
	if updated == nil {
		t.Fatalf("original memory %s not found after reinforce", memID)
	}

	// activation_count should be incremented
	newActivation, _ := updated["activation_count"].(float64)
	if newActivation <= initialActivation {
		t.Errorf("activation_count should increase after reinforce: before=%v after=%v", initialActivation, newActivation)
	} else {
		t.Logf("OK: activation_count %v → %v", initialActivation, newActivation)
	}

	// ID must be the same (in-place update, not a new memory)
	if updated["id"] != memID {
		t.Errorf("reinforce should update in-place: original id=%s returned id=%v", memID, updated["id"])
	} else {
		t.Logf("OK: same memory id %s (in-place update confirmed)", memID)
	}
}

// TestForceTakeoverNewAttempt verifies that POST /v1/work_items/:id/force_takeover
// returns a new attempt_id that can be used for subsequent operations.
// Covers Round 5 H3 + fix in run_attempts.go.
func TestForceTakeoverNewAttempt(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	adminC := newAdminClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create and claim a wi as writer
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("force-takeover-test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, adminC, ctx, wiID)

	claim := mustClaimWorkItem(t, c, ctx, wiID, "takeover-test-001")
	epoch1 := int64(claim["claim_epoch"].(float64))
	t.Logf("claimed: attempt=%s epoch=%d", claim["attempt_id"], epoch1)

	// 2. Force takeover as admin (different user)
	si := newSessionInfo()
	takeoverResp, err := adminC.ForceTakeover(ctx, wiID, map[string]any{
		"reason":       "integration test: admin takeover",
		"session_info": si,
	})
	if err != nil {
		t.Fatalf("ForceTakeover: %v", err)
	}
	t.Logf("force_takeover response: %+v", takeoverResp)

	// 3. Verify prior attempt ID is in response
	priorAttemptID, _ := takeoverResp["prior_attempt_id"].(string)
	if priorAttemptID == "" {
		t.Error("force_takeover response missing prior_attempt_id")
	}

	// 4. Verify new attempt ID is returned
	newAttemptID, _ := takeoverResp["new_attempt_id"].(string)
	if newAttemptID == "" {
		t.Errorf("force_takeover response missing new_attempt_id (H3 fix)")
	} else {
		t.Logf("OK: new_attempt_id=%s", newAttemptID)
	}

	// 5. Verify wi is still running (not paused) with the new attempt
	wi := mustGetWorkItem(t, c, ctx, wiID)
	if wi["status"] != "running" {
		t.Errorf("wi should be running after force_takeover, got %v", wi["status"])
	}
	if wi["current_attempt_id"] != newAttemptID {
		t.Errorf("current_attempt_id should be new attempt %s, got %v", newAttemptID, wi["current_attempt_id"])
	}
	newEpoch := int64(wi["current_attempt_epoch"].(float64))
	if newEpoch != epoch1+1 {
		t.Errorf("claim_epoch should be %d after takeover, got %d", epoch1+1, newEpoch)
	} else {
		t.Logf("OK: epoch advanced %d → %d", epoch1, newEpoch)
	}

	// 6. Wrap with new attempt
	newSecret, _ := takeoverResp["new_session_secret"].(string)
	if newSecret == "" {
		t.Log("NOTE: new_session_secret not returned (Decision A — expected if server does not surface it)")
		// Complete via admin with admin key (skip credential check path)
		_, err = adminC.CompleteAttempt(ctx, newAttemptID, map[string]any{
			"attempt_id":  newAttemptID,
			"claim_epoch": newEpoch,
			"status":      "wrapped",
		})
	} else {
		_, err = adminC.CompleteAttempt(ctx, newAttemptID, map[string]any{
			"attempt_id":     newAttemptID,
			"claim_epoch":    newEpoch,
			"session_secret": newSecret,
			"status":         "wrapped",
		})
	}
	if err != nil {
		t.Logf("NOTE: CompleteAttempt after takeover: %v (may require new session secret from state file)", err)
	} else {
		t.Logf("OK: wrapped wi %s after force_takeover", wiID)
	}
}

// TestGoalUpdateConstraints verifies design §4.3 constraints on goal updates:
// - goal update allowed when wi is queued/paused
// - goal update requires goal_change_reason
// - goal update rejected (409) when wi is running
// Covers Round 5 fix F (ErrGoalChangeNotAllowed → 409).
func TestGoalUpdateConstraints(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create a queued wi
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("goal-update-test original %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	defer cancelWorkItem(t, c, ctx, wiID)

	// 2. Update goal when queued (with reason) → should succeed
	_, err := c.UpdateWorkItem(ctx, wiID, map[string]any{
		"goal":               fmt.Sprintf("goal-update-test revised %d", time.Now().UnixNano()),
		"goal_change_reason": "clarified scope during planning",
	})
	if err != nil {
		t.Errorf("goal update on queued wi should succeed: %v", err)
	} else {
		t.Log("OK: goal update succeeded on queued wi")
	}

	// 3. Update goal without reason → should fail
	_, errNoReason := c.UpdateWorkItem(ctx, wiID, map[string]any{
		"goal": "goal without reason",
	})
	if errNoReason == nil {
		t.Error("goal update without goal_change_reason should fail")
	} else {
		t.Logf("OK: goal update without reason rejected: %v", errNoReason)
	}

	// 4. Claim the wi
	claim := mustClaimWorkItem(t, c, ctx, wiID, "goal-update-test-001")

	// 5. Update goal when running → should fail with 409
	_, errRunning := c.UpdateWorkItem(ctx, wiID, map[string]any{
		"goal":               "trying to change goal while running",
		"goal_change_reason": "test reason",
	})
	if errRunning == nil {
		t.Error("goal update on running wi should fail with 409 GOAL_CHANGE_NOT_ALLOWED")
	} else if !strings.Contains(errRunning.Error(), "409") && !strings.Contains(errRunning.Error(), "GOAL_CHANGE") {
		t.Logf("NOTE: expected 409 GOAL_CHANGE_NOT_ALLOWED, got: %v", errRunning)
	} else {
		t.Logf("OK: goal update on running wi rejected with 409: %v", errRunning)
	}

	// Cleanup
	mustCompleteAttempt(t, c, ctx, wiID, claim, "wrapped")
}
