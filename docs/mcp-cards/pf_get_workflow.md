# pf_get_workflow — contract card

```json
{
  "tool": "pf_get_workflow",
  "description_sha256": "6ada5a120dc24959a341e1c7c680000919b4d11044e1726995ec809952cef70c",
  "input_schema_sha256": "c4d438f796b6bcb601cf183ceadc4e65853f1dd853d27d799c7c2dba0e02fc8c",
  "params": {
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One parameter, `work_item_id`, and a description whose promises are all pinned
below at hop 4 by `internal/domain/workflow_db_test.go`.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | the work item whose workflow is read | <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

The description says the response carries the current pinned flow, the
append-only generation history, per-step progress with approval state, and open
repair authorizations, and that skill CONTENT is never included — both halves are
behavior: the shape is produced by `internal/domain/workflow_run.go`
(`GetWorkItemWorkflow`) and the no-content half is held by
`TestWorkflowPrivacyHoldsLatestAccessiblePin`, which greps the serialized view
for `bundle` and `contract` keys and for private version bytes.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_workflows.go` (`registerWorkflowTools`) forwards nothing but
the work-item ref to `pkg/client/workflows.go` (`GetWorkItemWorkflow`) →
GET /v1/work_items/:id/workflow, bound by `internal/server/routes_workflows.go`
(`handleGetWorkflow`). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the route/handler binding probe lands with Batch 2B's parity fixtures --> No credentials: this is a project-read view, gated at
`viewer` level by `checkProjectAccess` exactly like reading the work item
itself, and a hidden work item answers through `hideNotFound` the same 404 a
missing one does. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

## hop 4 — what it actually does

- **It reads frozen state, never the registry.** The view is assembled from the
  generation row, the invocation/result/approval/repair tables — a share revoked
  after pinning does not change what this returns, because
  `GetWorkItemWorkflow` performs no registry lookup (the design comment on it
  says the same thing). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the no-registry-lookup probe is an AST arm Batch 2B's review adds --> What a revoked share DOES change is the next
  `pf_start_workflow_step`, held by `TestWorkflowStartFencingAndAccessRecheck`.
- **`steps_version` 0 with null `steps` is the legacy answer** — read before
  any generation exists by `TestWorkflowRevisionCASAndInProgress`. A work item
  created without `steps` keeps that response shape forever unless a revision
  pins a first generation; `TestWorkflowCreatePinsAtomically` reads the pinned
  shape and `internal/domain/workflow_run.go` (`GetWorkItemWorkflow`) returns
  the zero shape before any generation exists.
- **`generations` is append-only history** (`TestWorkflowRevisionCASAndInProgress`
  reads generation 1's digest after the revision). Every revision inserts a row; none
  updates one, and `TestWorkflowRevisionCASAndInProgress` reads generation 1's
  digest after revising to generation 2 to prove the old row survives.

## hop 5 — what comes back

`jsonResult`, no projection: `steps`, `steps_version`,
`requires_human_session`, `generations[]`, `progress[]` (per-step latest
`status`/`review_verdict`/`step_attempt_id`/`artifact`, the open
`open_invocation_id`, tri-state `approved`, and the step's `rhs`) and `repairs[]`
(including `parent_episode_id` on retries that descend from an episode role
invocation — the immutable parent lineage, `TestWorkflowEpisodeProviderErrorRetryLineageCompletes`)
reach the model as the server sent them. `progress[].approved` is the state the
pure policy's `Decide` will read on the controller side for RHS-gated steps. <!-- prose-only: because=counterfactual -->

## Policy

- Spec D3 (aihub#708): WI visibility never widens skill content. This tool is
  the boundary's read side — references and progress only, bodies stay behind
  the registry's access checks.
- Spec D4: generations are immutable; this tool is the reader of that history.

## Open

- The UI flow view over this endpoint is future work for the UI worker; this
  card records only that the route read shape is JSON, checked as of 2026-09-17.
- No corpus record reads `repairs[]` yet — aihub#412's window closed before this
  tool existed, and the first consumer is Batch 2B's engine (aihub#708), as of
  2026-09-17.
  <!-- prose-only: because=external-state -->
