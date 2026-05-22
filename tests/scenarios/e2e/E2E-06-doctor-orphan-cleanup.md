# E2E-06 — Doctor detects and cleans orphan worktrees

Tests that doctor correctly identifies wrapped wi's worktrees as orphans and removes them
with --fix. Also verifies running wi's worktrees are NOT flagged as orphans.

## Preconditions

- WORKSPACE_ROOT: /root/code/aicoding/gmi-ws-v3
- polyforge binary: /usr/local/bin/polyforge

## Steps

### Create two wi's and claim both
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-06 worktree A (will be wrapped)",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_A

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-06 worktree B (will stay running)",
      wi_type="chore", priority="normal")
NOTE: save response.id as WI_B

CALL: pf_claim_work_item(work_item_id=WI_A, idempotency_key="e2e-06-claim-a", mode="fresh")
ASSERT: response.worktrees != null
NOTE: save WT_A = response.worktrees["marketplace"]

CALL: pf_claim_work_item(work_item_id=WI_B, idempotency_key="e2e-06-claim-b", mode="fresh")
ASSERT: response.worktrees != null
NOTE: save WT_B = response.worktrees["marketplace"]

### Doctor while both running — no orphans
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT polyforge doctor
ASSERT: output contains "[ok] worktrees" (no orphan warning)

### Wrap WI_A (its worktree becomes orphan)
CALL: pf_complete_attempt(work_item_id=WI_A, status="wrapped")
ASSERT: response.ok == true

### Doctor now detects WT_A as orphan, NOT WT_B
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT polyforge doctor
ASSERT: output contains "[warn] worktrees"
ASSERT: output contains dirname(WT_A)  (e.g. pf.N.XXXXXXXX)
ASSERT: output does NOT contain dirname(WT_B)

### doctor --fix removes only orphaned worktree
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT polyforge doctor --fix
ASSERT: output contains "[ok] worktrees"
ASSERT: output contains "removed 1 orphan"
ASSERT: directory WT_A does NOT exist
ASSERT: directory WT_B exists (still running)

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_B, status="wrapped")
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT polyforge doctor --fix

## PASS criteria

Running wi worktree never flagged as orphan;
wrapped wi worktree flagged and removed by --fix;
WT_B unaffected.
