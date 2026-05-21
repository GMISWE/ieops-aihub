//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"
)

// TestFixBugWIHappyPath tests the full lifecycle of a fix_bug work item:
// create → claim → verify running → complete attempt as wrapped
func TestFixBugWIHappyPath(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create a fix_bug wi
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     fmt.Sprintf("Fix: null pointer test %d", time.Now().UnixNano()),
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
		"priority": "normal",
	})
	t.Logf("created wi: %s", wiID)

	// 2. Verify it appears in the ready queue items[]
	queue := mustGetReadyQueue(t, c, ctx, testProject)
	items, _ := queue["items"].([]any)
	foundInQueue := false
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && m["id"] == wiID {
			foundInQueue = true
		}
	}
	if !foundInQueue {
		t.Logf("wi %s not in ready queue items[] (queue has %d items) — may be in unclassified[]", wiID, len(items))
		// Also check unclassified — wi_type lookup may put it there
		unclassified, _ := queue["unclassified"].([]any)
		for _, item := range unclassified {
			if m, ok := item.(map[string]any); ok && m["id"] == wiID {
				foundInQueue = true
			}
		}
	}
	if !foundInQueue {
		t.Errorf("wi %s not found in ready queue items[] or unclassified[]", wiID)
	}

	// 3. Claim the wi
	claim := mustClaimWorkItem(t, c, ctx, wiID, "happy-path-001")
	attemptID := claim["attempt_id"].(string)
	claimEpoch := int64(claim["claim_epoch"].(float64))
	t.Logf("claimed: attempt=%s epoch=%d", attemptID, claimEpoch)

	// 4. Verify wi status → running
	wi := mustGetWorkItem(t, c, ctx, wiID)
	if wi["status"] != "running" {
		t.Errorf("expected status=running after claim, got %v", wi["status"])
	}

	// 5. Verify claim epoch is 1
	if claimEpoch != 1 {
		t.Errorf("expected claim_epoch=1 for first claim, got %d", claimEpoch)
	}

	// 6. Verify the ready queue no longer shows this wi in items[]
	queue2 := mustGetReadyQueue(t, c, ctx, testProject)
	items2, _ := queue2["items"].([]any)
	for _, item := range items2 {
		if m, ok := item.(map[string]any); ok && m["id"] == wiID {
			t.Errorf("wi %s should not be in items[] after claim (status=running)", wiID)
		}
	}

	// 7. Wrap the attempt
	mustCompleteAttempt(t, c, ctx, wiID, claim, "wrapped")

	// 8. Verify wi status → wrapped
	wi2 := mustGetWorkItem(t, c, ctx, wiID)
	if wi2["status"] != "wrapped" {
		t.Errorf("expected status=wrapped after complete, got %v", wi2["status"])
	}

	// 9. Verify wrapped wi no longer appears in the ready queue
	params := url.Values{}
	params.Set("project", testProject)
	queue3, _ := c.GetReadyQueue(ctx, params)
	all3 := collectAllWIs(queue3)
	for _, id := range all3 {
		if id == wiID {
			t.Errorf("wrapped wi %s should not appear in ready queue", wiID)
		}
	}

	t.Logf("happy path complete: wi %s wrapped successfully", wiID)
}

// collectAllWIs returns all work item IDs across all segments of a ready queue response.
func collectAllWIs(queue map[string]any) []string {
	var ids []string
	for _, segKey := range []string{"items", "running", "stalled", "paused", "needs_human_session", "unclassified"} {
		seg, _ := queue[segKey].([]any)
		for _, item := range seg {
			if m, ok := item.(map[string]any); ok {
				if id, ok := m["id"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}
