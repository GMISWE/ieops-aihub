# M02 — Viewer read-only: Carol can see but not mutate

Real-world scenario: Carol (viewer on marketplace project) monitors team progress.
She can read wi state, events, ready queue, memories — but cannot claim, update steps,
or create wi's.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig  (xiaokang.w, admin)
- BOB_KEY=pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR  (Test Writer Bob, writer)
- CAROL_KEY=pf_k1_2j5gcKsUTBRazaEWydEQ1i4bDRwdR6Bh  (Test Viewer Carol, viewer on marketplace)

## Steps

### Setup: Admin creates wi, Bob claims it
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M02 viewer read-only validation",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

AS BOB:
HTTP POST /v1/work_items/WI_ID/claim
body: {"idempotency_key":"m02-bob-claim","session_info":{"machine_id":"bob-ws","session_secret":"<64hex>"}}
ASSERT: response.claim_epoch == 1
NOTE: save response.attempt_id as BOB_ATTEMPT

AS BOB:
HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"code_change","status":"in_progress","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}

### Carol: read operations (all must succeed)

AS CAROL:
HTTP GET /v1/work_items/WI_ID
ASSERT: HTTP 200
ASSERT: response.id == WI_ID
ASSERT: response.status == "running"

AS CAROL:
HTTP GET /v1/work_items/WI_ID/step
ASSERT: HTTP 200
ASSERT: response.current_step_status == "in_progress"

AS CAROL:
HTTP GET /v1/events?work_item_id=WI_ID&limit=10
ASSERT: HTTP 200
ASSERT: response.events is a list

AS CAROL:
HTTP GET /v1/work_items?project=marketplace&status=running
ASSERT: HTTP 200
ASSERT: any wi.id == WI_ID

### Carol: write operations (all must be REJECTED)

AS CAROL:
HTTP POST /v1/work_items
body: {"project":"marketplace","goal":"[test] M02 carol tries to create","wi_type":"chore"}
ASSERT_ERROR: HTTP 403

AS CAROL:
HTTP POST /v1/work_items/WI_ID/claim
body: {"idempotency_key":"m02-carol-claim","session_info":{"machine_id":"carol-ws","session_secret":"<64hex>"}}
ASSERT_ERROR: HTTP 403

AS CAROL:
HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"code_change","status":"completed","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"fake"}
ASSERT_ERROR: HTTP 403

AS CAROL:
HTTP POST /v1/work_items/WI_ID/complete
body: {"status":"wrapped","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"fake"}
ASSERT_ERROR: HTTP 403

## Cleanup

AS BOB:
HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"code_change","status":"completed","step_attempt_id":"sa_m02_done","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
AS BOB:
HTTP POST /v1/work_items/WI_ID/complete
body: {"status":"wrapped","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}

## PASS criteria

All Carol GET operations return 200; all Carol write operations return 403.
Bob's writes succeed. Carol never mutates server state.
