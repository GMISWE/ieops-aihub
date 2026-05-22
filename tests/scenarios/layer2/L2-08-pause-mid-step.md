# L2-08 — Pause while step in_progress (force_terminate on pause)

Tests that pausing a wi with an in_progress step force-terminates the step,
writing a wi_step_completions row with status=failed before releasing the lease.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] L2-08 pause mid step force terminate",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-08-claim-1", mode="fresh")
ASSERT: response.ok == true

## Steps

### Start a step (simulate mid-execution)
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.version == 1

### Pause with step in_progress — force_terminate implicitly applied
CALL: pf_complete_attempt(work_item_id=WI_ID, status="paused", force_terminate_step=true)
ASSERT: response.ok == true

### Step reset to idle after pause+force_terminate
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
NOTE: version incremented by force_terminate (in_progress→failed transition)

### State file retained on pause (non-terminal)
NOTE: Verify WORKSPACE_ROOT/.polyforge/state/WI_ID.json still exists

### Resume and verify step can be restarted
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-08-claim-2", mode="resume")
ASSERT: response.ok == true

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"
NOTE: Step restart successful after force_terminate + pause + resume

## Cleanup

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
      step_attempt_id="sa_l2_08_done")
CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")

## PASS criteria

Pause+force_terminate resets step to idle; state file kept; resume allows step restart.
