# SC-05 — pf-execute drives fix_bug wi end-to-end (auto, Session 1)

Tests pf-execute as the proper Layer 3 entry point for an auto wi
(requires_human_session=false). pf-execute reads the phase.yaml step graph and
dispatches each Step Agent in sequence: prepare_context → code_change →
commit_and_pr. It auto-calls pf-retro before the final pf_wrap.

NOTE: SC-01 tests direct per-step skill invocation for isolated step boundary
assertions. SC-05 tests pf-execute as the orchestrating Wi Agent — the recommended
production entry point. Direct step dispatch is pf-execute's internal mechanic, not
a user-facing step.

Runner: dispatch a subagent with access to all polyforge skills + MCP tools.
The subagent must follow pf-execute skill instructions, not call MCP tools directly.

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace
- WI_ID: pre-created fix_bug wi, already claimed (state file written, worktree exists)
  - WI_ID status = "running"
  - State file at WORKSPACE_ROOT/.polyforge/state/WI_ID.json (contains attempt_id,
    claim_epoch, workspace_root, repo, task_branch)
  - Worktree at WORKSPACE_ROOT/pf.<shortid>/marketplace/ (WT_PATH)
  - wi goal: "fix: remove stale cache entry on user logout"
  - wi_type: fix_bug, requires_human_session=false
  - Steps per phase.yaml: ["prepare_context", "code_change", "commit_and_pr"]
    mapped to start_step, step-2, ship

## Scenario

### Step 1: User invokes pf-execute
SKILL_INVOKE: polyforge:pf-execute
USER_INTENT: "execute" (or "run it" / "let's do it")

EXPECTED SKILL BEHAVIOR — Setup phase:
  1. Load wi info:
     pf_list_work_items(ids=[WI_ID], include_step_state=true)
     → requires_human_session=false, current_step="start_step", phase_mode="step"

  2. Memory-First before dispatch:
     pf_recall(project="marketplace", query=wi.goal,
               type=["experience.*","rule.*"], top_k=5)
     Activate any useful results: pf_activate_memory(id) for each match

ASSERT MCP CALLS (setup):
  - pf_list_work_items called with ids=[WI_ID], include_step_state=true
  - pf_recall called before any pf_update_step (Memory-First enforced)

---

### Step 2: pf-execute dispatches start_step (prepare_context) subagent
EXPECTED SKILL BEHAVIOR (from pf-execute step loop):
  Because requires_human_session=false → dispatch subagent (NOT inline execution):

  Subagent instruction:
    action: prepare_context
    skill: polyforge-coding:prepare_context

  Subagent executes (per prepare_context skill):
    a. pf_list_work_items(ids=[WI_ID], include_step_state=true)
    b. pf_recall(project="marketplace", query=wi.goal, type=["experience.*","rule.*"], top_k=5)
    c. pf_activate_memory(id) for each useful result
    d. Read codebase in WT_PATH (git log, read key files)
    e. pf_get_step(work_item_id=WI_ID) — get current version
    f. pf_update_step(work_item_id=WI_ID, step_id="start_step",
                      status="in_progress", expected_version=<version>)
       → returns step_attempt_id, new version
    g. Build initial_context JSON: {goal_analysis, relevant_files, prior_experience,
                                    known_pitfalls, suggested_approach, test_baseline}
    h. [If step takes >5min] pf_update_step(work_item_id=WI_ID, step_id="start_step",
                                             heartbeat=true)
    i. pf_update_step(work_item_id=WI_ID, step_id="start_step", status="completed",
                      step_attempt_id=<from f>,
                      artifact_summary=<initial_context JSON [0:4096]>)
    NOTE: prepare_context does NOT call pf_save_artifact. Output goes in artifact_summary only.

  Wi Agent verifies completion:
    pf_get_step(work_item_id=WI_ID)
    assert current_step != "start_step"  // must advance to code_change

ASSERT MCP CALLS (start_step dispatch):
  - pf_update_step(step_id="start_step", status="in_progress") called by subagent
  - pf_update_step(step_id="start_step", status="completed") called with artifact_summary
    containing initial_context JSON (not pf_save_artifact)
  - pf_get_step called after subagent completes (Wi Agent verification)
  - pf_save_artifact NOT called

ASSERT STATE after start_step:
  - pf_get_step(WI_ID) → current_step="code_change"
  - start_step.status="completed"
  - start_step.artifact_summary contains initial_context JSON

