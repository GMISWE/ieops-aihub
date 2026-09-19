# Workflow v2 — DB skill registry & pinned WI flows (aihub#708)

This directory is the migration and qualification book for aihub#708: the move from
**locally-authored skill bodies driving scenario step graphs** to **closed, versioned skill
bundles in the aihub skill registry, pinned per work item as DB workflow generations**.

The implementation landed in ordered batches; this documentation now describes the
integrated result rather than the original Batch 4A boundary. The migration order is
load migrations `0043_skill_registry.sql` then `0044_wi_workflows.sql`, run the explicit
authenticated `polyforge skills seed`, compose/pin new workflow generations, and only
then enable new-flow drain traffic in an isolated project. The seed command is
content-addressed and safe to rerun; it publishes private versions under exact
`expected_latest` CAS tokens and prints exact pins. It is never a server-startup side
effect.

| file | what it holds |
|---|---|
| `01-authority-and-compatibility.md` | which path a wi takes, why the DB workflow is the authority for newly composed flows, what stays for compatibility, and the safety invariants that never move |
| `02-seed-and-import.md` | the canonical seed set (grill-me, spec, plan, code-change, review, verification, ship, ci), its licenses/provenance, idempotency, and the no-silent-latest pinning rule |
| `03-contradictions-corrected.md` | the five contradictions this batch corrects in the shipped skill prose, each with the tool-surface truth it now matches |
| `04-unresolved-migration-references.md` | remaining bounded limitations, including interactive lifecycle ownership outside step execution and deferred DB-workflow cleanup |
| `05-qualification.md` | isolated DB/CLI/drain/browser qualification matrix and exact compatibility promises |
| `06-gate-semantics-mutation-audit.md` | isolated pure and not-publishable prototype contract for strict mutation-gate audit evidence |
| `07-wi-composition.md` | the aihub#720 create-with-steps REST contract, the `workflow_mode` three-state semantics (legacy/db/pending + `COMPOSE_FAILED`/`COMPOSE_PENDING`), the AI composer's standard path (create pending → discover via the skill read tools → pin), and the explicit legacy opt-in |

## The one-paragraph version

A work item's execution authority is its **step graph**. Legacy wis walk the **local
scenario step graph** resolved from the scenario repo — that path is unchanged and stays
the default for every wi without a pinned flow. A wi that carries a **pinned DB workflow
generation** (`GET /v1/work_items/:id/workflow` → `steps_version > 0`, migration 0044)
walks the **stored flow** instead: exact `(skill_id, version)` pins per step,
server-minted invocations, structured results, human approvals and authorized repairs —
all server state (migration 0044's five tables), read through `pf_get_workflow` and driven
by `polyforge drain` or the low-level `polyforge engine workflow` adapter. The adapter's
three execution modes share one controller policy: unattended flows use the driver;
automatic steps, every review/verification gate, and shipping steps use the preflighted
worker with the pinned model, effort and grant; and only human-facing, non-isolated
authoring can run
through `--prepare` / `--submit` in the main session. Effective RHS never makes an
independent gate local. A pinned-flow read/start/preflight failure is an error or
an actionable hold; it **never** falls back to the legacy scenario graph. Interactive
lifecycle ownership remains deliberately bounded: the adapter does not claim,
authenticate an approval, pause/wrap, or clean up on the human's behalf. The local
`/pf-*` skill bodies become **thin, DB-aware compatibility entries**: they keep the
session-side safety (Iron Rules, auth, doctor, bootstrap) and the legacy flows, and they
route to the stored flow instead of reimplementing it. No skill prose invents a controller.

## Ownership note

`docs/workflow-v2/**` is the *documentation* of the migration. The normative half of the
seed is code: `internal/skillregistry/seed.go` (the bundles) and
`internal/skillregistry/import.go` (tree → closed bundle). The normative half of the
stored flows is the server: migrations `0043_skill_registry.sql`, `0044_wi_workflows.sql`,
`internal/domain/skill_registry*.go`, `internal/domain/workflow*.go`,
`internal/workflow/*` (pure policy), `internal/controller/*` (the fail-closed selection boundary and shared driver),
`internal/cli/skills.go` plus `pkg/client/skill_seed.go` (authenticated seed/import), and
`internal/mcp/tools_workflows.go`. Where this
documentation and that code disagree, the code wins and this directory gets fixed.
