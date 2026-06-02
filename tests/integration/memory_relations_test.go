//go:build integration

package integration_test

// TestMemoryRelations verifies the memory_relations join table (aihub#74 Stream A):
//
//  1. Remember memA (no related links).
//  2. Remember memB with related_memory_ids=[memA].
//  3. Recall → memB.related contains memA (forward enrichment).
//  4. Redact memA → memB.related no longer contains memA. loadForwardRelations filters
//     status != 'redacted' (and applies the caller's visibility/project scope), so a
//     redacted (or private/admin/cross-project) memory cannot leak its id/type/content
//     through the relation graph.
//
// Backlinks and single-memory (GetMemoryByID) enrichment are intentionally NOT covered:
// they are deferred to the follow-up that wires them into a caller-scoped handler (there
// is no JSON GET /v1/memories/:id endpoint exposed to the client today).
//
// Run with: cd tests/integration && make test
// Requires a live aihub server at AIHUB_URL (default http://localhost:8081) with the
// 0024_memory_relations migration applied.

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestMemoryRelations(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. memA baseline (no related links).
	memAResult, err := c.Remember(ctx, map[string]any{
		"project":    testProject,
		"type":       "fact.test",
		"content":    "memory relations: memA baseline fact",
		"visibility": "project",
		"dedup_mode": "off",
	})
	if err != nil {
		t.Fatalf("Remember memA: %v", err)
	}
	memAID, ok := memAResult["memory_id"].(string)
	if !ok || memAID == "" {
		t.Fatalf("Remember memA: missing memory_id: %v", memAResult)
	}

	// 2. memB references memA.
	memBResult, err := c.Remember(ctx, map[string]any{
		"project":            testProject,
		"type":               "fact.test",
		"content":            "memory relations: memB references memA",
		"visibility":         "project",
		"dedup_mode":         "off",
		"related_memory_ids": []string{memAID},
	})
	if err != nil {
		t.Fatalf("Remember memB: %v", err)
	}
	memBID, ok := memBResult["memory_id"].(string)
	if !ok || memBID == "" {
		t.Fatalf("Remember memB: missing memory_id: %v", memBResult)
	}

	// memBRelatedHasMemA recalls and reports whether memB.related currently contains memA.
	memBRelatedHasMemA := func() bool {
		params := url.Values{}
		params.Set("project", testProject)
		params.Set("type", "fact.test")
		res, rerr := c.Recall(ctx, params)
		if rerr != nil {
			t.Fatalf("Recall: %v", rerr)
		}
		items, _ := res["items"].([]any)
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok || m["id"] != memBID {
				continue
			}
			related, _ := m["related"].([]any)
			for _, r := range related {
				if ref, ok := r.(map[string]any); ok && ref["id"] == memAID {
					return true
				}
			}
		}
		return false
	}

	// 3. Forward enrichment: memB.related contains memA.
	if !memBRelatedHasMemA() {
		t.Errorf("Recall: memB (%s) did not have memA (%s) in related", memBID, memAID)
	}

	// 4. Redaction/visibility filter: redacting memA must drop it from memB.related.
	if _, rerr := c.RedactMemory(ctx, memAID, map[string]any{}); rerr != nil {
		t.Skipf("RedactMemory memA failed (needs admin key?): %v", rerr)
	}
	if memBRelatedHasMemA() {
		t.Errorf("redacted memA (%s) still leaks through memB.related — loadForwardRelations status filter not applied", memAID)
	}
	t.Logf("redacted memA excluded from memB.related (privacy filter ok); memB=%s", memBID)
}
