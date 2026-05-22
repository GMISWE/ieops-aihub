# L2-04 — Step failure resets to idle (retry path)

Tests that a failed step resets current_step_status to idle so the step can be retried.

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-04 step failure retry",
      wi_type="fix_bug", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-04-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### Start step
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.version == 1

### Fail step
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="failed",
      step_attempt_id="sa_l2_04_fail", error_type="tool_error")
ASSERT: response.status == "failed"

### Verify reset to idle
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 2

### Retry (start again after failure) — must succeed
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.version == 3

## Cleanup

CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="failed",
      step_attempt_id="sa_l2_04_cleanup2")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="failed")

## PASS criteria

Failed step resets version to idle; retry (second in_progress) succeeds.
