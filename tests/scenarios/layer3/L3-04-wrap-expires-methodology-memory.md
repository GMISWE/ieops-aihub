# L3-04 — Wrap sets expires_at on methodology memories

Tests that when a wi is wrapped, all its associated methodology.* memories
get expires_at set to closed_at + 90 days (not immortal).
Reference: FnCompleteWorkItem sets expires_at on linked memories.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig

## Steps

### Create wi, claim, save artifacts
AS ADMIN: pf_create_work_item(project="marketplace", goal="[test] L3-04 wrap expires memory", wi_type="chore")
Save WI_ID

AS ADMIN: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l304-claim", mode="fresh")

AS ADMIN: pf_save_artifact(type="methodology.spec", work_item_id=WI_ID,
  content="L3-04 test spec — will expire after wrap", visibility="project")
ASSERT: response.is_new==true
NOTE: Before wrap, stability_days == 36500 (immortal artifact flag)
Save MEM_ID = response.id

### Complete steps and wrap
AS ADMIN: pf_update_step(WI_ID, code_change, in_progress)
AS ADMIN: pf_update_step(WI_ID, code_change, completed, step_attempt_id="sa_l304")
AS ADMIN: pf_update_step(WI_ID, commit_and_pr, in_progress)
AS ADMIN: pf_update_step(WI_ID, commit_and_pr, completed, step_attempt_id="sa_l304_pr")
AS ADMIN: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")

### Verify expires_at was set on methodology memory
NOTE: Query the memory directly or recall and check expires_at field
AS ADMIN: GET /v1/memories/MEM_ID
ASSERT: response.expires_at != null
NOTE: expires_at should be approximately closed_at + 90 days
NOTE: If expires_at is null or very far in future (>= year 9999), the expiry was NOT set (server gap).

## PASS criteria
After wrap, methodology.spec memory has expires_at set to ~90 days from wrap time.
