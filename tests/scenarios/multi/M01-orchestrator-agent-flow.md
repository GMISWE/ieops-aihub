# M01 — Orchestrator + Agent: Admin creates, Agent executes

Real-world scenario: Admin (xiaokang.w) acts as Orchestrator in Session 1,
creates wi's and monitors queue. Agent Alice (machine/writer) claims and executes.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig  (xiaokang.w, admin)
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V  (Test Agent Alice, machine/writer)

## Steps

### Admin: check ready queue before creating anything
AS ADMIN:
CALL: pf_get_ready_queue(project="marketplace")
NOTE: save baseline ready queue state

### Admin: create auto wi (requires_human_session=false — auto-agent eligible)
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M01 auto-fix: update dependency version",
      wi_type="fix_bug", requires_human_session=false, priority="normal")
ASSERT: response.status == "queued"
ASSERT: response.requires_human_session == false
NOTE: save response.id as WI_ID, response.slug as SLUG

### Admin: verify wi appears in ready queue items[] (auto-eligible)
AS ADMIN:
CALL: pf_get_ready_queue(project="marketplace")
ASSERT: any(item for item in response.items if item.id == WI_ID)
NOTE: items[] = auto-eligible wi's; needs_human_session[] = human-required

### Agent Alice: claim the wi (simulates Session 1 auto-dispatch)
AS ALICE:
HTTP POST /v1/work_items/WI_ID/claim
body: {"idempotency_key":"m01-alice-claim","session_info":{"machine_id":"alice-agent-01","session_secret":"<64hex>"}}
ASSERT: response.attempt_id != null
ASSERT: response.claim_epoch == 1
NOTE: save response.attempt_id as ATTEMPT_ID

### Admin: verify wi now running, owned by Alice
AS ADMIN:
CALL: pf_get_work_item(work_item_id=WI_ID)
ASSERT: response.status == "running"
ASSERT: response.current_attempt_epoch == 1

### Agent Alice: execute 3 steps
AS ALICE:
HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"prepare_context","status":"in_progress","attempt_id":ATTEMPT_ID,"claim_epoch":1,"session_secret":"<secret>"}
ASSERT: response.status == "in_progress"

HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"prepare_context","status":"completed","step_attempt_id":"sa_m01_ctx","attempt_id":ATTEMPT_ID,"claim_epoch":1,"session_secret":"<secret>"}
ASSERT: response.status == "completed"

HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"code_change","status":"in_progress",...}
ASSERT: response.status == "in_progress"

HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"code_change","status":"completed","step_attempt_id":"sa_m01_code",...}
ASSERT: response.status == "completed"

HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"commit_and_pr","status":"in_progress",...}
HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"commit_and_pr","status":"completed","step_attempt_id":"sa_m01_pr",...}
ASSERT: response.status == "completed"

### Agent Alice: complete attempt (wrapped)
AS ALICE:
HTTP POST /v1/work_items/WI_ID/complete
body: {"status":"wrapped","attempt_id":ATTEMPT_ID,"claim_epoch":1,"session_secret":"<secret>"}
ASSERT: response.ok == true

### Admin: verify wi wrapped, appears in event timeline
AS ADMIN:
CALL: pf_get_work_item(work_item_id=WI_ID)
ASSERT: response.status == "wrapped"

CALL: pf_read_events(work_item_id=WI_ID, limit="20")
ASSERT: any event.event_type=="attempt_started"
ASSERT: any event.event_type=="work_item_completed"
ASSERT: any(e for e in events if e.actor_user_id == "u_CX6BMioR")  (Alice's events)

### Ready queue: wi no longer in items[]
AS ADMIN:
CALL: pf_get_ready_queue(project="marketplace")
ASSERT: not any(item for item in response.items if item.id == WI_ID)

## Cleanup

Wi is already wrapped — no cleanup needed.

## PASS criteria

Admin creates wi → appears in ready queue → Alice claims and executes all steps →
Admin observes wrapped state → events show Alice as actor.
