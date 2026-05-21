//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestSameUserForceTakeover verifies C-R9-12:
// same user_id from a "different machine" can implicitly take over their own active attempt.
//
// Protocol: same API key → same user_id → implicit takeover (no explicit force_takeover flag needed).
// Epoch must increment by 1 on each successful takeover.
func TestSameUserForceTakeover(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create a chore wi (requires_human_session=false)
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("Force takeover test: %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "chore",
	})
	t.Logf("created wi: %s", wiID)

	// 2. Session 1 claims the wi
	claim1 := mustClaimWorkItem(t, c, ctx, wiID, "takeover-machine-A-001")
	epoch1 := int64(claim1["claim_epoch"].(float64))
	t.Logf("machine-A claim: attempt=%s epoch=%d", claim1["attempt_id"].(string), epoch1)

	// 3. Session 2 (different machine_id, same API key = same user) re-claims
	claim2, err := c.ClaimWorkItem(ctx, wiID, map[string]any{
		"idempotency_key": "takeover-machine-B-001",
		"session_info": map[string]any{
			"machine_id":     "test-machine-002", // different machine
			"session_secret": newSessionInfo()["session_secret"],
		},
		"mode": "fresh",
	})
	if err != nil {
		t.Fatalf("same-user takeover should succeed (C-R9-12), got: %v", err)
	}

	epoch2 := int64(claim2["claim_epoch"].(float64))
	t.Logf("machine-B takeover: attempt=%s epoch=%d", claim2["attempt_id"].(string), epoch2)

	// 4. Epoch must have incremented
	if epoch2 <= epoch1 {
		t.Errorf("expected epoch2(%d) > epoch1(%d) after takeover", epoch2, epoch1)
	}

	// 5. Attempt IDs must differ
	if claim2["attempt_id"] == claim1["attempt_id"] {
		t.Errorf("expected new attempt_id after takeover")
	}

	// 6. Multiple sequential takeovers must each increment epoch
	si3 := newSessionInfo()
	claim3, err := c.ClaimWorkItem(ctx, wiID, map[string]any{
		"idempotency_key": "takeover-machine-C-001",
		"session_info": map[string]any{
			"machine_id":     "test-machine-003",
			"session_secret": si3["session_secret"],
		},
		"mode": "fresh",
	})
	if err != nil {
		t.Fatalf("third takeover should succeed: %v", err)
	}
	claim3["session_secret"] = si3["session_secret"]
	epoch3 := int64(claim3["claim_epoch"].(float64))
	if epoch3 != epoch2+1 {
		t.Errorf("expected epoch3=%d (epoch2+1), got %d", epoch2+1, epoch3)
	}
	t.Logf("epoch progression: %d → %d → %d (correct)", epoch1, epoch2, epoch3)

	// 7. Clean up
	mustCompleteAttempt(t, c, ctx, wiID, claim3, "failed")
	t.Logf("force takeover test passed: wi %s", wiID)
}
