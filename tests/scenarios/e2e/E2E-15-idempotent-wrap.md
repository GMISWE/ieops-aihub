# E2E-15 — Idempotent complete: same attempt_id wrap called twice returns ok both times

Tests that calling complete(wrapped) twice with the same attempt_id is idempotent.
The second call should either return 200 ok or 409 CONFLICT_TERMINAL_STATE,
but must NOT create duplicate events or corrupt state.
Reference: FnCompleteAttempt idempotency via attempt status check.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig

## Steps

### Create and claim wi
AS ADMIN: pf_create_work_item(project="marketplace", goal="[test] E2E-15 idempotent wrap", wi_type="chore")
Save WI_ID

AS ADMIN: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e15-claim", mode="fresh")
ASSERT: ok==true; Save ATTEMPT_ID, CLAIM_EPOCH, SESSION_SECRET from state file

### Complete all steps
AS ADMIN: pf_update_step(WI_ID, code_change, in_progress)
AS ADMIN: pf_update_step(WI_ID, code_change, completed, step_attempt_id="sa_e2e15")
AS ADMIN: pf_update_step(WI_ID, commit_and_pr, in_progress)
AS ADMIN: pf_update_step(WI_ID, commit_and_pr, completed, step_attempt_id="sa_e2e15_pr")

### First wrap — must succeed
AS ADMIN: HTTP POST /v1/work_items/WI_ID/complete
body: {"status":"wrapped","attempt_id":ATTEMPT_ID,"claim_epoch":CLAIM_EPOCH,"session_secret":SESSION_SECRET}
ASSERT: HTTP 200, ok==true

### Second wrap with same credentials — must not corrupt state
AS ADMIN: HTTP POST /v1/work_items/WI_ID/complete
body: {"status":"wrapped","attempt_id":ATTEMPT_ID,"claim_epoch":CLAIM_EPOCH,"session_secret":SESSION_SECRET}
ASSERT: HTTP 200 (idempotent) OR HTTP 409 CONFLICT_TERMINAL_STATE
NOTE: Either response is acceptable; 500 or state corruption is NOT acceptable

### Verify wi status unchanged (still wrapped, not double-wrapped or corrupted)
AS ADMIN: GET /v1/work_items/WI_ID
ASSERT: response.status == "wrapped"

### Event count: work_item_completed should appear exactly once (not twice)
AS ADMIN: pf_read_events(work_item_id=WI_ID, limit="50")
NOTE: count events with event_type=="work_item_completed"
ASSERT: count == 1 (idempotent wrap must not emit duplicate terminal events)

## PASS criteria
Second wrap returns 200 or 409 (not 500); wi stays "wrapped"; no duplicate events.
