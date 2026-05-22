# SR-03 — Manager monitors queue and rescues stalled agent (realistic)

Tests real-world scenario: Alice's agent gets stuck mid-step (simulated crash).
Admin (as manager) monitors via pf-status, notices the stall, force-takes over
and completes the work.

## Users
- ALICE (writer/agent): starts work, then "crashes"
  API key: pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V
- ADMIN (manager/orchestrator): monitors and rescues
  API key: baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig

## Scenario

### Phase 1: Alice starts work normally
SKILL_INVOKE (as ALICE): polyforge:pf-work
USER_INTENT: "fix: connection pool exhaustion in database layer"

ASSERT:
  - WI_ID created with wi_type="fix_bug", requires_human_session=false
  - pf_claim called by Alice → WI_ID status=running
  - Worktree created for Alice
  Save WI_ID, ALICE_ATTEMPT_ID

SKILL_INVOKE (as ALICE): polyforge-coding:prepare_context
ASSERT: prepare_context step completed, methodology.spec artifact saved

SKILL_INVOKE (as ALICE): polyforge-coding:code_change
→ step code_change pf_update_step(in_progress) is called
→ Alice's session "crashes" here (simulated: do NOT call pf_update_step(completed))
→ The step remains in_progress with no further heartbeats

NOTE: In a real crash, the lease eventually expires (60s default). For testing,
wait for the lease to expire OR use Admin's force_takeover without waiting.

### Phase 2: Admin notices stall via pf-status
SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT rendered output:
  - WI_ID in running[] with owner=Alice (Test Agent Alice)
  - step=code_change, step_status=in_progress
  - last_active_at shows Alice's last heartbeat (stalled indicator when old)

NOTE: Admin judges stall by checking if code_change has been in_progress >30min
with no heartbeat. For this test, treat the lack of completed event as sufficient.

### Phase 3: Admin rescues via force-takeover
SKILL_INVOKE (as ADMIN): polyforge:pf-work WI_ID --force
USER_INTENT: "take over Alice's stalled wi and complete it"

EXPECTED SKILL BEHAVIOR (pf-work Mode D — force-takeover):
  1. Memory-First recall for context
  2. pf_force_takeover(work_item_id=WI_ID, reason="agent stalled; manager rescue")
     OR pf_claim_work_item(work_item_id=WI_ID, mode="fresh", force_takeover=true,
        force_reason="agent stalled; manager rescue")
  3. New attempt created with Admin as owner, claim_epoch incremented (epoch=2)
  4. pf_emit_event(event_type="note", payload={text: "force takeover: rescuing stalled code_change"})

ASSERT:
  - Admin now owns WI (claim_epoch=2, owner=Admin)
  - pf_get_work_item(WI_ID) → attempt.owner = Admin
  - Previous Alice attempt marked as superseded

### Phase 4: Admin resets stale step and completes work
NOTE: After force-takeover the previous step (code_change) is still in_progress
under the old attempt. Admin must acknowledge it and proceed.

AS ADMIN: pf_update_step(step_id="code_change", status="completed",
  artifact_summary="force-recovered: applying fix from Alice's prepare_context")
  (Admin is continuing from where Alice left off, using the existing spec artifact)

SKILL_INVOKE (as ADMIN): polyforge-coding:commit_and_pr
EXPECTED: pf_diff → pf_commit ("fix: connection pool exhaustion fix [rescued]") → pf_push → pf_pr

SKILL_INVOKE (as ADMIN): polyforge:pf-stop --wrap

### Phase 5: Verify event timeline shows both actors
AS ADMIN: pf_read_events(work_item_id=WI_ID, limit="50")
ASSERT:
  - Events with actor=Alice (claim, step_update: prepare_context completed, step_update: code_change in_progress)
  - Events with actor=Admin (force_takeover/claim, step_update: code_change completed, commit_and_pr, wrapped)
  - claim_epoch=1 on Alice's events, claim_epoch=2 on Admin's events

## PASS criteria
Admin sees stall in pf-status; force-takeover succeeds with epoch increment;
Admin completes wi; event timeline shows 2 actors across 2 epochs.
