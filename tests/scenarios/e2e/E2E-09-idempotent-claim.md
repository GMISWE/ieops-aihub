# E2E-09 — Idempotent claim: same idempotency_key returns same attempt

Tests that re-claiming with the same idempotency_key returns the same attempt_id
and claim_epoch (no new attempt created). Critical for network retry safety.
(Implements C6-2: run_attempts.go:142-187)

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-09 idempotent claim",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

## Steps

### First claim
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-09-idem-key",
      mode="fresh")
ASSERT: response.ok == true
NOTE: save response.attempt_id as ATTEMPT_1
NOTE: save response.claim_epoch as EPOCH_1 (should be 1)

### Second claim with SAME idempotency_key (simulates network retry)
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-09-idem-key",
      mode="fresh")
ASSERT: response.ok == true
ASSERT: response.attempt_id == ATTEMPT_1
ASSERT: response.claim_epoch == EPOCH_1
NOTE: Same attempt returned — no new attempt created

### Third claim with DIFFERENT idempotency_key creates new attempt
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-09-different-key",
      mode="fresh")
ASSERT: response.ok == true
ASSERT: response.attempt_id != ATTEMPT_1
ASSERT: response.claim_epoch == 2
NOTE: New attempt created — old attempt superseded

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")

## PASS criteria

Same idempotency_key returns same attempt_id + epoch; different key creates new attempt.
