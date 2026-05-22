# P1-07 — Cancel permission: reporter can cancel; other writer cannot (403)

## Users
- ALICE_KEY (reporter/creator of wi)
- BOB_KEY (writer, cannot cancel Alice's wi)
- ADMIN_KEY (can cancel any wi)

## Steps

### Alice creates wi (she is reporter)
AS ALICE: HTTP POST /v1/work_items
body: {"project":"marketplace","goal":"[test] P1-07 Alice wi p107a","wi_type":"chore","scenario":"coding"}
ASSERT: reporter_user_id == "u_CX6BMioR"
Save WI_ALICE

### Bob tries to cancel — 403
AS BOB: HTTP DELETE /v1/work_items/WI_ALICE
ASSERT_ERROR: HTTP 403

AS ADMIN: GET /v1/work_items/WI_ALICE -> ASSERT: status=="queued" (unchanged)

### Alice cancels her own wi
AS ALICE: HTTP DELETE /v1/work_items/WI_ALICE
ASSERT: HTTP 200 or 204

AS ADMIN: GET /v1/work_items/WI_ALICE -> ASSERT: status=="cancelled"

### Admin cancels another of Alice's wi's
AS ALICE: create WI_ALICE2
AS ADMIN: pf_cancel_work_item(WI_ALICE2, reason="p1-07 admin cancel")
ASSERT: ok==true
AS ADMIN: GET /v1/work_items/WI_ALICE2 -> ASSERT: status=="cancelled"

## PASS criteria
Bob gets 403; Alice cancels own; Admin cancels any.
