# SC-10 — pf-execute M16 retry loop and escalation path (code_change stall)

Tests pf-execute's M16 retry logic when a code_change subagent stalls (no heartbeat
for >60s). pf-execute must detect the stall, reset the step, renew its lease, and
re-dispatch a fresh subagent. On the second attempt the step succeeds normally. The
scenario also verifies the escalation path: if a step stalls on all 3 attempts,
pf-execute must NOT dispatch again but instead emit a human-escalation note and stop.

NOTE: SC-05 tests the happy-path dispatch loop. SC-10 is focused exclusively on the
M16 retry branch of the main step loop (SKILL.md lines "// Handle unresponsive Step
Agent (M16 retry logic)"). Only the code_change step is exercised here; prepare_context
and commit_and_pr are already past or elided for brevity.

Runner: dispatch a subagent with access to all polyforge skills + MCP tools.
The subagent must follow pf-execute skill instructions, not call MCP tools directly.

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace
- WI_ID: pre-created fix_bug wi, already claimed (state file written, worktree exists)
  - wi status: "running"
  - wi goal: "fix: deduplicate webhook delivery on retry"
  - wi_type: fix_bug, requires_human_session=false
  - Steps per phase.yaml: ["prepare_context", "code_change", "commit_and_pr"]
  - prepare_context.status = "completed" (already done — skipped in this scenario)
  - current_step = "code_change", current_step_status = "pending"
  - ATTEMPT_ID, CLAIM_EPOCH, SESSION_SECRET from state file at
    WORKSPACE_ROOT/.polyforge/state/WI_ID.json
  - Worktree at WT_PATH = WORKSPACE_ROOT/pf.<shortid>/marketplace/

## Scenario

### Step 1: User invokes pf-execute (execution loop resumes at code_change)
SKILL_INVOKE: polyforge:pf-execute
USER_INTENT: "execute" (or "run it" / "continue")

EXPECTED SKILL BEHAVIOR — Setup phase:
  1. Load wi info:
     pf_list_work_items(ids=[WI_ID], include_step_state=true)
     → requires_human_session=false, current_step="code_change", phase_mode="step"
     → prepare_context.status="completed" (previous_steps contains artifact_summary)

  2. Memory-First before entering loop:
     pf_recall(project="marketplace", query=wi.goal,
               type=["experience.*","rule.*"], top_k=5)
     pf_activate_memory(id) for each relevant result

ASSERT MCP CALLS (setup):
  - pf_list_work_items called with ids=[WI_ID], include_step_state=true
  - pf_recall called before any pf_update_step (Memory-First enforced)

---

### Step 2: pf-execute dispatches code_change subagent (attempt 1)
EXPECTED SKILL BEHAVIOR (main step loop iteration — code_change):
  step_info = pf_get_step(work_item_id=WI_ID)
  current_step = "code_change", version = V1

  Memory-First before dispatch:
    pf_recall(project="marketplace", query="code_change deduplicate webhook delivery",
              type=["experience.*","rule.*"], top_k=3)

  Dispatch subagent (auto wi → SUBAGENT, not inline):
    action: code_change
    skill: polyforge-coding:code_change
    previous_context: prepare_context.artifact_summary (initial_context JSON)

  Subagent begins execution:
    a. pf_get_step(work_item_id=WI_ID) → version V1
    b. pf_update_step(work_item_id=WI_ID, step_id="code_change",
                      status="in_progress", expected_version=V1)
       → returns step_attempt_id=SA_CODE_1, version V2
    c. Subagent begins editing files but then STALLS — no further MCP calls.
       (Simulated: step remains status="in_progress" past the 60s deadline.)

  Wi Agent waits (up to 60s):
    wait_with_check(timeout=60s,
                    check=lambda: pf_get_step(WI_ID).current_step_status != "in_progress")
    After 60s, status is still "in_progress" → stall detected.

ASSERT MCP CALLS (attempt 1 dispatch):
  - pf_recall called before subagent dispatch (Memory-First)
  - pf_update_step(step_id="code_change", status="in_progress") called by subagent
    with step_attempt_id=SA_CODE_1 returned
  - 60s wall-clock check: pf_get_step still returns status="in_progress"

ASSERT STATE (stall detected):
  - code_change.status remains "in_progress"
  - step_attempt_id is SA_CODE_1 (not yet reset)

---

### Step 3: M16 reset — pf-execute resets the stale step
EXPECTED SKILL BEHAVIOR (M16 retry logic, first stall):
  // Step 1: Reset stale step — clear old step_attempt_id and mark failed
  pf_update_step(
    work_item_id=WI_ID,
    step_id="code_change",
    status="failed",
    step_attempt_id=SA_CODE_1,
    escalated=false
  )
  → step.status resets to "pending", step_attempt_id cleared, version bumps to V3

ASSERT MCP CALLS (reset):
  - pf_update_step(step_id="code_change", status="failed",
                   step_attempt_id=SA_CODE_1, escalated=false) called
  - escalated=false (retry is still available — NOT yet at max)

ASSERT STATE after reset:
  - pf_get_step(WI_ID) → code_change.status="pending"
  - step_attempt_id is null/cleared

---

### Step 4: M16 renew lease before retry dispatch
EXPECTED SKILL BEHAVIOR:
  // Step 2: Renew lease — prevent zombie sweeper from claiming the wi
  //         during the retry dispatch window
  renew_result = pf_renew_lease(
    attempt_id=ATTEMPT_ID,
    claim_epoch=CLAIM_EPOCH,
    session_secret=SESSION_SECRET
  )

  if renew_result.error or renew_result.status == "lost_lease":
    // Lease already expired — do NOT retry; escalate to user:
    // "Lease lost during retry window. Use /pf-resume to reclaim."
    break  // terminate the loop; no further dispatch

  // Lease still valid → proceed to re-dispatch (retry_count += 1 → retry_count = 1)

ASSERT MCP CALLS (lease renewal):
  - pf_renew_lease called with attempt_id=ATTEMPT_ID, claim_epoch=CLAIM_EPOCH,
    session_secret=SESSION_SECRET
  - renew_result.status == "ok" (lease renewed successfully)
  - NO pf_update_step(escalated=true) emitted at this point (retry still within limit)

ASSERT STATE:
  - Lease extended; wi.status still "running" under ATTEMPT_ID

---

### Step 5: pf-execute re-dispatches code_change subagent (retry attempt 2)
EXPECTED SKILL BEHAVIOR:
  retry_count = 1  // incremented after first stall recovery

  Re-dispatch subagent (fresh subagent — not the stalled one):
    action: code_change
    skill: polyforge-coding:code_change
    previous_context: prepare_context.artifact_summary (initial_context JSON)
    retry_context: "retry attempt 2 — previous subagent stalled; step was reset"

  Subagent executes successfully this time:
    a. pf_get_step(work_item_id=WI_ID) → version V3 (bumped after reset)
    b. pf_update_step(work_item_id=WI_ID, step_id="code_change",
                      status="in_progress", expected_version=V3)
       → returns step_attempt_id=SA_CODE_2, version V4
    c. Read initial_context from previous_steps.prepare_context.artifact_summary
    d. Implement fix: deduplicate webhook delivery on retry
       (e.g., idempotency key check in WT_PATH/src/webhooks/dispatcher.py)
    e. [If >5min] pf_update_step(work_item_id=WI_ID, step_id="code_change",
                                  heartbeat=true)
    f. pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
                      step_attempt_id=SA_CODE_2,
                      artifact_summary=JSON({
                        summary: "added idempotency key check to webhook dispatcher",
                        files_changed: ["src/webhooks/dispatcher.py"],
                        tests_status: "passing",
                        notes: "retry attempt 2 succeeded"
                      }))

  Wi Agent verifies completion:
    pf_get_step(work_item_id=WI_ID)
    assert current_step == "commit_and_pr"  // code_change advanced

