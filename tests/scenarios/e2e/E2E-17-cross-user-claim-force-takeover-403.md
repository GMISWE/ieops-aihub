# E2E-17 — Cross-user force_takeover via claim (403 for writer)

Tests that a writer trying to claim a RUNNING wi owned by another user
via force_takeover=true returns 403 (not maintainer/admin).
Complements E2E-10 (same-user implicit takeover) and P1-01 (force_takeover endpoint 403).
Reference: FnClaimWorkItem forceTakeoverPerm check — cross-user requires maintainer/admin.

## Users
- ALICE_KEY (writer, wi owner)
- BOB_KEY (writer, different user — must get 403)
- ADMIN_KEY (admin, for setup + successful cross-user claim)

## Steps

### Alice claims wi
AS ADMIN: pf_create_work_item(project="marketplace", goal="[test] E2E-17 cross-user claim e17a", wi_type="chore")
Save WI_ID

Generate ALICE_SECRET
AS ALICE: HTTP POST /v1/work_items/WI_ID/claim
body: {"idempotency_key":"e17-alice","session_info":{"machine_id":"alice","session_secret":ALICE_SECRET}}
ASSERT: HTTP 200, claim_epoch==1

### Bob (writer) tries force_takeover via claim endpoint
AS BOB:
Generate BOB_SECRET
HTTP POST /v1/work_items/WI_ID/claim
body: {"idempotency_key":"e17-bob-force","session_info":{"machine_id":"bob","session_secret":BOB_SECRET},
      "force_takeover":true}
ASSERT_ERROR: HTTP 403 "requires maintainer role" or "FORBIDDEN"

### Wi still at epoch=1 (Bob's attempt had no effect)
AS ADMIN: HTTP GET /v1/work_items/WI_ID
ASSERT: response.current_attempt_epoch == 1

### Admin CAN force-takeover via claim (admin overrides permission check)
AS ADMIN: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e17-admin-force",
  mode="fresh", force_takeover=true)
ASSERT: response.ok == true
ASSERT: response.claim_epoch == 2

## Cleanup
AS ADMIN: pf_complete_attempt(work_item_id=WI_ID, status="wrapped", force_terminate_step=true)

## PASS criteria
Bob's force_takeover via claim returns 403; epoch unchanged; Admin succeeds with epoch=2.
