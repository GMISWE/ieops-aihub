# E2E-02 — Worktree creation on claim

Tests that pf_claim_work_item creates the git worktree at pf.<seq>.<ulid8>/<repo>/
and records it in the state file. Also tests that re-claim reuses the existing worktree.

## Preconditions

- WORKSPACE_ROOT: /root/code/aicoding/gmi-ws-v3 (has .repo/marketplace/)
- polyforge MCP server running with POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT

## Steps

### Create wi with declared resource
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-02 worktree creation verification",
      wi_type="chore", priority="normal",
      declared_resources=[{
        "type": "repo",
        "uri": "repo:marketplace",
        "intent": "exclusive",
        "task_branch": "polyforge/e2e-02-test"
      }])
ASSERT: response.status == "queued"
NOTE: save response.id as WI_ID, response.seq as SEQ

### Claim with explicit requested_locks (different format from declared_resources)
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-02-claim",
      mode="fresh",
      requested_locks=[{
        "resource_type": "git_branch",
        "resource_key": "marketplace/polyforge/e2e-02-test"
      }])
ASSERT: response.ok == true
ASSERT: response.slug != null
ASSERT: response.project == "marketplace"
ASSERT: response.worktrees != null
ASSERT: response.worktrees["marketplace"] contains "/pf."
NOTE: save WT_PATH = response.worktrees["marketplace"]
NOTE: expected format: WORKSPACE_ROOT/pf.SEQ.<ulid8>/marketplace

### Verify state file
NOTE: Read WORKSPACE_ROOT/.polyforge/state/WI_ID.json
ASSERT: state.slug == response.slug
ASSERT: state.project == "marketplace"
ASSERT: state.worktrees["marketplace"] == WT_PATH
ASSERT: state.claimed == true

### Verify worktree exists on disk
NOTE: Check that directory WT_PATH exists
CALL: bash git -C WT_PATH log --oneline -1
ASSERT: output is non-empty (worktree has commits from base branch)

### Verify worktree directory name format: pf.<seq>.<8chars>
NOTE: Extract parent directory name of WT_PATH (e.g. pf.3.cTJvYHAy)
ASSERT: dirname matches regex pf\.\d+\.[A-Za-z0-9]{8}
ASSERT: dirname starts with "pf." + SEQ + "."

### Re-claim (simulate crash recovery) — must reuse existing worktree
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-02-reclaim",
      mode="fresh")
ASSERT: response.ok == true
NOTE: Worktree directory already exists — MCP should skip git worktree add and reuse it
NOTE: Read updated state file
ASSERT: state.worktrees["marketplace"] == WT_PATH (same path, reused)

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT: response.ok == true
CALL: bash POLYFORGE_WORKSPACE_ROOT=WORKSPACE_ROOT polyforge doctor --fix
ASSERT: doctor output does not mention WT_PATH as orphan (already cleaned by --fix)

## PASS criteria

Worktree created at pf.<seq>.<ulid8>/marketplace; state file has correct worktrees map;
re-claim reuses same path without error; doctor --fix removes orphan after wrap.
