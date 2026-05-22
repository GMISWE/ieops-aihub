# SC-02 — feature wi with spec+plan (Session 2, human-led)

Tests the full skill chain for a human-led feature wi:
pf-work → pf-spec → pf-plan → code_change → commit_and_pr → pf-stop

## Scenario

### Step 1: User starts feature wi
SKILL_INVOKE: polyforge:pf-work
USER_INTENT: "add dark mode toggle to marketplace UI"

EXPECTED SKILL BEHAVIOR:
  1. Memory-First recall
  2. Infer wi_type=feature, requires_human_session=true (design decisions needed)
  3. pf_create_work_item(wi_type="feature", requires_human_session=true)
  4. Show wi in needs_human_session[] — notify user this is a human-led wi
  5. pf_claim_work_item → worktree created

ASSERT:
  - WI_ID created with wi_type="feature", requires_human_session=true
  - pf_get_ready_queue → WI_ID in running[] (since claimed in same session)

### Step 2: Write spec (human discusses with Claude)
SKILL_INVOKE: polyforge:pf-spec
USER_INTENT: "let's spec out the dark mode feature"

EXPECTED SKILL BEHAVIOR:
  1. pf_update_step(step_id="spec", status="in_progress")
  2. Heartbeat protocol: pf_update_step(heartbeat=true) during discussion
  3. Guide user through: What/Why, Non-goals, Decisions, Acceptance criteria
  4. pf_save_artifact(type="methodology.spec", structured_payload={decisions:[...], acceptance_criteria:[...]})
  5. pf_emit_event(note, payload={text: "spec saved: mem_XXX"})
  6. pf_update_step(step_id="spec", status="completed", artifact_summary="spec: dark mode via CSS vars")

ASSERT:
  - pf_save_artifact called with type="methodology.spec"
  - pf_update_step(spec, completed) called with artifact_summary

### Step 3: Write plan
SKILL_INVOKE: polyforge:pf-plan

EXPECTED SKILL BEHAVIOR:
  1. Read spec artifact from memory
  2. pf_update_step(step_id="plan", status="in_progress")
  3. Break into implementation steps
  4. pf_save_artifact(type="methodology.plan", structured_payload={steps:[...]})
  5. pf_update_step(step_id="plan", status="completed")

ASSERT:
  - methodology.plan artifact saved
  - pf_get_step → version incremented after plan completed

### Steps 4-5: code_change + commit_and_pr
(Same as SC-01 steps 3-4)

SKILL_INVOKE: polyforge-coding:code_change
SKILL_INVOKE: polyforge-coding:commit_and_pr

ASSERT:
  - File edits present in WT_PATH (git diff --stat)
  - pf_commit called with conventional commit message
  - pf_pr called → PR URL in response

### Step 6: Wrap
SKILL_INVOKE: polyforge:pf-stop --wrap

EXPECTED SKILL BEHAVIOR:
  1. pf_emit_event(event_type="note", payload={text: "wrapped: dark mode feature complete, PR opened"})
  2. pf_complete_attempt(status="wrapped")

ASSERT:
  - WI_ID status=="wrapped"
  - State file deleted

## PASS criteria
spec and plan artifacts saved; full 5-step feature flow completes; wi wrapped.
