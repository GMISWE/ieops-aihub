# E2E-02 — Worktree creation on claim

Tests that pf_claim_work_item creates the git worktree at pf.<seq>.<ulid8>/<repo>/
and records it in the state file.

## Preconditions

- WORKSPACE_ROOT: /root/code/aicoding/gmi-ws-v3 (has .repo/marketplace/)
- polyforge MCP server running with POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT

## Steps

### Create and claim a wi
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-02 worktree creation verification",
      wi_type="chore", priority="normal",
      declared_resources=[{"resource_type": "git_branch",
                           "resource_key": "marketplace/polyforge/e2e-02-test"}])
NOTE: save response.id as WI_ID, response.seq as SEQ

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-02-claim",
      mode="fresh",
      requested_locks=[{"resource_type": "git_branch",
                        "resource_key": "marketplace/polyforge/e2e-02-test"}])
ASSERT: response.ok == true
ASSERT: response.slug != null
ASSERT: response.project == "marketplace"
ASSERT: response.worktrees != null
ASSERT: response.worktrees["marketplace"] contains "/pf." + SEQ + "."

### Verify state file
NOTE: Read WORKSPACE_ROOT/.polyforge/state/WI_ID.json
ASSERT: state.slug == response.slug
ASSERT: state.project == "marketplace"
ASSERT: state.worktrees["marketplace"] == response.worktrees["marketplace"]
ASSERT: state.claimed == true

### Verify worktree on disk
NOTE: WT_PATH = response.worktrees["marketplace"]
ASSERT: directory WT_PATH exists
CALL: bash git -C WT_PATH log --oneline -1
ASSERT: output is non-empty (worktree has commits)

### Verify worktree naming format: pf.<seq>.<ulid8>/marketplace
NOTE: extract dirname from WT_PATH → should be pf.SEQ.ULID8
ASSERT: dirname matches regex "pf\.\d+\.[A-Za-z0-9]{8}"

### Re-claim (simulate resume) — worktrees must be restored
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-02-reclaim",
      mode="fresh")
ASSERT: response.ok == true
NOTE: Read updated state file
ASSERT: state.worktrees["marketplace"] == WT_PATH (same path, re-attached)

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT polyforge doctor --fix
ASSERT: doctor output contains "[ok] worktrees"

## PASS criteria

Worktree created at correct pf.<seq>.<ulid8>/<repo> path;
state file has worktrees map; re-claim restores same path.
