# SC-06 — pf-stop --pause then pf-work --resume

Tests that pf-stop --pause correctly releases the lease while keeping locks and
state file, and that pf-work --resume reclaims the wi and continues from where it
left off (step preserved, worktree restored).

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace
- wi goal: "fix: debounce rapid search queries to avoid API hammering"
- wi_type: fix_bug, requires_human_session=false
- Steps per phase.yaml: ["prepare_context", "code_change", "commit_and_pr"]

## Scenario

### Step 1: User starts fix_bug wi
SKILL_INVOKE: polyforge:pf-work
USER_INTENT: "fix: debounce rapid search queries to avoid API hammering"

EXPECTED SKILL BEHAVIOR:
  1. pf_recall(project="marketplace", query="debounce search queries",
               type=["experience.*","rule.*"])
  2. Infer wi_type=fix_bug, requires_human_session=false
  3. pf_create_work_item(project="marketplace",
                          goal="fix: debounce rapid search queries...",
                          wi_type="fix_bug",
                          requires_human_session=false,
                          priority="normal")
  4. pf_predict_conflicts(declared_resources=[...], dry_run=true)
  5. pf_claim_work_item(work_item_id=WI_ID, mode="fresh",
                         idempotency_key=<client ULID>,
                         session_info={machine_id: <hostname>},
                         requested_locks=[{resource_type: "git_branch",
                                           resource_key: "marketplace/polyforge/<slug>"}])
     → returns {attempt_id, claim_epoch, expires_at}
     Save ATTEMPT_ID, CLAIM_EPOCH
  6. State file written at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
  7. Worktree created at WORKSPACE_ROOT/pf.<shortid>/marketplace/
     Save WT_PATH

ASSERT MCP CALLS:
  - pf_recall called
  - pf_create_work_item called with wi_type="fix_bug"
  - pf_claim_work_item returns ok=true

ASSERT STATE:
  - WI_ID status="running"
  - State file present at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
  - Worktree present at WT_PATH

---

### Step 2: User begins prepare_context (partial progress)
NOTE: User starts working but does not complete the step before pausing.
The step is in_progress state when pause is requested.

Partial prepare_context work:
  a. pf_get_step(work_item_id=WI_ID) → current_step="prepare_context", version=V1
  b. pf_update_step(work_item_id=WI_ID, step_id="prepare_context",
                    status="in_progress", expected_version=V1)
     → returns step_attempt_id=SA_ID
  (User's environment interrupted before completing the step)

ASSERT STATE:
  - prepare_context.status="in_progress"
  - step_attempt_id=SA_ID is set

---

### Step 3: User pauses (step still in_progress)
SKILL_INVOKE: polyforge:pf-stop --pause
USER_INTENT: "pause, I'll come back later"

EXPECTED SKILL BEHAVIOR (pf-stop pause mode):
  1. Detect in_progress step (prepare_context):
     pf_update_step(
       work_item_id=WI_ID,
       step_id="prepare_context",
       status="failed",
       step_attempt_id=SA_ID
     )
     This resets the step so it can be retried on resume.

  2. Release lease (keep locks):
     pf_complete_attempt(
       work_item_id=WI_ID,
       status="paused"
     )

  3. Keep state file at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
     (do NOT delete — needed for resume)

  4. Output three-segment format:
     ## 结果
     wi WI_ID paused. Locks retained; resume with `/pf-work WI_ID --resume`.

     ## 状态
     | wi      | marketplace#<seq>      |
     | status  | paused                 |
     | step    | prepare_context reset  |

     ## 下一步
     - Resume with `/pf-work WI_ID --resume`

ASSERT MCP CALLS:
  - pf_update_step(step_id="prepare_context", status="failed", step_attempt_id=SA_ID) called FIRST
    (resets in-progress step before releasing lease)
  - pf_complete_attempt(work_item_id=WI_ID, status="paused") called
  - pf_wrap NOT called (pause is not terminal)
  - pf_complete_attempt(status="wrapped") NOT called

ASSERT STATE after pause:
  - WI_ID status="paused"
  - prepare_context.status="failed" (reset for retry on resume)
  - Lease released (expires_at in the past or cleared)
  - Locks retained on git_branch resource
  - State file still present at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
  - Worktree still present at WT_PATH (not cleaned up on pause)

NOTE: A different agent cannot claim WI_ID while locks are held (lock conflict).
pf-status will show WI_ID in the paused[] segment, not items[].

---

### Step 4: (Time passes — user returns in a new session)

---

### Step 5: User resumes the wi
SKILL_INVOKE: polyforge:pf-work
USER_INTENT: "resume WI_ID" (or "/pf-work WI_ID --resume")

EXPECTED SKILL BEHAVIOR (Mode C — resume paused wi):
  1. pf_claim_work_item(
       work_item_id=WI_ID,
       mode="resume",
       idempotency_key=<new client ULID>
     )
     Restores: prepared workspace + step state from previous attempt.
     → returns {attempt_id=NEW_ATTEMPT_ID, claim_epoch=NEW_CLAIM_EPOCH, expires_at}

  2. Read state file at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
     (updated with new attempt_id, claim_epoch)

  3. Show step progress in output:
     "Resuming at step 1/3 (prepare_context — reset, will retry)"


  5. Output three-segment format with step progress in 状态 table:
     ## 结果
     Resumed wi WI_ID. Worktree restored at WT_PATH.

     ## 状态
     | wi      | marketplace#<seq>                       |
     | status  | running                                 |
     | step    | 1/3 prepare_context (resuming)          |
     | expires | 60min                                   |

     ## 下一步
     - Run `/pf-execute` to continue from prepare_context
     - Or manually invoke `polyforge-coding:prepare_context`

ASSERT MCP CALLS:
  - pf_claim_work_item(work_item_id=WI_ID, mode="resume") called
  - Returns NEW_ATTEMPT_ID, NEW_CLAIM_EPOCH (different from original ATTEMPT_ID)

ASSERT STATE after resume:
  - WI_ID status="running"
  - State file updated: attempt_id=NEW_ATTEMPT_ID, claim_epoch=NEW_CLAIM_EPOCH
  - Worktree at WT_PATH still present (or re-materialized if cleaned)
  - prepare_context.status="failed" still (ready to retry from step 1)
  - pf_get_step(WI_ID) → current_step="prepare_context" (reset, not "code_change")

---

### Step 6: Work continues from start_step
SKILL_INVOKE: polyforge:pf-execute (or polyforge-coding:prepare_context directly)
USER_INTENT: "execute" / "continue from where we left off"

NOTE: This is a normal pf-execute invocation after resume. The step graph picks up
at current_step="start_step" and proceeds through the full 3-step sequence.
See SC-05 for the complete pf-execute dispatch flow.

EXPECTED SKILL BEHAVIOR:
  pf_list_work_items(ids=[WI_ID], include_step_state=true)
  → current_step="prepare_context" (resumed state)
  Dispatch prepare_context subagent → code_change → commit_and_pr → retro → pf_wrap

ASSERT MCP CALLS:
  - pf_update_step(step_id="prepare_context", status="in_progress") called (fresh attempt)
  - All subsequent steps complete normally (see SC-05 for detailed assertions)
  - pf_wrap called at end (state file deleted)

## PASS criteria
pf-stop --pause resets in-progress step before releasing lease; lease released via
pf_complete_attempt(status="paused"); state file kept; locks retained; WI_ID appears
in paused[] segment; pf-work --resume calls pf_claim_work_item(mode="resume") and
returns a new attempt_id; work continues from prepare_context on resume.
