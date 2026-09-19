# 07 — WI composition: create-with-steps, workflow modes, and the composer path (aihub#720)

This is the contract document for aihub#720: making the create → compose → pin
boundary explicit so that no work item ever *silently* walks the legacy
scenario graph. It sits on top of the workflow-v2 base (01–06): the registry,
the seed, the pinned-generation machinery and the fail-closed execution rules
are all unchanged — what aihub#720 adds is the **explicit mode discriminator**
on the work item row and the **create-time composition path** that pins
generation 1 atomically.

Where this document and the code disagree, the code wins
(`internal/domain/workflow.go`, `internal/domain/work_items.go`,
`internal/domain/workflow_run.go`, migration
`internal/db/migrations/0045_work_items_workflow_mode.sql`).

## 1. The create-with-steps REST contract

`POST /v1/work_items` gained two fields. Everything else on the route is
unchanged; both fields are additive and optional, and a request that carries
neither is **byte-identical to the pre-aihub#720 create**.

```jsonc
{
  "project": "pilot",
  "goal": "add retry budget to the claim gate",
  "wi_type": "fix_bug",
  "requires_human_session": false,     // REQUIRED (explicit) whenever steps is present
  "workflow_mode": "db",              // "", "legacy", "db" or "pending" — see §2
  "steps": [                          // the SAME per-step shape PUT /workflow takes
    {
      "id": "spec",
      "skill_id": "skill_dp4bDA1C",
      "skill_version": 0,              // 0 = pin the caller's latest accessible
      "models": [ {"harness": "pi", "model": "<model>", "effort": "medium"} ]
    },
    {
      "id": "review",
      "skill_id": "skill_7trQkOHs",
      "skill_version": 1,
      "rhs": false,                   // omitted means false; pointer, not bool
      "params": { "strictness": "blocker" },
      "inputs": [ {"name": "spec", "step_id": "spec", "output": "artifact"} ]
    }
  ]
}
```

Rules the server enforces — one place, `resolveCreateWorkflowMode`
(`internal/domain/workflow.go`), the single judge of the steps/mode
combination:

| request | result |
|---|---|
| `steps` present, `workflow_mode` `""` or `"db"` | one transaction: WI row + resolved skill refs + derived grants + validated graph + generation 1 pinned; row lands `workflow_mode='db'`, `steps_version=1` |
| `workflow_mode: "pending"`, no `steps` | row filed `workflow_mode='pending'`, `steps_version=0` — for an orchestrator to compose later (§3) |
| `workflow_mode: "legacy"`, no `steps` | explicit scenario-graph opt-in; `steps_version=0`; the compatibility path |
| neither field | legacy create, byte-identical to before (row lands `'legacy'` via the column DEFAULT — not via a second code path) |
| `steps` + `"legacy"` | 400 `COMPOSE_FAILED`, reason `steps_with_legacy_mode` |
| `steps` + `"pending"` | 400 `COMPOSE_FAILED`, reason `steps_with_pending_mode` |
| `"db"` without `steps` | 400 `COMPOSE_FAILED`, reason `db_mode_requires_steps` |
| any other mode value | 400 `COMPOSE_FAILED`, reason `invalid_workflow_mode` |
| `steps` without explicit `requires_human_session` | 400 `COMPOSE_FAILED` — a workflow-bearing work item is classified at birth, never NULL |

The per-step shape is exactly the `PUT /v1/work_items/:id/workflow` shape
(`domain.WorkflowStepSpec`) — there is no second step schema. Validation,
grant derivation and version resolution are the SAME code path as
`pf_update_workflow` (`pinWorkflowGeneration`); a create-with-steps is
semantically "create the WI, then pin generation 1 in the same transaction".
Atomicity is the transaction's: on any failure the whole create rolls back —
no partial WI, no orphan generation, and **never a silent fallback to the
legacy scenario graph**.

### Error shapes

- **`COMPOSE_FAILED`** — HTTP 400. Create-time composition failed: unknown or
  inaccessible skill, invalid graph/input reference, grant or capability
  mismatch, invalid model/effort, or a contradictory steps/mode combination.
  `details` always carries a non-empty machine-readable `reason` from the
  closed set (`invalid_workflow_mode`, `steps_with_legacy_mode`,
  `steps_with_pending_mode`, `db_mode_requires_steps`, plus the pin path's own
  reasons re-typed through `composeFailedFromPin`). Fail-closed by
  construction: the error is the answer, not a retry in disguise.
- **`COMPOSE_PENDING`** — HTTP 409, from the **claim** gate, not create: the
  work item is `workflow_mode='pending'` and a claim arrived before any
  generation was pinned. A state conflict, not a bad request — the recovery is
  to pin a generation (`PUT /v1/work_items/:id/workflow`) and claim again.
  Deliberately not `COMPOSE_FAILED`: composition has not failed, it has not
  *happened yet*.