ASSERT MCP CALLS (retry attempt 2):
  - pf_update_step(step_id="code_change", status="in_progress")
    called with expected_version=V3 (the post-reset version)
  - step_attempt_id returned = SA_CODE_2 (distinct from SA_CODE_1)
  - File edit present in WT_PATH (verify via git -C WT_PATH diff --stat)
  - pf_update_step(step_id="code_change", status="completed") called with artifact_summary
    containing files_changed
  - pf_save_artifact NOT called (code_change puts output in artifact_summary only)

ASSERT STATE after retry:
  - pf_get_step(WI_ID) → current_step="commit_and_pr"
  - code_change.status="completed", step_attempt_id=SA_CODE_2
  - retry_count remains 1 (escalation threshold NOT reached)
  - `git -C WT_PATH diff --stat` shows modified file(s)

NOTE: pf-execute continues the main step loop normally after a successful retry.
The subsequent commit_and_pr step is dispatched exactly as in SC-05 (not tested here).

---

### Step 6 (escalation path) — step stalls 3 times; pf-execute escalates
NOTE: This step describes a SEPARATE variant of the scenario.
Run it as an independent sub-case where the wi is reset so that retry_count reaches 3.
Setup for this sub-case: code_change step is "pending"; retry_count starts at 0.

EXPECTED SKILL BEHAVIOR — three consecutive stalls:

  STALL 1 → RESET 1 (as in Steps 2-4 above, retry_count = 1)
  STALL 2 → RESET 2 (same sequence):
    pf_update_step(step_id="code_change", status="failed",
                   step_attempt_id=SA_CODE_2, escalated=false)
    pf_renew_lease(attempt_id=ATTEMPT_ID, claim_epoch=CLAIM_EPOCH,
                   session_secret=SESSION_SECRET)
    retry_count = 2

  STALL 3 detected → retry_count is now 2; incrementing to 3 ≥ threshold:
    // Do NOT dispatch another subagent
    // Escalation: mark step failed with escalated=true
    pf_update_step(
      work_item_id=WI_ID,
      step_id="code_change",
      status="failed",
      step_attempt_id=SA_CODE_3,
      escalated=true
    )
    // Emit human-escalation note
    pf_emit_event(
      work_item_id=WI_ID,
      event_type="note",
      payload={"text": "step code_change failed 3 times, escalating to human"}
    )
    // Break out of step loop — DO NOT attempt further dispatch or retry
    break

  After break: pf-execute MUST NOT call pf_wrap or dispatch retro.
  It outputs the three-segment status indicating manual intervention needed.

