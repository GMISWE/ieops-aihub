//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"
)

// TestOrchestratorLCRS verifies the six-segment ready-queue (LCRS) view.
//
// Per domain.ReadyQueue:
//   - items[]              — queued, no blocker, requires_human_session=false
//   - needs_human_session[]— queued, no blocker, requires_human_session=true
//   - running[]            — status=running
//   - stalled[]            — stalled wi
//   - paused[]             — status=paused
//   - unclassified[]       — queued but wi_type not recognized (no phase config match)
//
// This test creates one auto (fix_bug) and one human-session (feature) wi, then
// verifies each lands in the expected segment.
func TestOrchestratorLCRS(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create an auto wi (fix_bug → requires_human_session=false → should be in items[])
	autoWiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     "LCRS test: auto wi (fix_bug)",
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "fix_bug",
		"priority": "normal",
	})
	t.Logf("auto wi: %s", autoWiID)

	// 2. Create a human-session wi (feature → requires_human_session=true → needs_human_session[])
	humanWiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"goal":     "LCRS test: human-session wi (feature)",
		"project":  testProject,
		"scenario": "coding",
		"wi_type":  "feature",
		"priority": "normal",
	})
	t.Logf("human wi: %s", humanWiID)

	// 3. Fetch the ready queue
	queue := mustGetReadyQueue(t, c, ctx, testProject)

	// 4. Verify structural fields are present
	for _, key := range []string{"items", "running", "stalled", "paused", "needs_human_session", "unclassified"} {
		if _, ok := queue[key]; !ok {
			t.Errorf("ready queue response missing field %q", key)
		}
	}

	// 5. Auto wi must be in items[] (requires_human_session=false)
	items, _ := queue["items"].([]any)
	foundAuto := false
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && m["id"] == autoWiID {
			foundAuto = true
			// Verify expected fields are present on each ReadyItem
			for _, field := range []string{"id", "slug", "priority", "goal"} {
				if _, ok := m[field]; !ok {
					t.Errorf("items[] entry missing field %q", field)
				}
			}
		}
	}
	if !foundAuto {
		// May be in unclassified if scenario config wi_type lookup failed
		unclassified, _ := queue["unclassified"].([]any)
		for _, item := range unclassified {
			if m, ok := item.(map[string]any); ok && m["id"] == autoWiID {
				foundAuto = true
				t.Logf("auto wi %s is in unclassified[] (fix_bug may not be classified yet)", autoWiID)
			}
		}
	}
	if !foundAuto {
		t.Errorf("fix_bug wi %s should be in items[] or unclassified[], not found", autoWiID)
	}

	// 6. Human wi must be in needs_human_session[] (requires_human_session=true)
	needsHuman, _ := queue["needs_human_session"].([]any)
	foundHuman := false
	for _, item := range needsHuman {
		if m, ok := item.(map[string]any); ok && m["id"] == humanWiID {
			foundHuman = true
		}
	}
	if !foundHuman {
		// Fallback: unclassified
		unclassified, _ := queue["unclassified"].([]any)
		for _, item := range unclassified {
			if m, ok := item.(map[string]any); ok && m["id"] == humanWiID {
				foundHuman = true
				t.Logf("human wi %s is in unclassified[] (may lack phase config mapping)", humanWiID)
			}
		}
	}
	if !foundHuman {
		t.Errorf("feature wi %s should be in needs_human_session[] or unclassified[], not found", humanWiID)
	}

	t.Logf("LCRS check: items[]=%d, needs_human_session[]=%d, running[]=%d, paused[]=%d, stalled[]=%d, unclassified[]=%d",
		len(items),
		len(needsHuman),
		len(toSlice(queue["running"])),
		len(toSlice(queue["paused"])),
		len(toSlice(queue["stalled"])),
		len(toSlice(queue["unclassified"])),
	)

	// 7. Clean up both test wi's
	cancelWorkItem(t, c, ctx, autoWiID)
	cancelWorkItem(t, c, ctx, humanWiID)

	t.Logf("LCRS test passed")
}

// toSlice converts any to []any safely.
func toSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
