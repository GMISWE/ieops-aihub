# pf_emit_event — contract card

```json
{
  "tool": "pf_emit_event",
  "description_sha256": "4555d2a5b9c4ae07cebd69b13ffc2ed9d5a1b3841983b7debcb6a1d58be9b7a7",
  "input_schema_sha256": "b8f37a89868feca9cf0bc48888c29b15e81b39377cde3c3df11bcbc5fb126754",
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

Five parameters, and `event_type` is the one this card exists to carry: it had no
published vocabulary at all until `aihub#444` put one on the wire; that count and
the three set sizes in hop 4 are held against the live schema and the domain sets by
`internal/mcp/emit_event_published_counts_test.go`
(`TestPublishedEventTypeCountsAreTheEnforcedOnes`).

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item |
| `event_type` | string | yes | "NOT a closed set" — 45 published names, plus the three rules that really are enforced |
| `payload` | object | yes | arbitrary JSON object; a non-object is a 400, and the 64 KB cap is checked first |
| `pinned` | boolean | no | surfaces first in status/resume |
| `admin` | boolean | no | requires role=admin |

`event_type` is still a free string and there is still no CHECK behind it —
`agent_events.event_type` is `TEXT NOT NULL` with no constraint, and **every other
string is accepted**: `internal/domain/event_type_free_string_test.go`
(`TestEventTypeCarriesNoVocabularyCheckAtHead`) replays every migration and holds
that the one CHECK naming this column is disjunctive on `work_item_id IS NOT NULL`,
`internal/mcp/emit_event_wire_shape_test.go`
(`TestEmitEventTypeIsNotRefusedInProcess`) holds the client half, and
`internal/domain/event_types_db_test.go` (`TestEmitEventNullWorkItem_IsA400NotA500`,
`TestMigration0036_AdmitsAdminGcManualWithNoWorkItem`) drives both against a real
column. What changed is that the description now says so, names the
45 types this tree can produce, and states each rule that IS enforced with the
status code it answers: an admin-only set (403), an `admin:true` whitelist (403)
and the `work_item_id`-optional set (400) — that description is asserted by
`internal/mcp/tools_events_vocab_test.go`
(`TestEmitEventTypeDescriptionPublishesTheVocabulary`) to name every
`EventVocabulary` member, every admin-only and admin-whitelisted type, and the
literal strings "NOT a closed set", "no CHECK", "403" and "400", and the count of 45 is bound
to `EventVocabulary` itself by
`internal/mcp/emit_event_published_counts_test.go`. It is deliberately NOT published
under the JSON Schema `enum` key, which `internal/mcp/tools_events_vocab_test.go`
(`TestEmitEventTypeIsNotPublishedAsAClosedEnum`) refuses in that exact form — see
hop 4.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_events.go` (`registerEventTools`) resolves the state file,
injects `attempt_id` / `claim_epoch` / `session_secret`, and calls
`pkg/client/client.go` (`EmitEvent`) → `POST /v1/events`, bound by
`internal/server/routes_memory.go` (`handleEmitEvent`).

`pinned` and `admin` are forwarded **only when true**, so "explicitly false" and
"unset" are the same on the wire — three arms of
`internal/mcp/emit_event_wire_shape_test.go`
(`TestEmitEventForwardsPinnedAndAdminOnlyWhenTrue`) read a request a fake aihub
received for each case, and the two absences are compared as an exact key set.
`payload` is forwarded as given, byte for byte
(`TestEmitEventForwardsThePayloadAsGiven`, same file, over a fixture carrying a
nested object, an array, a fraction, an empty string and a null).

Three other tools used to reach this same endpoint without publishing
`event_type` — `pf_adopt_artifact`, `pf_close_artifact` and `pf_ignore_artifact`
each sent `event_type: "artifact_action"` from `internal/mcp/tools_memory.go`.
`aihub#446` retired all three (`aihub#411` T2-7: nothing in the tree read the
event), so `artifact_action` now has **no publisher of its own** — a caller that
wants one sends it through this tool, which accepts the string like any other, and
`internal/domain/event_type_free_string_test.go`
(`TestArtifactActionHasNoPublisherLeft`) holds both halves of that: the name is still
in `EventVocabulary`, and the only Go string literal declaring it is the vocabulary
entry itself.
The coding tools still emit `commit` / `push` / `pr_opened` here, best-effort,
via `internal/mcp/tools_coding.go` (`emitCodingEvent`).

