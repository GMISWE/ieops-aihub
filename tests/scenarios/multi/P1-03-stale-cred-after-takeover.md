# P1-03 — Stale credentials after force-takeover

Tests that Alice's old credentials (epoch=1) fail after Admin force-takes over (epoch=2).

## Users
- ALICE_KEY (original claimer, epoch=1)
- ADMIN_KEY (force-takes over, creating epoch=2)

## Steps

### Alice claims (epoch=1)
AS ADMIN: create wi, save WI_ID
Generate ALICE_SECRET
AS ALICE: claim -> epoch=1; Save ALICE_ATTEMPT

AS ALICE: PATCH /step (code_change in_progress) -> ASSERT: HTTP 200

### Admin force-takeover (epoch becomes 2)
AS ADMIN: pf_force_takeover(work_item_id=WI_ID, reason="p1-03 test")

### Alice uses OLD epoch=1 credentials
AS ALICE: PATCH /v1/work_items/WI_ID/step
body: {"step":"code_change","status":"completed","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":ALICE_SECRET}
ASSERT_ERROR: HTTP 409 containing CONFLICT_EPOCH_MISMATCH or ATTEMPT_MISMATCH

### Admin completes wi with new credentials
AS ADMIN: pf_complete_attempt(WI_ID, status="wrapped", force_terminate_step=true)
ASSERT: ok==true

## PASS criteria
Alice's epoch=1 rejected; Admin wraps cleanly with epoch=2.
