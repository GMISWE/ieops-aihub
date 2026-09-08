# pf_read_events — contract card

```json
{
  "tool": "pf_read_events",
  "description_sha256": "c495ca6b4e50e46b49bc56d8e8eeb357784284e54b737ff696a6734c6f6d42ae",
  "input_schema_sha256": "bbd3846e6d7b3bb0b6d94865efbe2e90bb3f49cd3663c47ec5c1bbf0c5497aa4",
  "params": {
    "cursor": {
      "type": "string",
      "required": false
    },
    "limit": {
      "type": "string",
      "required": false
    },
    "pinned_first": {
      "type": "boolean",
      "required": false
    },
    "project": {
      "type": "string",
      "required": false
    },
    "since": {
      "type": "string",
      "required": false
    },
    "types": {
      "type": "array",
      "required": false
    },
    "user_id": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "events",
    "next_cursor"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Eight parameters, none required — though the handler refuses a call carrying
neither `work_item_id` nor `project`.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | no | one work item (or use `project`) |
| `project` | string | no | one project (or use `work_item_id`) |
| `user_id` | string | no | "Filter by ACTOR: matches agent_events.actor_user_id" |
| `types` | array | no | "A FILTER, not a whitelist and not a validator" — a claim emits one `lock_acquired` PER declared path |
| `cursor` | string | no | pass a previous response's `next_cursor` |
| `since` | string | no | RFC3339 |
| `limit` | string | no | max events (server default 50) |
| `pinned_first` | boolean | no | pinned events first |

The description carries a **cutover caveat** on the tool itself rather than only in
the design doc: `lock_acquired` / `lock_released` / `wi_resources_updated` exist only
from the deploy that shipped `aihub#343`, with no backfill, because
`resource_locks` keeps no trace of a deleted row. It says *deploy*, not *commit*,
deliberately — aihub rollouts need an explicit human instruction and can trail a
merge by days, and during that gap a reader holding the commit date would read the
emptiness as "the recorder was running and saw nothing". Cost measured rather than
waved at: +437 chars of wire text on every `tools/list`, ~109 tokens, 0.66% of the
quoted schema text in the tool files.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_events.go` (`registerEventTools`) builds a query string for
`pkg/client/client.go` (`ReadEvents`) → `GET /v1/events`, bound by
`internal/server/routes_memory.go` (`handleListEvents`).

Two parameters here have first-class defect histories:

- **`types` was published and never put on the wire.** Neither end was broken — the
  handler splits on commas and the domain turns it into `event_type IN (...)` — the
  parameter simply never left this process, so a type that cannot exist filtered
  nothing and returned the full stream. That is a false green in BOTH directions: a
  non-empty result reads as "those events exist" when they are some other type, and
  not finding a work item reads as "it was never cancelled" when no filtering
  occurred. One executor came within a step of publishing a "zero cancels" report
  over 44 real cancellations. It is forwarded through `csvArg`, not `strSliceArg`,
  because the wire form is comma-separated and a caller sending the bare scalar form
  of an array-typed param must not be dropped either.
- **`cursor` was neither published NOR forwarded.** The handler has always bound it
  and the endpoint has always returned `next_cursor`, so a caller holding one had no
  way to spend it and the second page of any event stream was unreachable from MCP.

`pinned_first` is forwarded only when true.

## hop 4 — what it actually does

- `work_item_id` **must be the canonical id**. The filter compares against a column
  that FK-references `work_items(id)`, so a slug matches nothing and the call answers
  200 with an empty list — indistinguishable from a work item that genuinely has no
  events. `pf_get_step` echoes the canonical id and is the cheapest way to get one.
- **`types` is a FILTER, not a whitelist**, and `aihub#444` renamed it for that
  reason (`aihub#411` §6.2 T2-5). It validates nothing: an unrecognised value becomes
  `event_type IN ('typo')`, matches no row, and answers 200 with an empty list — so a
  typo, a type that has never existed and an event that genuinely did not happen are
  the same result. Measured over 2,244 transcripts: 48 calls passed `types` carrying
  34 distinct values, and roughly half of them — `wi_cancelled`, `attempt_claimed`,
  `wi_updated`, `wi_claimed`, `attempt_lost_lease`, `wi_wrapped`, `goal_changed`,
  `wi_created`, `wi_note`, `correction`, `attempt_paused`, `wi_rhs_changed` among
  them — name nothing any code path emits. That is `aihub#259`'s failure mode
  surviving its own fix: the parameter now reaches the server and filters correctly
  on a name that cannot exist. The vocabulary is published on
  `pf_emit_event`'s `event_type` rather than repeated here, so the two tools share
  one copy of it on the wire.
- **`user_id` filters the ACTOR** — `internal/domain/memory.go` (`ListEvents`)
  compares `e.actor_user_id`, the id stamped on the event by whoever emitted it. That
  is a FOURTH identity, none of the three §6.2 T2-18 enumerates, which is why the
  description names it rather than picking one of them. It also excludes every event
  written with no actor at all — the GC sweeps, `wi_unblocked`, `attempt_completed` —
  because `actor_user_id = $n` never matches NULL.
- The stream is a **whitelisted semantic record**, not a wire log: the server keeps
  no per-request log, which is why `aihub#412` had to reconstruct the request/response
  chain from transcripts instead.
- `cursor` is **validated at the handler and refused with a 400** naming the
  parameter and quoting the value (`aihub#435`). It used to go raw into
  `e.created_at < $n::timestamptz`, where a token this server never issued failed
  the cast at execute time and came back 500 carrying the driver's text — a
  caller error reported as a server fault, which sends the reader to the logs
  rather than to their own client. An accepted token is forwarded as the
  caller's own string — trimmed, never re-serialised — because the cast stays the
  domain's.

## hop 5 — what comes back

`jsonResult`, no projection, including `next_cursor`. The corpus record above is the
union of top-level keys real callers have been handed.

## Policy

- **§6.2 T2-5 — LANDED** (`aihub#444`). This filter's description no longer calls
  itself a whitelist and states the consequence a caller cannot see: an unmatched
  name returns the same empty list as an event that did not happen. The vocabulary is
  published on `pf_emit_event.event_type` — as an open list rather than an `enum`,
  because an MCP enum is advisory and the server enforces no vocabulary at all.
- **§6.2 T2-18 — LANDED** (`aihub#444`). `user_id` now names the identity it filters:
  the ACTOR (`agent_events.actor_user_id`), explicitly not the reporter, not the
  attempt owner and not a watcher. `pf_list_work_items.user_id` stays REPORTER
  (`aihub#383`); the two now say different things because they do different things.
- **§6.1 T1-6 — LANDED** (`aihub#435`). A caller-supplied `cursor` that will not
  parse is a 400 at the handler, not a 500 that sends the reader to the server
  logs. The check lives in `internal/server/queryparam.go` (`queryCursor`), which
  is now the one reader all three cursor-carrying list endpoints go through —
  this one, `pf_list_work_items` and `pf_recall`. Two readings of one parameter
  name is how the next variant gets in.

## Open

- **`user_id` was passed zero times in the measured corpus** — 0 of 152
  `pf_read_events` calls across 2,244 transcripts (measured 2026-09-08 for
  `aihub#444`, the same sweep that found 34 distinct `types` values). The
  description is now honest about which identity it filters; what that count
  raises and this change did not settle is whether a filter on the emitter earns
  its wire bytes at all. Deciding that needs a reason to keep it, not another
  count of a parameter nobody sends.
