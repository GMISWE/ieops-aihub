# L3-02 — feature wi: full 5-step methodology chain

Tests spec → plan → code_change → commit_and_pr → review for a feature wi.
Verifies artifact save at each step and final memory recall.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] L3-02 feature full 5-step chain",
      wi_type="feature", requires_human_session=true, priority="normal")
ASSERT: response.wi_type == "feature"
ASSERT: response.requires_human_session == true
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l3-02-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### Step 1: spec
CALL: pf_update_step(work_item_id=WI_ID, step_id="spec", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_save_artifact(type="methodology.spec", work_item_id=WI_ID,
      content="## Spec\nAdd dark mode toggle.\n### Acceptance Criteria\n- Toggle persists across sessions",
      structured_payload={"feature": "dark-mode",
                          "decisions": [{"decision": "use CSS variables", "reason": "easy theming"}],
                          "acceptance_criteria": ["toggle persists", "no flash on reload"],
                          "non_goals": ["mobile-only support"]},
      visibility="project")
ASSERT: response.type == "methodology.spec"
ASSERT: response.project == "marketplace"
NOTE: save response.id as SPEC_ID

CALL: pf_update_step(work_item_id=WI_ID, step_id="spec", status="completed",
      step_attempt_id="sa_l3_02_spec",
      artifact_summary="spec saved: dark mode toggle with CSS variables")
ASSERT: response.status == "completed"

### Step 2: plan
CALL: pf_update_step(work_item_id=WI_ID, step_id="plan", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_save_artifact(type="methodology.plan", work_item_id=WI_ID,
      content="## Plan\n1. Add CSS vars\n2. Add toggle component\n3. Persist to localStorage",
      structured_payload={"steps": ["add-css-vars", "add-toggle", "persist-state"]},
      visibility="project")
ASSERT: response.type == "methodology.plan"
NOTE: save response.id as PLAN_ID

CALL: pf_update_step(work_item_id=WI_ID, step_id="plan", status="completed",
      step_attempt_id="sa_l3_02_plan",
      artifact_summary="plan saved: 3-step implementation plan")
ASSERT: response.status == "completed"

### Step 3: code_change
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
      step_attempt_id="sa_l3_02_code",
      artifact_summary="CSS vars + toggle component added")
ASSERT: response.status == "completed"

### Step 4: commit_and_pr
CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="completed",
      step_attempt_id="sa_l3_02_pr",
      artifact_summary="PR opened: feat/dark-mode-toggle")
ASSERT: response.status == "completed"

### Step 5: review
CALL: pf_update_step(work_item_id=WI_ID, step_id="review", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_update_step(work_item_id=WI_ID, step_id="review", status="completed",
      step_attempt_id="sa_l3_02_review",
      artifact_summary="code review passed: no blocking issues")
ASSERT: response.status == "completed"

### Verify final step state
CALL: pf_get_step(work_item_id=WI_ID)
ASSERT: response.current_step_status == "idle"
ASSERT: response.version == 10

### Verify both artifacts recallable
CALL: pf_recall(project="marketplace", work_item_id=WI_ID, top_k="5")
ASSERT: len(response.items) >= 2

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT: response.ok == true

## PASS criteria

All 5 steps complete in order; spec+plan artifacts saved; final step version == 10;
wrap succeeds with no errors.
