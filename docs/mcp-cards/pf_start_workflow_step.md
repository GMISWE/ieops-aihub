# pf_start_workflow_step — contract card

```json
{
  "tool": "pf_start_workflow_step",
  "description_sha256": "73a65c08f786b84e916b179d5a904c94eedcb72be8c054af0615d77f0111cff1",
  "input_schema_sha256": "cf05d1f4e11b73c8deb3e1e27f93dbe044b05b5bf593338a7e873af092fd8994",
  "params": {
    "repair_episode_id": {
      "type": "string",
      "required": false
    },
    "step_id": {
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

Three parameters; `work_item_id` and `step_id` are required, `repair_episode_id`
binds a repair invocation. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | the work item the step belongs to |
| `step_id` | string | yes | the step from the pinned flow to start |
| `repair_episode_id` | string | no | the open repair authorization to bind to |

The description promises the access recheck, the composition re-validation,
ordering enforcement with repair-only reopening, server-minted identities and
state-file-injected credentials.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_workflows.go` (`registerWorkflowTools`) resolves the state
file (`config.ResolveStateFile`, refusal through `config.StateFileMissingErr`)
and sends the three credentials plus `step_id` and `repair_episode_id` to
`pkg/client/workflows.go` (`StartWorkflowStep`) →
POST /v1/work_items/:id/workflow/start, bound by
`internal/server/routes_workflows.go` (`handleWorkflowStart`). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the route/handler binding probe lands with Batch 2B's parity fixtures --> Error
classification goes through `classifyStepUpdateErr`, the same classifier
`pf_update_step` uses, so a dead credential deletes the state file the same way. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the classifier-deletes-state-file probe is Batch 2B's driver work -->

## hop 4 — what it actually does

- **Attempt fencing first**: wrong secret answers ATTEMPT_MISMATCH and wrong
  epoch CONFLICT_EPOCH_MISMATCH, both driven in
  `TestWorkflowStartFencingAndAccessRecheck`.
- **Registry access is re-checked from the ATTEMPT ACTOR's view** —
  `userRecordForAttempt` in `internal/domain/workflow_run.go` rebuilds the
  actor's scoped UserRecord and every pinned version must still be readable; a
  stranger's attempt on an owner-pinned private flow answers NOT_FOUND
  (`TestWorkflowStartFencingAndAccessRecheck`).
- **Ordering**: a later step cannot start before its predecessors completed,
  one open invocation per step at a time, and a step with a recorded result can
  only reopen through a repair authorization — all three refusals are driven in
  `TestWorkflowStartFencingAndAccessRecheck` and
  `TestWorkflowRetryAuthorizationBounded`.
- **Identities are server-minted**: `step_attempt_id` and `producer_id` come
  from `domain.NewID` inside the start transaction; the request has no field
  that reaches either (`internal/domain/workflow_run.go`, `StartWorkflowStep`),
  and `TestWorkflowResultStaleRefusals` refuses a forged `producer_id`.

## hop 5 — what comes back

`jsonResult`, no projection: the invocation descriptor (`invocation_id`,
`steps_version`, `step_id`, `step_attempt_id`, `producer_id`, `claim_epoch`,
`grant`, `skill_id`, `skill_version`, `models`, `params`, `inputs`,
`effective_rhs`). The worker result must echo the identity fields exactly.

## Policy

- Spec D3: access rechecked before every new invocation; already-authorized
  invocations finish.
- Spec D7: shared control enforces attempt epoch, flow version, step instance
  and step attempt; the server owns them all.

## Open

- The repair-role rules here are the server-side mirror of the pure policy's
  episode validation; whether `internal/workflow` will export a shared
  validator is a Batch 2B question as of 2026-09-17.
  <!-- prose-only: because=external-state -->
