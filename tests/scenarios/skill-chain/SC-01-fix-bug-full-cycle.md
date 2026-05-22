# SC-01 — fix_bug full cycle via skills (Session 3, auto)

Tests the complete skill chain for a fix_bug wi:
pf-work → prepare_context → code_change → commit_and_pr → pf-stop

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
  - Worktree created at WORKSPACE_ROOT/pf.<seq>.<ulid8>/marketplace/
  - Save WT_PATH

### Step 2: prepare_context step
SKILL_INVOKE: polyforge-coding:prepare_context

EXPECTED SKILL BEHAVIOR:
  1. pf_recall(project="marketplace", query=wi.goal, type=["experience.*","rule.*","pitfall.*"])
  2. Read codebase in WT_PATH to understand the target area
  3. pf_update_step(step_id="prepare_context", status="in_progress")
  4. Heartbeat if taking >5min: pf_update_step(heartbeat=true)
  5. pf_save_artifact(type="methodology.spec", content="Context: found handler at ...; Fix: add guard at line X")
  6. pf_update_step(step_id="prepare_context", status="completed", artifact_summary="context: null check location identified")

ASSERT MCP CALLS:
  - pf_recall called
  - pf_update_step(prepare_context, in_progress) called
  - pf_save_artifact called → mem_id saved
  - pf_update_step(prepare_context, completed) called with artifact_summary

ASSERT STATE:
  - pf_get_step → current_step="prepare_context", current_step_status="idle" (after completed)
  - methodology.spec artifact in memory for WI_ID

### Step 3: code_change step
SKILL_INVOKE: polyforge-coding:code_change

EXPECTED SKILL BEHAVIOR:
  1. pf_update_step(step_id="code_change", status="in_progress")
  2. Read initial_context artifact from prepare_context
  3. Edit file in WT_PATH (the actual fix — add null check)
  4. Periodic heartbeat: pf_update_step(heartbeat=true) every ~5min
  5. pf_save_artifact(type="methodology.plan", content="Changed: added `if input == nil { return ErrBadInput }` at line X")
  6. pf_update_step(step_id="code_change", status="completed", artifact_summary="added null guard in handler.go:42")

ASSERT MCP CALLS:
  - pf_update_step(code_change, in_progress)
  - File edit in WT_PATH (verify via `git -C WT_PATH diff --stat`)
  - pf_update_step(code_change, completed)

ASSERT STATE:
  - `git -C WT_PATH status` shows modified files
  - pf_get_step → version incremented

### Step 4: commit_and_pr step
SKILL_INVOKE: polyforge-coding:commit_and_pr

EXPECTED SKILL BEHAVIOR:
  1. pf_update_step(step_id="commit_and_pr", status="in_progress")
  2. pf_diff(work_item_id=WI_ID) — review the diff
  3. pf_commit(work_item_id=WI_ID, message="fix: add null check for empty user input") — conventional commit format
  4. pf_push(work_item_id=WI_ID) — push to task branch
  5. pf_pr(work_item_id=WI_ID, title="fix: add null check", body="...") — open PR
  6. pf_update_step(step_id="commit_and_pr", status="completed", artifact_summary="PR #N opened")

ASSERT MCP CALLS:
  - pf_diff called
  - pf_commit called with conventional format message
  - pf_push called
  - pf_pr called → PR URL in response

ASSERT STATE:
  - `git -C WT_PATH log --oneline -1` shows the commit
  - pf_get_step → commit_and_pr completed

### Step 5: wrap up
SKILL_INVOKE: polyforge:pf-stop --wrap

EXPECTED SKILL BEHAVIOR:
  1. pf_emit_event(event_type="note", payload={text: "wrapped: null check fix complete, PR opened"})
  2. pf_complete_attempt(status="wrapped")
  3. State file deleted

ASSERT MCP CALLS:
  - pf_emit_event called
  - pf_complete_attempt(wrapped) called

ASSERT FINAL STATE:
  - WI_ID status=="wrapped"
  - State file deleted: ls WORKSPACE_ROOT/.polyforge/state/WI_ID.json → not found
  - `POLYFORGE_WORKSPACE_ROOT= polyforge doctor --fix` → worktree cleaned up

## PASS criteria
All 5 skill invocations complete their expected MCP calls;
worktree created and cleaned up; PR opened; wi wrapped.
