# E2E-12 — force_takeover on PAUSED wi returns 400

Tests that force_takeover is only valid on RUNNING wi's.
Attempting force_takeover on a PAUSED wi must return 400.
Reference: FnForceTakeover validates wi.status == "running".

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V (writer, original claimer)

## Steps

### Alice claims and pauses a wi
AS ADMIN: pf_create_work_item(project="marketplace", goal="[test] E2E-12 force_takeover paused", wi_type="chore")
Save WI_ID

Generate ALICE_SECRET
AS ALICE: HTTP POST /v1/work_items/WI_ID/claim
body: {"idempotency_key":"e2e12-alice","session_info":{"machine_id":"alice","session_secret":ALICE_SECRET}}
ASSERT: HTTP 200, claim_epoch==1; Save ALICE_ATTEMPT

AS ALICE: HTTP POST /v1/work_items/WI_ID/complete
body: {"status":"paused","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":ALICE_SECRET}
ASSERT: HTTP 200

### Verify wi is paused
AS ADMIN: GET /v1/work_items/WI_ID
ASSERT: response.status == "paused"

### Admin tries force_takeover on PAUSED wi — must return 400
AS ADMIN: HTTP POST /v1/work_items/WI_ID/force_takeover
body: {"reason":"e2e12 test force_takeover on paused wi"}
ASSERT_ERROR: HTTP 400 OR HTTP 409 containing "not running" or "paused"
NOTE: Design requires force_takeover only valid on running wi's.
      If server returns 200 here, this is a server bug.

## Cleanup
AS ALICE: re-claim WI_ID mode=resume, then complete wrapped.

## PASS criteria
force_takeover on paused wi returns 4xx error; wi remains paused and Alice can resume.
