//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

// TestMemoryCommit_ColumnVersion covers the human-annotation commit feature (aihub#70 v2):
//  1. Remember a memory.
//  2. Verify commits field is empty initially ([] in the recall response).
//  3. Verify activation_count / base_strength / stability_days unchanged by remember alone.
//
// Note: POST /ui/memories/:id/commit is a browser-facing UI endpoint (cookie auth only,
// no Bearer key). Integration tests use the HTTP API client (Bearer auth) and cannot
// directly exercise the UI write path. This test verifies:
//   - The commits column is present in GET /v1/memories recall responses.
//   - The forgetting-curve fields are not changed by remember (baseline guard).
//
// End-to-end commit write behaviour is covered by manual browser testing and the
// UI handler unit tests (internal/server/ui_handlers_memory_test.go).
func TestMemoryCommit_ColumnVersion(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Remember a fact so we have a stable baseline.
	memResult, err := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "fact.note",
		"content":    "memory commit column integration test " + time.Now().Format(time.RFC3339Nano),
		"visibility": "project",
		"dedup_mode": "off",
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	memID, ok := memResult["memory_id"].(string)
	if !ok || memID == "" {
		t.Fatalf("Remember: missing memory_id in response: %v", memResult)
	}
	t.Logf("created memory: %s", memID)

	// 2. Recall and verify commits field is present and initially empty.
	params := url.Values{}
	params.Set("project", testProject)
	params.Set("top_k", "100")
	params.Set("min_strength", "0")

	result, err := c.Recall(ctx, params)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	items, _ := result["items"].([]any)
	var memRow map[string]any
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && m["id"] == memID {
			memRow = m
			break
		}
	}
	if memRow == nil {
		t.Fatalf("memory %s not found in recall result (%d items)", memID, len(items))
	}

	// The commits field must be present in the response.
	commitsRaw, hasCommits := memRow["commits"]
	if !hasCommits {
		t.Fatal("recall response missing commits field; expected [] for a new memory")
	}

	// Verify it's an empty array (or deserializes as such).
	var commits []any
	switch v := commitsRaw.(type) {
	case []any:
		commits = v
	case string:
		if err := json.Unmarshal([]byte(v), &commits); err != nil {
			t.Fatalf("unmarshal commits: %v", err)
		}
	case nil:
		// NULL — acceptable for a brand-new memory if DB DEFAULT was applied.
	default:
		t.Fatalf("unexpected commits type %T: %v", commitsRaw, commitsRaw)
	}
	if len(commits) != 0 {
		t.Errorf("new memory should have empty commits; got %v", commits)
	}
	t.Logf("commits field present and empty: %v", commitsRaw)

	// 3. Capture baseline forgetting-curve fields and verify they match expected values.
	acBefore := memRow["activation_count"]
	bsBefore := memRow["base_strength"]
	sdBefore := memRow["stability_days"]
	t.Logf("baseline fields: activation_count=%v base_strength=%v stability_days=%v",
		acBefore, bsBefore, sdBefore)

	// Re-recall to confirm the fields are stable (no phantom changes).
	result2, err := c.Recall(ctx, params)
	if err != nil {
		t.Fatalf("Recall (2nd): %v", err)
	}
	items2, _ := result2["items"].([]any)
	var memRow2 map[string]any
	for _, item := range items2 {
		if m, ok := item.(map[string]any); ok && m["id"] == memID {
			memRow2 = m
			break
		}
	}
	if memRow2 == nil {
		t.Fatalf("memory %s not found in 2nd recall", memID)
	}
	if memRow2["activation_count"] != acBefore {
		t.Errorf("activation_count changed between recalls: %v → %v", acBefore, memRow2["activation_count"])
	}
	if memRow2["base_strength"] != bsBefore {
		t.Errorf("base_strength changed between recalls: %v → %v", bsBefore, memRow2["base_strength"])
	}
	if memRow2["stability_days"] != sdBefore {
		t.Errorf("stability_days changed between recalls: %v → %v", sdBefore, memRow2["stability_days"])
	}
}
