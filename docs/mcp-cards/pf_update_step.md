# pf_update_step — contract card

```json
{
  "tool": "pf_update_step",
  "description_sha256": "bf7c33aea1609e73cdfc5dc2a1fdae405f73e64e9bf440693e41c665c8400b6f",
  "input_schema_sha256": "1c8e279af9e6b7fe6df861a7d593481caa99e456f05e6a12f8b25b1c74260f51",
  "params": {
    "artifact_summary": {
      "type": "string",
      "required": false
    },
    "error_type": {
      "type": "string",
      "required": false
    },
    "escalated": {
      "type": "boolean",
      "required": false
    },
    "heartbeat": {
      "type": "boolean",
      "required": false
    },
    "next_step": {
      "type": "string",
      "required": false
    },
    "next_step_attempt_id": {
      "type": "string",
      "required": false
    },
    "status": {
      "type": "string",
      "required": true
    },
    "step_attempt_id": {
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
  "response_keys_observed": [
    "next_step",
    "next_step_status",
    "status"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Ten parameters, and a description that was deliberately rewritten twice — by
`aihub#290` (round-trips) and `aihub#398` (three statements the server did not
honour). Its whole cost was measured rather than estimated: 2,759 B → 3,217 B of
Description + InputSchema, ~115 tokens on every request, with
`internal/mcp/tools_step_contract_test.go` holding the ceiling.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item; also names the local state file |
| `step_id` | string | yes | on terminal transitions must equal the server's `current_step` |
| `status` | string | yes | `in_progress` \| `completed` \| `failed` |
| `step_attempt_id` | string | no | REQUIRED for completed/failed, enforced server-side |
| `artifact_summary` | string | no | ≤4096 chars; longer is 413, not truncation |
| `error_type` | string | no | read on `failed`; ignored, not refused, on `completed` |
| `escalated` | boolean | no | same: read only on `failed` |
| `next_step` | string | no | complete-and-start in one call; only with `completed` |
| `next_step_attempt_id` | string | no | the attempt id of the step being STARTED |
| `heartbeat` | boolean | no | resets `step_started_at`; DISCARDS `step_id`/`status` |

Three of those sentences exist because the earlier wording was false in a way that
changed what a caller sends. "Concurrency is guarded server-side by the idle-step
predicate" named one predicate and implied it covered the endpoint; "keep the lease
alive" on `heartbeat` described a lease that v1.21 removed; and `error_type` /
`escalated` on a `completed` call are silently dropped, which is documented rather
than changed because rejecting them is a behaviour change.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_step.go` (`updateStepBody`) renders the arguments into the body
of `PATCH /v1/work_items/<id>/step`, bound by
`internal/server/routes_step.go` (`handleUpdateStep`). Three things happen at this
hop that the schema cannot show:

- **`step_id` is renamed on the wire.** The body key is `step`; the server reads
  `json:"step"`. `updateStepBody` was extracted from the handler precisely so a test
  can hold it against `server.UpdateStepRequest` — the `aihub#290` defect was a key
  this function emitted for which no bound field existed.
- **Optional keys are forwarded only when non-empty**, so omitting one stays
  distinguishable from sending `""`. The server binds them as `*string`.
- **`heartbeat` returns early with a credentials-only body**, so on that path
  `step_id`, `status`, `next_step` and everything else go nowhere. Two client-side
  validators — `validateNextStepArgs` and `validateTerminalStepArgs` — mirror the
  server's checks of the same names so a caller error costs no round trip; the
  server remains the authority.

Credentials (`attempt_id`, `claim_epoch`, `session_secret`) are injected from the
local state file via `internal/config/state.go` (`ResolveStateFile`).

## hop 4 — what it actually does

- **`in_progress`** is guarded by the idle predicate. **`completed` / `failed`**
  must name the step the server has open; a mismatch is 409 naming both. Neither
  checks step STATE, so an idle step with a matching name can be completed twice.
- **A 200 on a terminal transition means BOTH records landed** — the step-history
  row `pf_get_step`'s `completed_steps` reads from, and the
  `step_completed`/`step_failed` event. A transition that cannot deliver both is
  refused with nothing committed: 400 for a missing or blank `step_attempt_id`, 409
  for one that already has a history row, 413 for an oversized `artifact_summary`.
  That is `aihub#399` closing `aihub#390`, where a best-effort INSERT wrapped in a
  SAVEPOINT swallowed a CHECK violation and the handler still answered 200.
- **`next_step` is not guaranteed to be honoured by the peer.** This binary and the
  aihub server deploy on separate schedules, so the schema publishing `next_step`
  says nothing about what the remote binds. `checkNextStepHonoured` turns an old
  server's silent drop into a loud failure by looking for `next_step` echoed in the
  response — the one capability signal available, because `GET /v1/version` carries
  no capability list. Left unchecked the failure is corrupting rather than inert:
  `current_step` never advances, and the server derives each completion row's
  `step_id` from `current_step`, so every later completion files under the first
  step's name.
- **Error classification compares the server's CODE field, not the rendered text.**
  `classifyStepUpdateErr` used to run `strings.Contains` over the whole error
  string, so a step literally NAMED `ATTEMPT_MISMATCH` turned a step-identity refusal
  into the stale-credential arm and DELETED the caller's credential file. There is
  deliberately no substring fallback: the only arm with a side effect is the
  deleting one.

## hop 5 — what comes back

Passed through by `jsonResult`; no projection. `heartbeat_ok` is what a heartbeat
answers, which is why sending `status="completed"` with `heartbeat=true` completes
nothing and still looks like a success. The corpus record above is the union over
1,617 real calls at a 0.99% error rate.

## Policy

- **§6.1 T1-9** — this tool is where the "description-only defect is still a
  defect" rule was applied three times in one change, and all three were fixed by
  correcting the prose to match hop 4 rather than by cancelling the finding.
- **§6.2 T2-3** — one status code for "invalid attempt credential" across all
  tools. This handler's classification is the client-side half of that question.

## Open

- **§6.4 item 8** — nothing here describes the live server. `aihub#399`'s
  enforcement is on `origin/main`; production can trail a merge by days, so a
  caller seeing a 200 with no history row is seeing an older server, not a
  regression.
