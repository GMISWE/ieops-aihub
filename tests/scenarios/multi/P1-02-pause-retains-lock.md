# P1-02 — Pause retains resource lock; second agent stays blocked

Tests that pausing a wi keeps resource_locks (unlike wrap which releases them).

## Users
- ALICE_KEY (writer, holds lock via pause)
- BOB_KEY (writer, blocked while Alice is paused)
- ADMIN_KEY (setup)

## Steps

### Alice claims wi with git_branch lock
AS ADMIN: create wi (chore) with declared_resources=[{type:repo,uri:repo:marketplace,intent:exclusive,task_branch:polyforge/p1-02-lock}]
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
Lock survives pause; Bob blocked during pause; released on wrap.
