# E2E-16 — Zombie sweep: stale attempt force-taken-over with is_zombie=true

Tests that when a run_attempt's last_active_at exceeds the zombie threshold
(typically 24h), the system or admin can force-takeover and the resulting
event includes is_zombie=true.

NOTE: This scenario requires manipulating last_active_at directly via DB
or waiting 24h — both impractical in automated testing. This documents the
expected behavior for manual verification.

## Manual verification steps

### Setup
1. Create and claim a wi (normal flow)
2. Do NOT call renew_lease or update_step for >24h
   (OR: directly UPDATE run_attempts SET last_active_at = now() - interval '25 hours' WHERE id=ATTEMPT_ID)

### Verify zombie detection
polyforge doctor should show the attempt as stale/orphaned:
```bash
POLYFORGE_WORKSPACE_ROOT= polyforge doctor
```
ASSERT: output mentions stale attempt or orphan worktree

### Admin force-takeover (is_zombie path)
AS ADMIN: pf_force_takeover(work_item_id=WI_ID, reason="zombie recovery test")
ASSERT: ok==true

### Verify is_zombie in event
AS ADMIN: pf_read_events(work_item_id=WI_ID)
ASSERT: any event with event_type=="force_takeover" AND payload.is_zombie==true

## SKIP condition
Skip in automated test runs unless DB manipulation is available.
Use manual DB UPDATE for integration testing:
```sql
UPDATE run_attempts SET last_active_at = now() - interval '25 hours' WHERE id = 'ATTEMPT_ID';
```

## PASS criteria
After 24h idle, admin force_takeover succeeds with is_zombie=true in event payload.
