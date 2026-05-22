# L2-05 — Stale credential: STALE_LOCAL_CREDENTIAL + auto state file cleanup

Tests that when claim_epoch in the local state file is stale (doesn't match server),
the MCP layer returns STALE_LOCAL_CREDENTIAL and auto-deletes the state file.

Note: STALE_LOCAL_CREDENTIAL is produced by the MCP wrapper (tools_step.go), not the
HTTP server. The HTTP server returns CONFLICT_EPOCH_MISMATCH. This scenario requires
the full MCP transport path.

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-05 stale credential",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-05-claim-1", mode="fresh")
ASSERT: response.ok == true
NOTE: save response.attempt_id as ATTEMPT_1, response.claim_epoch (should be 1)

### Corrupt state file to simulate stale epoch
NOTE: Read WORKSPACE_ROOT/.polyforge/state/WI_ID.json
NOTE: Set claim_epoch to 999
NOTE: Write back to same path (keep all other fields intact)

## Steps

### Step call with stale epoch → MCP detects mismatch, deletes state file
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT_ERROR: "STALE_LOCAL_CREDENTIAL"

### State file must be deleted
NOTE: Verify WORKSPACE_ROOT/.polyforge/state/WI_ID.json does NOT exist
NOTE: If file still exists, this is a FAIL

### Re-claim with new idempotency_key (creates attempt #2)
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-05-claim-2", mode="fresh")
ASSERT: response.ok == true
ASSERT: response.claim_epoch == 2

### Fresh state file created; step works now
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

## Cleanup

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="failed",
      step_attempt_id="sa_l2_05_cleanup")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="failed")

## PASS criteria

Stale epoch triggers STALE_LOCAL_CREDENTIAL; state file auto-deleted;
re-claim creates epoch=2; step update succeeds with fresh credentials.
