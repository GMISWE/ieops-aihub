# P1-05 — Multi-blocker: WI_C stays blocked until ALL blockers wrap (AND semantics)

## Users
- ADMIN_KEY (creates wi's and dependencies)
- ALICE_KEY (wraps WI_A)
- BOB_KEY (wraps WI_B)

## Steps

### Create 3 wi's
AS ADMIN: pf_create_work_item(goal="[test] P1-05 blocker A p105a", chore) -> WI_A
AS ADMIN: pf_create_work_item(goal="[test] P1-05 blocker B p105b", chore) -> WI_B
AS ADMIN: pf_create_work_item(goal="[test] P1-05 dependent C p105c", chore) -> WI_C

### Add both dependencies to WI_C
AS ADMIN: pf_create_dependency(dependent_id=WI_C, dependency_id=WI_A)
AS ADMIN: pf_create_dependency(dependent_id=WI_C, dependency_id=WI_B)

### WI_C blocked
AS ADMIN: GET /v1/work_items/WI_C -> ASSERT: status=="blocked"

### Ready queue: A+B in items[], C NOT
AS ADMIN: pf_get_ready_queue(project="marketplace")
ASSERT: WI_A in items[], WI_B in items[], WI_C NOT in items[]

### Alice wraps WI_A only
AS ALICE: claim WI_A -> run code_change+commit_and_pr steps -> wrap

### WI_C still blocked (WI_B not done)
AS ADMIN: GET /v1/work_items/WI_C -> ASSERT: status=="blocked"  <- KEY
AS ADMIN: pf_get_ready_queue -> ASSERT: WI_C NOT in items[]

### Bob wraps WI_B
AS BOB: claim WI_B -> run steps -> wrap

### NOW WI_C unblocked
AS ADMIN: GET /v1/work_items/WI_C -> ASSERT: status=="queued"
AS ADMIN: pf_get_ready_queue -> ASSERT: WI_C in items[]

## Cleanup
AS ADMIN: pf_cancel_work_item(WI_C, reason="p1-05 cleanup")

## PASS criteria
WI_C stays blocked after only WI_A wraps; unblocks only when both wrap.
