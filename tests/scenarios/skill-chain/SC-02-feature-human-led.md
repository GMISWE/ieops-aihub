# SC-02 — feature wi with spec+plan (Session 2, human-led)

Tests the full skill chain for a human-led feature wi (requires_human_session=true):
pf-work → pf-spec → pf-plan → code_change → commit_and_pr → review → pf-stop

NOTE: For requires_human_session=true wi's, pf-work surfaces the wi in
needs_human_session[] after creation. The human (user) must explicitly claim it by
invoking pf-work with the wi slug. pf-execute is the proper entry point for driving
all steps; direct per-step skill invocation here is intentional for isolated testing.

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace

## Scenario

NOTE: feature steps per phase.yaml: ["spec", "plan", "code_change", "commit_and_pr", "review"]
     (5 steps, human session required)

### Step 1: User starts feature wi
SKILL_INVOKE: polyforge:pf-work
USER_INTENT: "add dark mode toggle to marketplace UI"

EXPECTED SKILL BEHAVIOR:
  1. Memory-First recall
  2. Infer wi_type=feature, requires_human_session=true (design decisions needed)
  3. pf_create_work_item(wi_type="feature", requires_human_session=true)
  4. Because requires_human_session=true: surface wi in needs_human_session[] queue;
     notify user "This wi requires a human-led session."
     Auto-agents will NOT see this in items[].
  5. Human explicitly claims: pf_claim_work_item(work_item_id=WI_ID, mode="fresh", ...)
     → returns {attempt_id, claim_epoch, expires_at}
  6. Worktree created at WORKSPACE_ROOT/pf.<shortid>/marketplace/
     Save WT_PATH

ASSERT:
  - WI_ID created with wi_type="feature", requires_human_session=true
  - pf_claim_work_item returns ok=true (human claims it explicitly)
  - pf_get_ready_queue → WI_ID in running[] (since human claimed it in this session)

### Step 2: Write spec (human discusses with Claude)
SKILL_INVOKE: polyforge:pf-spec
USER_INTENT: "let's spec out the dark mode feature"

EXPECTED SKILL BEHAVIOR:
  1. pf_recall(project="marketplace", query=wi.goal, type=["methodology.spec","fact.*","rule.*"], top_k=3)
  2. pf_get_step(work_item_id=WI_ID) — get current step and version
  3. pf_update_step(work_item_id=WI_ID, step_id="spec", status="in_progress", expected_version=<version>)
     → returns step_attempt_id
  4. Heartbeat protocol: pf_update_step(heartbeat=true) every 5 min during discussion
  5. Guide user through: What/Why, Non-goals, Decisions, Acceptance criteria
  6. pf_save_artifact(type="methodology.spec", work_item_id=WI_ID,
       content=<markdown spec>,
       structured_payload={decisions:[...], acceptance_criteria:[...]},
       visibility="project")
  7. pf_emit_event(work_item_id=WI_ID, event_type="note", payload={text: "spec saved: mem_XXX"})
  8. pf_update_step(work_item_id=WI_ID, step_id="spec", status="completed",
       step_attempt_id=<from 3>, artifact_summary="spec saved: mem_XXX — dark mode via CSS vars")

ASSERT:
  - pf_recall called before pf_update_step (Memory-First)
  - pf_save_artifact called with type="methodology.spec"
  - pf_update_step(spec, completed) called with artifact_summary
  - pf_get_step(WI_ID) → current_step="plan"

### Step 3: Write plan
SKILL_INVOKE: polyforge:pf-plan

EXPECTED SKILL BEHAVIOR:
  1. pf_recall(project="marketplace", query=wi.goal, type=["methodology.plan","experience.*"], top_k=3)
  2. pf_recall(work_item_id=WI_ID, type="methodology.spec", top_k=1) — read spec
  3. pf_get_step(work_item_id=WI_ID) — get current step and version
  4. pf_update_step(work_item_id=WI_ID, step_id="plan", status="in_progress", expected_version=<version>)
  5. Break into implementation steps
  6. pf_save_artifact(type="methodology.plan", work_item_id=WI_ID,
       content=<markdown plan>,
       structured_payload={steps:[{id, title, depends_on, effort_hint}]},
       visibility="project")
  7. pf_update_step(work_item_id=WI_ID, step_id="plan", status="completed",
       step_attempt_id=<from 4>, artifact_summary="plan: 4 steps, ~1 day")

