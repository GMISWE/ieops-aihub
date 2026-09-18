# 01 — Authority and compatibility: which path a wi takes

aihub#708 Batch 4A. The decision this file records: **the DB workflow is the new authority
for newly composed work-item flows; the local skill bodies are thin, DB-aware
compatibility entries.** Nothing about the legacy path is deleted.

## The authority split, precisely

A claimed wi walks exactly ONE step graph. Which one is a property of the work item, read
from the server, never guessed:

```
wf = pf_get_workflow(work_item_id=<wi>)

wf.steps_version == 0 AND wf.steps == null
    → NO workflow. The LOCAL scenario step graph (the legacy path) is the authority:
      resolved from the scenario repo by `polyforge engine startup`, walked by
      /pf-execute (session) or `polyforge drain` (L3), reported through
      pf_update_step / wi_step_state.

wf.steps_version > 0
    → A PINNED DB workflow generation. The STORED flow is the authority: exact
      (skill_id, version) pins per step, append-only generations (CAS via
      expected_steps_version), server-minted invocations, structured results in
      wi_workflow_results, human approvals in wi_workflow_approvals, authorized
      repairs in wi_workflow_repair_episodes. It is driven by `polyforge drain`
      (rhs=false) or the workflow MCP tools / low-level `polyforge engine workflow`
      adapter. The adapter's three modes share controller classification: unattended
      execution uses the driver; automatic work and every review, verification or shipping
      capability use the preflighted worker with the pinned model/effort and grant; only
      human-facing non-isolated authoring uses the main session's `--prepare` / `--submit`
      protocol. Effective rhs=true does not waive producer isolation. The adapter still
      intentionally executes at most one worker step per call and reports exact
      approval/refusal as data. Claim, authenticated approval, pause/wrap and cleanup remain
      outside this low-level adapter, so callers must not report whole-lifecycle success
      from it alone. It is NOT driven by /pf-execute's scenario loop, and no skill prose reimplements its
      controller.
```

"Newly composed flows" means: a flow pinned from now on — at `POST /v1/work_items
(steps=...)` or via `pf_update_workflow` — resolves its skill references **through the
registry** (exact version, or latest-accessible frozen at pin time) and validates the
composition server-side. Local authoring bodies stop being the place new flows get their
step content from; the registry is.

## What the local skills became (and stay)

`pf-spec`, `pf-plan` and `pf-execute` are **thin DB-aware compatibility entries**:

- **pf-execute** (`skills/pf-execute/SKILL.md`) — the stub now checks `pf_get_workflow`
  first: `steps_version > 0` → follow the stored flow (pointer to
  `using-polyforge/fragments/workflow-v2.md`); `0` → the legacy scenario loop, unchanged.
  The `PreToolUse(Skill)` router (`hooks/pf-skill-router`) carries the same gate in the
  injected step-body header, so the dispatch channel cannot start the scenario loop on a
  pinned-flow wi either.
- **pf-spec / pf-plan** — each carries a DB-aware compatibility banner: on a pinned-flow
  wi, spec/plan authoring is a step of the stored flow (a registry skill version pinned
  server-side — the seeded `spec` / `grill-me` / `plan` bundles, or the owner's own); the
  local skill remains the compatibility entry for legacy scenario wis and keeps the
  session-side mechanics (Memory-First recall, step bracketing, artifact recording,
  declared-resources derivation).
- **pf-status** — the single-wi view renders stored-flow progress when `steps_version > 0`
  (a workflow wi's legacy `step_state` is absent **by design** — its results live in
  wi_workflow_results and create no wi_step_state rows; "no steps" would be a false
  report).
- **pf-stop** — pause now states the true lock semantics; wrap on a pinned-flow wi passes
  the no-steps gate's reason field (see `03-contradictions-corrected.md`).

## What did NOT change (compatibility, kept whole)

- **The legacy scenario path**: wi_types, their `{wi_type}[.{project}].md` graphs,
  `@include: common/...`, `polyforge engine startup` resolution, the native loop, the
  superpowers engine delegation, role/tier model resolution, drain's legacy branch.
  Existing no-steps and in-flight scenario work items keep this fixed definition after an
  upgrade. The compatibility branch is selected only when the server successfully confirms
  `steps_version == 0`; a DB/workflow read error never selects it.
- **The `wi_type` post-claim routing table** (`fragments/post-claim-routing.md`) remains
  the single source of truth for "Next steps" on every wi **without** a pinned flow.
- **pf-crystallize's scenario branch** (chore wi + `pf_ship` to polyforge-coding) remains
  executable for the fleet; the registry publication is the new primary product.
- **pf-release** stays exactly as it is: the inert-truth record of aihub#448/#176.
  Workflow v2 does not resurrect it.

## The safety that never moves to prose

These are enforced by mechanisms, not by skill text, and this batch changed none of them:

| invariant | enforced by |
|---|---|
| IR1 work-item-gated writes | `hooks/pf-commit-guard` (worktree + attribution gates) |
| IR3 no raw-HTTP fallback | MCP-only surfaces; the one raw-curl block in skill prose was deleted this batch (`03`, §4) |
| bootstrap safety | `hooks/pf-session-start` scan + `pf doctor`; `fragments/bootstrap.md` untouched |
| auth safety | server-side caller validation everywhere (scoped keys stay scoped, no-oracle reads, human-only approvals) |
| doctor | `polyforge doctor` + the binary-status relay — unchanged |

The resident session-start payload stays inside its two-sided band
(`tests/using-polyforge-payload.test.sh`): the additions this batch made to the resident
tier (one NL-routing row, one on-demand-index pointer) were paid for by trims in the same
fragments; the measured assembly is 8,479 characters against the `[8397, 8497]` band.
The new stored-workflow state language itself ships **on demand**
(`fragments/workflow-v2.md`) — a wi with a pinned flow is exactly the trigger to read it.

## How a flow gets composed (the honest, bounded statement)

Composing or revising a pinned flow is an **author-tier act**: `pf_update_workflow` with
`expected_steps_version` (CAS), an explicit `requires_human_session`, and a step list.
The server resolves every skill ref through the accessibility predicate inside the
caller's transaction, derives each step's grant from the resolved contract's capabilities
(`workflowGrantForContract`), validates the composition with the pure policy
(`internal/workflow`), and refuses invalid compositions atomically. Approvals bind exact
artifacts and are human-only; results are untrusted worker output that can never carry an
approval; review FAILs pause rather than terminate, and recover only through explicit
repair authorizations. None of that is restated as prose for an agent to follow — the
skills point at the surfaces and the server enforces the rest.

## Which skill bodies back a new flow

The registry starts empty on a fresh deployment; after migrations 0043 then 0044, run
`polyforge skills seed` with an unscoped writer/admin credential. The command is explicit,
idempotent, private-by-default and prints exact version pins; startup and migration never
silently publish content. See `02-seed-and-import.md`.
