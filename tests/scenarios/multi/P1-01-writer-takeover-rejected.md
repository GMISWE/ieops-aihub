# P1-01 — Writer cannot force-takeover another user's wi (403)

Tests that a writer-role user cannot force_takeover another writer's RUNNING wi.
Only maintainer/admin can take over across users.

## Users
- ALICE_KEY (writer, wi owner)
- BOB_KEY (writer, illegal takeover attempt)
- ADMIN_KEY (admin, setup + legal takeover)

## Steps

### Setup: Admin creates wi, Alice claims
AS ADMIN: HTTP POST http://10.146.0.16:8080/v1/work_items (admin key)
body: {"project":"marketplace","goal":"[test] P1-01 Alice wi p101a","wi_type":"chore","scenario":"coding"}
Save WI_ID

Generate ALICE_SECRET via: python3 -c "import secrets; print(secrets.token_hex(32))"
AS ALICE: HTTP POST .../claim
body: {"idempotency_key":"p1-01-alice","session_info":{"machine_id":"alice","session_secret":ALICE_SECRET}}
ASSERT: HTTP 200, claim_epoch==1; Save ALICE_ATTEMPT

AS ALICE: PATCH .../step body: {"step":"code_change","status":"in_progress","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":ALICE_SECRET}

### Bob (writer) force_takeover — must return 403
AS BOB: HTTP POST .../force_takeover body: {"reason":"p1-01 Bob illegal"}
ASSERT_ERROR: HTTP 403

### Wi still at epoch=1 (Bob's attempt had no effect)
AS ADMIN: GET /v1/work_items/WI_ID
ASSERT: response.current_attempt_epoch == 1

### Admin force_takeover — succeeds
AS ADMIN: HTTP POST .../force_takeover body: {"reason":"p1-01 admin legal"}
ASSERT: HTTP 200

## Cleanup
AS ADMIN: complete WI_ID (failed, force_terminate)

## PASS criteria
Bob gets 403; epoch unchanged; Admin succeeds.
