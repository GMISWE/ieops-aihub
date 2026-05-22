# E2E-08 — Auto-derive locks from declared_resources when requested_locks is empty

Tests that when pf_claim_work_item is called without explicit requested_locks=[],
the server automatically derives locks from the wi's declared_resources.
(Implements run_attempts.go:286-308)

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-08 auto-derive locks from declared_resources",
      wi_type="chore", priority="normal",
      declared_resources=[{
        "type": "repo",
        "uri": "repo:marketplace",
        "intent": "exclusive",
        "task_branch": "polyforge/e2e-08-auto-lock"
      }])
NOTE: save response.id as WI_ID

## Steps

### Claim WITHOUT requested_locks — server derives from declared_resources
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-08-claim", mode="fresh")
NOTE: No requested_locks parameter passed
ASSERT: response.ok == true
ASSERT: len(response.acquired_locks) == 1
ASSERT: response.acquired_locks[0].resource_type == "git_branch"
ASSERT: response.acquired_locks[0].resource_key == "marketplace/polyforge/e2e-08-auto-lock"

### Verify lock is active: second wi competing for same resource must be blocked
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-08 contender",
      wi_type="chore", priority="normal",
      declared_resources=[{
        "type": "repo",
        "uri": "repo:marketplace",
        "intent": "exclusive",
        "task_branch": "polyforge/e2e-08-auto-lock"
      }])
NOTE: save response.id as WI_CONTENDER

CALL: pf_claim_work_item(work_item_id=WI_CONTENDER, idempotency_key="e2e-08-contender-claim",
      mode="fresh")
ASSERT_ERROR: "CONFLICT_LOCK_TAKEN"
NOTE: If no error, auto-derive failed to acquire the lock

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
CALL: pf_cancel_work_item(work_item_id=WI_CONTENDER, reason="e2e-08 cleanup")

## PASS criteria

Claim without requested_locks acquires git_branch lock derived from declared_resources;
second wi with same resource is blocked.
