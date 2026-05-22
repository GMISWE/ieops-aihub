# E2E-11 — Cross-project permission: viewer cannot update steps

Tests that a user with "viewer" role on a project can GET the step state
but cannot PATCH (update) the step. Requires a second API key with viewer role.

Note: This test requires admin setup to create a viewer-role user.
Skip if a viewer API key is not available.

## Preconditions

- ADMIN_KEY: baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig (admin)
- VIEWER_KEY: <a key belonging to a user with only "viewer" role on marketplace>
  If not available, create one: POST /v1/users + grant viewer role via admin

## Setup (as admin)

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-11 permission check", wi_type="chore", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-11-claim", mode="fresh")
ASSERT: response.ok == true

CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="in_progress")
ASSERT: response.status == "in_progress"

## Steps

### Viewer can read step state
CALL: HTTP GET /v1/work_items/WI_ID/step  (with VIEWER_KEY)
ASSERT: response.current_step_status == "in_progress"
ASSERT: HTTP status == 200

### Viewer cannot update step (write operation)
CALL: HTTP PATCH /v1/work_items/WI_ID/step (with VIEWER_KEY)
      body: {step: "code_change", status: "completed", attempt_id: "...", claim_epoch: 1,
             session_secret: "..."}
ASSERT_ERROR: HTTP 403 OR "FORBIDDEN" OR "insufficient role"

### Viewer cannot complete attempt
CALL: HTTP POST /v1/work_items/WI_ID/complete (with VIEWER_KEY)
      body: {status: "wrapped", attempt_id: "...", claim_epoch: 1, session_secret: "..."}
ASSERT_ERROR: HTTP 403

### Admin can still update step normally
CALL: pf_update_step(work_item_id=WI_ID, step_id="code_change", status="completed",
      step_attempt_id="sa_e2e11_done")
ASSERT: response.status == "completed"

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")

## PASS criteria

Viewer gets 200 on GET, 403 on PATCH/POST; admin can still write.
