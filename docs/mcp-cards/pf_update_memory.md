# pf_update_memory — contract card

```json
{
  "tool": "pf_update_memory",
  "description_sha256": "e373c1b2dd6fc7d2da8301632578d70f162feaa3cc682aaced84abaffddb0f1a",
  "input_schema_sha256": "945ba1d2776c5da93d813ec1c338f47f4fb161ed2700a9558907623044549151",
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
| `base_strength` | number | no | new base strength, **integer** 1-5 (omit to keep current); a fractional value is a 400 |

"Any id in the lineage" is load-bearing: a memory is versioned, and updating creates
a **new version** and advances the `latest_id` cursor, so the id a caller holds from
an old recall still resolves — driven against a real database by
`internal/domain/memory_latest_test.go` (`TestUpdateMemory`), which updates from
v1's id twice and requires v1's own id to keep resolving to the newest head, with
the resolver itself held by (`TestGetLatestByID`).

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildUpdateMemoryBody`) sends the three credentials
plus `work_item_id`, then copies each of `updateMemoryPassthroughFields` **only when
the key is present** — absent means "keep current", which is not the same fact as
sending a zero value, and the server binds them as optional fields (`*string` /
`[]string` / `*float64`) so an absent key inherits the lineage head's value; both
directions are measured on the wire by `internal/mcp/memory_wire_shape_test.go`,
which asserts the base body as an exact key set
(`TestReinforceAndUpdateSendCredentialsAndTheWorkItemAndNoMemoryId`) and drives
every optional field set AND omitted
(`TestMemoryToolsForwardAnOptionalParamOnlyWhenItCarriesAValue`), with the
inheritance the absence buys driven at the layer that decides it by
`internal/domain/memory_latest_test.go` (`TestUpdateMemory`) — ⚠️ the omitted
direction having been invisible to every arm until this slice, because the
`aihub#325` wire gate only ever sends values it picked itself, so a `content: ""`
reaching the body and overwriting a memory's text with nothing went unobserved.

Destination: `PATCH /v1/memories/<id>/update` via `pkg/client/client.go`
(`UpdateMemory`), bound by `internal/server/routes_memory.go` (`handleUpdateMemory`).
`memory_id` is the path segment rather than a body field, which the exact key set
above and `internal/mcp/memory_tools_wire_test.go`
(`TestMemoryToolsForwardEveryPublishedPropertyByValue`) assert from the two
directions absence needs: the value is required to arrive in the URL, and the body
is required to hold exactly the four keys that are not it.

**An unpublished argument dies at the boundary before the builder sees it
(`aihub#586`, owner ruling 2026-09-10).** The passthrough list was always a
whitelist, so a caller-invented key never reached the PATCH body — a property of
this builder, not a guarantee. This tool is in the memory-write family whose
unknown arguments `internal/mcp/server.go` (`addTool`) now strips before any
handler runs and names in the response's `request_adjusted` under `unknown_params`
— driven with a bogus key plus a caller-spelled `rendered_html`, the name whose
wholesale forwarding on `pf_remember` was `aihub#586`'s measured finding, by
`internal/mcp/wire_strip_family_test.go`
(`TestWireStrippedFamilyDropsUnpublishedKeysAndDisclosesThem`), with the roster
pinned by `internal/mcp/wire_strip_test.go`
(`TestWireStrippedToolsAreExactlyTheMemoryWriteFamily`).

## hop 4 — what it actually does

- **Creates a new version rather than mutating in place**, and advances the cursor
  that points at the latest. So an update is append-only from the storage side and
  a reader holding an older id follows the chain forward.
