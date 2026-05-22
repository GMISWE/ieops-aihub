# E2E-03 — Pause and resume preserves step progress

Tests that pausing a wi keeps the state file, and resuming picks up from where left off.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-03 pause and resume step continuity",
      wi_type="fix_bug", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-03-claim-1", mode="fresh")
ASSERT: response.ok == true
NOTE: save response.attempt_id as ATTEMPT_1, response.claim_epoch as EPOCH_1

## Steps

### Advance to prepare_context completed
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="completed",
      step_attempt_id="sa_e2e03_ctx", artifact_summary="context ready")
ASSERT: response.status == "completed"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.version == 2

### Pause mid-work (before code_change starts)
CALL: pf_complete_attempt(work_item_id=WI_ID, status="paused")
ASSERT: response.ok == true

NOTE: Verify state file still exists at WORKSPACE/.polyforge/state/WI_ID.json
NOTE: Verify wi status == "paused" via pf_get_work_item

### Resume (re-claim)
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-03-claim-2",
      mode="resume")
ASSERT: response.ok == true
ASSERT: response.claim_epoch > EPOCH_1

### Verify step state preserved after resume
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 2
NOTE: prepare_context already completed; next step is code_change

### Continue from code_change
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
      step_attempt_id="sa_e2e03_code", artifact_summary="code done post-resume")
ASSERT: response.status == "completed"

CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="in_progress")
CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="completed",
      step_attempt_id="sa_e2e03_pr", artifact_summary="PR created post-resume")

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT: response.ok == true
NOTE: Verify state file deleted

## PASS criteria

State file kept on pause; resume gets new claim_epoch;
step version preserved; remaining steps complete; wrap succeeds.
