# E2E-04 — Resource lock conflict: second claim blocked

Tests that two wi's competing for the same git_branch lock are correctly blocked at claim time.
Also verifies predict_conflicts advisory (declare only) vs hard lock enforcement.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-04 lock holder",
      wi_type="chore", priority="normal",
      declared_resources=[{"resource_type": "git_branch",
                           "resource_key": "marketplace/e2e-04-conflict-branch"}])
NOTE: save response.id as WI_A

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-04 lock contender",
      wi_type="chore", priority="normal",
      declared_resources=[{"resource_type": "git_branch",
                           "resource_key": "marketplace/e2e-04-conflict-branch"}])
NOTE: save response.id as WI_B

## Steps

### WI_A acquires lock
CALL: pf_claim_work_item(work_item_id=WI_A, idempotency_key="e2e-04-claim-a",
      mode="fresh",
      requested_locks=[{"resource_type": "git_branch",
                        "resource_key": "marketplace/e2e-04-conflict-branch"}])
ASSERT: response.ok == true
ASSERT: len(response.acquired_locks) == 1
ASSERT: response.acquired_locks[0].resource_key == "marketplace/e2e-04-conflict-branch"

### WI_B claim blocked by lock
CALL: pf_claim_work_item(work_item_id=WI_B, idempotency_key="e2e-04-claim-b",
      mode="fresh",
      requested_locks=[{"resource_type": "git_branch",
                        "resource_key": "marketplace/e2e-04-conflict-branch"}])
ASSERT_ERROR: "CONFLICT_LOCK_TAKEN"

### WI_B status is still queued (not running)
CALL: pf_get_work_item(work_item_id=WI_B)
ASSERT: response.status == "queued"

### WI_A wrap releases lock
CALL: pf_complete_attempt(work_item_id=WI_A, status="wrapped")
ASSERT: response.ok == true

### WI_B claim now succeeds (lock released)
CALL: pf_claim_work_item(work_item_id=WI_B, idempotency_key="e2e-04-claim-b-retry",
      mode="fresh",
      requested_locks=[{"resource_type": "git_branch",
                        "resource_key": "marketplace/e2e-04-conflict-branch"}])
ASSERT: response.ok == true
ASSERT: len(response.acquired_locks) == 1

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_B, status="wrapped")

## PASS criteria

WI_B claim blocked while WI_A holds lock; WI_B succeeds after WI_A wraps.
