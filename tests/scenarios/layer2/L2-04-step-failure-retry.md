# L2-04 — Step failure resets to idle (retry path)

Tests that a failed step resets current_step_status to idle so the step can be retried.
Version increments on fail (idle→in_progress = +1, in_progress→failed = +1).

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-04 step failure retry",
      wi_type="fix_bug", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-04-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### Start step (version 0→1)
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.version == 1

### Fail step (version 1→2, resets to idle)
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="failed",
      step_attempt_id="sa_l2_04_fail", error_type="tool_error")
ASSERT: response.status == "failed"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 2
NOTE: current_step stays "prepare_context" after failure (not cleared)

### Retry: start again (version 2→3)
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.version == 3

### Complete on retry (version 3→4)
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="completed",
      step_attempt_id="sa_l2_04_retry_ok", artifact_summary="context gathered on retry")
ASSERT: response.status == "completed"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 4

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")

## PASS criteria

Failed step resets to idle at version 2; retry succeeds; final version == 4.
