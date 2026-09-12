# E2E-19 — Claim fails with 503 when scenario_phase_configs missing

Tests that claiming a wi returns 503 SERVICE_UNAVAILABLE when the
scenario_phase_configs row for the wi's scenario has been deleted.
Reference: the config lookup this scenario exercised lived in run_attempts.go
of the tree this scenario was written against; the scenario_phase_configs
table has since been REMOVED (aihub#38), and a claim now falls back to a named
constant instead of a lookup — `internal/domain/run_attempts.go`
(`defaultRequiresHumanSession`) documents the removal.

NOTE: OBSOLETE against the current server — the 503-on-missing-config path no
longer exists, because the table it read no longer exists. Kept as a record of
what the scenario verified; do not run it against a current build.

NOTE: This is an edge-case/operational scenario. It requires deleting a
scenario_phase_configs row, which is a destructive admin operation.
Only run in isolated test environments.

## Precondition
Must have admin DB access OR an admin API endpoint to delete scenario config.

## Steps (manual/admin DB)

### Backup current scenario config
AS ADMIN: pf_get_scenario_config(scenario="coding")
Save BACKUP_CONTENT, BACKUP_VERSION

### Delete the coding scenario config (via DB)
```sql
DELETE FROM scenario_phase_configs WHERE scenario = 'coding';
```

### Create and try to claim a wi
AS ADMIN: pf_create_work_item(project="marketplace",
  goal="[test] E2E-19 scenario config missing", wi_type="chore")
Save WI_ID

AS ADMIN: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e19-claim", mode="fresh")
ASSERT_ERROR: HTTP 503 or "scenario config not found" or "SERVICE_UNAVAILABLE"

### Restore scenario config
AS ADMIN: pf_update_scenario_config(scenario="coding", content=BACKUP_CONTENT, version=BACKUP_VERSION)
ASSERT: HTTP 200 or response.version > BACKUP_VERSION

### Verify claim works again after restore
AS ADMIN: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e19-claim-retry", mode="fresh")
ASSERT: HTTP 200, ok==true

## Cleanup
AS ADMIN: pf_complete_attempt(work_item_id=WI_ID, status="failed")

## PASS criteria
Claim returns 503 when scenario config deleted; works after restore.
SKIP if no DB access available.
