# E2E-18 — goal field is immutable after wi creation

Tests that pf_update_work_item(goal=...) is rejected — goal is immutable
per design (CLAUDE.md: "goal is immutable — pf_update_work_item rejects it").
Reference: `internal/domain/work_items.go` (`UpdateWorkItem`) — a goal update
without `goal_change_reason` is rejected with 400.
NOTE: goal is no longer unconditionally immutable — supplying
`goal_change_reason` (min 10 chars) makes it editable. This scenario's bare
`goal=` update, with no reason, must still 4xx, so the steps below still hold;
only the original "not in patchable fields" framing is stale.

## Users
- ADMIN_KEY=$ADMIN_KEY

## Steps

### Create wi with original goal
AS ADMIN: pf_create_work_item(project="marketplace",
  goal="[test] E2E-18 original goal — must not change", wi_type="chore")
ASSERT: response.goal == "[test] E2E-18 original goal — must not change"
Save WI_ID, ORIGINAL_GOAL

### Try to update goal — must be rejected
AS ADMIN: pf_update_work_item(work_item_id=WI_ID,
  goal="[test] E2E-18 CHANGED goal — must not be saved")
ASSERT_ERROR: HTTP 400 or HTTP 422 containing "goal" and "immutable" or "not allowed"
NOTE: If server returns 200, check that goal was NOT actually changed (silently ignored)

### Verify goal unchanged
AS ADMIN: HTTP GET /v1/work_items/WI_ID
ASSERT: response.goal == ORIGINAL_GOAL

### Other fields CAN be updated (control group — priority is patchable)
AS ADMIN: pf_update_work_item(work_item_id=WI_ID, priority="high")
ASSERT: response.priority == "high"

AS ADMIN: HTTP GET /v1/work_items/WI_ID
ASSERT: response.goal == ORIGINAL_GOAL  (goal still unchanged after valid update)
ASSERT: response.priority == "high"

## Cleanup
AS ADMIN: pf_cancel_work_item(work_item_id=WI_ID, reason="e2e-18 cleanup")

## PASS criteria
goal update rejected with 4xx; other fields patchable; goal preserved after valid update.
