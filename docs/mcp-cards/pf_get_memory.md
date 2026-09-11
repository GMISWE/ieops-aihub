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
giving it no way to complete it — the published 800 bound to the constant that
enforces it by `internal/server/recall_snippet_limit_test.go`
(`TestPublishedRecallSnippetLimitIsTheEnforcedOne`), which also requires the
truncating branch to set both flags, since a shortened body carrying neither is
`aihub#269` itself.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID (the `id` of a pf_recall item)" |

The parenthetical is the contract: it names where the value comes from, which is the
same reason `pf_update_work_item`'s `resources_version` names `pf_get_work_item` —
both pairs read off the live schemas by
`internal/mcp/get_memory_card_claims_test.go`
(`TestGetMemoryIdDescriptionNamesWhereTheValueComesFrom`), which quantifies over the
pair so the convention cannot survive in one description alone.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`getMemorySchema`) publishes it; the handler rejects
an empty value and calls `pkg/client/client.go` (`GetMemory`) →
`GET /v1/memories/<id>`, bound by `internal/server/routes_memory.go`
(`handleGetMemory`) — with the refusal and the route driven through the real tool by
`internal/mcp/get_memory_card_claims_test.go`
(`TestGetMemoryRefusesAnEmptyIdBeforeAnyRequest`), which requires the empty value to
be refused with zero requests made, because forwarding it spends a round trip per
truncated recall item on a call that cannot succeed. Path segment, no body, no
credentials.

That route did not always exist — a by-id memory read was absent from the router
entirely, which is what made the truncation flags unusable before `aihub#269`.
<!-- prose-only: because=history -->

## hop 4 — what it actually does

Returns one memory with its **full, untruncated** content, subject to the same
visibility scoping as recall. It does not activate the memory: incrementing the
activation count is `pf_activate_memory`'s job, so reading the full text of a
truncated recall item leaves its strength alone — driven on a request recorder by
`internal/mcp/get_memory_card_claims_test.go`
(`TestGetMemoryMakesNoActivationRequestWhileActivateDoes`), which drives the sibling
tool alongside it so the absence is a decision about this tool rather than a build
with no activation path at all.

`memory_id` accepts any id in a lineage in the sense recall returns them; a version
chain's `latest_id` cursor is what `pf_update_memory` advances, with the read half —
no version predicate on the by-id query, and `latest_id` inside its SELECT list —
held by `internal/domain/get_memory_card_claims_test.go`
(`TestGetMemoryByIDTakesAnyLineageMemberAndCarriesTheHeadCursor`) and the advancing
half held against a database by `internal/domain/memory_latest_test.go`
(`TestUpdateMemory` and `TestSupersedeAdvancesCursor`).

## hop 5 — what comes back

`jsonResult` marshals with no indentation, because this payload is read by the
model and is reached precisely when the content is long, and it is held
indentation-free by `internal/mcp/get_memory_card_claims_test.go`
(`TestGetMemoryAndItsIndentingSiblingAreBothCompact`), which drives this tool
together with a sibling on the same helper and refuses a newline in either.
Same rationale as `pf_recall`'s.

⚠️ **This line used to claim this tool answered through a second helper,
and the server no longer has one.** The history in two steps: `34df071` changed
`marshalJSON` from `json.MarshalIndent` to `json.Marshal` for every tool in the
server, which made `jsonResult` and the then-separate `jsonResultCompact`
byte-identical — a difference this card asserted until aihub#592 measured it
false — and aihub#598 then folded the redundant helper away entirely. There is
now exactly ONE JSON result serializer, held as a census by
`internal/mcp/json_result_identity_test.go`
(`TestJSONResultIsTheOnlyJSONResultSerializer`): any second function that both
marshals JSON and builds a tool result is red, whatever its name and even if
its output is byte-identical, because a second marshal point is two sites of
truth about one output shape. The behaviour was never the problem; the sentence
was, because it told a reader this tool differed from its siblings in a way it
does not and pointed the next token-saving change at a conversion that has
already landed everywhere.

No field projection: unlike recall, nothing is dropped here, which is the point of
the tool.

The corpus record above is the union of top-level keys real callers have been handed.

## Policy

- **§6.1 T1-5** — this tool needs no projection at all, which is the cheapest
  possible answer to the keep-list-versus-delete-list question.
- **§6.2 T2-6** — the memory-type vocabulary question does not change this read
  path, but a memory stored under an unembeddable type is reachable **only** through
  this tool plus a text recall, never through a semantic one.

## Open

- Nothing this card can settle.
