# L2-05 — Stale credential: STALE_LOCAL_CREDENTIAL + auto state file cleanup

Tests that when claim_epoch in state file doesn't match server, pf_update_step
returns STALE_LOCAL_CREDENTIAL and auto-deletes the state file.

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-05 stale credential",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-05-claim-1", mode="fresh")
ASSERT: response.ok == true
NOTE: save response.attempt_id as ATTEMPT_1

### Corrupt state file (simulate stale epoch)
NOTE: Edit <WORKSPACE>/.polyforge/state/<WI_ID>.json, set claim_epoch to 999

## Steps

### Step call with stale credentials
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT_ERROR: "STALE_LOCAL_CREDENTIAL"

### State file must be deleted
NOTE: Verify <WORKSPACE>/.polyforge/state/<WI_ID>.json does NOT exist

### Re-claim creates fresh state
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-05-claim-2", mode="fresh")
ASSERT: response.ok == true
ASSERT: response.claim_epoch > 1

### Step now works with fresh credentials
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

## Cleanup

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="failed",
      step_attempt_id="sa_l2_05_cleanup")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="failed")

## PASS criteria

Stale epoch returns STALE_LOCAL_CREDENTIAL; state file deleted; re-claim + retry succeeds.