ASSERT:
  - methodology.plan artifact saved
  - pf_update_step(plan, completed) called with artifact_summary
  - pf_get_step(WI_ID) → current_step="code_change"

### Step 4: code_change
SKILL_INVOKE: polyforge-coding:code_change

NOTE: code_change does NOT call pf_save_artifact. Output goes in step artifact_summary only.

EXPECTED SKILL BEHAVIOR:
  1. pf_get_step(work_item_id=WI_ID) — get current version
  2. pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress", expected_version=<version>)
  3. Read context from previous steps (spec, plan, prepare_context if present)
  4. Edit files in WT_PATH (add dark mode toggle: CSS vars, toggle component)
  5. pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
       step_attempt_id=<from 2>,
       artifact_summary=JSON({summary, files_changed, tests_status}))

ASSERT:
  - File edits present in WT_PATH (git diff --stat shows changes)
  - pf_update_step(code_change, completed) called
  - pf_save_artifact NOT called

### Step 5: commit_and_pr
SKILL_INVOKE: polyforge-coding:commit_and_pr

EXPECTED SKILL BEHAVIOR:
  1. pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="in_progress", ...)
  2. pf_diff(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace", vs_base=true)
  3. pf_commit(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace",
       message="feat(ui): add dark mode toggle\n\n...\n\nwi: marketplace#<seq>")
  4. pf_push(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace")
  5. pf_pr(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace",
       title="feat(ui): add dark mode toggle", body="...")
  6. pf_update_step(step_id="commit_and_pr", status="completed", artifact_summary="PR #N: <url>")

ASSERT:
  - pf_commit called with conventional commit message
  - pf_pr called → PR URL in response
  - pf_get_step(WI_ID) → current_step="review"

### Step 6: review step (5th step of feature wi type)
SKILL_INVOKE: polyforge:pf-execute (or human inline review)
USER_INTENT: "review the PR and confirm it meets acceptance criteria"

NOTE: The "review" step is the 5th and final step for feature wi's (per phase.yaml).
It is a human-interactive step where Alice/Bob reviews the PR diff against acceptance
criteria and marks the step completed. pf-execute dispatches this inline when
requires_human_session=true.

EXPECTED SKILL BEHAVIOR:
  1. pf_get_step(work_item_id=WI_ID) — confirm current_step="review"
  2. pf_update_step(work_item_id=WI_ID, step_id="review", status="in_progress", ...)
  3. Human reviews PR: check acceptance criteria from spec artifact
  4. pf_update_step(work_item_id=WI_ID, step_id="review", status="completed",
       artifact_summary="review: all acceptance criteria met; PR approved")

ASSERT:
  - pf_update_step(review, completed) called
  - pf_get_step(WI_ID) → current_step=null (all 5 steps done)

### Step 7: Wrap
SKILL_INVOKE: polyforge:pf-stop --wrap

EXPECTED SKILL BEHAVIOR (coding scenario — use pf_wrap, not pf_complete_attempt):
  1. pf_wrap(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID, repo="marketplace")
  2. pf_emit_event(work_item_id=WI_ID, event_type="note",
       payload={text: "wrapped: dark mode feature complete, PR opened"})
  3. State file deleted

ASSERT:
  - pf_wrap called (NOT pf_complete_attempt directly)
  - WI_ID status=="wrapped"
  - State file deleted

## PASS criteria
spec and plan artifacts saved; all 5 feature steps complete (spec→plan→code_change→commit_and_pr→review);
pf_wrap used for coding scenario wrap; wi wrapped.
