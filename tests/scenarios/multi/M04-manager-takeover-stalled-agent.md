# M04 — Manager takeover: Admin rescues stalled Alice agent

Real-world scenario: Alice's agent session crashes or stalls mid-work.
Admin (as maintainer) force-takes over the wi, picks up from where Alice left off,
completes and wraps. Tests the cross-user force_takeover flow.

## Users
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V  (Test Agent Alice, writer)
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig  (xiaokang.w, admin)

## Steps

### Alice claims and starts work
AS ALICE:
HTTP POST /v1/work_items — create wi (via Alice's key):
body: {"project":"marketplace","goal":"[test] M04 Alice starts, Admin rescues",
       "wi_type":"fix_bug","priority":"high","scenario":"coding"}
NOTE: If create requires admin, switch to ADMIN for creation, Alice for claim.
NOTE: save response.id as WI_ID

AS ALICE:
HTTP POST /v1/work_items/WI_ID/claim
body: {"idempotency_key":"m04-alice-claim",
       "session_info":{"machine_id":"alice-agent-02","session_secret":"<64hex_A>"}}
ASSERT: response.claim_epoch == 1
NOTE: save ALICE_ATTEMPT, ALICE_SECRET = <64hex_A>

AS ALICE:
HTTP PATCH /v1/work_items/WI_ID/step
body: {"step":"prepare_context","status":"in_progress","attempt_id":ALICE_ATTEMPT,"claim_epoch":1,"session_secret":ALICE_SECRET}

NOTE: Alice's agent "crashes" here — step left in_progress, lease will eventually expire.
NOTE: In this test we simulate by just having Admin forcibly take over.

### Admin: observe the stalled wi
AS ADMIN:
CALL: pf_get_work_item(work_item_id=WI_ID)
ASSERT: response.status == "running"
ASSERT: response.current_attempt_epoch == 1

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "in_progress"
ASSERT: response.current_step == "prepare_context"

### Admin: force takeover (same user = always allowed; cross-user admin = always allowed)
AS ADMIN:
CALL: pf_force_takeover(work_item_id=WI_ID,
      reason="Alice agent crashed — admin rescuing stalled wi M04")
ASSERT: response.ok == true OR response.new_attempt_id != null
NOTE: force_takeover creates a new attempt with claim_epoch==2 and writes the state file.
NOTE: save response.new_attempt_id as ADMIN_ATTEMPT; Admin credentials are already active (no second claim needed).

### Admin: clean up in-progress step, continue from where Alice left off
AS ADMIN:
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context",
      status="failed", step_attempt_id="sa_m04_alice_crash",
      error_type="agent_crashed")
ASSERT: response.status == "failed"

CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"

### Admin: complete all remaining steps
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="completed",
      step_attempt_id="sa_m04_admin_ctx", artifact_summary="admin rescued context step")
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
      step_attempt_id="sa_m04_admin_code", artifact_summary="admin completed code change")
CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="in_progress")
CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="completed",
      step_attempt_id="sa_m04_admin_pr", artifact_summary="admin completed PR")

### Admin: wrap
CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT: response.ok == true

### Verify event timeline shows both Alice and Admin as actors
CALL: pf_read_events(work_item_id=WI_ID, limit="20")
ASSERT: any event with actor == Alice (u_CX6BMioR) — attempt_started
ASSERT: any event with actor == Admin (u_5dFjeaMZ) — force_takeover + step_* + wrapped
ASSERT: any event.event_type == "attempt_started" with claim_epoch==1  (Alice's)
ASSERT: any event.event_type == "force_takeover" with actor == Admin
NOTE: force_takeover creates Admin's attempt at epoch=2; no separate claim event for Admin.

## PASS criteria

Alice claims (epoch=1) → starts step → Admin force-takes over (epoch=2, credentials written to state file) →
Admin uses epoch=2 credentials directly → step completed → wi wrapped.
Event timeline shows 2 actors across 2 attempts.
