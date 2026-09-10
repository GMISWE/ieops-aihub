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
statement; the arithmetic is held against the live registry's own `required` list
by `internal/mcp/memory_published_word_test.go`
(`TestPublishedParamCountSentencesAreTheEnforcedOnes`), which walks every card
stating its own parameter counts rather than this one.

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
`internal/server/routes_memory.go` (`handleRedactMemory`), and
`internal/mcp/memory_wire_shape_test.go` (`TestRedactMemoryBodyCarriesOnlyTheReason`)
asserts that body as an EXACT key set on a request a fake aihub received — a
check for the absence of the three credential keys would stay green the day a
fourth started riding along. `memory_id` is the path segment, asserted on the
real request URL by the same arm and by
`internal/mcp/memory_tools_wire_test.go`
(`TestMemoryToolsForwardEveryPublishedPropertyByValue`).

The absence of attempt credentials is deliberate and is the same shape the
dependency tools carry: authorization is by role, and building unread credentials
into the body would make a reader — including a reviewer — conclude the path is
attempt-gated when it is not. ⚠️ The aihub#325 credential invariant
(`TestMemoryToolsSendCredentialsWithTheirWorkItem`) `continue`s on any tool whose
body carries no `attempt_id`, so it has always SKIPPED this tool — a day when
this renderer started injecting credentials without a work item would have moved
it from skipped to skipped. The exact key set above is the positive control that
gap needed.

## hop 4 — what it actually does

- Marks the memory redacted rather than deleting it, so recall stops returning it
  while the row and its reason remain for audit.
- `admin_redact` is in **all three** of the event-type sets `§6.2 T2-5` is about —
  the 4-entry admin-only set, the 8-entry admin whitelist, and the 22-entry
  null-work-item set that mirrors `chk_evt_work_item_id` — with every one of those
  three sizes compared against `len()` of the declaration it names, and the
  membership in all three required, by
  `internal/mcp/memory_published_word_test.go`
  (`TestPublishedAdminEventSetSizesAreTheEnforcedOnes`).
- ⚠️ **The bullet above used to say the whitelist held 7 entries, and that
  `admin_gc_manual` was "in the first and not the second".** Both numbers came
  from the `aihub#411` audit table, which measured them BEFORE `aihub#444`, and
  that work item closed the gap: `internal/domain/event_types.go`
  (`AdminEventWhitelist`) is now DERIVED as the admin-only set plus the four types
  a non-admin may also emit, so the admin-only set NESTS inside it by
  construction — held by `internal/domain/event_types_test.go`
  (`TestEventTypes_AdminOnlyIsASubsetOfTheAdminWhitelist`) — and the flag
  inversion it described, where an admin declaring `admin: true` was
  refused where the same admin omitting it succeeded, no longer exists.
- The live pair that still overlaps without nesting is the admin whitelist
  against the null-work-item set: `attempt_superseded` is in the whitelist and
  absent from the null-work-item set, while `memory_redacted` is in that set and
  absent from the whitelist, and
  `internal/mcp/memory_published_word_test.go`
  (`TestPublishedAdminEventSetSizesAreTheEnforcedOnes`) requires BOTH of those
  directions to stay live — which is what keeps `admin_redact`'s membership in
  all three a measured comparison rather than a restatement of whichever
  difference happens to exist.
- There is no un-redact tool. Reversal is not part of this surface.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.2 T2-5** — three overlapping but different event-type sets, and this
  operation's event sits in the awkward part of the overlap. The ruling reads
  "publish the event vocabulary as an enum on `pf_emit_event`" and LANDED there
  (`aihub#444`) with one deliberate deviation the `pf_emit_event` card records:
  the vocabulary is published in the `event_type` DESCRIPTION and the `enum` key
  is not used, because `agent_events.event_type` is `TEXT` with no CHECK and an
  MCP enum is advisory, so publishing one would state a closed set nothing keeps
  — both halves held by `internal/mcp/tools_events_vocab_test.go`
  (`TestEmitEventTypeIsNotPublishedAsAClosedEnum` and
  `TestEmitEventTypeDescriptionPublishesTheVocabulary`).
- **§6.1 T1-9** — "soft-delete" is stated rather than implied, which is the standard
  the rule sets for prose that a caller would otherwise have to measure.

## Open

- Nothing this card can settle. Whether redaction should be reversible from MCP is
  not a question any adjudicated row covers.