NOTE: If subagent does not advance within 60s, pf-execute triggers M16 retry logic:
  1. pf_update_step(status="failed", step_attempt_id=<old>, escalated=false) — reset stale
  2. pf_renew_lease(attempt_id=<current_attempt_id>, claim_epoch=<current_claim_epoch>,
                    session_secret=<session_secret>) — prevent zombie sweep during retry
     If renew fails or status="lost_lease" → stop, escalate to user
  3. Dispatch new subagent (retry_count += 1; fail escalated=true if retry_count >= 3)

---

### Step 3: pf-execute dispatches code_change subagent
EXPECTED SKILL BEHAVIOR (main step loop iteration 1):
  step_info = pf_get_step(work_item_id=WI_ID)
  current_step = "code_change"

  Memory-First before dispatch:
    pf_recall(project="marketplace", query=<step description>,
              type=["experience.*","rule.*"], top_k=3)

  Dispatch subagent with:
    action: code_change
    skill: polyforge-coding:code_change
    previous_context: start_step.artifact_summary (initial_context JSON)

  Subagent executes (per code_change skill):
    a. pf_get_step(work_item_id=WI_ID) — get current version
    b. pf_update_step(work_item_id=WI_ID, step_id="code_change",
                      status="in_progress", expected_version=<version>)
       → returns step_attempt_id
    c. Read initial_context from previous_steps.start_step.artifact_summary
    d. Edit file(s) in WT_PATH (apply fix: remove stale cache entry on logout)
    e. [If >5min] pf_update_step(work_item_id=WI_ID, step_id="code_change",
                                  heartbeat=true)
    f. pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
                      step_attempt_id=<from b>,
                      artifact_summary=JSON({summary, files_changed, tests_status, notes}))
    NOTE: code_change does NOT call pf_save_artifact.

  Knowledge distillation (if artifact contains interesting findings):
    candidates = pf_recall(query=<finding>, type="experience.*", top_k=3)
    if similarity > 0.85 → pf_activate_memory(existing_id) +
                            pf_remember(dedup_mode="merge", supersedes_memory_id=existing_id)
    elif similarity > 0.65 → pf_remember(dedup_mode="suggest")
    else → pf_remember(dedup_mode="off")

ASSERT MCP CALLS (code_change dispatch):
  - pf_recall called before dispatch (Memory-First before each step)
  - pf_update_step(step_id="code_change", status="in_progress") called by subagent
  - File edit in WT_PATH (verify via `git -C WT_PATH diff --stat`)
  - pf_update_step(step_id="code_change", status="completed") called with artifact_summary
  - pf_save_artifact NOT called

ASSERT STATE after code_change:
  - `git -C WT_PATH status` shows modified files
  - pf_get_step(WI_ID) → current_step="commit_and_pr" (or equivalent ship step)
  - code_change.artifact_summary contains files_changed

---

### Step 4: pf-execute dispatches commit_and_pr subagent
EXPECTED SKILL BEHAVIOR (main step loop iteration 2):
  step_info = pf_get_step(work_item_id=WI_ID)
  current_step = "commit_and_pr" (step id may be "ship" — per phase.yaml mapping)

  Memory-First before dispatch:
    pf_recall(project="marketplace", query=<step description>,
              type=["experience.*","rule.*"], top_k=3)

  Dispatch subagent with:
    action: commit_and_pr
    skill: polyforge-coding:commit_and_pr
    previous_context: code_change.artifact_summary

  Subagent executes (per commit_and_pr skill):
    a. pf_get_step(work_item_id=WI_ID) — get current version
    b. pf_update_step(work_item_id=WI_ID, step_id="ship",
                      status="in_progress", expected_version=<version>)
    c. pf_diff(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
               repo="marketplace", vs_base=true)
    d. pf_commit(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
                 repo="marketplace",
                 message="fix(cache): remove stale entry on user logout\n\n...\n\nwi: marketplace#<seq>")
    e. pf_push(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
               repo="marketplace", skip_base_check=false)
    f. pf_pr(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
             repo="marketplace",
             title="fix(cache): remove stale entry on user logout", body="...")
    g. pf_update_step(work_item_id=WI_ID, step_id="ship", status="completed",
                      step_attempt_id=<from b>, artifact_summary="PR #N: <url>")

