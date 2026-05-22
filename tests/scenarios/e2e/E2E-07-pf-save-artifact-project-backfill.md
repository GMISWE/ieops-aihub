# E2E-07 — pf_save_artifact infers project from work_item_id

Tests the ISSUE-12 fix: pf_save_artifact does not require an explicit project parameter —
the server backfills it from the work_item_id.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-07 artifact project backfill",
      wi_type="fix_bug", priority="normal")
NOTE: save response.id as WI_ID, response.project as EXPECTED_PROJECT

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-07-claim", mode="fresh")
ASSERT: response.ok == true

## Steps

### Save artifact WITHOUT explicit project — must succeed
CALL: pf_save_artifact(type="methodology.spec", work_item_id=WI_ID,
      content="## Spec\nTest that project is backfilled from work_item_id automatically.",
      visibility="project")
ASSERT: response.is_new == true
ASSERT: response.project == EXPECTED_PROJECT
ASSERT: response.type == "methodology.spec"
NOTE: save response.id as MEM_ID

### Recall verifies correct project assignment
CALL: pf_recall(project=EXPECTED_PROJECT, work_item_id=WI_ID, top_k="3")
ASSERT: len(response.items) >= 1
ASSERT: response.items[0].id == MEM_ID
ASSERT: response.items[0].project == EXPECTED_PROJECT

### Save plan artifact (second artifact type, same backfill path)
CALL: pf_save_artifact(type="methodology.plan", work_item_id=WI_ID,
      content="## Plan\nStep 1: verify backfill. Step 2: done.",
      visibility="project")
ASSERT: response.project == EXPECTED_PROJECT

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")

## PASS criteria

Both artifacts saved without explicit project; project == "marketplace" in responses;
artifacts recallable by wi_id.
