# 05 — Migration and qualification

## Migration order

1. Back up and migrate the database through `0043_skill_registry.sql`, then
   `0044_wi_workflows.sql`. Both are additive; do not remove scenario or legacy step data.
2. With an **unscoped** writer/admin credential, run `polyforge skills seed`. Record the
   returned exact `(skill_id, version, digest)` pins. Rerun once: every operation must be
   `present`; any publish on the second pass is a content or visibility investigation, not
   something to ignore.
3. Compose a workflow only in a throwaway project first. New-flow creation requires an
   explicit `requires_human_session` Boolean and pins accessible versions atomically.
4. Exercise create → pin → start → structured result → exact human approval, plus review
   FAIL → pause → resumed repair producer → verification → fresh independent review. A
   provider retry is a distinct explicit authorization and never review shopping.
5. Enable unattended drain only for isolated `rhs=false` items. Observe the active snapshot
   and graceful stop behavior before widening scope. Never point qualification at a live
   production queue.

There is no startup auto-seed and no production deployment in this migration procedure.

## Compatibility matrix

| shape | authority after upgrade | qualification promise |
|---|---|---|
| legacy work item, no stored steps | pinned/local scenario graph | continues through the unchanged legacy path |
| legacy in-flight work item | its existing scenario + `wi_step_state` | is not repinned and completed history is not duplicated |
| stored workflow generation | DB generation + exact skill versions | never falls back to local scenario text on a DB/read/start error |
| `rhs=false` stored workflow | shared controller through drain | human/interactive-only steps are refused; approval holds pause |
| `rhs=true` stored workflow | shared controller three-mode adapter | human-facing non-isolated authoring uses prepare/submit; gates and shipping use the pinned preflighted worker; outer claim/approval/pause/wrap remains explicit |
| old role/preset model config | legacy path only | remains compatible; new flow uses pinned model candidates and explicit preflight |

## Automated gates

The CI step **aihub#708 workflow-v2 integration and migration qualification** sets
`AIHUB_TEST_DB` explicitly and uses `set -o pipefail`. It rejects every `--- SKIP` in the
DB logs and asserts representative PASS lines rather than trusting `go test`'s package-level
`ok`. The complete DB-gated function set is also registered in
`internal/citest/dbtestcov/gated_tests.txt`, so an unregistered test or a renamed skip guard
fails the repository ratchet.

The suite covers:

- private registry publication, sharing/revocation, latest-accessible pinning, CAS races,
  access recheck and no metadata oracle;
- deterministic authenticated HTTP/client seed, second-run idempotency, exact pins and
  private visibility;
- atomic create/revision, legacy zero-flow fixture, attempt/generation/result fencing,
  exact artifact approval, FAIL pause, resumed repair/verification/fresh review, provider
  retry lineage, stale approval rejection and no review shopping;
- Batch4 verification `base_ref`, import/seed contracts, and Pi/OpenCode privacy bridges;
- controller timeout → confirmed process-group stop → pause, and unconfirmed stop → retain
  claim/locks with no lifecycle mutation;
- drain's active pinned-step snapshot and stop semantics. Successful DB-flow wrap preserves
  the worktree and reports cleanup deferred; it does not apply forceful legacy cleanup.

## Browser coverage

Registry list/detail and write flows were exercised with the existing Chromium fixture:
HTML escaping, authenticated list/detail, CSRF rejection, publish/share/revoke and
same-origin navigation. CI also runs the DB-backed UI route fixture. No new browser UI was
added for Batch5's CLI seed/import route, so there is no browser claim for that command.
If Chromium is unavailable during a release qualification, disclose that explicitly and
retain the existing pause gate; a server unit test is not a substitute for browser coverage.

## Remaining limitations

- The main interactive session supports human-facing non-isolated authoring, while
  `--continue` routes RHS review/verification/shipping and independent-producer steps to
  the shared preflighted worker with their pinned model/effort/grant. The remaining bound
  is the outer lifecycle: use authenticated workflow/lifecycle tools for claim, exact
  artifact approval, pause/wrap and cleanup; treat `approval_required`, refusal and pause
  as holds, never successful completion.
- Pinned DB-flow worktree cleanup is intentionally deferred. The preserved worktree may
  require a human to inspect delivery state and remove it safely.
- Real harness smoke is opt-in because model availability and credentials are machine-local.
  Deterministic CI uses fake/local runners and never installs global models or skills.
- The registry has no MCP publication surface; the supported seed/import entry is the
  authenticated CLI/client route. Raw HTTP fallback remains prohibited.
