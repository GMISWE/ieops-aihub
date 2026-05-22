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

EXPECTED SKILL BEHAVIOR (pf-work Mode D — force-takeover):
  1. Memory-First recall for context
  2. pf_list_work_items(ids=[WI_ID]) — check expires_at
  3. pf_force_takeover(id_or_slug=WI_ID, reason="agent stalled; manager rescue")
     → terminates Alice's attempt, increments claim_epoch to 2,
       creates a new attempt for Admin (epoch+1),
       calls fnForceTerminateStep internally → resets stale code_change step to idle.
     → returns {ok: true, new_attempt_id: ADMIN_ATTEMPT_ID, claim_epoch: 2}
  4. pf_emit_event(work_item_id=WI_ID, event_type="note",
       payload={text: "force takeover: rescuing stalled code_change"})

  NOTE: pf_force_takeover ALREADY: creates the new attempt (epoch+1), writes the
        state file, and calls fnForceTerminateStep to reset the stale step to idle.
        No separate pf_claim_work_item(mode="resume") is needed — Admin is immediately
        the owner with a valid attempt after force_takeover returns.

ASSERT:
  - pf_force_takeover called → ok==true
  - Admin now owns WI with claim_epoch=2 (new attempt created by force_takeover)
  - pf_get_step(WI_ID) → code_change.status="idle" (reset by fnForceTerminateStep)
  - pf_get_work_item(WI_ID) → attempt.owner = Admin
  - pf_claim_work_item NOT called (force_takeover handles attempt creation)

### Phase 4: Admin proceeds directly with executing steps
NOTE: pf_force_takeover already reset the stale code_change step to idle and created
Admin's attempt. Admin can proceed directly — no manual step reset needed.

AS ADMIN: Take code_change step in_progress:
  version_info = pf_get_step(work_item_id=WI_ID)
  pf_update_step(
    work_item_id=WI_ID,
    step_id="code_change",
    status="in_progress",
    expected_version=version_info.version
  )
  → returns ADMIN_STEP_ATTEMPT_ID (claim_epoch=2 on Admin's attempt)

AS ADMIN: Complete the code change (applying fix from Alice's prepare_context context):
  (Read prepare_context.artifact_summary, make file changes, then mark completed)
  pf_update_step(
    work_item_id=WI_ID,
    step_id="code_change",
    status="completed",
    step_attempt_id=ADMIN_STEP_ATTEMPT_ID,
    artifact_summary="force-recovered: applied connection pool fix from Alice's context"
  )

ASSERT:
  - claim_epoch=2 on Admin's step_attempt (confirms correct epoch after force_takeover)
  - No manual pf_update_step(status="failed") needed (step already reset by force_takeover)

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
  - Events with actor=Alice (claim, step_update: prepare_context completed, step_update: code_change in_progress)
  - Events with actor=Admin (force_takeover [includes step reset internally],
      step_update: code_change in_progress, step_update: code_change completed,
      commit, push, pr_opened, wrapped)
  - claim_epoch=1 on Alice's events, claim_epoch=2 on Admin's events
  NOTE: No separate "claim" event for Admin (force_takeover creates the attempt).
        No "step_update: code_change failed[reset]" event for Admin (step reset is
        handled internally by fnForceTerminateStep inside force_takeover).

## PASS criteria
Admin sees stall in pf-status; pf_force_takeover called with reason (ok==true);
force_takeover internally creates Admin's attempt (claim_epoch=2) and resets stale step to idle
(fnForceTerminateStep); no separate pf_claim_work_item or manual step reset needed;
Admin proceeds directly to pf_update_step(code_change, in_progress) with claim_epoch=2;
Admin completes wi via pf_wrap; event timeline shows 2 actors across 2 epochs.

NOTE: Aligned with M04 reference pattern — pf_force_takeover is atomic: terminates old
attempt, creates new attempt, resets stale step. One call, not three.
