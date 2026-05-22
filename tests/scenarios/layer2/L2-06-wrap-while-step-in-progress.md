# L2-06 — Wrap while step in_progress: CONFLICT_STEP_IN_PROGRESS + force_terminate

Tests that pf_complete_attempt(wrapped) is rejected when a step is in_progress,
and that force_terminate_step=true forces completion.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] L2-06 wrap while step in_progress",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-06-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### Start a step
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

### Attempt wrap without force_terminate — must be rejected
CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT_ERROR: "CONFLICT_STEP_IN_PROGRESS" OR "step is in_progress"

### Wi still running, step still in_progress
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"

### Wrap with force_terminate_step=true — must succeed
CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped", force_terminate_step=true)
ASSERT: response.ok == true

### Step reset to idle after force terminate
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"

## Cleanup

NOTE: Wi is already wrapped; no further cleanup needed.
NOTE: State file should be deleted (terminal status).

## PASS criteria

Wrap without force rejected; wrap with force succeeds; step reset to idle.
