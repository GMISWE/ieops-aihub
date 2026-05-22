# P1-08 — emit_event: writer can emit on any wi; viewer cannot (403)

## Users
- ALICE_KEY (wi owner)
- BOB_KEY (writer, different user — can emit)
- CAROL_KEY (viewer — cannot emit)
- ADMIN_KEY (setup)

## Steps

### Setup: Admin creates wi, Alice claims
AS ADMIN: pf_create_work_item(goal="[test] P1-08 emit event p108a", chore) -> WI_ID
AS ALICE: pf_claim_work_item(WI_ID, idempotency_key="p1-08-alice", mode="fresh")
AS ALICE: pf_update_step(WI_ID, code_change, in_progress)

### Bob (writer, not owner) emits note — succeeds
AS BOB: HTTP POST http://10.146.0.16:8080/v1/events
body: {"work_item_id":WI_ID,"event_type":"note","payload":{"text":"P1-08 Bob note on Alice wi"}}
ASSERT: HTTP 200 or 201; Save BOB_EVENT_ID

### Carol (viewer) tries to emit — 403
AS CAROL: HTTP POST /v1/events
body: {"work_item_id":WI_ID,"event_type":"note","payload":{"text":"P1-08 Carol viewer note"}}
ASSERT_ERROR: HTTP 403

### Verify events timeline
AS ADMIN: pf_read_events(WI_ID, limit="20")
ASSERT: any event actor_user_id=="u_1FwUhHGM" (Bob's event present)
ASSERT: NOT any event with "Carol viewer note" in payload

## Cleanup
AS ALICE: complete step, wrap WI_ID

## PASS criteria
Bob emits 200/201; Carol gets 403; only Bob in timeline.
