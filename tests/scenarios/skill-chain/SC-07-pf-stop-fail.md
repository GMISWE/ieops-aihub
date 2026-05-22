# SC-07 — pf-stop --fail on a terminal dead end

Tests that pf-stop --fail moves the wi to a terminal failed state, emits the failure
reason as a note event, deletes the state file, and suggests creating a follow-up
bug wi to track the root cause.

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace
- WI_ID: pre-created fix_bug wi, already claimed and running
  - wi goal: "fix: migrate legacy auth tokens to JWT"
  - wi_type: fix_bug, requires_human_session=false
  - Status: running, current_step="code_change" (in progress)
  - State file at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
  - Worktree at WT_PATH = WORKSPACE_ROOT/pf.<shortid>/marketplace/
  - ATTEMPT_ID, CLAIM_EPOCH loaded from state file

## Scenario

### Step 1: User is working and discovers a fundamental blocker
Context: code_change step is in_progress (step_attempt_id=SA_ID set).
User discovers that the legacy auth system has undocumented dependencies that make
the migration fundamentally unsafe in the current sprint scope.

USER_INTENT: "this is a dead end — the legacy token system is too entangled to
             migrate safely. Abandoning this."

---

### Step 2: User invokes pf-stop --fail
SKILL_INVOKE: polyforge:pf-stop --fail

EXPECTED SKILL BEHAVIOR (pf-stop fail mode):
  NOTE: There is an in_progress step (code_change). The SKILL.md for pf-stop --fail
  does NOT describe resetting in_progress steps (unlike --pause). However, a clean
  terminal transition requires the step to not be left dangling. The agent SHOULD
  reset it before calling pf_complete_attempt if a step is in_progress.

  Recommended sequence:
  1. [Optional but correct] If any step is in_progress, reset it:
     pf_update_step(
       work_item_id=WI_ID,
       step_id="code_change",
       status="failed",
       step_attempt_id=SA_ID,
       escalated=false
     )

  2. Terminal failure:
     pf_complete_attempt(
       work_item_id=WI_ID,
       status="failed"
     )

  3. Emit failure reason:
     pf_emit_event(
       work_item_id=WI_ID,
       event_type="note",
       payload={text: "failed reason: legacy auth token system has undocumented
                        dependencies — migration is unsafe in current sprint scope.
                        Root cause needs investigation before retry."}
     )

  4. Delete state file:
     Remove WORKSPACE_ROOT/.polyforge/state/WI_ID.json

  5. Output three-segment format:
     ## 结果
     wi WI_ID marked as failed. State file deleted.

     ## 状态
     | wi      | marketplace#<seq>   |
     | status  | failed              |
     | reason  | dead end: legacy auth entanglement |

     ## 下一步
     - Create a bug wi to investigate the root cause:
       `/pf-work --goal 'bug: document legacy auth token dependencies before migration'`
     - Or: `/pf-work --goal 'investigate: legacy auth token system undocumented deps'`

ASSERT MCP CALLS:
  - pf_complete_attempt(work_item_id=WI_ID, status="failed") called
  - pf_emit_event(work_item_id=WI_ID, event_type="note") called
    payload.text contains the failure description (not empty)
  - pf_wrap NOT called (fail is not a success wrap)
  - pf_complete_attempt(status="wrapped") NOT called

ASSERT STATE after fail:
  - WI_ID status="failed" (terminal)
  - State file deleted: ls WORKSPACE_ROOT/.polyforge/state/WI_ID.json → not found
  - WI_ID does NOT appear in items[], running[], or paused[] segments
  - pf_get_ready_queue → WI_ID not in any active segment

NOTE: Worktree cleanup on --fail is implementation-defined (the SKILL.md does not
specify worktree removal for fail). Agents SHOULD clean up the worktree to avoid
orphan directories, but this is not a PASS/FAIL criterion here.

---

### Step 3: User creates a follow-up bug wi to track root cause
SKILL_INVOKE: polyforge:pf-work
USER_INTENT: "create a bug wi to investigate the legacy auth token dependencies"

EXPECTED SKILL BEHAVIOR:
  1. pf_recall(project="marketplace", query="legacy auth token dependencies",
               type=["experience.*","rule.*"])
  2. Infer wi_type=critical_bug (root cause unknown, needs investigation),
     requires_human_session=true
  3. pf_create_work_item(
       project="marketplace",
       goal="bug: document undocumented legacy auth token system dependencies",
       wi_type="critical_bug",
       requires_human_session=true,
       priority="high",
       labels=["tech-debt","auth"]
     )
  4. Because requires_human_session=true: surface in needs_human_session[] queue.
     Notify: "This wi requires a human-led session."

ASSERT MCP CALLS:
  - pf_create_work_item called with a goal referencing the root cause
  - New wi created in needs_human_session[] (not auto-claimed)

ASSERT STATE:
  - BUG_WI_ID created, status="queued", requires_human_session=true
  - pf_get_ready_queue → BUG_WI_ID in needs_human_session[]
  - Original WI_ID remains failed (immutable terminal state)

NOTE: Creating the follow-up wi is optional from the user's perspective — pf-stop
--fail only suggests it. The assertion on Step 3 is conditional on user following
the suggestion. Test passes if Step 2 PASS criteria are met, regardless of Step 3.

## PASS criteria
pf-stop --fail calls pf_complete_attempt(status="failed") — NOT "wrapped" or "paused";
pf_emit_event called with failure reason in payload.text;
state file deleted after fail;
WI_ID moves to terminal failed state (not retrievable via pf_get_ready_queue);
output suggests creating a follow-up bug wi.
