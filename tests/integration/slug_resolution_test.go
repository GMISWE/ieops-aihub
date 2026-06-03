//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"
)

// TestSlugResolution_NoFKViolation is the regression test for aihub#127 / IEBE-1734.
//
// Bug: when a work item is addressed by its SLUG (e.g. "aihub#3") instead of the
// canonical work_items.id, several handlers resolved the slug to a *WorkItem for
// access/credential checks but then used the raw slug in the actual DB operation.
// The slug then reached work_item_id columns that FK-reference work_items(id),
// producing "violates foreign key constraint ... (SQLSTATE 23503)" -> HTTP 500 for
// emit_event / save_artifact (remember) / update_step, and not-found for
// complete/pause/cancel.
//
// Fix: resolve id-or-slug to the canonical id once, then use the canonical id for
// every downstream DB operation. The claim response also now echoes the canonical
// id so the MCP client can persist it.
//
// This test drives the whole lifecycle BY SLUG and asserts every step succeeds.
func TestSlugResolution_NoFKViolation(t *testing.T) {
	ctx := context.Background()
	c := newAdminClient(t)
	waitForHealth(t, c, 30*time.Second)

	// 1. Create a work item; capture its canonical id, then look up its slug.
	wiID := mustCreateWorkItem(t, c, ctx, map[string]any{
		"project": testProject,
		"goal":    "aihub#127 slug-resolution regression " + time.Now().Format(time.RFC3339Nano),
		"wi_type": "fix_bug",
	})
	wi := mustGetWorkItem(t, c, ctx, wiID)
	slug, _ := wi["slug"].(string)
	if slug == "" {
		t.Fatalf("work item missing slug: %v", wi)
	}
	if slug == wiID {
		t.Fatalf("expected slug to differ from canonical id (slug=%q id=%q)", slug, wiID)
	}
	t.Logf("canonical id=%s slug=%s", wiID, slug)

	// 2. Claim BY SLUG. The claim response must echo the canonical id so the client
	//    can key its state file on the canonical id (not the slug).
	claim := mustClaimWorkItem(t, c, ctx, slug, "slug-regression-"+time.Now().Format("150405.000000"))
	if got, _ := claim["id"].(string); got != wiID {
		t.Fatalf("claim-by-slug: response id = %q, want canonical %q", got, wiID)
	}

	// 3. emit_event BY SLUG must succeed (regression: was FK 500 on agent_events).
	if _, err := c.EmitEvent(ctx, map[string]any{
		"event_type":   "note",
		"work_item_id": slug,
		"payload":      map[string]any{"msg": "slug-regression"},
	}); err != nil {
		t.Fatalf("EmitEvent by slug %q: %v", slug, err)
	}

	// 4. remember / save_artifact BY SLUG must succeed (regression: was FK 500 on memories).
	if _, err := c.Remember(ctx, map[string]any{
		"project":      testProject,
		"type":         "fact.note",
		"content":      "slug-regression memory " + time.Now().Format(time.RFC3339Nano),
		"visibility":   "project",
		"dedup_mode":   "off",
		"work_item_id": slug,
	}); err != nil {
		t.Fatalf("Remember by slug %q: %v", slug, err)
	}

	// 5. update_step BY SLUG must succeed (regression: was FK 500 on wi_step_state).
	if _, err := c.UpdateStep(ctx, slug, map[string]any{
		"attempt_id":     claim["attempt_id"],
		"claim_epoch":    claim["claim_epoch"],
		"session_secret": claim["session_secret"],
		"step":           "prepare_context",
		"status":         "in_progress",
	}); err != nil {
		t.Fatalf("UpdateStep by slug %q: %v", slug, err)
	}

	// 6. complete BY SLUG must succeed (regression: FnCompleteAttempt looked up by the
	//    raw slug and found nothing). force_terminate_step because step 5 deliberately
	//    left prepare_context in_progress.
	if _, err := c.CompleteAttempt(ctx, slug, map[string]any{
		"attempt_id":           claim["attempt_id"],
		"claim_epoch":          claim["claim_epoch"],
		"session_secret":       claim["session_secret"],
		"status":               "wrapped",
		"force_terminate_step": true,
	}); err != nil {
		t.Fatalf("CompleteAttempt by slug %q: %v", slug, err)
	}
}
