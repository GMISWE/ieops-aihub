# pf_get_memory — contract card

```json
{
  "tool": "pf_get_memory",
  "description_sha256": "91a1d36ed1f79695bbde2f64b6c9146daaee338f4982b4c0fdcd76d781aa23c8",
  "input_schema_sha256": "d8b070884ea6bfca420b507d729d4aa5a2a7ce9e98be09114524090d3360d1aa",
  "params": {
    "memory_id": {
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
    "expires_at",
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

One parameter. This tool is one half of an escape hatch: `pf_recall` truncates
content to 800 runes and flags it with `content_truncated` / `content_full_len`, and
without a by-id read those flags would tell the model its text is incomplete while
giving it no way to complete it.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID (the `id` of a pf_recall item)" |

The parenthetical is the contract: it names where the value comes from, which is the
same reason `pf_update_work_item`'s `resources_version` names `pf_get_work_item`.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`getMemorySchema`) publishes it; the handler rejects
an empty value and calls `pkg/client/client.go` (`GetMemory`) →
`GET /v1/memories/<id>`, bound by `internal/server/routes_memory.go`
(`handleGetMemory`). Path segment, no body, no credentials.

That route did not always exist — a by-id memory read was absent from the router
entirely, which is what made the truncation flags unusable before `aihub#269`.

## hop 4 — what it actually does

Returns one memory with its **full, untruncated** content, subject to the same
visibility scoping as recall. It does not activate the memory: incrementing the
activation count is `pf_activate_memory`'s job, so reading the full text of a
truncated recall item does not disturb its strength.

`memory_id` accepts any id in a lineage in the sense recall returns them; a version
chain's `latest_id` cursor is what `pf_update_memory` advances.

## hop 5 — what comes back

`jsonResultCompact`, not `jsonResult` — compact rather than indented, because this
payload is read by the model and is reached precisely when the content is long. Same
rationale as `pf_recall`'s. No field projection: unlike recall, nothing is dropped
here, which is the point of the tool.

The corpus record above is the union of top-level keys real callers have been handed.

## Policy

- **§6.1 T1-5** — this tool needs no projection at all, which is the cheapest
  possible answer to the keep-list-versus-delete-list question.
- **§6.2 T2-6** — the memory-type vocabulary question does not change this read
  path, but a memory stored under an unembeddable type is reachable **only** through
  this tool plus a text recall, never through a semantic one.

## Open

- Nothing this card can settle.
