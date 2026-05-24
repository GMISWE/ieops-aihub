# E2E-13 — renew_lease endpoint removed (410 Gone)

Tests that `/work_items/:id/renew` returns 410 Gone. The lease renewal mechanism
was removed in aihub#36: claim is now static ownership, no heartbeat required.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V

## Steps

### Alice claims wi
AS ADMIN: pf_create_work_item(project="marketplace", goal="[test] E2E-13 renew lease removed", wi_type="chore")
Save WI_ID

AS ALICE: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e13-alice", mode="fresh")
ASSERT: ok==true

### Verify endpoint returns 410
HTTP PATCH /v1/work_items/WI_ID/renew
  Authorization: Bearer ALICE_KEY
  Body: {"attempt_id": ATTEMPT_ID, "claim_epoch": 1, "session_secret": "xxx"}

ASSERT: HTTP 410 Gone
ASSERT: response body contains "error" field

### Verify ownership unchanged
AS ALICE: pf_get_work_item(work_item_id=WI_ID)
ASSERT: status == "running" (claim still active despite no renewal)

## Cleanup
AS ALICE: pf_complete_attempt(WI_ID, status="wrapped")

## PASS criteria
PATCH /renew returns 410; wi remains running without renewal calls.