## hop 4 — what it actually does

- The event is appended to the work item's timeline and is the **only durable record**
  of several things: a wrap that actually delivered something, a lock release with
  its cause, a note whose credentials are about to be deleted.
- **`payload` must be a JSON object, and the 400 says so in `payload`'s own terms**
  (`aihub#465`). It is published as an object and bound to a bare
  `json.RawMessage`, so a JSON-encoded STRING of an object used to be inserted
  verbatim under a 200 and the column came back holding a string — measured twice
  live. The rejection names the type and the byte length, and reports through
  `details.string_decodes_to` whether the quoted text was itself valid JSON —
  asserted field by field, for this parameter among the four, by
  `internal/domain/json_object_params_test.go`
  (`TestStringifiedObjectParamIsRejected`). 18 of
  19 real cases are hand-escaped JSON that came out malformed rather than a client
  wrapping a good object, so that branch carries the parse error and its own
  repair instruction. Nothing is coerced — decoding the string would rescue 1 of
  the 19 and guess at the other 18.
- **This is the one guarded field where size CAN be the reason.** The 64 KB cap is
  checked first, so the shape rejection's closing sentence says the cap was
  checked and passed rather than repeating `attrs_patch`'s "no length cap", which
  is false here — `internal/domain/json_object_params_test.go`
  (`TestSizeNoteMatchesTheCapThatActuallyExists`) rejects a 70 KB payload and requires
  this field's note to differ from the other three's.
- **Three sets govern `event_type` and they answer three different questions**, which
  is why `aihub#444` made them consistent rather than equal; one arm each drives the
  refusal its set produces, in `internal/domain/event_types_db_test.go`
  (`TestEmitEventAdminFlag_DeclaringItIsNeverWorseThanOmittingIt`,
  `TestEmitEventAdminFlag_TheWhitelistStillRefusesSomething`,
  `TestEmitEventNullWorkItem_IsA400NotA500`). They now live together in
  `internal/domain/event_types.go`: `AdminOnlyEventTypes` (4) requires admin role
  whatever `admin` says; `AdminEventWhitelist` (8) gates `admin: true` and is now
  DERIVED as that set plus four types a non-admin may also emit; and
  `NullWorkItemEventTypes` (22) mirrors the CHECK in
  `internal/db/migrations/0036_agent_events_admin_gc_manual.sql` controlling which
  types may omit `work_item_id` — the containment is held by
  `internal/domain/event_types_test.go`
  (`TestEventTypes_AdminOnlyIsASubsetOfTheAdminWhitelist`), the mirror by
  `TestEventTypes_NullWorkItemMirrorsTheMigration`, and all three stated sizes by
  `internal/mcp/emit_event_published_counts_test.go`.
- **The flag used to invert, and that was the live defect.** `admin_gc_manual` was in
  the admin-only set and not in the whitelist, so an admin sending it with
  `admin: true` got 403 "not in the admin whitelist" while the SAME admin omitting
  the flag succeeded — measured both ways before and after the fix. Deriving the
  whitelist from the admin-only set makes the containment structural: declaring an
  admin event can no longer be stricter than not declaring it.
- **Omitting `work_item_id` for a type the CHECK does not permit is now a 400 rather
  than a 500.** That CHECK was enforced in the database only, so the request ran to
  the INSERT and came back as the driver's `SQLSTATE 23514` text wrapped in
  `INTERNAL_ERROR` — measured; `internal/domain/event_types_db_test.go`
  (`TestEmitEventNullWorkItem_IsA400NotA500`) asserts the STATUS over three accepted
  and three refused types, because a 500 and a 400 can carry the same words.
  `internal/domain/memory.go` (`EmitEvent`) now refuses
  first and names the constraint and its list (`aihub#411` §6.1 T1-4), and
  `TestEmitEventNullWorkItem_IsA400NotA500` requires that message to carry
  `chk_evt_work_item_id` and every name the Go mirror admits, built from
  `NullWorkItemEventTypes` so the two move together.
- The 22-entry CHECK is still **not** a vocabulary: it says which events may be filed
  without a work item, not which events exist. The vocabulary is
  `internal/domain/event_types.go` (`EventVocabulary`), and it is published here —
  `internal/mcp/tools_events_vocab_test.go`
  (`TestEmitEventTypeDescriptionPublishesTheVocabulary`) requires every member on the
  wire and `TestEmitEventTypeDescriptionIsDerivedNotRetyped` requires the description
  to be BUILT from the list rather than retyped beside it.