ASSERT MCP CALLS (commit_and_pr dispatch):
  - pf_recall called before dispatch (Memory-First)
  - pf_diff called with workspace_root, work_item_id, repo
  - pf_commit called with conventional-format message containing "wi: marketplace#<seq>"
  - pf_push called with workspace_root, work_item_id, repo
  - pf_pr called → PR URL in response

ASSERT STATE after commit_and_pr:
  - `git -C WT_PATH log --oneline -1` shows the commit
  - pf_get_step(WI_ID) → current_step=null (all 3 steps done — loop breaks)

---

### Step 5: pf-execute dispatches retro subagent (automatic, built-in)
NOTE: pf-execute dispatches retro automatically — it is NOT in the phase.yaml step
graph. It runs after step loop exits (current_step=null) and before pf_wrap.

EXPECTED SKILL BEHAVIOR (automatic retro):
  Dispatch retro subagent with:
    wi_id: WI_ID
    wi_goal: "fix: remove stale cache entry on user logout"
    step_summaries: [
      {step_id: "start_step", artifact_summary: "<initial_context JSON>"},
      {step_id: "code_change", artifact_summary: "<files_changed JSON>"},
      {step_id: "ship",       artifact_summary: "PR #N: <url>"}
    ]

  Retro subagent executes (per pf-retro skill):
    1. pf_read_events(work_item_id=WI_ID, limit=50,
                      types=["commit","push","pr_opened","step_completed"])
    2. pf_recall(project="marketplace", query=wi.goal, type="experience.*", top_k=3)
       + pf_activate_memory(id) for each relevant result
    3. LLM retrospective analysis (planned vs actual, deviations, learnings)
    4. For each finding: pf_recall(query=...) FIRST, then pf_remember(body=...,
                                   type="experience.*", visibility="team")
    5. pf_save_artifact(type="methodology.wrap_summary", work_item_id=WI_ID,
                        content="<1-paragraph summary>")

  If retro fails partially:
    pf_emit_event(work_item_id=WI_ID, event_type="retro_partial_failure",
                  payload={error: <description>})
    Continue to wrap (best-effort; incomplete knowledge preferred over stalled wi).

ASSERT MCP CALLS (retro):
  - pf_read_events called with work_item_id=WI_ID
  - pf_recall called BEFORE any pf_remember (Memory-First enforced)
  - pf_save_artifact called with type="methodology.wrap_summary"
  - pf_remember called with body= param (NOT content=), visibility="team" or "project"

---

### Step 6: pf-execute calls pf_wrap (coding scenario)
EXPECTED SKILL BEHAVIOR (coding scenario wrap):
  pf_wrap(
    workspace_root=WORKSPACE_ROOT,
    work_item_id=WI_ID,
    attempt_id=<from state file>,
    claim_epoch=<from state file>,
    session_secret=<injected by MCP server from state file>
  )
  NOTE: pf_wrap = on_wrap hook + pf_complete_attempt(wrapped) + workspace cleanup.
  Do NOT call pf_complete_attempt directly for coding scenarios.

  After pf_wrap:
    State file deleted: WORKSPACE_ROOT/.polyforge/state/WI_ID.json → removed
    Worktree at WT_PATH removed by pf_wrap

ASSERT MCP CALLS (wrap):
  - pf_wrap called (NOT pf_complete_attempt directly)
  - pf_wrap called with workspace_root, work_item_id, attempt_id, claim_epoch

ASSERT FINAL STATE:
  - WI_ID status == "wrapped"
  - State file deleted: ls WORKSPACE_ROOT/.polyforge/state/WI_ID.json → not found
  - Worktree WT_PATH removed

---

### pf-execute output (three-segment format)
After kicking off execution, pf-execute outputs:

  ## 结果
  Started execution of wi <slug>. All 3 steps dispatched and completed.
  Retro completed. wi wrapped.

  ## 状态
  | wi      | marketplace#<seq>             |
  | steps   | 3/3 completed                 |
  | status  | wrapped                       |

  ## 下一步
  - PR #N is open for review
  - Run `/pf-status` to verify wi state

## PASS criteria
pf-execute reads phase.yaml step graph and dispatches all 3 Step Agents in sequence;
pf_recall called before each step dispatch (Memory-First); pf_update_step called
in_progress then completed for every step; pf_wrap called (not pf_complete_attempt);
retro subagent dispatched automatically before wrap; state file deleted after wrap.