## 2. `workflow_mode` — the three-state semantics

Migration `0045_work_items_workflow_mode.sql` adds
`work_items.workflow_mode TEXT NOT NULL DEFAULT 'legacy'` with a CHECK over
exactly `legacy | db | pending`. The DEFAULT is the backfill arm: every row
that predates the column reads `'legacy'` — conservative, no history
rewritten — and `steps_version=0` is **never again read alone** as a legacy
signal. The migration is idempotent (`ADD COLUMN IF NOT EXISTS`, constraint
inline), so replaying it against an already-migrated database is a no-op.

| mode | meaning | `steps_version` | claimable |
|---|---|---|---|
| `legacy` | walks the local scenario step graph — the path every wi without a pinned flow has always taken, unchanged | 0 | yes (ordinary lifecycle) |
| `db` | walks the pinned DB workflow generation — the aihub#708 stored-flow path | ≥ 1, pinned in the same create or revision | yes |
| `pending` | filed for DB composition that has not happened yet; nothing to execute | 0 until a first generation is pinned | **no — `COMPOSE_PENDING` (409)** |

The pending claim gate sits under the work item row lock in
`FnClaimWorkItem`, *before* idempotency replay and status checks: a claim
either sees pending and refuses, or sees the pinned generation and proceeds —
there is no window where a claim can hand a pending work item to the legacy
scenario dispatch. The refusal leaves no attempt behind; the row is untouched.

## 3. The AI composer's standard path

The canonical orchestration flow, end to end on the MCP surface:

1. **Create pending** — `pf_create_work_item` with
   `workflow_mode="pending"` (and *no* steps). The work item exists, is
   visible in every queue read, and cannot be claimed: `COMPOSE_PENDING` is
   the typed "not composed yet" answer to anyone who tries.
2. **Discover skills** — the four read-only registry tools (§4) are the
   composer's discovery surface: what exists, what the caller can access, what
   each version's contract demands.
3. **Pin generation 1** — `pf_update_workflow` (`PUT
   /v1/work_items/:id/workflow`) with `expected_steps_version: 0`, an explicit
   `requires_human_session`, and the steps array. The server resolves every
   skill ref through the accessibility predicate, derives grants from the
   pinned contracts, validates the graph with the same pure
   `internal/workflow` policy the create path uses, and pins generation 1
   atomically.
4. **Claim and run** — the ordinary lifecycle; the work item now walks the
   stored flow (fail-closed, per 01-authority-and-compatibility.md).

> ✅ **Gap closed in-change (2026-09-19)**: the review-draft version of this
> section recorded that `UpdateWorkItemWorkflow`'s pointer UPDATE did not flip
> `workflow_mode` from `pending` to `db`, leaving a pinned-but-forever-pending
> work item the claim gate would keep refusing. That is fixed on this branch:
> both pin paths (the create transaction's first-generation insert AND the
> `UpdateWorkItemWorkflow` pointer move) now write `workflow_mode='db'` in the
> same statement that moves the pointer, so `pending` → pin → claim is a
> working path. The regression that pins this is
> `TestCreateWorkItemWorkflowMode_PendingFlipsToDbOnFirstPin`: it creates a
> `pending` work item, pins generation 1 via `UpdateWorkItemWorkflow`,
> asserts the mode reads `db`, and asserts the claim gate that refused the
> pending row now accepts it. Both composer paths (§1 one-shot create with
> steps, and §3 create-pending → pin → claim) are therefore live.

The single-call alternative is §1 itself: a composer that already knows the
steps sends them with the create. Both paths share one validator; the
difference is only whether composition happens inside the create transaction
or in a later `PUT`. A **failed** composition is never a created work item:
`COMPOSE_FAILED` rolls the whole create back.

## 4. The MCP skill read surface (the composer's discovery tools)

Four read-only tools over the registry's GET surface, registered in
`internal/mcp/tools_skills.go` and carded under `docs/mcp-cards/`. The write
and share surface (create, publish, share, revoke, visibility) is deliberately
NOT published: composition is a read-then-propose act, and a write tool would
let a caller widen registry access from inside a session.

- **`pf_list_skills`** — owned + shared-with-caller's-projects + public
  skills, with latest-accessible version metadata; `owner`, `cursor`,
  `limit`. A skill with no version the caller can read is not listed.
- **`pf_get_skill`** — one skill identity + its latest accessible version.
- **`pf_list_skill_versions`** — the versions the caller can read, newest
  first.
- **`pf_get_skill_version`** — one exact version *including* its bundle and
  runtime contract — what a composer reads to know the params schema,
  capability grants and allowed model candidates before proposing the step.

Visibility is the registry's own, applied server-side on every call: an
unknown skill and an inaccessible one answer the same `NOT_FOUND`
(the no-metadata-oracle rule), and an exact-version request never falls back
to latest — `version` is required and a mismatch is a refusal.

### Worked example: composing a small-fix flow from the canonical seed

