# SC-01 — fix_bug full cycle via skills (Session 1, auto)

Tests the complete skill chain for a fix_bug wi (auto, requires_human_session=false):
pf-work → pf-execute (dispatches prepare_context → code_change → commit_and_pr) → pf-stop

NOTE: In production, pf-execute dispatches the per-step skills as subagents.
Direct per-step skill invocation here is intentional for isolated step testing —
it mirrors what pf-execute's subagents do, allowing assertion at each step boundary.

Runner: dispatch a subagent with access to all polyforge skills + MCP tools.
The agent must follow each skill's instructions, not call MCP tools directly.

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace (has .repo/marketplace/ canonical clone)
- All polyforge skills available

## Scenario

### Step 1: User says "let's fix the missing null check"
SKILL_INVOKE: polyforge:pf-work
USER_INTENT: "fix: add null check for empty user input in marketplace auth handler"

EXPECTED SKILL BEHAVIOR (from pf-work skill):
  1. Memory-First: pf_recall(project="marketplace", query="null check auth handler", type=["experience.*","rule.*"])
  2. Infer wi_type=fix_bug (small focused bug fix), requires_human_session=false
  3. pf_predict_conflicts(declared_resources=[...])
  4. pf_create_work_item(project="marketplace", goal="fix: add null check...", wi_type="fix_bug", requires_human_session=false)
  5. pf_claim_work_item(work_item_id=WI_ID, mode="fresh", ...)

ASSERT MCP CALLS:
  - pf_recall called with project="marketplace"
  - pf_create_work_item called with wi_type="fix_bug"
  - pf_claim_work_item called → response.ok==true

ASSERT STATE:
  - WI_ID created, status="running"
  - State file written at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
  - Worktree created at WORKSPACE_ROOT/pf.<shortid>/marketplace/
  - Save WT_PATH (= WORKSPACE_ROOT/pf.<shortid>/marketplace/)

NOTE: fix_bug steps per phase.yaml: ["prepare_context", "code_change", "commit_and_pr"]
      (3 steps, all auto — no spec/plan steps)

### Step 2: prepare_context step
SKILL_INVOKE: polyforge-coding:prepare_context

NOTE: prepare_context saves its output ONLY in the step's artifact_summary via
pf_update_step(completed, artifact_summary=<initial_context JSON>).
It does NOT call pf_save_artifact. The artifact_summary is consumed by code_change
via pf_get_step(previous_steps.prepare_context.artifact_summary).

EXPECTED SKILL BEHAVIOR:
  1. pf_list_work_items(ids=[WI_ID], include_step_state=true) — load wi context
  2. pf_recall(project="marketplace", query=wi.goal, type=["experience.*","rule.*"], top_k=5)
  3. pf_activate_memory(id) for each useful result
  4. Read codebase in WT_PATH to understand the target area (git log, Read key files)
  5. pf_get_step(work_item_id=WI_ID) — get current version
  6. pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress", expected_version=<version>)
  7. Build initial_context JSON: {goal_analysis, relevant_files, prior_experience, known_pitfalls, suggested_approach, test_baseline}
  8. pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="completed", step_attempt_id=<from 6>, artifact_summary=<initial_context JSON [0:4096]>)

ASSERT MCP CALLS:
  - pf_recall called
  - pf_update_step(prepare_context, in_progress) called
  - pf_update_step(prepare_context, completed) called with artifact_summary containing initial_context JSON
  - pf_save_artifact NOT called (prepare_context does not save artifacts)

ASSERT STATE:
  - pf_get_step(WI_ID) → current_step="code_change" (advanced past prepare_context)
  - prepare_context.artifact_summary contains initial_context JSON

### Step 3: code_change step
SKILL_INVOKE: polyforge-coding:code_change

NOTE: code_change saves its output ONLY in the step's artifact_summary via
pf_update_step(completed, artifact_summary=...). It does NOT call pf_save_artifact.

EXPECTED SKILL BEHAVIOR:
  1. pf_get_step(work_item_id=WI_ID) — get current version
  2. pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress", expected_version=<version>)
     → returns step_attempt_id
  3. pf_get_step(work_item_id=WI_ID) — read initial_context from previous_steps.prepare_context.artifact_summary
  4. Edit file in WT_PATH (the actual fix — add null check)
  5. Periodic heartbeat if taking >5min: pf_update_step(heartbeat=true)
  6. pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
       step_attempt_id=<from 2>,
       artifact_summary=JSON({summary, files_changed, tests_status, notes}))

ASSERT MCP CALLS:
  - pf_update_step(code_change, in_progress) called
  - File edit in WT_PATH (verify via `git -C WT_PATH diff --stat`)
  - pf_update_step(code_change, completed) called with artifact_summary
  - pf_save_artifact NOT called

ASSERT STATE:
  - `git -C WT_PATH status` shows modified files
  - pf_get_step(WI_ID) → current_step="commit_and_pr"

### Step 4: commit_and_pr step
SKILL_INVOKE: polyforge-coding:commit_and_pr

EXPECTED SKILL BEHAVIOR:
  1. pf_get_step(work_item_id=WI_ID) — get current version
  2. pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="in_progress", expected_version=<version>)
  3. pf_diff(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace", vs_base=true) — review the diff
  4. pf_commit(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace",
       message="fix(auth): add null check for empty user input\n\n...\n\nwi: marketplace#<seq>")
  5. pf_push(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace", skip_base_check=false)
  6. pf_pr(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace",
       title="fix(auth): add null check for empty user input", body="...")
  7. pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="completed",
       step_attempt_id=<from 2>, artifact_summary="PR #N: <url>")

ASSERT MCP CALLS:
  - pf_diff called with workspace_root, work_item_id, repo params
  - pf_commit called with workspace_root, work_item_id, repo, message (conventional format)
  - pf_push called with workspace_root, work_item_id, repo
  - pf_pr called with workspace_root, work_item_id, repo, title, body → PR URL in response

ASSERT STATE:
  - `git -C WT_PATH log --oneline -1` shows the commit
  - pf_get_step(WI_ID) → commit_and_pr completed; current_step=null (all steps done)

### Step 5: wrap up
SKILL_INVOKE: polyforge:pf-stop --wrap

EXPECTED SKILL BEHAVIOR (coding scenario — use pf_wrap, not pf_complete_attempt):
  1. pf_wrap(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace")
     (pf_wrap = on_wrap hook + pf_complete_attempt(wrapped) + workspace cleanup;
      credentials injected from state file by MCP server)
  2. pf_emit_event(work_item_id=WI_ID, event_type="note",
       payload={text: "wrapped: null check fix complete, PR opened"})
  3. State file deleted at WORKSPACE_ROOT/.polyforge/state/WI_ID.json

ASSERT MCP CALLS:
  - pf_wrap called (NOT pf_complete_attempt directly)
  - pf_emit_event called

ASSERT FINAL STATE:
  - WI_ID status=="wrapped"
  - State file deleted: ls WORKSPACE_ROOT/.polyforge/state/WI_ID.json → not found
  - Worktree at WT_PATH removed (pf_wrap cleans up)

## PASS criteria
All 5 skill invocations complete their expected MCP calls;
worktree created and cleaned up; PR opened; wi wrapped via pf_wrap.
