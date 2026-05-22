# M03 — Concurrent agents compete: Alice and Bob race for same resource

Real-world scenario: Two agents start simultaneously, both want the same git branch.
One wins the lock, the other gets 409 and must wait.
Tests the real multi-agent coordination flow.

## Users
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V  (Test Agent Alice, writer)
- BOB_KEY=pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR  (Test Writer Bob, writer)
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig  (admin, for setup/verification)

## Steps

### Admin: create two wi's competing for the same branch
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M03 Alice's task — needs shared-feature branch",
      wi_type="fix_bug", priority="high",
      declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
                           "task_branch":"polyforge/m03-shared-feature"}])
NOTE: save response.id as WI_ALICE

AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M03 Bob's task — also needs shared-feature branch",
      wi_type="fix_bug", priority="normal",
      declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
                           "task_branch":"polyforge/m03-shared-feature"}])
NOTE: save response.id as WI_BOB

### predict_conflicts: both wi's show potential conflict with each other
AS ADMIN:
CALL: pf_predict_conflicts(
      declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
                           "task_branch":"polyforge/m03-shared-feature"}],
      work_item_id=WI_ALICE)
NOTE: record severity; advisory only

### Alice claims first (wins the lock)
AS ALICE:
HTTP POST /v1/work_items/WI_ALICE/claim
body: {"idempotency_key":"m03-alice-claim",
       "session_info":{"machine_id":"alice-agent","session_secret":"<64hex>"},
       "requested_locks":[{"resource_type":"git_branch",
                           "resource_key":"marketplace/polyforge/m03-shared-feature"}]}
ASSERT: HTTP 200
ASSERT: response.claim_epoch == 1
ASSERT: len(response.acquired_locks) == 1
NOTE: save response.attempt_id as ALICE_ATTEMPT

### Bob tries to claim — must be blocked
AS BOB:
HTTP POST /v1/work_items/WI_BOB/claim
body: {"idempotency_key":"m03-bob-claim",
       "session_info":{"machine_id":"bob-ws","session_secret":"<64hex>"},
       "requested_locks":[{"resource_type":"git_branch",
                           "resource_key":"marketplace/polyforge/m03-shared-feature"}]}
ASSERT: HTTP 409
ASSERT_ERROR: "CONFLICT_LOCK_TAKEN"

### WI_BOB stays queued (lock rejected, not running)
AS ADMIN:
CALL: pf_get_work_item(work_item_id=WI_BOB)
ASSERT: response.status == "queued"

### Admin: stalled queue shows WI_BOB waiting
AS ADMIN:
CALL: pf_list_work_items(project="marketplace", status="queued")
ASSERT: any(item for item in response.items if item.id == WI_BOB)

### Alice completes work and releases lock
AS ALICE:
HTTP PATCH /v1/work_items/WI_ALICE/step
body: {"step":"prepare_context","status":"in_progress","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
HTTP PATCH /v1/work_items/WI_ALICE/step
body: {"step":"prepare_context","status":"completed","step_attempt_id":"sa_m03_ctx","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
HTTP PATCH /v1/work_items/WI_ALICE/step
body: {"step":"code_change","status":"in_progress","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
HTTP PATCH /v1/work_items/WI_ALICE/step
body: {"step":"code_change","status":"completed","step_attempt_id":"sa_m03_code","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
HTTP PATCH /v1/work_items/WI_ALICE/step
body: {"step":"commit_and_pr","status":"in_progress","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
HTTP PATCH /v1/work_items/WI_ALICE/step
body: {"step":"commit_and_pr","status":"completed","step_attempt_id":"sa_m03_pr","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
HTTP POST /v1/work_items/WI_ALICE/complete
body: {"status":"wrapped","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}
ASSERT: response.ok == true

### Lock released: Bob can now claim
AS BOB:
HTTP POST /v1/work_items/WI_BOB/claim
body: {"idempotency_key":"m03-bob-claim-retry",
       "session_info":{"machine_id":"bob-ws","session_secret":"<64hex>"},
       "requested_locks":[{"resource_type":"git_branch",
                           "resource_key":"marketplace/polyforge/m03-shared-feature"}]}
ASSERT: HTTP 200
ASSERT: response.claim_epoch == 1
ASSERT: len(response.acquired_locks) == 1
NOTE: save response.attempt_id as BOB_ATTEMPT

### Verify WI_ALICE wrapped, WI_BOB running
AS ADMIN:
CALL: pf_get_work_item(work_item_id=WI_ALICE)
ASSERT: response.status == "wrapped"

CALL: pf_get_work_item(work_item_id=WI_BOB)
ASSERT: response.status == "running"

## Cleanup

AS BOB:
HTTP POST /v1/work_items/WI_BOB/complete
body: {"status":"wrapped","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}

## PASS criteria

Alice wins lock; Bob gets 409; WI_BOB stays queued; Alice wraps → lock released →
Bob's retry succeeds; both wi's end up wrapped.
