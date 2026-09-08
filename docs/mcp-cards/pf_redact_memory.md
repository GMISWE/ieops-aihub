# pf_redact_memory — contract card

```json
{
  "tool": "pf_redact_memory",
  "description_sha256": "9ddb21e3ef1f242a297befdcfc964838a6119b5a6458cf6fc26204234776e34f",
  "input_schema_sha256": "b122f01fa303e488002154f845c0704fdfd911af073ff93f28daf130c52a4656",
  "params": {
    "memory_id": {
      "type": "string",
      "required": true
    },
    "reason": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "ok"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters, **both required** — and `reason` being required is the design
statement.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID" |
| `reason` | string | yes | "Reason for redaction" |

"Redact (soft-delete) a memory" is the whole description. Soft is the operative
word: the row survives, which is what makes the reason worth storing.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildRedactMemoryBody`) sends exactly `{reason}` —
no credentials at all — to `PATCH /v1/memories/<id>/redact` via
`pkg/client/client.go` (`RedactMemory`), bound by
`internal/server/routes_memory.go` (`handleRedactMemory`). `memory_id` is the path
segment.

The absence of attempt credentials is deliberate and is the same shape the
dependency tools carry: authorization is by role, and building unread credentials
into the body would make a reader — including a reviewer — conclude the path is
attempt-gated when it is not.

## hop 4 — what it actually does

- Marks the memory redacted rather than deleting it, so recall stops returning it
  while the row and its reason remain for audit.
- `admin_redact` is in **both** the 4-entry admin-only set and the 7-entry admin
  whitelist. That is what makes it the useful comparison for `§6.2 T2-5`: the sets
  overlap without nesting, and `admin_gc_manual` is in the first and not the second,
  so membership in one does not imply membership in another.
- There is no un-redact tool. Reversal is not part of this surface.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.2 T2-5** — three overlapping but different event-type sets, and this
  operation's event sits in the awkward part of the overlap. The ruling is to publish
  the vocabulary as an enum on `pf_emit_event`.
- **§6.1 T1-9** — "soft-delete" is stated rather than implied, which is the standard
  the rule sets for prose that a caller would otherwise have to measure.

## Open

- Nothing this card can settle. Whether redaction should be reversible from MCP is
  not a question any adjudicated row covers.
