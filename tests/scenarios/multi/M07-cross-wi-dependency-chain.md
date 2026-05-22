# M07 — Cross-wi dependency chain: blocked claim rejected; claim succeeds after blocker wraps

Tests the full lifecycle of a wi blocked by a dependency:
1. WI_B depends on WI_A → WI_B status transitions to "blocked" immediately.
2. Attempting to claim WI_B while blocked returns HTTP 409 (Fix: blocked claim rejection).
3. WI_A wraps → WI_B transitions to "queued".
4. WI_B claim now succeeds.

## Actors

- Alice (user_id: u_aliceXXXXXX)
- Bob   (user_id: u_bobYYYYYYYY)

## Setup

### Create WI_A (blocker)
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M07 dependency blocker WI_A",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_A

### Create WI_B (dependent)
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M07 dependent WI_B",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_B

## Steps

### Step 1: Alice claims WI_A
AS ALICE:
CALL: pf_claim_work_item(work_item_id=WI_A, idempotency_key="m07-alice-claim-a",
      mode="fresh")
ASSERT: response.ok == true
ASSERT: response.claim_epoch == 1

### Step 2: Create dependency WI_B → blocked_by WI_A
CALL: pf_create_dependency(dependent_id=WI_B, dependency_id=WI_A)
ASSERT: response.ok == true

### Step 3: Verify WI_B is now blocked (not queued)
CALL: pf_get_work_item(work_item_id=WI_B)
ASSERT: response.status == "blocked"

### Step 4: Bob attempts to claim WI_B — must be rejected (dependency unresolved)
AS BOB:
HTTP POST /v1/work_items/WI_B/claim
body: {"idempotency_key": "m07-bob-claim-b", "mode": "fresh"}
ASSERT_ERROR: HTTP 409 "blocked"

### Step 5: Verify WI_B still blocked (claim failure had no side-effects)
CALL: pf_get_work_item(work_item_id=WI_B)
ASSERT: response.status == "blocked"

### Step 6: Alice completes WI_A — releases the dependency
AS ALICE:
CALL: pf_complete_attempt(work_item_id=WI_A, status="wrapped")
ASSERT: response.ok == true

### Step 7: WI_B should now be queued (blocker resolved)
CALL: pf_get_work_item(work_item_id=WI_B)
ASSERT: response.status == "queued"

### Step 8: Bob can now claim WI_B successfully
AS BOB:
CALL: pf_claim_work_item(work_item_id=WI_B, idempotency_key="m07-bob-claim-b-retry",
      mode="fresh")
ASSERT: response.ok == true
ASSERT: response.claim_epoch == 1

## Cleanup

AS BOB:
CALL: pf_complete_attempt(work_item_id=WI_B, status="wrapped")

## PASS criteria

WI_B enters "blocked" as soon as the dependency is created (not "queued").
Claim attempt on a blocked wi returns 409 with message containing "blocked".
After WI_A wraps, WI_B transitions to "queued" and becomes claimable.