ASSERT MCP CALLS (escalation):
  - pf_update_step(step_id="code_change", status="failed",
                   step_attempt_id=SA_CODE_3, escalated=true) called
  - pf_emit_event(event_type="note",
                  payload={"text": "step code_change failed 3 times, escalating to human"})
    called immediately after
  - NO additional pf_update_step(status="in_progress") after the escalation
  - pf_wrap NOT called
  - Retro subagent NOT dispatched
  - retry_count did NOT exceed 3 (loop exits at exactly 3)

ASSERT STATE (escalation):
  - code_change.status="failed", escalated=true
  - wi.status="running" (NOT "wrapped" or "failed" — human must resolve)
  - pf_read_events(WI_ID) contains the escalation note event

---

### Step 7: pf-execute output after successful retry (Step 5 path)
EXPECTED SKILL BEHAVIOR (three-segment format after retry succeeds):

  ## 结果
  code_change completed on retry attempt 2 (attempt 1 stalled >60s; step was reset and
  re-dispatched). Continuing to commit_and_pr.

  ## 状态
  | wi      | marketplace#<seq>                        |
  | step    | 2/3 code_change (retry=1)                |
  | status  | running — advancing to commit_and_pr     |
  | expires | ~58min                                   |

  ## 下一步
  - commit_and_pr step will be dispatched next
  - Monitor with `/pf-status`

EXPECTED SKILL BEHAVIOR (three-segment format after escalation — Step 6 path):

  ## 结果
  step code_change failed 3 times (M16 retry exhausted). Escalation note emitted.
  Manual intervention required.

  ## 状态
  | wi      | marketplace#<seq>              |
  | step    | code_change (escalated=true)   |
  | status  | stalled — awaiting human       |

  ## 下一步
  - Investigate subagent stall root cause (check aihub logs, worktree state)
  - Fix the underlying issue, then re-claim the wi with `/pf-resume`
  - Or cancel the wi with `/pf-stop --fail`

---

## PASS criteria

### Retry path (Steps 1-5)
- pf_recall called before first dispatch (Memory-First enforced)
- 60s wait executed; stall detected when step remains "in_progress"
- pf_update_step(status="failed", escalated=false) called to reset stale step
- pf_renew_lease called before retry dispatch with correct attempt_id + claim_epoch
- Fresh subagent dispatched with retry_count=1; uses post-reset version (V3)
- Retry subagent calls pf_update_step(in_progress) with expected_version=V3
- Retry subagent calls pf_update_step(completed) with non-empty artifact_summary
- pf_get_step after retry shows current_step="commit_and_pr"
- pf_save_artifact NOT called at any point during code_change

### Escalation path (Step 6)
- pf_update_step(escalated=true) called on 3rd failure (NOT on 1st or 2nd)
- pf_emit_event(event_type="note") called with exact payload text:
  "step code_change failed 3 times, escalating to human"
- NO 4th subagent dispatch after escalation
- pf_wrap NOT called; retro NOT dispatched
- Wi remains in "running" state for human recovery