- `base_strength` here writes the same column `pf_remember` initialises, and since
  `aihub#433` it is validated by the same guard: `internal/domain/memory.go`
  (`UpdateMemory`) builds a `RememberRequest` and calls `internal/domain/memory.go`
  (`Remember`), whose `internal/domain/memory.go` (`validateBaseStrength`) sits above
  the first query — the reach is a code property, held by
  `internal/domain/memory_base_strength_range_test.go`
  (`TestUpdateMemoryInheritsTheStrengthGuardRatherThanRestatingIt`), and the guard
  running before any query is held by (`TestRememberRejectsOutOfRangeBaseStrengthBeforeThePool`),
  which drives `Remember` with a nil pool so a refusal cannot have touched the
  database. So an out-of-range value is a 400 here too, and this tool's own
  description publishes that range as well — `aihub#433` changed all three strength
  surfaces in one commit, and `internal/mcp/tools_memory_test.go`
  (`TestPublishedBaseStrengthRangeIsTheEnforcedOne`) iterates `pf_remember` and
  `pf_update_memory` alike, so a description here that went silent about the range
  would turn that gate red.
- **Since `aihub#459` (owner ruling 2026-09-09) the value must also be a WHOLE
  NUMBER, and it is inherited here rather than decided here.** The same guard gained
  `internal/domain/memory.go` (`ValidateIntegralStrength`) after its range check, so
  `2.5` is a 400 on this tool without a line of this tool's own code changing —
  which is the point of the guard sitting in `Remember` rather than in either
  handler, and both halves are checked:
  `internal/domain/memory_base_strength_range_test.go`
  (`TestUpdateMemoryInheritsTheStrengthGuardRatherThanRestatingIt`) requires this
  path to reach `Remember` and to carry NO strength check of its own, while
  `internal/domain/memory_latest_test.go` (`TestUpdateMemory`) drives `2.5` down
  this path against a real database and requires a 400 naming the value, with a
  whole in-range value accepted as the control — 🔴 DB-gated rather than free
  because the nil-pool technique the range arms use cannot reach this claim:
  `UpdateMemory` reads the lineage head before it builds anything, so a nil pool
  panics on the read rather than answering. The description was updated for the reason the range one was: a caller
  told about a refusal by only one of two tools that share a guard meets the other's
  400 with no warning, and the gate above iterates both.
- `tags` is a **replacement**, like every other field here: the value sent becomes
  the list, which `internal/domain/memory_latest_test.go` (`TestUpdateMemory`)
  drives by updating a memory tagged `keep` with `fresh` and requiring the stored
  list to be exactly `fresh` — ⚠️ which every other arm in that function misses,
  because they only ever OMIT `tags` and observe it inherited, so a handler that
  MERGED the sent list into the stored one (the intuitive reading of "new tags",
  and what a caller adding one tag would expect) was green until this slice.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.1 T1-3 / §6.2 T2-19 — LANDED on all three strength surfaces** (`aihub#433`).
  `pf_remember` publishes 1-5, `pf_recall`'s `min_strength` names the scale it
  thresholds, and this tool publishes 1-5 too, so a caller who reads only this schema
  learns the enforced range without having to provoke a 400. The agreement is held by
  two gates rather than by these three strings happening to match:
  `TestPublishedBaseStrengthRangeIsTheEnforcedOne` covers the two `base_strength`
  surfaces and `TestRecallMinStrengthPublishesWhichScaleItIsOn` covers `min_strength`,
  both in `internal/mcp/tools_memory_test.go`, and both build the range from
  `domain.MinBaseStrength` / `domain.MaxBaseStrength` rather than typing it out.
- **§6.1 T1-3 residual (owner ruling 2026-09-09) — LANDED** (`aihub#459`): the legal
  set is the INTEGERS in that range, not the reals in it. The range could be
  anchored on a constant; integrality cannot, so
  `TestPublishedBaseStrengthRangeIsTheEnforcedOne` anchors it on the enforcement —
  it drives `domain.Remember` with a nil pool, where a refused value returns before
  the pool is touched and an accepted one can only prove it got through by dying on
  it. That covers this tool as well as `pf_remember`, because this tool's body
  reaches the same function.
- **§6.2 T2-1** — one editability matrix for the whole struct and one error code per
  rejection kind; the "omit to keep current" convention is this tool's local version
  of that matrix.

## Open

- Whether `base_strength` should be publishable on an update at all, given that
  activation and reinforcement also move strength, is not settled anywhere this card
  can cite.
  <!-- prose-only: because=judgement -->
