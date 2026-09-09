# P1-02 — Pause retains resource lock; second agent stays blocked

Tests that pausing a wi keeps resource_locks (unlike wrap which releases them).

🔴 **THE TITLE STILL HOLDS AFTER aihub#416 — but its REACH shrank** (2026-09-09).
Pause releases `file_scope` only (`acquireLocksReleasePausedSQL` names that type
literally) and retains every other type, and that SQL is byte-unchanged. What changed
is what there is to retain: aihub#416 retired the `repo → git_branch` and
`service → deploy_env` derivations, so on a current build the retained set is EMPTY
for an ordinary claim.

This scenario keeps working because every claim in it passes `requested_locks`
explicitly, which is now the only way a `git_branch` row exists (owner ruling Q-3
kept that path open). ⚠️ So read it as "a lock this attempt asked for survives a
pause", NOT as "a wi that declares a repo blocks other agents while paused" — the
second was the reported symptom aihub#416 exists to remove, and it is gone.

`task_branch` is dropped from the declarations below: aihub#416 withdrew it from the
published schema, since its only reader was the lock key that is no longer derived.

## Users
- ALICE_KEY (writer, holds lock via pause)
- BOB_KEY (writer, blocked while Alice is paused)
- ADMIN_KEY (setup)

## Steps

### Alice claims wi with git_branch lock
AS ADMIN: create wi (chore) with declared_resources=[{type:repo,uri:repo:marketplace,intent:exclusive}]
Save WI_ALICE

AS ALICE: claim WI_ALICE with requested_locks=[{resource_type:git_branch,resource_key:marketplace/polyforge/p1-02-lock}]
ASSERT: HTTP 200, len(acquired_locks)==1; Save ALICE_ATTEMPT, ALICE_SECRET

### Bob creates competing wi — claim blocked
AS ADMIN: create WI_BOB with same declared_resources
AS BOB: claim WI_BOB with same requested_locks
ASSERT_ERROR: HTTP 409 CONFLICT_LOCK_TAKEN

### Alice pauses (lock RETAINED)
AS ALICE: HTTP POST /complete body: {"status":"paused","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":ALICE_SECRET}
ASSERT: HTTP 200

### Bob still blocked after pause
AS BOB: retry claim WI_BOB
ASSERT_ERROR: HTTP 409 CONFLICT_LOCK_TAKEN  <- KEY: pause does not release lock

### Alice resumes and wraps (lock released)
AS ALICE: claim WI_ALICE with mode="resume"; wrap it

### Bob now succeeds
AS BOB: retry claim WI_BOB
ASSERT: HTTP 200, len(acquired_locks)==1

## Cleanup
AS BOB: wrap WI_BOB

## PASS criteria
A requested lock survives pause; Bob blocked during pause; released on wrap.

⚠️ NOT a pass criterion, and it was one before aihub#416: that the DECLARATION alone
produced the lock. Remove the `requested_locks` from Alice's claim and this scenario
fails at its first assertion — `len(acquired_locks)==1` becomes 0 — which is the
correct new behaviour, not a regression.
