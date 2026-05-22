# E2E-05 — Dedup detection blocks identical wi creation

Tests that creating a wi with an identical or near-identical goal returns 409 CONFLICT_DUPLICATE.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-05 implement OAuth2 PKCE flow for CLI login",
      wi_type="feature", priority="normal")
ASSERT: response.status == "queued"
NOTE: save response.id as WI_ORIGINAL, response.slug as SLUG_ORIGINAL

## Steps

### Exact duplicate blocked
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-05 implement OAuth2 PKCE flow for CLI login",
      wi_type="feature", priority="normal")
ASSERT_ERROR: "CONFLICT_DUPLICATE"
NOTE: error message should reference SLUG_ORIGINAL

### Near-duplicate also blocked (≥80% similarity)
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-05 OAuth2 PKCE flow implementation for CLI",
      wi_type="feature", priority="normal")
ASSERT_ERROR: "CONFLICT_DUPLICATE" OR "CONFLICT_CANDIDATES"

### Force-create bypasses dedup
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-05 implement OAuth2 PKCE flow for CLI login",
      wi_type="feature", priority="normal",
      force_create=true)
ASSERT: response.status == "queued"
ASSERT: response.id != WI_ORIGINAL
NOTE: save response.id as WI_FORCED

## Cleanup

CALL: pf_cancel_work_item(work_item_id=WI_ORIGINAL, reason="e2e test cleanup")
CALL: pf_cancel_work_item(work_item_id=WI_FORCED, reason="e2e test cleanup")

## PASS criteria

Exact duplicate blocked; force_create succeeds and creates distinct wi;
both cleanup cancellations succeed.
