# pf_resolve_commit — contract card

```json
{
  "tool": "pf_resolve_commit",
  "description_sha256": "871900ea3d9a6c052811c628cf28306450bde29f301c24b9d39961997c56582b",
  "input_schema_sha256": "d2c60621b3317daed0a4464b8140087acfb6e6e0062fcd2c05529da13a867783",
  "params": {
    "commit_id": {
      "type": "string",
      "required": true
    },
    "memory_id": {
      "type": "string",
      "required": true
    },
    "reply": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three parameters, all required.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID" — the artifact carrying the annotation |
| `commit_id` | string | yes | "Commit annotation ID" |
| `reply` | string | yes | what was changed, or why the annotation is resolved |

"Commit" here means a **review annotation on a spec/plan artifact** rather than a git
commit. That collision is worth stating on the card because `pf_commit` is a
different tool in a different file that does mean git — both tools published, their
name literals in disjoint files and `pf_commit`'s description still describing a git
commit, per `internal/mcp/resolve_commit_contract_test.go`
(`TestResolveCommitAndCommitAreDifferentPublishedTools`).

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildResolveCommitBody`) sends exactly `{reply}` to
`POST /v1/memories/<id>/commit/<commit_id>/resolve` via
`pkg/client/client.go` (`ResolveCommit`), bound by
`internal/server/routes_memory.go` (`handleResolveCommit`) — one request, that path
with the two ids in that order, and a one-key body, all read off a request the tool
really made in `internal/mcp/resolve_commit_contract_test.go`
(`TestResolveCommitSendsOnlyTheReplyToTheResolveRoute`), which also requires the body
to carry no attempt credential.

**Two of the three parameters are path segments**, not body fields, which is why the
body is a single key. No attempt credentials are sent.

There is a sibling route, `.../commit/<commit_id>/reply`, bound by
`internal/server/routes_memory.go` (`handleV1ReplyCommit`), that this tool leaves
alone: replying and resolving are different operations and only the second is
published here — and `internal/mcp/resolve_commit_contract_test.go`
(`TestResolveCommitCannotReachTheReplySibling`) holds all three parts of that — the
route is registered, `pkg/client` declares no method issuing a `/reply` path at all,
and the one request this tool makes is the `/resolve` one.

## hop 4 — what it actually does

- Marks the annotation `status=resolved` and emits a `memory_commit_resolved` event,
  so the resolution is on the timeline rather than only in the artifact; both tokens
  are read out of the live tool description and held to what the domain really writes
  — the status to `resolveCommitSQL` and the event type to the `agent_events` insert
  inside `ResolveCommit` — by `internal/mcp/resolve_commit_contract_test.go`
  (`TestPublishedResolveCommitEffectsAreTheEnforcedOnes`), and the SQL's own jsonb
  paths by `internal/domain/memory_commit_test.go` (`TestResolveCommitSQLKeys`).
- The annotations themselves live on the memory's commit list and are what the
  artifact viewer renders; **three** entry points write that state and all of them
  go through `internal/domain/memory.go` (`ResolveCommit`) — this tool's `/v1`
  handler, plus the web UI's own `handleUIArtifactResolveCommit`
  (`internal/server/routes_artifacts.go`) and `handleUIResolveCommit`
  (`internal/server/ui_handlers_memory.go`), the last two through the
  `doResolveCommitFn` seam — a set held to exactly those three, in both
  directions, by `internal/mcp/resolve_commit_contract_test.go`
  (`TestEveryResolveCommitEntryPointGoesThroughTheOneDomainFunction`); this card used
  to say there was one UI writer, and the census found two on 2026-09-10.
- `reply` is stored rather than merely acknowledged, which is why it is required
  rather than optional: required in the published schema and refused by the tool
  before any request leaves, per `internal/mcp/resolve_commit_contract_test.go`
  (`TestResolveCommitReplyIsRequiredAtEveryHopAndStored`), and written to the
  `'{reply}'` jsonb path per `internal/domain/memory_commit_test.go`
  (`TestResolveCommitSQLKeys`).

## hop 5 — what comes back

`jsonResult`, no projection. **`response_keys_observed` is `null`** — the
`aihub#412` corpus holds no record for this tool, so nothing here is measured
traffic; K7 in `internal/mcp/contract_cards_gate_test.go`
(`TestContractCardsMatchTheCorpusResponseKeys`) refuses a `null` that masks a record,
and `internal/mcp/diff_result_shape_test.go`
(`TestNullResponseKeysMeansNoCorpusRecordAtAll`) holds the absence of the record
itself. That is a statement about usage, not about correctness.

The key is pinned anyway, just not here: `aihub#482`'s K10 in
`internal/mcp/card_response_keys_live_e2e_db_test.go` drives this tool against a
live server — seeding the annotation row directly, since annotations are made in
the `/ui` viewer and no MCP tool reaches it — and holds the result to `ok`,
declared in `docs/mcp-cards/live-response-keys.json`.

## Policy

- **§6.2 T2-7** — the "does anything read this?" question the ruling asks about
  adopt/close/ignore applies to the annotation flow too, and the ruling's own caveat
  names that flow explicitly as the thing that was not checked.
- **§6.2 T2-5 — LANDED (`aihub#444`).** `memory_commit_resolved` is a free-text event
  type in the sense that nothing enforces it — `agent_events.event_type` has no CHECK
  and no published tool declares an `enum` containing this value, `pf_emit_event`'s
  `event_type` declaring no `enum` at all because an MCP enum is advisory — while the
  vocabulary IS published, in that parameter's DESCRIPTION, built from
  `domain.EventVocabulary` rather than retyped and carrying this event type, all three
  halves censused against a control requiring the published set to contain some real
  enum somewhere by `internal/mcp/resolve_commit_contract_test.go`
  (`TestResolveCommitEventTypeIsPublishedInProseAndNotAsAnEnum`), with the general
  form of the same statement held by `internal/mcp/tools_events_vocab_test.go`
  (`TestEmitEventTypeIsNotPublishedAsAClosedEnum` and
  `TestEmitEventTypeDescriptionPublishesTheVocabulary`).
  This bullet read "another free-text event type with no published vocabulary" until
  2026-09-10 (`aihub#543`), which had been false in its second half since `aihub#444`
  landed the publication — the same correction `pf_pr` and `pf_redact_memory` already
  carry.
  <!-- prose-only: because=history -->

## Open

- **§6.4 item 3** — whether the UI flow and this tool are two writers of one state
  that can disagree is exactly what T2-7's caveat says was not checked. This card
  records the second writer rather than asserting they agree.
