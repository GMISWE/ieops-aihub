# pf_update_workflow — contract card

```json
{
  "tool": "pf_update_workflow",
  "description_sha256": "6812c8a6e078f432b6dc8117f99cde98cc236c67ef7fbf3bf14621f6b6456dd4",
  "input_schema_sha256": "c6f5f415c58abdb5829e1577a3ba170d54049ad8fbc05f3edeb5349a43bff6d0",
  "params": {
    "expected_steps_version": {
      "type": "number",
      "required": true
    },
    "requires_human_session": {
      "type": "boolean",
      "required": true
    },
    "steps": {
      "type": "array",
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

Four parameters; `work_item_id`, `expected_steps_version`,
`requires_human_session` and `steps` are all forwarded (hop 2-3). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | the work item being revised |
| `expected_steps_version` | number | yes | the CAS token the revision compares against |
| `requires_human_session` | boolean | yes | explicit classification, never defaulted |
| `steps` | array | yes | the flow to pin, resolved and validated atomically |

The description promises atomicity, latest-accessible resolution for
`skill_version` 0, a 409 carrying the current value on CAS mismatch, an
explicit `requires_human_session`, refusal while a worker is in progress, and
refusal for scenario-graph work items — each named below. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=a summary of promises each held by a named arm below; a probe per promise, not per summary, is the plan -->

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_workflows.go` (`registerWorkflowTools`) builds the body
verbatim from its arguments and sends it to `pkg/client/workflows.go`
(`UpdateWorkItemWorkflow`) → PUT /v1/work_items/:id/workflow, bound by
`internal/server/routes_workflows.go` (`handlePutWorkflow`), which requires
`writer` on the project and then lets the domain apply the stricter
contract-tier matrix. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop --> No attempt credentials: a revision is an author edit,
not a controller action.

## hop 4 — what it actually does

- **Resolve → validate → pin is ONE transaction.** The generation row is
  inserted and the pointer moved inside the same transaction that resolved the
  refs through `skillVersionAccessSQL`; any refusal leaves the work item
  untouched (`TestWorkflowCreatePinsAtomically` drives the create twin and
  `TestWorkflowRevisionCASAndInProgress` the revision). `TestWorkflowCreatePinsAtomically` drives the create-time twin and
  `TestWorkflowRevisionCASAndInProgress` drives this route's CAS both ways.
- **`skill_version` 0 pins the caller's latest ACCESSIBLE version, never a
  private newer one** — `TestWorkflowPrivacyHoldsLatestAccessiblePin` publishes
  v1 shared and v2 private and asserts the member pins v1.
- **Grants are derived server-side from the pinned contracts' capabilities**
  (`internal/domain/workflow.go`, `workflowGrantForContract`); the request has
  no field that reaches the grant, and `TestWorkflowCreatePinsAtomically`
  asserts the frozen grants. `TestWorkflowCreatePinsAtomically` asserts
  the frozen grants for write, review and verification steps.
- **An invalid composition refuses everything**: duplicate ids, missing review
  and verification gates, interactive-only steps in an rhs=false flow, and
  steps without an explicit `requires_human_session` are each refused with
  nothing written, each in its own subtest of
  `TestWorkflowCreatePinsAtomically` and `TestWorkflowRevisionCASAndInProgress`.
- **Revision while running is refused** with CONFLICT_WI_ALREADY_CLAIMED
  (`TestWorkflowRevisionCASAndInProgress`), and the same updateGate matrix the
  goal/wi_type edits use governs who may revise at all
  (`internal/domain/work_items.go`, `updateGate`).

## hop 5 — what comes back

`jsonResult`, no projection: the same `WorkItemWorkflow` shape
`pf_get_workflow` returns, now describing the new generation, including the
`generations` history with the just-written row. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

## Policy

- Spec D4: immutable generations, expected_version CAS, no revision while in
  progress, dependent approvals/evidence invalidated by version, not deletion.
- Spec D5: `requires_human_session` is explicit on every revision and lands on
  the work item row (`TestWorkflowRevisionCASAndInProgress` reads it back).

## Open

- Invalidating dependent approvals on revision happens by VERSION PINNING, the
  rows staying readable (`TestWorkflowRevisionCASAndInProgress` reads generation
  1 after the revision); no UI request for a softer view is recorded in
  aihub#708's attrs as of 2026-09-17.
