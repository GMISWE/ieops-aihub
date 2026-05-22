# L3-01 — fix_bug methodology: prepare_context artifact → code_change → commit_and_pr

Tests the full Layer 3 methodology chain for fix_bug wi_type.
Verifies artifacts are saved with correct project (backfilled from wi), recallable by wi_id and type.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] L3-01 fix_bug artifact chain",
      wi_type="fix_bug", priority="normal")
ASSERT: response.status == "queued"
NOTE: save response.id as WI_ID, response.slug as SLUG

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l3-01-claim", mode="fresh")
ASSERT: response.ok == true
ASSERT: response.project == "marketplace"

## Steps

### prepare_context step + save spec artifact
CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_save_artifact(type="methodology.spec", work_item_id=WI_ID,
      content="## Context\nRoot cause: missing null check in handler.\nFix: add guard at line 42.",
      structured_payload={"feature": "null-check-fix",
                          "acceptance_criteria": ["null input returns 400 not 500"]},
      visibility="project")
ASSERT: response.is_new == true
ASSERT: response.type == "methodology.spec"
ASSERT: response.project == "marketplace"
NOTE: save response.id as SPEC_MEM_ID (also in response.memory_id — same value)

CALL: pf_update_step(work_item_id=WI_ID, step_id="prepare_context", status="completed",
      step_attempt_id="sa_l3_01_ctx",
      artifact_summary="spec saved: context + root cause identified")
ASSERT: response.status == "completed"

### code_change step + save plan artifact
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_save_artifact(type="methodology.plan", work_item_id=WI_ID,
      content="## Code Change Plan\n1. Add null guard\n2. Add unit test",
      visibility="project")
ASSERT: response.is_new == true
ASSERT: response.type == "methodology.plan"
ASSERT: response.project == "marketplace"
NOTE: save response.id as PLAN_MEM_ID

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
      step_attempt_id="sa_l3_01_code",
      artifact_summary="plan saved: code change steps")
ASSERT: response.status == "completed"

### commit_and_pr step
CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="in_progress")
ASSERT: response.status == "in_progress"

CALL: pf_update_step(work_item_id=WI_ID, step_id="commit_and_pr", status="completed",
      step_attempt_id="sa_l3_01_pr",
      artifact_summary="PR created: fix/null-check")
ASSERT: response.status == "completed"

### Recall by wi_id — order not guaranteed, check by id
CALL: pf_recall(project="marketplace", work_item_id=WI_ID, top_k="5")
ASSERT: len(response.items) >= 2
ASSERT: any(item for item in response.items if item.id == SPEC_MEM_ID)
ASSERT: any(item for item in response.items if item.id == PLAN_MEM_ID)

### Recall by type — all items must be methodology.spec
CALL: pf_recall(project="marketplace", type=["methodology.spec"],
      work_item_id=WI_ID, top_k="5")
ASSERT: len(response.items) >= 1
ASSERT: all(item.type == "methodology.spec" for item in response.items)

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT: response.ok == true

## PASS criteria

Both artifacts saved with project="marketplace"; recall by wi_id and type correct;
all 3 steps completed; wrap succeeds.
