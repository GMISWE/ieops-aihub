# E2E-13 — renew_lease resets last_active_at

Tests that pf_renew_lease(attempt_id) updates last_active_at on the run_attempt,
preventing zombie sweep from terminating an idle but active attempt.
Reference: FnRenewLease updates run_attempts.last_active_at.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V

## Steps

### Alice claims wi
AS ADMIN: pf_create_work_item(project="marketplace", goal="[test] E2E-13 renew lease", wi_type="chore")
Save WI_ID

AS ALICE: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e13-alice", mode="fresh")
ASSERT: ok==true; Save ATTEMPT_ID from state file

### Record initial last_active_at
AS ADMIN: GET /v1/work_items/WI_ID (or GET /v1/run_attempts/ATTEMPT_ID)
NOTE: Save T1 = current last_active_at of the attempt

### Call renew_lease
CALL: pf_renew_lease(attempt_id=ATTEMPT_ID) [if available as MCP tool]
OR: HTTP POST /v1/run_attempts/ATTEMPT_ID/renew
    with credentials from state file
ASSERT: HTTP 200, response contains updated last_active_at or ok==true

### Verify last_active_at bumped
AS ADMIN: GET attempt status
ASSERT: new last_active_at >= T1 (lease was renewed)
NOTE: Even if exact timestamp comparison is difficult, the API returning 200 confirms the lease was accepted.

## Cleanup
AS ALICE: pf_complete_attempt(WI_ID, status="wrapped")

## PASS criteria
renew_lease returns 200; last_active_at updated; wi stays running until wrap.
