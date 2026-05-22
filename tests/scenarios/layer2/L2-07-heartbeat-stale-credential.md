# L2-07 — Heartbeat with stale credential

Tests that heartbeat path also validates credentials via VerifyAttemptCredentialPool,
and returns an appropriate error when state file has stale epoch.

Note: The heartbeat PATCH /v1/work_items/:id/step endpoint requires valid credentials
(attempt_id, claim_epoch, session_secret). Stale credentials should fail just like
a regular step update.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] L2-07 heartbeat stale credential",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-07-claim-1", mode="fresh")
ASSERT: response.ok == true

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

### Corrupt state file
NOTE: Edit WORKSPACE_ROOT/.polyforge/state/WI_ID.json, set claim_epoch to 999

## Steps

### Heartbeat with stale state file
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress",
      heartbeat=true)
ASSERT_ERROR: "STALE_LOCAL_CREDENTIAL" OR "CONFLICT_EPOCH_MISMATCH" OR "ATTEMPT_MISMATCH"
NOTE: Specific error depends on whether MCP layer or HTTP server detects it first

### State file deleted after credential failure
NOTE: Verify WORKSPACE_ROOT/.polyforge/state/WI_ID.json does NOT exist

## Cleanup

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-07-claim-2", mode="fresh")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="failed", force_terminate_step=true)

## PASS criteria

Heartbeat with stale epoch returns an error (not heartbeat_ok);
state file deleted by MCP layer; re-claim re-establishes valid credentials.
