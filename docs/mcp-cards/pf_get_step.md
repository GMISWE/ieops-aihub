# pf_get_step — contract card

```json
{
  "tool": "pf_get_step",
  "description_sha256": "9bb68684365a1050698910b8d356c29e77db93829b76422d141a77629252d501",
  "params": {
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "completed_steps",
    "completed_steps_truncated",
    "current_step",
    "current_step_attempt",
    "current_step_status",
    "scenario_ref",
    "step_started_at",
    "version",
    "wi_type",
    "work_item_id"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One parameter, and the description makes five promises: that this record is
authoritative and unique, that `completed_steps` is the history oldest-first with
retries included, that a resuming agent should call it FIRST, that a slug is
accepted and the canonical id is echoed, and that there is no step graph here.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | "Work item ID" — a slug or a canonical id |

The negative half of the description is load-bearing and was added by `aihub#265`:
it used to advertise "step graph, current status, progress, previous steps" while
the endpoint returned none of the last two, which is why every scenario step graph
once opened by telling the agent to read `.pf_steps.json` — a file nothing in this
repo ever writes.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_step.go` (`registerStepTools`) validates the argument is
non-empty and calls `pkg/client/client.go` (`GetStep`), which issues
`GET /v1/work_items/<id>/step`. The value travels as a **path segment**, not a
query parameter or a body field, so there is no forwarding table for it to be
dropped from.

`internal/server/routes_step.go` (`handleGetStep`) binds it from the route and
resolves slug-or-id server-side. No credential is sent: this is a read.

## hop 4 — what it actually does

The handler returns `server.StepState`, and the relationship between the
description and that struct is itself gated: `internal/mcp/tools_step_contract_test.go`
asserts in BOTH directions that every response field the description names is a
bound JSON key on the struct, so the tool cannot go back to promising something the
struct does not carry.

Two behaviours a caller has to know and cannot see from the schema:

- **`completed_steps` is the step-history table, not a derived view.** Rows are
  filed by `pf_update_step` on a terminal transition and keyed on
  `step_attempt_id`. Since `aihub#399` a terminal transition without one is refused
  400 rather than answered 200, so "the step completed but is missing from the
  history" is no longer reachable through the tool — `aihub#390` is where that was
  reachable, and the two records disagreeing is what that work item measured.
- **`completed_steps: []` and the key being absent are different answers.** Empty
  means nothing has completed. Absent means the server predates `aihub#265`. The
  description states this because a client that conflates them reports "no prior
  steps" for a work item that has six.

The echoed `work_item_id` is the canonical id even when a slug was passed, and
that is the value `pf_recall` and `pf_read_events` need — both return nothing for a
slug, silently, because their filters compare against a column that holds canonical
ids only.

## hop 5 — what comes back

The result is passed through by `jsonResult` with no projection: this tool has no
slim function, so every key the server sends reaches the model. The corpus record
above is the union of top-level keys any caller has actually been handed over 435
calls with a 0.00% error rate — the highest-volume error-free tool in the census.

`current_step_attempt` and `scenario_ref` appear there and are not named in the
description; they are carried, not promised.

## Policy

- **§6.1 T1-9** — prose that contradicts hop 3 or 4 is a bug at the same priority
  as the behaviour, and "description-only" is not a cancellation reason. This tool
  is the worked example: `aihub#265` fixed the description by fixing what the
  endpoint returns, rather than by trimming the promise.
- **§6.2 T2-21** — the `aihub#400` findings about this call were revived rather
  than left cancelled, on the grounds that it is "the one call every resuming agent
  is told to make first".

## Open

- Nothing this card can settle. The `§6.4` items do not touch this tool.
