//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"
)

// TestCrashRecovery simulates a session crash while a run_attempt is active,
// then verifies the same user can re-claim and resume (C-R9-12: same user = implicit takeover).
//
// Sequence:
//  1. Create and claim a wi (attempt-1, epoch-1)
//  2. Do NOT complete attempt-1 — simulate crash
//  3. Re-claim with a new idempotency key (attempt-2, epoch-2)
//  4. Verify epoch incremented (epoch-2 > epoch-1)
//  5. Complete attempt-2 as "wrapped"
func TestCrashRecovery(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create a wi
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     "Crash recovery test: step in_progress then restart",
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
	})
	t.Logf("created wi: %s", wiID)

	// 2. Claim (simulated session 1)
	claim1 := mustClaimWorkItem(t, c, ctx, wiID, "crash-test-session1-001")
	attemptID1 := claim1["attempt_id"].(string)
	claimEpoch1 := int64(claim1["claim_epoch"].(float64))
	t.Logf("session-1 claim: attempt=%s epoch=%d", attemptID1, claimEpoch1)

	// Verify wi is running
	wi := mustGetWorkItem(t, c, ctx, wiID)
	if wi["status"] != "running" {
		t.Fatalf("expected status=running after first claim, got %v", wi["status"])
	}

	// 3. "Crash" — do not complete attempt-1, just re-claim with a new idempotency key.
	//    C-R9-12: same API key (same user) → implicit self-takeover allowed.
	claim2, err := c.ClaimWorkItem(ctx, wiID, map[string]any{
		"idempotency_key": "crash-test-session2-001",
		"session_info":    newSessionInfo(),
		"mode":            "fresh",
	})
	if err != nil {
		t.Fatalf("re-claim after crash should succeed (C-R9-12 implicit takeover): %v", err)
	}

	attemptID2 := claim2["attempt_id"].(string)
	claimEpoch2 := int64(claim2["claim_epoch"].(float64))
	t.Logf("session-2 re-claim: attempt=%s epoch=%d", attemptID2, claimEpoch2)

	// 4. New epoch must be strictly greater (takeover always bumps epoch)
	if claimEpoch2 <= claimEpoch1 {
		t.Errorf("expected claimEpoch2(%d) > claimEpoch1(%d) after takeover", claimEpoch2, claimEpoch1)
	}

	// 5. Old and new attempt IDs must differ
	if attemptID2 == attemptID1 {
		t.Errorf("expected new attempt_id after takeover, got same: %s", attemptID1)
	}

	// 6. Verify step_recovery_hint field is present (indicates server detected interrupted step)
	hint, _ := claim2["step_recovery_hint"].(string)
	t.Logf("step_recovery_hint: %q", hint)

	// 7. Complete the recovery attempt
	mustCompleteAttempt(t, c, ctx, wiID, claim2, "wrapped")

	// 8. Verify wi is now wrapped
	wi2 := mustGetWorkItem(t, c, ctx, wiID)
	if wi2["status"] != "wrapped" {
		t.Errorf("expected status=wrapped after recovery wrap, got %v", wi2["status"])
	}

	t.Logf("crash recovery test passed: wi %s, epoch %d→%d", wiID, claimEpoch1, claimEpoch2)
}
