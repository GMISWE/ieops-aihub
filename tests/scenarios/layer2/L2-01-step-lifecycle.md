# L2-01 — Step Lifecycle: idle → in_progress → completed

Tests the basic step state machine for a fix_bug wi (3 steps: prepare_context → code_change → commit_and_pr).
Verifies version counter increments correctly at each transition.

## Setup

CALL: pf_create_work_item(project="marketplace", goal="[test] L2-01 step lifecycle",
      wi_type="fix_bug", priority="normal")
ASSERT: response.status == "queued"
ASSERT: response.wi_type == "fix_bug"
NOTE: save response.id as WI_ID, response.slug as SLUG

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l2-01-claim", mode="fresh")
ASSERT: response.ok == true
ASSERT: response.slug == SLUG
ASSERT: response.project == "marketplace"

## Steps

### Initial state
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 0
NOTE: Server returns version=0 when no wi_step_state row exists yet

### Transition 1: idle → prepare_context/in_progress (version: 0→1)
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step == "prepare_context"
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.step_started_at != null
ASSERT: response.version == 1

### Transition 2: in_progress → idle/completed (version: 1→2)
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="completed",
      step_attempt_id="sa_l2_01_ctx", artifact_summary="L2-01 context gathered")
ASSERT: response.status == "completed"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 2

### Transitions 3+4: code_change (version: 2→3→4)
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.version == 3

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
      step_attempt_id="sa_l2_01_code", artifact_summary="L2-01 code change done")
ASSERT: response.status == "completed"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.version == 4

### Transitions 5+6: commit_and_pr (version: 4→5→6)
CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="completed",
      step_attempt_id="sa_l2_01_pr", artifact_summary="L2-01 PR created")
ASSERT: response.status == "completed"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 6

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT: response.ok == true

## PASS criteria

All 6 transitions succeed; version increments 0→1→2→3→4→5→6.
