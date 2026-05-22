# M04 — Manager force-takeover of a stalled agent

Tests that an Admin can force-take a running wi whose agent has gone stale,
without issuing a second claim. force_takeover writes its own credentials into
the state file; the Admin can then proceed directly.

## Actors

- Alice  (user_id: u_aliceXXXXXX) — original agent
- Admin  (user_id: u_5dFjeaMZ)   — manager with force_takeover permission

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] M04 stalled agent wi",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

## Steps

### Step 1: Alice claims the wi
AS ALICE:
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="m04-alice-claim",
      mode="fresh")
ASSERT: response.ok == true
ASSERT: response.claim_epoch == 1
NOTE: save response.attempt_id as ATTEMPT_ALICE

### Step 2: Alice starts a step (then stalls — no further heartbeats)
AS ALICE:
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

### Step 3: Verify wi is running under Alice
CALL: pf_get_work_item(work_item_id=WI_ID)
ASSERT: response.status == "running"
ASSERT: response.current_attempt_epoch == 1

### Step 4: Admin force-takeover (different user, explicit force_over=true)
AS ADMIN (u_5dFjeaMZ):
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="m04-admin-takeover",
      mode="fresh", force_over=true)
ASSERT: response.ok == true
ASSERT: response.claim_epoch == 2
NOTE: save response.attempt_id as ATTEMPT_ADMIN
NOTE: force_takeover supersedes Alice's attempt; Admin's credentials are now active

### Step 5: Admin completes the wi (no second claim needed)
NOTE: Admin uses ATTEMPT_ADMIN credentials written by force_takeover above.
AS ADMIN (u_5dFjeaMZ):
CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped", force_terminate_step=true)
ASSERT: response.ok == true

## Event assertions

ASSERT: any event with event_type=="attempt_started" AND claim_epoch==1
        (Alice's original claim)

ASSERT: any event with event_type=="force_takeover" AND actor==u_5dFjeaMZ
        (Admin's takeover — force_takeover does NOT emit attempt_started)

ASSERT: any event with event_type=="work_item_completed" AND actor==u_5dFjeaMZ
        (Admin wraps)

## PASS criteria

Admin force-takeover (epoch=2) supersedes Alice (epoch=1) without a second
pf_claim call. Admin wraps the wi directly using the takeover credentials.
Events: attempt_started(epoch=1, actor=Alice) + force_takeover(actor=Admin)
+ work_item_completed(actor=Admin).