The production registry holds the canonical 8 skills seeded by aihub#719
(all private, owner `u_5dFjeaMZ`; artifact `mem_olRL90gz`). Seed pins as
measured at aihub#720's spec time:

| skill | seed pin |
|---|---|
| `spec` | `skill_dp4bDA1C:1` |
| `plan` | `skill_h4wJN3cQ:1` |
| `code-change` | `skill_BcQp6bg4:1` *(later versions may exist — read the registry, do not freeze this table)* |
| `review` | `skill_7trQkOHs:1` |
| `verification` | `skill_3moejwaW:1` |
| `ship` | `skill_WBRARkZn:1` |
| `ci` | `skill_KrozF5VM:1` |
| `grill-me` | `skill_RyrugUwI:1` |

The pins above are a *reading*, not a *contract*: exact versions move when a
seed is republished (the code-change bundle gained an implementation-discipline
clause after the seed), and a composer must **always** read the live registry:

```
pf_get_skill_version(skill_id="skill_dp4bDA1C", version=1)
  → the spec bundle + runtime contract: capability grants, params schema,
    allowed model candidates — everything a proposal must respect.

pf_list_skills()  →  confirm the skill ids and latest accessible versions

POST /v1/work_items
{ "project": "pilot", "goal": "...", "wi_type": "fix_bug",
  "requires_human_session": false, "workflow_mode": "db",
  "steps": [
    {"id": "spec",        "skill_id": "skill_dp4bDA1C", "skill_version": 1,
     "models": [{"harness": "pi", "model": "<model>", "effort": "medium"}]},
    {"id": "code-change", "skill_id": "skill_BcQp6bg4", "skill_version": 0,
     "models": [...],
     "inputs": [{"name": "spec", "step_id": "spec", "output": "artifact"}]},
    {"id": "review",      "skill_id": "skill_7trQkOHs", "skill_version": 1, "rhs": false, ...},
    {"id": "verification","skill_id": "skill_3moejwaW", "skill_version": 1, ...},
    {"id": "ship",        "skill_id": "skill_WBRARkZn", "skill_version": 1, ...},
    {"id": "ci",          "skill_id": "skill_KrozF5VM", "skill_version": 1, ...}
  ] }
```

`skill_version: 0` pins the caller's latest accessible version at composition
time — the server freezes the exact number; it never means "latest at
execution time".

## 5. Legacy: the explicit opt-in

The legacy scenario path is not removed, not deprecated and not scheduled for
removal (aihub#710's cleanup is a separate owner decision). What changed is
that it is now **explicit**:

- A create with **neither** `steps` nor `workflow_mode` is legacy — this is
  the byte-identical compatibility path every existing caller and scenario
  step graph keeps using. Nothing about it moved.
- A create that wants legacy *and* wants the mode visible on the row says
  `"workflow_mode": "legacy"` explicitly. The two are the same path; the
  explicit form is documentation in the row.
- `steps` + `"legacy"` is refused (`steps_with_legacy_mode`): a work item
  cannot carry two step authorities. Once a work item has pinned a generation,
  or has run legacy scenario-graph steps, the two authority rules of
  01-authority-and-compatibility.md apply unchanged.
- A pinned-flow work item **never** falls back to the scenario graph, on any
  read/start/preflight failure — that invariant predates aihub#720 and is
  restated here because the mode column makes it checkable:
  `workflow_mode='db'` + `steps_version≥1` is the stored-flow contract, and
  every failure against it is an error or an actionable hold.

**No default switch happened.** Every newly created work item without a
composition proposal still walks the legacy graph; switching the *default* for
new wis to DB composition is a separate owner decision gated on the aihub#715
review evidence (≥ 6 same-class wrapped WIs through composed flows), not part
of aihub#720.

## 6. Test evidence and the ratchet

The DB-gated half of this contract is eight top-level functions, registered
in `internal/citest/dbtestcov/gated_tests.txt` and executed by the CI step
"aihub#720 workflow_mode composition DB tests"
(`-run '^(TestMigration0045WorkflowMode_|TestCreateWorkItemWorkflowMode_)'`,
fail on any `--- SKIP`):

- `internal/domain/migration_0045_workflow_mode_db_test.go` — backfill, row
  default, vocabulary acceptance, invalid-mode rejection (SQLSTATE 23514),
  replay no-op.
- `internal/domain/create_work_item_workflow_mode_db_test.go` — mode
  `'db'` persisted with steps in the same transaction; legacy default without
  steps; pending stored, queued, and claim-refused with 409 `COMPOSE_PENDING`
  leaving no attempt behind.

The non-DB halves (the pure `resolveCreateWorkflowMode` combination table,
the COMPOSE_FAILED re-typing boundary, the route's pre-transaction refusals
and wire classification, the client's typed mirror
`pkg/client.CreateWorkItemWithWorkflow`) run in the ordinary unit steps.
