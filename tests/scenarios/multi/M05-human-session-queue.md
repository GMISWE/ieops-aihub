# M05 — Human-led session: requires_human_session=true sits in needs_human_session[]

Real-world scenario: A critical_bug wi requires a human developer (Bob) to review.
It should NOT appear in auto-agent Alice's ready queue items[]. Only Bob can claim it.
Tests the Session 1 (auto) vs Session 2 (human-led) routing.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig  (admin, orchestrator)
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V  (Test Agent Alice, auto-agent)
- BOB_KEY=pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR  (Test Writer Bob, human developer)

## Steps

### Admin: create critical_bug wi (requires_human_session=true)
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M05 critical auth bypass — requires human review",
      wi_type="critical_bug", requires_human_session=true, priority="urgent")
ASSERT: response.requires_human_session == true
ASSERT: response.wi_type == "critical_bug"
NOTE: save response.id as WI_CRITICAL

### Admin: create a normal fix_bug wi (auto-eligible)
AS ADMIN:
CALL: pf_create_work_item(project="marketplace",
      goal="[test] M05 normal auto fix",
      wi_type="fix_bug", requires_human_session=false, priority="normal")
NOTE: save response.id as WI_AUTO

### Ready queue: WI_CRITICAL must be in needs_human_session[], NOT in items[]
AS ADMIN:
CALL: pf_get_ready_queue(project="marketplace")
ASSERT: any(item for item in response.needs_human_session if item.id == WI_CRITICAL)
ASSERT: not any(item for item in response.items if item.id == WI_CRITICAL)
ASSERT: any(item for item in response.items if item.id == WI_AUTO)

### Agent Alice: CAN claim WI_AUTO (auto-eligible)
AS ALICE:
HTTP POST /v1/work_items/WI_AUTO/claim
body: {"idempotency_key":"m05-alice-auto-claim","session_info":{"machine_id":"alice-agent","session_secret":"<64hex>"}}
ASSERT: HTTP 200
ASSERT: response.claim_epoch == 1
NOTE: save ALICE_AUTO_ATTEMPT

### Agent Alice: attempts to claim WI_CRITICAL (should succeed at API level — no role block,
###  but the skill/orchestrator logic should prevent it based on requires_human_session flag)
NOTE: The API itself does not block Alice from claiming a requires_human_session=true wi.
NOTE: The restriction is enforced by the skill/orchestrator layer (pf-execute).
NOTE: This assertion documents the INTENDED behavior per the design doc.
AS ALICE:
HTTP POST /v1/work_items/WI_CRITICAL/claim
body: {"idempotency_key":"m05-alice-critical-claim","session_info":{"machine_id":"alice-agent","session_secret":"<64hex>"}}
NOTE: Record actual HTTP status — design says skill should prevent this, not server

### Bob: claims WI_CRITICAL (human-led session)
AS BOB:
HTTP POST /v1/work_items/WI_CRITICAL/claim
body: {"idempotency_key":"m05-bob-claim","session_info":{"machine_id":"bob-laptop","session_secret":"<64hex_B>"}}
ASSERT: HTTP 200
ASSERT: response.claim_epoch >= 1
NOTE: save BOB_ATTEMPT

### Bob: run spec + plan steps (human-led methodology)
AS BOB:
HTTP PATCH /v1/work_items/WI_CRITICAL/step
body: {"step":"spec","status":"in_progress","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_B>"}

HTTP PATCH /v1/work_items/WI_CRITICAL/step
body: {"step":"spec","status":"completed","step_attempt_id":"sa_m05_spec",
       "attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_B>"}

### Admin: verify WI_CRITICAL no longer in needs_human_session (it's running now)
AS ADMIN:
CALL: pf_get_ready_queue(project="marketplace")
ASSERT: not any(item for item in response.needs_human_session if item.id == WI_CRITICAL)
ASSERT: any(item for item in response.running if item.id == WI_CRITICAL)

## Cleanup

AS BOB:
HTTP POST /v1/work_items/WI_CRITICAL/complete
body: {"status":"wrapped","attempt_id":BOB_ATTEMPT,"claim_epoch":1,"session_secret":"<secret_B>"}

AS ALICE:
HTTP POST /v1/work_items/WI_AUTO/complete
body: {"status":"wrapped","attempt_id":ALICE_AUTO_ATTEMPT,"claim_epoch":1,"session_secret":"<secret>"}

## PASS criteria

WI_CRITICAL in needs_human_session[], WI_AUTO in items[]; Alice claims auto wi;
Bob claims critical wi; once running, WI_CRITICAL leaves needs_human_session[].
