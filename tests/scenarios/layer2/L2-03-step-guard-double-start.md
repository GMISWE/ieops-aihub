# L2-03 — Guard: cannot start in_progress step again

Tests that idle→in_progress is allowed but in_progress→in_progress is rejected (409).

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-03 step guard",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-03-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### First start — allowed
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

### Second start — must be rejected
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT_ERROR: "CONFLICT_CAS_FAILED" OR "step already in_progress"

### Step state unchanged after rejected call
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.current_step == "code_change"
ASSERT: response.version == 1

## Cleanup

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="failed",
      step_attempt_id="sa_l2_03_cleanup")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="failed")

## PASS criteria

Second in_progress call returns 409; step state unchanged.
