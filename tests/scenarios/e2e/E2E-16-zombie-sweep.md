# E2E-16 — Stale running wi appears in stale_running list

Tests that a running wi with updated_at older than 24h appears in the
`stale_running` field of `pf_get_ready_queue`. No automatic action is taken.
The zombie sweep was removed in aihub#36.

NOTE: This scenario requires DB manipulation to backdate updated_at.

## Users
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V

## Manual verification steps

### Setup
1. Create and claim a wi (normal flow), save WI_ID
2. Backdate updated_at via DB:
   ```sql
   UPDATE work_items SET updated_at = now() - interval '25 hours' WHERE id = 'WI_ID';
   ```

### Verify stale_running appears in ready queue
AS ALICE: pf_get_ready_queue(project="marketplace")
ASSERT: response contains `stale_running` array
ASSERT: WI_ID appears in stale_running

### Verify wi ownership unchanged
AS ALICE: pf_get_work_item(work_item_id=WI_ID)
ASSERT: status == "running" (NOT paused/lost — no automatic action)

### Cleanup
AS ALICE: pf_complete_attempt(WI_ID, status="wrapped")

## PASS criteria
Running wi with updated_at > 24h appears in stale_running. Wi ownership
is not affected — only a warning signal, no forced release.