- **The enum was not used, on purpose.** An MCP enum is advisory — the untyped
  `AddTool` path validates nothing, which `internal/mcp/create_user_vocab_test.go`
  (`TestCreateUserVocabEnumDoesNotRefuseInProcess`) measures on `pf_create_user`, one
  of the SIX tools whose published schema carries an `enum` key at all — measured
  2026-09-10 over the 45 cards' generated machine blocks, which K3 pins against the
  live schema: `pf_create_user`, `pf_create_work_item`, `pf_list_work_items`,
  `pf_recall`, `pf_update_user` and `pf_update_work_item` — and the server accepts any
  string, which
  `internal/mcp/emit_event_wire_shape_test.go`
  (`TestEmitEventTypeIsNotRefusedInProcess`) measures here — so publishing
  45 names under `enum` would state a closed contract nothing keeps. `aihub#445`
  withdrew exactly such an enum from `pf_remember` in the commit this change is
  based on.
  <!-- prose-only: because=history -->
  The ruling's
  substance (publish the vocabulary) is delivered; the one word that would make it
  false is not.

## hop 5 — what comes back

`jsonResult`, no projection; the corpus record above spans 437 calls at a 7.32%
error rate. `event_id` is what a caller keeps, and it is the only top-level key the
`aihub#412` corpus ever saw this tool answer with — the machine block's copy of that
record is held to it by `internal/mcp/contract_cards_gate_test.go`
(`TestContractCardsMatchTheCorpusResponseKeys`).

## Policy

- **§6.2 T2-5 — LANDED** (`aihub#444`), with one deliberate deviation. The ruling
  reads "publish the event vocabulary as an enum on this tool"; the vocabulary is
  published and the `enum` key is not used, because the CAUTION attached to the same
  ruling says an MCP enum is advisory and the published set and the enforced set must
  be one set (`aihub#238`, and the inverse of `aihub#463`) — both halves of that
  deviation are pinned by `internal/mcp/tools_events_vocab_test.go`
  (`TestEmitEventTypeIsNotPublishedAsAClosedEnum` and
  `TestEmitEventTypeDescriptionPublishesTheVocabulary`). The flag inversion and the
  500-instead-of-400 were fixed alongside it, and `pf_read_events`' `types` filter was
  renamed in the same change.
- **§6.2 T2-18** — every `user_id`-shaped parameter must say which of the three
  identities it filters. This tool publishes none, but the events it writes are what
  `pf_read_events`' `user_id` filters on — and that parameter now says ACTOR, a fourth
  identity none of the three names, which `internal/mcp/tools_events_vocab_test.go`
  (`TestReadEventsUserIDNamesTheIdentityItFilters`) requires of its live description.

## Open

- **§6.4 item 5 — the census is HALF taken** (measured 2026-09-08 for `aihub#444`,
  recorded in `internal/domain/event_types.go`).
  <!-- prose-only: because=measurement -->
  Across 2,244 transcripts, 457
  `pf_emit_event` calls used exactly TWO distinct `event_type` strings — `note`
  (455) and `pr_opened` (2) — none sent `admin: true`, none omitted `work_item_id`.
  What that bounds is MCP callers. What it does not bound is direct HTTP callers of
  `POST /v1/events` and the distinct set already in `agent_events`, both of which
  need a read of the production database (inside the compose network, per
  `docs/deployment.md`).
  <!-- probe-waiver: kind=structurally-unreachable | decided=2026-09-10 |
  citation=aihub#543 spec §4.2 for the kind's definition, and docs/deployment.md,
  which puts the production database inside the compose network |
  reason=its one unprobed claim is that these two populations need a production
  database read before they can be bounded. What is missing is that database. The
  harness creates an empty schema per run, so the distinct set already in
  agent_events is by construction the set the run just inserted, and a direct HTTP
  caller of an aihub nobody in the harness deployed cannot be counted at all. It
  closes the day a read-only production replica is reachable from CI, the way the
  six git tools' K10 gap closed once a real gh was available. The half that IS
  assertable — that the column takes any string, so closing the vocabulary would
  break whatever those callers send — is held by
  internal/domain/event_type_free_string_test.go's
  TestEventTypeCarriesNoVocabularyCheckAtHead. -->
  So the blast radius of CLOSING the vocabulary is known for
  one caller class and unknown for the other, and the vocabulary stays open.
