# M03 — Concurrent agents compete: Alice and Bob race for same resource

Real-world scenario: Two agents start simultaneously, both want the same git branch.
One wins the lock, the other gets 409 and must wait.
Tests the real multi-agent coordination flow.

## Users
- ALICE_KEY=$ALICE_KEY  (Test Agent Alice, writer)
- BOB_KEY=$BOB_KEY  (Test Writer Bob, writer)
- ADMIN_KEY=$ADMIN_KEY  (admin, for setup/verification)

## Steps

🔴 **aihub#416 (2026-09-09) changed WHERE the contention comes from.** The two work
items below still compete for one `git_branch` lock and Bob is still blocked — but
the lock now exists only because both claims ASK for it in `requested_locks`. The
`repo` declaration derives nothing (owner ruling of 2026-09-07), so `task_branch` has
been dropped from the declarations and the shared-branch key survives only in the
explicit claim bodies. Read this scenario as "two agents asking for one lock", not as
"two agents declaring one repo".


### Admin: create two wi's competing for the same branch
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M03 Alice's task — needs shared-feature branch",
      wi_type="fix_bug", priority="high",
      declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive"}])
NOTE: save response.id as WI_ALICE

AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M03 Bob's task — also needs shared-feature branch",
      wi_type="fix_bug", priority="normal",
      declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive"}])
NOTE: save response.id as WI_BOB

### predict_conflicts: both wi's show potential conflict with each other
AS ADMIN:
CALL: pf_predict_conflicts(
      declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive"}],
      work_item_id=WI_ALICE)
NOTE: record severity; advisory only
NOTE: aihub#416 (2026-09-09) — a repo-only payload now tops out at `soft_block`
      (predict rule 2, a declaration join) and can NEVER return `hard_block`,
      because a repo entry derives no lock for the lock-table rule to find. Each
      prediction also carries `last_active_age_seconds`. Do not read a
      severity below `hard_block` here as "no conflict"; read it as "another
      running wi declares the same repo, judge for yourself".

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
