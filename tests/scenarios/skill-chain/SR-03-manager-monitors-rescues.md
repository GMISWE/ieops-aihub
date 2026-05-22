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
  - Worktree created for Alice at WORKSPACE_ROOT/pf.<shortid>/marketplace/
  Save WI_ID, ALICE_ATTEMPT_ID

SKILL_INVOKE (as ALICE): polyforge-coding:prepare_context
ASSERT:
  - prepare_context (start_step) completed
  - pf_get_step(WI_ID) → current_step="code_change"
  - NOTE: prepare_context does NOT save a separate artifact (no pf_save_artifact call);
    context is in start_step.artifact_summary only.

SKILL_INVOKE (as ALICE): polyforge-coding:code_change
→ pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress") is called
   → returns STALE_STEP_ATTEMPT_ID (Alice's step_attempt_id)
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

EXPECTED SKILL BEHAVIOR (pf-work Mode D — force-takeover, TWO sequential calls):
  1. Memory-First recall for context
  2. pf_list_work_items(ids=[WI_ID]) — check expires_at
  3. pf_force_takeover(id_or_slug=WI_ID, reason="agent stalled; manager rescue")
     → terminates Alice's attempt, increments claim_epoch
  4. pf_claim_work_item(work_item_id=WI_ID, mode="resume", idempotency_key=<client ULID>)
     → Admin gets new attempt_id, claim_epoch=2, new state file written
  NOTE: Both calls are required. pf_force_takeover alone does NOT create a new attempt.
        pf_claim_work_item(mode="resume") continues from task_branch commits.
  5. pf_emit_event(work_item_id=WI_ID, event_type="note",
       payload={text: "force takeover: rescuing stalled code_change"})

ASSERT:
  - BOTH pf_force_takeover AND pf_claim_work_item(mode="resume") called
  - Admin now owns WI (claim_epoch=2, owner=Admin)
  - pf_get_work_item(WI_ID) → attempt.owner = Admin
  - Previous Alice attempt marked as superseded

### Phase 4: Admin resets stale step and completes work
NOTE: After force-takeover the previous code_change step is still in_progress
under Alice's old attempt. Admin must reset it using status="failed" (not "completed")
to clear the stale step_attempt_id and allow a fresh in_progress transition.

AS ADMIN: Reset stale step (clear old step_attempt_id):
  pf_update_step(
    work_item_id=WI_ID,
    step_id="code_change",
    status="failed",
    step_attempt_id=STALE_STEP_ATTEMPT_ID,
    escalated=false
  )
  → This resets the step so it can be retried with a new step_attempt_id.

NOTE: Use status="failed" (not "completed") for stale step reset. "completed" would
incorrectly advance the step without actual work being done under the new attempt.

AS ADMIN: Take code_change step in_progress again:
  version_info = pf_get_step(work_item_id=WI_ID)
  pf_update_step(
    work_item_id=WI_ID,
    step_id="code_change",
    status="in_progress",
    expected_version=version_info.version
  )
  → returns new ADMIN_STEP_ATTEMPT_ID

AS ADMIN: Complete the code change (applying fix from Alice's prepare_context context):
  (Read start_step.artifact_summary, make file changes, then mark completed)
  pf_update_step(
    work_item_id=WI_ID,
    step_id="code_change",
    status="completed",
    step_attempt_id=ADMIN_STEP_ATTEMPT_ID,
    artifact_summary="force-recovered: applied connection pool fix from Alice's context"
  )

SKILL_INVOKE (as ADMIN): polyforge-coding:commit_and_pr
EXPECTED:
  - pf_diff(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace", vs_base=true)
  - pf_commit(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace",
      message="fix(db): connection pool exhaustion fix [rescued]")
  - pf_push(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace")
  - pf_pr(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace",
      title="fix(db): connection pool exhaustion fix [rescued]", body="...")

SKILL_INVOKE (as ADMIN): polyforge:pf-stop --wrap

EXPECTED SKILL BEHAVIOR (coding scenario — pf_wrap, not pf_complete_attempt):
  - pf_wrap(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace")

ASSERT:
  - pf_wrap called (NOT pf_complete_attempt directly)
  - WI_ID status=="wrapped"

### Phase 5: Verify event timeline shows both actors
AS ADMIN: pf_read_events(work_item_id=WI_ID, limit=50)
ASSERT:
  - Events with actor=Alice (claim, step_update: start_step completed, step_update: code_change in_progress)
  - Events with actor=Admin (force_takeover, claim, step_update: code_change failed[reset],
      step_update: code_change in_progress, step_update: code_change completed,
      commit, push, pr_opened, wrapped)
  - claim_epoch=1 on Alice's events, claim_epoch=2 on Admin's events

## PASS criteria
Admin sees stall in pf-status; force-takeover calls BOTH pf_force_takeover AND
pf_claim_work_item(mode="resume"); stale step reset uses status="failed" not "completed";
Admin completes wi via pf_wrap; event timeline shows 2 actors across 2 epochs.
