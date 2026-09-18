# pf_approve_workflow — contract card

```json
{
  "tool": "pf_approve_workflow",
  "description_sha256": "d123edf33eb1217b3f6d1ad75af1f3f17d1fddf5e6af837ead41323f57308cc5",
  "input_schema_sha256": "9cf217ef217122b4b6340029dbc6563d563325297064f3eeaef9a840da06a5d3",
  "params": {
    "artifact": {
      "type": "object",
      "required": true
    },
    "decision": {
      "type": "string",
      "required": true,
      "enum": [
        "approved",
        "rejected"
      ]
    },
    "step_id": {
      "type": "string",
      "required": true
    },
    "steps_version": {
      "type": "number",
      "required": true
    },
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

Five parameters; all forwarded verbatim.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | the work item being approved on |
| `steps_version` | number | yes | the generation the approval binds |
| `step_id` | string | yes | the gating step |
| `artifact` | object | yes | the exact {id, version, hash} being approved |
| `decision` | string | yes | approved or rejected |

The description promises human-only authorization, no actor argument, exact
artifact binding, the effective-RHS requirement, and generation-scoped
approvals.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_workflows.go` (`registerWorkflowTools`) forwards the five
fields to `pkg/client/workflows.go` (`ApproveWorkflowStep`) →
POST /v1/work_items/:id/workflow/approve, bound by
`internal/server/routes_workflows.go` (`handleWorkflowApprove`). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the route/handler binding probe lands with Batch 2B's parity fixtures --> No state file:
the authenticated API key of a HUMAN user is the credential, and the
authenticated `UserContext.UserType` is what the route checks first. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the handler-level user_type gate probe rides the Batch 2B engine loop -->

## hop 4 — what it actually does

- **A machine credential is refused whatever its role** — the domain repeats
  the gate so it is testable, and `TestWorkflowApprovalHumanOnlyExactArtifact`
  drives the 403 with `actorType` machine.
- **The artifact triple must equal the step's latest recorded result**; a
  mismatch answers CONFLICT_STEP_ATTEMPT_MATCH with both triples in details,
  and `steps_version` mismatches answer the same, driven in
  `TestWorkflowApprovalHumanOnlyExactArtifact`.
- **Idempotent same-decision, conflicting-decision 409**: the unique key is
  the artifact identity EXCLUDING the decision (one decision per artifact at
  the DB level), and the transaction locks the work item row, so re-approving
  returns the recorded row and a `rejected` after an `approved` on the same
  triple is refused — no check-then-insert race can record both
  (`TestWorkflowApprovalHumanOnlyExactArtifact`).
- **The actor is the authenticated principal**: the request has no actor field
  at all, and the recorded `actor_user_id` is the caller
  (`internal/domain/workflow_run.go`, `ApproveWorkflowStep`), asserted by
  `TestWorkflowApprovalHumanOnlyExactArtifact`.

## hop 5 — what comes back

`jsonResult`, no projection: the recorded approval row (`id`, `work_item_id`,
`steps_version`, `step_id`, `artifact`, `decision`, `actor_user_id`,
`actor_display`, `created_at`). The approval is then visible on
`pf_get_workflow`'s `progress[]` as the tri-state `approved` for exactly that
artifact (`TestWorkflowApprovalHumanOnlyExactArtifact`).

## Policy

- Spec D7: approval is a distinct authenticated human action bound to artifact
  ID/version/digest and flow/step identity; resolving an annotation is not
  approval and model prose cannot approve itself.

## Open

- UI approval authentication through the cookie session is future work for the
  UI worker; the API-key path here is the checked one, as of 2026-09-17
  (`TestWorkflowApprovalHumanOnlyExactArtifact`).

