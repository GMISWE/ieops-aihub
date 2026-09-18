# pf_reconcile_workflow — contract card

```json
{
  "tool": "pf_reconcile_workflow",
  "description_sha256": "73c272bf27f2592f4e734b44cd47efcde29178ea393e13120a29ba3250ea8fe5",
  "input_schema_sha256": "7d5c144e3ff30fe7a4efb36ff1ac0bb179f5b0f83d748e30be5905bfd040df4a",
  "params": {
    "supersede_attempt_id": {
      "type": "string",
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

Two parameters, both required, plus the state-file credentials the handler
injects and never publishes.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | the work item whose workflow carries the dead attempt's open invocations | <!-- probe-waiver: kind=pending-implementation | decided=2026-09-18 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->
| `supersede_attempt_id` | string | yes | the attempt a pause/takeover ended, whose open invocations are abandoned | <!-- probe-waiver: kind=pending-implementation | decided=2026-09-18 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

The description names the no-implicit-trust fence (the named attempt's own row
is read under the work item lock; it must belong to this work item and must not
be running), the both-kinds scope (ordinary and repair-episode-bound
invocations alike), the stale-result consequence for the superseded invocation,
and the idempotent 0.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_workflows.go` (`registerWorkflowTools`) resolves the state
file and sends the three credentials plus the two fields to
`pkg/client/workflows.go` (`ReconcileWorkflowInvocations`) →
POST /v1/work_items/:id/workflow/reconcile, bound by
`internal/server/routes_workflows.go` (`handleWorkflowReconcile`), gated on
`writer` plus the attempt credentials. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-18 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

## hop 4 — what it actually does

- **Locks the work item row first and holds it through the supersede**, the
  same lifecycle-fence order every attempt-mutating route uses, so a takeover
  racing the reconcile cannot leave one attempt's leftovers half-fenced — the
  parked-loser window is `TestWorkflowLifecycleFenceBlocksLoserMutations`
  (aihub#708 B2).
- **Reads the named attempt's own row under that lock and refuses to trust the
  request's word**: unknown id → 404, an attempt of ANOTHER work item →
  CONFLICT_STEP_ATTEMPT_MATCH, the CURRENT attempt → refused, a still-RUNNING
  attempt → refused, and only a paused, superseded or ended attempt of this
  work item can be fenced (`TestWorkflowReconcileFencesDeadAttemptInvocations`).
- **Supersedes every OPEN invocation of the named attempt** — ordinary,
  retry-bound and episode-bound alike — writing `status='superseded'` with
  `superseded_at` plus one `workflow_invocation_superseded` event per
  invocation, all in one transaction
  (`TestWorkflowReconcileFencesDeadAttemptInvocations`).
- **A superseded invocation refuses results with a distinct stale refusal**,
  stops counting toward an episode's role set and its binding bounds, and frees
  the step for the live attempt's replacement, which the episode then completes
  through (`TestWorkflowReconcileFencesDeadAttemptInvocations`). Nothing else
  in production writes the `superseded` status. <!-- prose-only: because=judgement -->

## hop 5 — what comes back

`jsonResult`, no projection: the reconcile outcome (`work_item_id`,
`superseded_attempt_id`, `superseded`, `invocation_ids`). `superseded` is 0 on
the idempotent re-run (`TestWorkflowReconcileFencesDeadAttemptInvocations`).

## Policy

- aihub#708 B3: an open invocation from a paused/taken-over attempt is fenced
  by an explicit controller-reconcile transition — never silently superseded by
  a start, and never trusted from the request; the new attempt can start
  replacements, the stale old result is rejected.

## Open

- Whether the Batch 2B controller reconciles eagerly on resume/takeover or
  leaves the timing to the engine loop is an engine design question as of
  2026-09-18; the transition itself is live and tested from the domain layer.
