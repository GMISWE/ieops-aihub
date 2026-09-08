# pf_update_memory — contract card

```json
{
  "tool": "pf_update_memory",
  "description_sha256": "e373c1b2dd6fc7d2da8301632578d70f162feaa3cc682aaced84abaffddb0f1a",
  "params": {
    "base_strength": {
      "type": "number",
      "required": false
    },
    "content": {
      "type": "string",
      "required": false
    },
    "memory_id": {
      "type": "string",
      "required": true
    },
    "tags": {
      "type": "array",
      "required": false
    },
    "visibility": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "activation_count",
    "attrs",
    "author_display",
    "author_user_id",
    "base_strength",
    "commits",
    "content",
    "created_at",
    "emb_dims",
    "emb_model",
    "id",
    "is_immortal",
    "last_activated_at",
    "last_activated_by",
    "latest_id",
    "project",
    "rendered_html",
    "stability_days",
    "status",
    "tags",
    "type",
    "updated_at",
    "visibility",
    "work_item_id"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Six parameters, two required. Four of them are "omit to keep current", which is a
distinct semantic from sending a zero value.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "any id in the lineage" |
| `work_item_id` | string | yes | for credential injection |
| `content` | string | no | new content (omit to keep current) |
| `visibility` | string | no | new visibility (omit to keep current) |
| `tags` | array | no | new tags (omit to keep current) |
| `base_strength` | number | no | new base strength (omit to keep current) |

"Any id in the lineage" is load-bearing: a memory is versioned, and updating creates
a **new version** and advances the `latest_id` cursor, so the id a caller holds from
an old recall still resolves.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildUpdateMemoryBody`) sends the three credentials
plus `work_item_id`, then copies each of `updateMemoryPassthroughFields` **only when
the key is present** — absent means "keep current", which is not the same fact as
sending a zero value, and the server binds these as optional fields.

Destination: `PATCH /v1/memories/<id>/update` via `pkg/client/client.go`
(`UpdateMemory`), bound by `internal/server/routes_memory.go` (`handleUpdateMemory`).
`memory_id` is the path segment, not a body field.

## hop 4 — what it actually does

- **Creates a new version rather than mutating in place**, and advances the cursor
  that points at the latest. So an update is append-only from the storage side and
  a reader holding an older id follows the chain forward.
- `base_strength` here writes the same column `pf_remember` initialises, so it
  inherits the same scale question — and this tool publishes no range at all, which
  is the milder version of `pf_remember`'s wrong one.
- `tags` is a **replacement**, like every other field here: the value sent becomes
  the list.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.1 T1-3 / §6.2 T2-19** — the strength scale must be published consistently
  across the tools that write and threshold it. This one publishes no scale, which is
  silent rather than wrong, but a caller reading `pf_remember` first will carry the
  wrong one here.
- **§6.2 T2-1** — one editability matrix for the whole struct and one error code per
  rejection kind; the "omit to keep current" convention is this tool's local version
  of that matrix.

## Open

- Whether `base_strength` should be publishable on an update at all, given that
  activation and reinforcement also move strength, is not settled anywhere this card
  can cite.
