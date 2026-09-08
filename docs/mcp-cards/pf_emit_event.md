# pf_emit_event — contract card

```json
{
  "tool": "pf_emit_event",
  "description_sha256": "4555d2a5b9c4ae07cebd69b13ffc2ed9d5a1b3841983b7debcb6a1d58be9b7a7",
  "params": {
    "admin": {
      "type": "boolean",
      "required": false
    },
    "event_type": {
      "type": "string",
      "required": true
    },
    "payload": {
      "type": "object",
      "required": true
    },
    "pinned": {
      "type": "boolean",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "event_id"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Five parameters, and one of them has **no published vocabulary at all** — which is
the finding this card exists to carry.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item |
| `event_type` | string | yes | "Event type (e.g. note, wi_reclassified, step_started)" |
| `payload` | object | yes | arbitrary JSON object |
| `pinned` | boolean | no | surfaces first in status/resume |
| `admin` | boolean | no | requires role=admin |

`event_type` is published as a free string with three examples. There is no enum
here and no CHECK behind it: `agent_events.event_type` is `TEXT NOT NULL` with no
constraint, and the domain function validates only payload size (64 KB), an
admin-only list, and — when `admin:true` — an admin whitelist. **Every other string
is accepted.**

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_events.go` (`registerEventTools`) resolves the state file,
injects `attempt_id` / `claim_epoch` / `session_secret`, and calls
`pkg/client/client.go` (`EmitEvent`) → `POST /v1/events`, bound by
`internal/server/routes_memory.go` (`handleEmitEvent`).

`pinned` and `admin` are forwarded **only when true**, so "explicitly false" and
"unset" are the same on the wire. `payload` is forwarded as given.

Three other tools reach this same endpoint without publishing `event_type`:
`pf_adopt_artifact`, `pf_close_artifact` and `pf_ignore_artifact` all send
`event_type: "artifact_action"` from `internal/mcp/tools_memory.go`
(`buildArtifactActionBody`). The coding tools emit `commit` / `push` / `pr_opened`
here too, best-effort, via `internal/mcp/tools_coding.go` (`emitCodingEvent`).

## hop 4 — what it actually does

- The event is appended to the work item's timeline and is the **only durable record**
  of several things: a wrap that actually delivered something, a lock release with
  its cause, a note whose credentials are about to be deleted.
- **Three overlapping but different sets govern `event_type`, and membership in one
  does not imply membership in another**: `internal/domain/memory.go`
  (`adminOnlyEventTypes`), 4 entries, which require admin role whatever `admin` says;
  `internal/domain/memory.go` (`adminEventWhitelist`), 7 entries, which gates
  `admin: true`; and a 21-entry CHECK in
  `internal/db/migrations/0026_memories_latest_id.sql` controlling which event types
  may omit `work_item_id`. `admin_gc_manual` is in the first and **not** the second, so
  an admin may emit it but may not set `admin: true` on it — that combination answers
  403 "not in the admin whitelist" (`internal/domain/memory.go`, `EmitEvent`).
- The only thing that looks like a vocabulary — that 21-entry CHECK — is **not** one:
  it says which events may be filed without a work item, not which events exist.
- Free text plus three partial whitelists means **a typo is indistinguishable from a
  new kind of event**, and `pf_read_events`' `types` filter will silently match
  nothing for it.

## hop 5 — what comes back

`jsonResult`, no projection; the corpus record above spans 437 calls at a 7.32%
error rate. `event_id` is what a caller keeps.

## Policy

- **§6.2 T2-5** — the ruling is to **publish the event vocabulary as an enum on this
  tool** and rename the `types` filter's description. Free text plus three partial
  whitelists is the state described above.
- **§6.2 T2-18** — every `user_id`-shaped parameter must say which of the three
  identities it filters. This tool publishes none, but the events it writes are what
  `pf_read_events`' `user_id` filters on.

## Open

- **§6.4 item 5** — **T2-5 has no census.** How many distinct `event_type` strings
  are actually in flight needs a DB read this line could not make, so the cost of
  closing the vocabulary is unknown. Until that is measured, the enum's membership
  cannot be chosen without risking rejection of live traffic.
