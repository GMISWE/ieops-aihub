# pf_workflow_result — contract card

```json
{
  "tool": "pf_workflow_result",
  "description_sha256": "8d0ce6d56b53e764d3fa433402d25421a8ff0e8957a13dbf9f95b2b0c2fab30e",
  "input_schema_sha256": "dce210acd461ff69b7867f591564ae408bf7c711e9821c22bdb7dd27c97ef298",
  "params": {
    "result": {
      "type": "object",
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

Two parameters: `work_item_id` and the whole `result` envelope, forwarded
verbatim.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | the work item the result belongs to |
| `result` | object | yes | the StepResult envelope echoing the invocation identity |

The description promises the exact-identity echo, that a worker result may not
carry an approval, the status and verdict vocabulary, that a review FAIL pauses
rather than fails, and that stale results change nothing.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_workflows.go` (`registerWorkflowTools`) resolves the state
file and sends the three credentials plus `result` UNCHANGED to
`pkg/client/workflows.go` (`RecordWorkflowResult`) →
POST /v1/work_items/:id/workflow/result, bound by
`internal/server/routes_workflows.go` (`handleWorkflowResult`). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the verbatim-forwarding wire probe lands with Batch 2B's parity fixtures --> The handler
deliberately performs no local shape validation: the server is the validator,
and a local pre-check would refuse shapes before the request exists. <!-- prose-only: because=judgement -->

## hop 4 — what it actually does

- **The identity fence, in order**: current attempt credentials, current
  generation, an OPEN invocation of this attempt with this producer and this
  epoch. A forged `producer_id` answers ATTEMPT_MISMATCH, an unknown or already
  recorded `step_attempt_id` and a superseded `flow_version` answer
  CONFLICT_STEP_ATTEMPT_MATCH — every arm driven in
  `TestWorkflowResultStaleRefusals`, including the refusal of a result
  envelope that attempts to smuggle an `approval` key (the pure package's
  UnmarshalJSON refuses it before the transaction opens).
- **One transaction carries everything**: result row, invocation close, the
  step_completed/step_failed timeline events, and — on a review FAIL — the
  pause (`internal/domain/workflow_run.go`, `RecordWorkflowResult`); the
  workflow's step history lives ONLY in `wi_workflow_results` (nothing is
  copied into the legacy `wi_step_completions`, whose readers would read a
  superseded generation's rows as current history), with the events and that
  isolation asserted by `TestWorkflowResultStaleRefusals` and the
  pause-with-result atomicity by `TestWorkflowReviewFailPausesNotTerminal`.
- **A review FAIL pauses, never terminally fails**: run_attempt and work item
  go to `paused`, file_scope locks release with cause-bearing events, and the
  paused credential is dead for further results
  (`TestWorkflowReviewFailPausesNotTerminal`).
- **Review/verification results must carry a verdict and, when completed,
  evidence** — refused with a 400 naming the reason
  (`TestWorkflowResultStaleRefusals`).

## hop 5 — what comes back

`jsonResult`, no projection: `work_item_id`, `steps_version`, `step_id`,
`step_attempt_id`, `status`, `review_verdict` and `paused` — the last telling
the controller the attempt was paused by a review FAIL in the same transaction.

## Policy

- Spec D8: FAIL records evidence and pauses; recovery is authorized repair,
  never an automatic reroll and never a terminal write.
- Spec D7: the worker does not advance lifecycle; results are records, and the
  controller's pure policy decides on them.

## Open

- Whether the pure package will export its single-result validator (this
  server carries a local mirror, `validateWorkflowResultShape`) is an API gap
  recorded with aihub#708 Batch 2A as of 2026-09-17.
  <!-- prose-only: because=external-state -->
