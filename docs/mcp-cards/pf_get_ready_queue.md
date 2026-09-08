# pf_get_ready_queue — contract card

```json
{
  "tool": "pf_get_ready_queue",
  "description_sha256": "732dfc544de531cae069f2b65eed4c55d866bb57154926113faa82a5668ca6f6",
  "input_schema_sha256": "8b4e1f590c87688c34098d4d5dd3d1e1f733c1876b7a67189e87c5220883d96f",
  "params": {
    "max": {
      "type": "string",
      "required": false
    },
    "project": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "items",
    "needs_human_session",
    "paused",
    "running",
    "stale_running",
    "stalled",
    "unclassified"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters, and this tool is the canonical example in this repo of a published
parameter that reached the wire and was read by nothing.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | "Project name" |
| `max` | string | no | "Max items in ready section (default 10). A JSON number is also accepted" |

`non_conflicting` was published here and forwarded as `?non_conflicting=true` from
the day the tool was added until `aihub#387` withdrew it. Nothing ever read it:
`handleGetReadyQueue` reads `project` and `max` only, and the domain function takes
no such argument — measured 2026-09-07, a repo-wide grep across
`internal/server`, `internal/domain`, `pkg` and `cmd` returned zero hits. Passing it
returned the ordinary queue with no error and no warning.

It is gone rather than implemented, by owner decision: "non-conflicting" has no
agreed definition here — predicted from `declared_resources`, or read off the locks
actually held? — and this repo has measured `pf_predict_conflicts` untrustworthy in
both directions, so building on it would have produced a second untrustworthy
predicate.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) renders both into a
query string for `pkg/client/client.go` (`GetReadyQueue`) →
`GET /v1/work_items/ready`, bound by `internal/server/router.go`
(`handleGetReadyQueue`). That route is registered BEFORE `/work_items/:id` so the
literal `ready` is not swallowed as an id.

`max` goes through `scalarArg`, not `strArg`. It is published as a string but "max
5" is most naturally written as a JSON number, and `strArg` returns `""` for a
non-string — so the value was dropped and the server fell back to its own default
of 10, at no hop with an error. Same defect and same fix as `limit` on
`pf_list_work_items`.

🔴 `internal/mcp/ready_queue_param_wiring_test.go` fails on any parameter this
schema publishes that the handler does not read. Do not re-add a parameter here
without a hop-3 reader to go with it.

## hop 4 — what it actually does

Returns the six-section LCRS view for one project. The `items[]` section uses the
**same SQL predicate** as `pf_list_work_items`' `ready_only` — one shared constant —
but **not the same page**: this defaults to 10 ordered by priority desc, that one to
50 ordered by `created_at` desc. With more ready items than either limit the two
return different subsets, which is stated on `ready_only`'s own description because
the natural reading is that they agree.

`max` above 200 is clamped. That clamp is the one self-declared exemption from the
disclosure convention: the `ReadyQueue` struct carries no `request_adjusted` field
at all, so `max=5000` and `max=200` return byte-identical responses.

## hop 5 — what comes back

`jsonResult`, no projection.

🔴 **The published description says "LCRS (6-section)" and the struct has seven keys.**
`internal/domain/work_items.go` (`ReadyQueue`) declares `items`, `running`, `stalled`,
`paused`, `needs_human_session`, `unclassified` and `stale_running`, the last with
`omitempty` — so a caller sees six until it is non-empty, and cannot tell an empty
`stale_running` from a server that does not have one. The design document draws a
third count: six keys, no `stale_running`, and three fields that cannot exist
(`unblocked_at`, `expires_at` removed in v1.21, and `kind` deleted in v1.22).

The corpus record above spans 155 calls with a 0.00%
error rate. Four of those results were prose rather than strict JSON, which is why
the census counts `json_object_results` and `prose_results` separately — a prose
result contributes no keys, and reading that as "returns nothing" is the trap the
corpus README warns about.

## Policy

- **§6.1 T1-2 / T1-12** — the two numeric rules stand: unparseable is 400, out of
  range is clamped AND disclosed via `request_adjusted`. **This tool is the named
  counter-example** — it obeys half of rule 2. The ruling is to close the exemption
  by giving `ReadyQueue` the disclosure field, filed as `aihub#432`, and explicitly
  NOT to narrow the policy to match the code.
- **§6.1 T1-9** — `non_conflicting` was withdrawn rather than left as prose that
  contradicts hop 3, which is the disposition that rule requires.
- **§6.2 T2-20** — say "**6 plus `stale_running`**" (or drop the `omitempty`), and
  delete `unblocked_at`. Both are properties of THIS response and are measured on this
  tree: `internal/domain/work_items.go` (`ReadyQueue`) declares six segments plus
  `stale_running` with `omitempty`, so an absent `stale_running` cannot be told apart
  from an older server; and `internal/domain/work_items.go` (`ReadyItem`) declares
  `UnblockedAt`, which **no code in this repository ever writes** — a field that is
  published and never populated. The `created_at` asymmetry between the segments is
  settled as designed and is not to be re-opened.

## Open

- The `request_adjusted` exemption above is a known open gap, not a settled design.
  A caller cannot today distinguish "you asked for 5000 and got 200" from "you asked
  for 200".
