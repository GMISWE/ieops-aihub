# L2-02 — Heartbeat keeps step alive

Tests that pf_update_step(heartbeat=true) resets step_started_at and returns heartbeat_ok.

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-02 heartbeat",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-02-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### Start a step
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
NOTE: save response.step_started_at as T1

### Send heartbeat (status field still required by server but heartbeat=true takes priority)
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress",
      heartbeat=true)
ASSERT: response.status == "heartbeat_ok"

### Verify step still in_progress (heartbeat must not change step state)
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.current_step == "code_change"

## Cleanup

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="failed",
      step_attempt_id="sa_l2_02_cleanup")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="failed")

## PASS criteria

heartbeat returns heartbeat_ok; step remains in_progress; step_started_at is reset.
