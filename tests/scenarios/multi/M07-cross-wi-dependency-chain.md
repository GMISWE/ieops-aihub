# M07 — Cross-wi dependency chain: B blocked by A, auto-unblocks on wrap

Real-world scenario: Wi B depends on Wi A (database migration must complete before
API refactor). B stays blocked and out of ready queue until A wraps.
Tests the dependency enforcement and auto-unblock flow.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig  (admin, creates wi's)
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V  (Test Agent Alice, does WI_A)
- BOB_KEY=pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR  (Test Writer Bob, waits for WI_B)

## Steps

### Admin: create WI_A (database migration — prerequisite)
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M07 Step 1: run database migration (prerequisite)",
      wi_type="chore", priority="high")
NOTE: save response.id as WI_A, response.slug as SLUG_A

### Admin: create WI_B blocked by WI_A
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M07 Step 2: API refactor (blocked until migration done)",
      wi_type="fix_bug", priority="normal",
      blocked_by=[WI_A])
ASSERT: response.status == "blocked"
NOTE: save response.id as WI_B
NOTE: WI_B is immediately blocked because blocked_by=[WI_A] is set at creation time

### Ready queue: WI_A in items[], WI_B in blocked[] or not in items[]
AS ADMIN:
CALL: pf_get_ready_queue(project="marketplace")
ASSERT: any(item for item in response.items if item.id == WI_A)
ASSERT: not any(item for item in response.items if item.id == WI_B)

### Bob tries to claim WI_B — must be blocked
AS BOB:
HTTP POST /v1/work_items/WI_B/claim
body: {"idempotency_key":"m07-bob-early-claim","session_info":{"machine_id":"bob-ws","session_secret":"<64hex_B>"}}
ASSERT_ERROR: HTTP 409 "CONFLICT_TERMINAL_STATE" (wi status is "blocked" — treated as terminal for claim purposes)

### Alice claims and completes WI_A
AS ALICE:
HTTP POST /v1/work_items/WI_A/claim
body: {"idempotency_key":"m07-alice-claim","session_info":{"machine_id":"alice-agent","session_secret":"<64hex_A>"}}
ASSERT: HTTP 200
ASSERT: response.claim_epoch == 1
NOTE: save ALICE_ATTEMPT

AS ALICE:
HTTP PATCH /v1/work_items/WI_A/step
body: {"step":"code_change","status":"in_progress","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_A>"}
HTTP PATCH /v1/work_items/WI_A/step
body: {"step":"code_change","status":"completed","step_attempt_id":"sa_m07_code","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_A>"}
HTTP PATCH /v1/work_items/WI_A/step
body: {"step":"commit_and_pr","status":"in_progress","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_A>"}
HTTP PATCH /v1/work_items/WI_A/step
body: {"step":"commit_and_pr","status":"completed","step_attempt_id":"sa_m07_pr","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_A>"}

AS ALICE:
HTTP POST /v1/work_items/WI_A/complete
body: {"status":"wrapped","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_A>"}
ASSERT: response.ok == true

### WI_A wrapped → WI_B auto-unblocked
AS ADMIN:
CALL: pf_get_ready_queue(project="marketplace")
ASSERT: any(item for item in response.items if item.id == WI_B)
NOTE: WI_B now unblocked and in ready queue

CALL: pf_get_work_item(work_item_id=WI_B)
NOTE: Status should now be "queued" (no longer blocked)

### Bob can now claim WI_B
AS BOB:
HTTP POST /v1/work_items/WI_B/claim
body: {"idempotency_key":"m07-bob-claim","session_info":{"machine_id":"bob-ws","session_secret":"<64hex_B>"}}
ASSERT: HTTP 200
ASSERT: response.claim_epoch == 1
NOTE: save BOB_ATTEMPT

CALL: pf_get_work_item(work_item_id=WI_B)
ASSERT: response.status == "running"

## Cleanup

AS BOB:
HTTP POST /v1/work_items/WI_B/complete
body: {"status":"wrapped","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_B>"}

## PASS criteria

WI_B blocked until WI_A wraps; Bob's early claim rejected; after Alice wraps WI_A,
WI_B appears in ready queue; Bob's second claim succeeds.
