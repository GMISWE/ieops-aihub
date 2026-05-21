//go:build integration

package integration_test

import (
	"context"
	"net/url"
	"testing"
	"time"
)

// TestMemoryRecall tests the full memory lifecycle:
//  1. Remember a fact (POST /v1/memories)
//  2. Activate it to simulate recall reinforcement (POST /v1/memories/:id/activate)
//  3. Recall by type filter (GET /v1/memories?type=...)
//  4. Recall by tag filter — note: server currently supports type/query filters;
//     tag-based recall falls back to full-project recall if unsupported
func TestMemoryRecall(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Remember a rule.coding fact
	memResult, err := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "rule.coding",
		"content":    "Always check for nil before dereferencing a pointer returned from a map lookup in Go",
		"visibility": "project",
		"dedup_mode": "off",
		"tags":       []string{"go", "nil-safety"},
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Server returns {"memory_id": "..."}
	memID, ok := memResult["memory_id"].(string)
	if !ok || memID == "" {
		t.Fatalf("Remember: missing memory_id in response: %v", memResult)
	}
	t.Logf("remembered: %s", memID)

	// 2. Activate the memory (spaced repetition)
	activateResult, err := c.ActivateMemory(ctx, memID)
	if err != nil {
		t.Fatalf("ActivateMemory: %v", err)
	}
	// Response should contain activation_count and new_stability_days
	if _, ok := activateResult["activation_count"]; !ok {
		t.Errorf("ActivateMemory: missing activation_count in response: %v", activateResult)
	}
	t.Logf("activated: activation_count=%v", activateResult["activation_count"])

	// 3. Recall by type filter
	recallParams := url.Values{}
	recallParams.Set("project", testProject)
	recallParams.Set("type", "rule.coding")

	recallResult, err := c.Recall(ctx, recallParams)
	if err != nil {
		t.Fatalf("Recall by type: %v", err)
	}

	recallItems, _ := recallResult["items"].([]any)
	foundByType := false
	for _, item := range recallItems {
		if m, ok := item.(map[string]any); ok && m["id"] == memID {
			foundByType = true
		}
	}
	if !foundByType {
		t.Errorf("recalled items by type rule.coding don't include our memory %s (got %d items)", memID, len(recallItems))
	} else {
		t.Logf("type-based recall found %s in %d items", memID, len(recallItems))
	}

	// 4. Recall without type filter (full project recall) — memory should still appear
	recallAllParams := url.Values{}
	recallAllParams.Set("project", testProject)

	recallAll, err := c.Recall(ctx, recallAllParams)
	if err != nil {
		t.Fatalf("Recall all: %v", err)
	}

	allItems, _ := recallAll["items"].([]any)
	foundAll := false
	for _, item := range allItems {
		if m, ok := item.(map[string]any); ok && m["id"] == memID {
			foundAll = true
			// Verify expected fields on each memory item
			for _, field := range []string{"id", "project", "type", "content"} {
				if _, ok := m[field]; !ok {
					t.Errorf("memory item missing field %q", field)
				}
			}
		}
	}
	if !foundAll {
		t.Errorf("full project recall doesn't include our memory %s (got %d items)", memID, len(allItems))
	} else {
		t.Logf("full-project recall confirmed memory %s present", memID)
	}

	t.Logf("memory recall test passed for %s", memID)
}
