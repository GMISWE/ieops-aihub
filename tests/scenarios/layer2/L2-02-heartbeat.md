# L2-02 — Heartbeat: resets step_started_at, does not change version or status

Tests that pf_update_step(heartbeat=true) returns heartbeat_ok, step remains in_progress,
and version is NOT incremented (heartbeat is not a state transition).

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-02 heartbeat",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-02-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### Start step
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.version == 1
NOTE: save response.step_started_at as T1

### Send heartbeat (MCP layer sends heartbeat=true, step_id is passed but not used)
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress",
      heartbeat=true)
ASSERT: response.status == "heartbeat_ok"

### Verify step unchanged, version unchanged
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.current_step == "code_change"
ASSERT: response.version == 1
NOTE: version must stay 1 — heartbeat does not increment version
NOTE: step_started_at should be >= T1 (reset by heartbeat)

## Cleanup

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="failed",
      step_attempt_id="sa_l2_02_cleanup")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="failed")

## PASS criteria

heartbeat returns heartbeat_ok; step remains in_progress; version stays 1 (no increment).
