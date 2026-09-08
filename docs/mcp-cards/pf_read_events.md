# pf_read_events — contract card

```json
{
  "tool": "pf_read_events",
  "description_sha256": "c495ca6b4e50e46b49bc56d8e8eeb357784284e54b737ff696a6734c6f6d42ae",
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
| `user_id` | string | no | "Filter by user" |
| `types` | array | no | event-type whitelist; a claim emits one `lock_acquired` PER declared path |
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
- `types` is a whitelist, so an unrecognised value narrows to nothing rather than
  erroring — which is only safe now that the parameter actually reaches the server.
- The stream is a **whitelisted semantic record**, not a wire log: the server keeps
  no per-request log, which is why `aihub#412` had to reconstruct the request/response
  chain from transcripts instead.

## hop 5 — what comes back

`jsonResult`, no projection, including `next_cursor`. The corpus record above is the
union of top-level keys real callers have been handed.

## Policy

- **§6.2 T2-5** — the ruling names this filter's description directly: it is to be
  renamed alongside publishing the event vocabulary as an enum on `pf_emit_event`,
  because a `types` value that matches nothing and a typo look the same.
- **§6.2 T2-18** — state on each `user_id`-shaped parameter which of the three
  identities it filters. **This tool's `user_id` says "Filter by user" and does not.**
  That is the row's live instance here.
- **§6.1 T1-6** — a caller-supplied `cursor` that will not parse should be a 400 at
  the handler, not a 500 that sends the reader to the server logs.

## Open

- **§6.1 T1-6 is filed, not landed** (`aihub#435`): today a malformed `cursor` is not
  guaranteed to come back as a 400.
- **§6.2 T2-18 is unaddressed on this tool.** Which identity `user_id` filters is not
  stated in the schema, and this card does not settle it by asserting one.
