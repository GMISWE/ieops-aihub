# E2E-10 — Force takeover: same user re-claims a running wi

Tests that the same user can implicitly take over a running wi by claiming it with a
new idempotency_key. The old attempt becomes "superseded".
(Implements §7.2.1: same user_id → always allowed)

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-10 force takeover same user",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

### Initial claim (simulates Agent 1 owning the wi)
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-10-agent1",
      mode="fresh")
ASSERT: response.ok == true
NOTE: save response.attempt_id as ATTEMPT_1, response.claim_epoch as EPOCH_1 (==1)

## Steps

### Agent 1 starts a step
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

### Agent 2 (same user, different session) re-claims with new idempotency_key
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-10-agent2",
      mode="fresh")
ASSERT: response.ok == true
ASSERT: response.attempt_id != ATTEMPT_1
ASSERT: response.claim_epoch == 2
NOTE: Old attempt ATTEMPT_1 is now superseded; step may still be in_progress

### Agent 1's old state file is now stale (epoch=1, server has epoch=2)
NOTE: If testing with actual state file manipulation:
NOTE: Any pf_update_step call using ATTEMPT_1 credentials should fail

### Verify wi status still running with new attempt
CALL: pf_get_work_item(work_item_id=WI_ID)
ASSERT: response.status == "running"
ASSERT: response.current_attempt_epoch == 2

### New attempt can proceed (force_terminate stale in_progress step)
CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped", force_terminate_step=true)
ASSERT: response.ok == true

## Cleanup

NOTE: Wi is wrapped. State file deleted.

## PASS criteria

Same user re-claim with new key creates new attempt (epoch=2); old attempt superseded;
new attempt can wrap with force_terminate.
