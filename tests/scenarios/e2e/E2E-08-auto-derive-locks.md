# E2E-08 — Auto-derive locks from declared_resources when requested_locks is empty

Tests that when pf_claim_work_item is called without explicit requested_locks=[],
the server automatically derives locks from the wi's declared_resources.
(Implements internal/domain/run_attempts.go `deriveClaimLocks`)

🔴 **REWRITTEN BY aihub#416 (2026-09-09) — read this before the steps.**

This scenario used to declare a `repo` entry, claim with no `requested_locks`, and
assert a **`git_branch`** lock came back keyed `marketplace/polyforge/<branch>`. Both
assertions are now FALSE, and not by accident: the owner ruling of 2026-09-07
retired the `repo → git_branch` and `service → deploy_env` derivations outright
(`internal/domain/conflicts.go` `resourceToLock`). A repo declaration derives NO lock
under any intent.

The scenario is kept rather than deleted because the property it tests — *the server
derives locks from stored declared_resources when the client asks for none* — is
still live and still the normal polyforge flow. What changed is the ONE type it can
derive: `file_scope`, from `path` / `document` / `section` entries. So the payload
moves to a path entry and the assertions move with it.

⚠️ Two things NOT to "restore" here:

- **The `task_branch` field is gone from the published schema** (aihub#416, following
  the `base_branch` precedent of aihub#395). It had exactly one reader — the
  `git_branch` lock key — and that reader no longer exists. Sending it is accepted
  and does nothing.
- **The contention half is now about a PATH, not a branch.** Two work items on one
  repo are no longer expected to block each other; that was the serialisation
  aihub#416 removed. Two work items editing one FILE still block, and that is what
  the contender step below measures.

## Setup

CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-08 auto-derive locks from declared_resources",
      wi_type="chore", priority="normal",
      declared_resources=[{
        "type": "repo",
        "uri": "repo:marketplace",
        "intent": "exclusive"
      }, {
        "type": "path",
        "uri": "file:internal/e2e08/contended.go",
        "intent": "write"
      }])
NOTE: save response.id as WI_ID
NOTE: the repo entry is kept deliberately — it is the negative control for the
      retirement (it must contribute NO lock), and it is also what makes the
      derived file_scope key repo-qualified (aihub#261).

## Steps

### Claim WITHOUT requested_locks — server derives from declared_resources
CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="e2e-08-claim")
NOTE: No requested_locks parameter passed
ASSERT: response.ok == true
ASSERT: len(response.acquired_locks) == 1
NOTE: ONE lock, not two — the repo entry derives nothing (aihub#416)
ASSERT: response.acquired_locks[0].resource_type == "file_scope"
ASSERT: response.acquired_locks[0].resource_key == "marketplace:marketplace:internal/e2e08/contended.go"
NOTE: key shape is "<project>:<repo>:<repo-relative-path>" (aihub#222 + aihub#261);
      the repo segment is inferred from this payload's own repo entry
ASSERT: no element of response.acquired_locks has resource_type == "git_branch"
ASSERT: no element of response.acquired_locks has resource_type == "deploy_env"

### Verify lock is active: second wi competing for the same FILE must be blocked
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-08 contender",
      wi_type="chore", priority="normal",
      declared_resources=[{
        "type": "repo",
        "uri": "repo:marketplace",
        "intent": "exclusive"
      }, {
        "type": "path",
        "uri": "file:internal/e2e08/contended.go",
        "intent": "write"
      }])
NOTE: save response.id as WI_CONTENDER

CALL: pf_claim_work_item(work_item_id=WI_CONTENDER, idempotency_key="e2e-08-contender-claim")
ASSERT_ERROR: "CONFLICT_LOCK_TAKEN"
NOTE: If no error, auto-derive failed to acquire the file_scope lock

### Control: a wi declaring only the REPO is NOT blocked
CALL: pf_create_work_item(project="marketplace",
      goal="[test] E2E-08 repo-only bystander",
      wi_type="chore", priority="normal",
      declared_resources=[{
        "type": "repo",
        "uri": "repo:marketplace",
        "intent": "exclusive"
      }])
NOTE: save response.id as WI_BYSTANDER

CALL: pf_claim_work_item(work_item_id=WI_BYSTANDER, idempotency_key="e2e-08-bystander-claim")
ASSERT: response.ok == true
ASSERT: len(response.acquired_locks) == 0
NOTE: 🔴 THIS IS THE aihub#416 CRITERION. Before the retirement this claim answered
      409 CONFLICT_LOCK_TAKEN, because WI_ID held git_branch:marketplace/main — two
      work items on one repo, touching no common file, serialised. If this step
      errors, the derivation has come back.

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
CALL: pf_complete_attempt(work_item_id=WI_BYSTANDER, status="wrapped")
CALL: pf_cancel_work_item(work_item_id=WI_CONTENDER, reason="e2e-08 cleanup")

## PASS criteria

Claim without requested_locks acquires exactly the file_scope lock derived from the
path entry and NOTHING for the repo entry; a second wi declaring the same FILE is
blocked; a wi declaring only the same REPO is not.
