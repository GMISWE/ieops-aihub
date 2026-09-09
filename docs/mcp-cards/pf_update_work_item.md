# pf_update_work_item — contract card

```json
{
  "tool": "pf_update_work_item",
  "description_sha256": "29b3c7434085f3bd45cf9acebf95466a7fe1ae8757a6d86786d03886c6687c30",
  "input_schema_sha256": "53bbfa577b63dcb85a695ad7be32284df348876425aeb35cfa25d2d778a44894",
  "params": {
    "attrs": {
      "type": "object",
      "required": false
    },
    "attrs_patch": {
      "type": "object",
      "required": false
    },
    "attrs_unset": {
      "type": "array",
      "required": false
    },
    "brief": {
      "type": "boolean",
      "required": false
    },
    "content": {
      "type": "string",
      "required": false
    },
    "declared_resources": {
      "type": "array",
      "required": false
    },
    "goal": {
      "type": "string",
      "required": false
    },
    "goal_change_reason": {
      "type": "string",
      "required": false
    },
    "labels": {
      "type": "array",
      "required": false
    },
    "milestone": {
      "type": "string",
      "required": false
    },
    "priority": {
      "type": "string",
      "required": false,
      "enum": [
        "high",
        "low",
        "normal",
        "urgent"
      ]
    },
    "reclassify_reason": {
      "type": "string",
      "required": false
    },
    "requires_human_session": {
      "type": "boolean",
      "required": false
    },
    "resources_version": {
      "type": "integer",
      "required": false
    },
    "wi_type": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "attrs",
    "closed_at",
    "content",
    "content_len",
    "created_at",
    "current_attempt_epoch",
    "current_attempt_id",
    "declared_resources",
    "external_share_key",
    "external_share_type",
    "goal",
    "id",
    "labels",
    "milestone",
    "parent_work_item_id",
    "priority",
    "project",
    "reporter_display",
    "reporter_user_id",
    "requires_human_session",
    "resources_version",
    "scenario",
    "seq",
    "slug",
    "source",
    "status",
    "updated_at",
    "wi_type"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Sixteen parameters, three of which carry a compare-and-set or destructive semantic
that a caller gets wrong by default.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | id or slug |
| `goal` | string | no | single-line; only while status is queued, paused or blocked, and only for the reporter / a maintainer / an admin |
| `goal_change_reason` | string | no | required with `goal` |
| `priority` | enum | no | from the domain list |
| `milestone` | string | no | updated milestone |
| `wi_type` | string | no | updated type |
| `requires_human_session` | boolean | no | sets `true` or `false`; **cannot reach the third state** — no way back to `NULL` |
| `reclassify_reason` | string | no | required with a `wi_type` change, min 10 chars |
| `labels` | array | no | max from the domain constant |
| `declared_resources` | array | no | the whole list, replaced |
| `resources_version` | integer | no | CAS guard — omitting it overwrites unconditionally |
| `attrs` | object | no | **REPLACES** the whole object; unsent keys are DELETED; a non-object is a 400 |
| `attrs_patch` | object | no | shallow merge; `null` STORES a null rather than deleting; a non-object is a 400 |
| `attrs_unset` | array | no | applied AFTER `attrs_patch`, so a key in both is deleted |
| `content` | string | no | markdown ≤20000; not echoed back |
| `brief` | boolean | no | replaces the body with `content_len` |

`kind` is deliberately **absent**. It was published here and forwarded, but no
server struct has a `kind` json tag, so the value died at bind: 200, `wi_type`
untouched, no signal. It was measured live on a RUNNING work item — had it bound to
`wi_type`, the status gate would have rejected the call. Withdrawing the promise is
the fix; wiring it to `wi_type` would have opened a bypass around
`reclassify_reason`. `pf_list_work_items`' `kind` is a different parameter and stays.

`requires_human_session` publishes only two of the column's three states, and says
so. `domain.buildWorkItemUpdate` gates it behind a non-nil check, so omitting the
field and sending an explicit `null` are the same no-op — this tool can correct a
classification but not withdraw one. `aihub#447` measured both spellings against a
scratch work item on a live-era build and on `origin/main`; the stored value did not
move either time. That measurement is also what closes `§6.4` item 2 under **Open**
below — the `null` to `true` transition once attributed to an `attrs_patch`-only call
on this tool was written by the claim path.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) copies every argument
except `work_item_id` and `brief` into the body of
`PATCH /v1/work_items/<id>` via `pkg/client/client.go` (`UpdateWorkItem`), bound by
`internal/server/router.go` (`handleUpdateWorkItem`).

- **`resources_version` is coerced before the body is built.** It is an INT column
  and `*int` on the wire, so a quoted `"0"` from a mixed-version client becomes a
  JSON number here rather than failing bind two layers away as an opaque 400
  "invalid request body" — which is indistinguishable from the server not knowing
  the parameter at all.
- **`brief` is withheld on purpose.** It shapes this process's reply and means
  nothing to the server; forwarding a field the peer does not bind is how
  `expected_version` travelled the whole way and was discarded in silence.
- `internal/mcp/tools_update_wi_schema_test.go` is the class gate: it fails on any
  parameter this schema publishes that the server does not bind.

## hop 4 — what it actually does

- **`resources_version` is the only thing standing between two writers.** Leaving it
  out overwrites unconditionally: a concurrent writer's list is silently discarded,
  locks and all, and the caller still gets a 200. The token comes from
  `pf_get_work_item`, which is named in the description for the reason `aihub#260`
  gives about `members_version` — a guard whose input nobody can find is a guard
  nobody passes.
- **Narrowing `declared_resources` releases the corresponding `file_scope` locks**
  at the moment of the update (`aihub#264`). Any other lock type is not released that
  way and is held until the attempt ends. ⚠️ Since `aihub#416` there is normally no
  other type to hold: `repo` and `service` entries derive no lock, so narrowing a
  declaration to nothing releases everything the update path can reach.
- **`attrs` vs `attrs_patch` is the difference between a merge and a wipe.**
  `attrs_patch` is shallow: a top-level key replaces that key's stored value
  outright rather than merging into it recursively, and `null` stores a JSON null.
- **Both are shape-checked, and `attrs` only since `aihub#465`.** `attrs_patch`
  had to be, because `jsonb || jsonb` silently does something else with an array;
  `attrs` is a plain column assignment, so Postgres stored whatever JSON arrived
  and answered 200 — including a JSON-encoded STRING of the object the caller
  meant, which two live calls did. Both now reject any non-object with a 400
  naming the type and the byte length, and for a string they report through
  `details.string_decodes_to` whether the quoted text was valid JSON. Neither
  coerces: 18 of the 19 stringified payloads in the corpus are malformed, so
  decoding would rescue one and guess at the rest. A literal `null` keeps its
  existing meaning in both fields.
- **`goal` and `wi_type` are status-gated, permission-gated and reason-gated** —
  by the matrix below rather than by a guard of their own.
- **`goal` does carry one guard of its own: a newline refuses the write.**
  `internal/domain/work_items.go` (`UpdateWorkItem`) rejects a `goal` containing `\n`
  or `\r` with `ErrGoalMultiline`, and that check sits BEHIND the matrix for the same
  reason `goal_change_reason`'s does — an edit refused on state is not first told its
  goal is multiline. What this path does NOT check is length: `pf_create_work_item`
  refuses a goal over 500 characters and this tool has no equivalent, so the same
  string is accepted here and rejected there. Both facts are stated as found;
  `aihub#474` owns whether the cap should apply on update, and this card does not
  anticipate that call.

### The editability matrix (`aihub#440`)

`internal/domain/work_items.go` (`wiEditTierByField`) is the whole rule: **one**
matrix for every field this tool can write, and **one error code per rejection
KIND** — 409 means the work item is in the wrong state, 403 means you are the
wrong caller.

| tier | fields | `queued` · `paused` · `blocked` | `running` | `wrapped` · `failed` · `cancelled` |
|---|---|---|---|---|
| contract | `goal`, `wi_type` | reporter / project maintainer / admin only, else **403 `FORBIDDEN`** | **409 `CONFLICT_WI_ALREADY_CLAIMED`** — pause first | **409 `CONFLICT_TERMINAL_STATE`** |
| working | `content`, `labels`, `priority`, `milestone`, `requires_human_session`, `declared_resources` | allowed | allowed | **409 `CONFLICT_TERMINAL_STATE`** |
| record | `attrs`, `attrs_patch`, `attrs_unset` | allowed | allowed | **allowed** — the one exemption, and it is deliberate |

- **The strictest tier a patch touches governs the whole patch.** `attrs_patch`
  plus `labels` against a wrapped work item is refused whole, not applied in part:
  a PATCH that wrote some fields and refused others would need a response shape
  that says which, and there is none.
- **`goal_change_reason`, `reclassify_reason` and `resources_version` carry no
  tier** — they write no column of their own, so sending one alone is a no-op
  rather than a refusal. The reason checks still run, and they run *after* the
  matrix: an edit refused on state is not first told its reason string is short.
- **Two codes were retired and are no longer produced anywhere:** 409
  `GOAL_CHANGE_NOT_ALLOWED` and 403 `WI_RECLASSIFY_FORBIDDEN`. Each answered BOTH
  halves of its own field's gate, so a wrong CALLER on `goal` came back as a state
  conflict and a wrong STATE on `wi_type` came back as a permission failure — the
  conflation `aihub#242` had already removed from `pf_cancel_work_item`. The three
  codes in the table are that tool's own three, so one rule now covers both tools.
  The constants stay declared and mapped (`internal/domain/errors.go`
  (`ErrGoalChangeNotAllowed`)) so a stale client's branch still resolves.
- **`blocked` is new for the contract tier.** `goal` and `wi_type` used to be
  refused there; a blocked work item has no live attempt to invalidate, which is
  the argument `aihub#242` already accepted for cancel.

## hop 5 — what comes back

The updated record, with one of two content treatments:
`internal/mcp/wi_echo_slim.go` (`dropContentEcho`) when `brief` is set, otherwise
(`suppressContentEcho`) which only removes a body the caller sent in this very call.
`brief` is the wider rule and is checked first.

`brief` here is **not** `pf_get_work_item`'s `brief`: this one reports
`content_len`, that one reports nothing. A work item with no body comes back as
`content: null` with no `content_len`, so a missing `content_len` means "this wi has
no body", never "the body was withheld".

## Policy

- **§6.1 T1-9** — `kind`'s withdrawal is the rule applied: prose contradicting hop 3
  is a bug, and the legal dispositions are withdraw, fix, or file.
- **§6.1 T1-9, second application** — the published `goal` description states the
  status gate and is silent about the multiline refusal, while
  `pf_create_work_item`'s states both its shape constraints. The disposition split:
  the refusal that exists is documented here and on the hop 0-1 row above, and the
  cap that does not exist is FILED (`aihub#474`) rather than written into prose as
  though it were there.
- **§6.2 T2-1** — one editability matrix for the whole struct, one error code per
  rejection KIND (409 state, 403 permission), and no field silently exempt.
  **Implemented** in `internal/domain/work_items.go` (`updateGate`); "no field
  silently exempt" is enforced structurally rather than by prose, by
  `TestEveryWritableUpdateFieldHasATier` — a field added to
  `UpdateWorkItemRequest` without a tier fails the build gate.
- **§6.1 T1-5** — both content treatments are deletes.

## Open

- **§6.4 item 2 — CLOSED by `aihub#447`, and this is the card it lands on.** T2-9's
  live side effect was a `pf_update_work_item` call sending **only** `work_item_id`
  and `attrs_patch` that moved `requires_human_session` from `null` to `true` and
  persisted it, with no field of that name anywhere in the request. Both parameters
  involved are published by this tool and documented at length above, so the README
  rule — a card touching a `§6.4` item says so here — points at this card. **It did
  not reproduce, and the write has a different author.** The audit's recipe was run
  on both arms it asks for: the same sequence (create with the field omitted,
  `attrs_patch`-only update through the MCP layer, then claim) against a server built
  from `origin/main` and against one built from a live-era commit, on a real
  database. The update left the column `NULL` on both; the CLAIM set it `true` on
  both, emitting `wi_classification_resolved` (`source: server_default`)
  sub-millisecond before `attempt_started`. That is the same ordered pair
  `aihub#411`'s own live timeline carries, one minute after it was filed and **51
  minutes before** the update it was attributed to. So the mechanism is
  `domain.FnClaimWorkItem`'s C-R9-12 fallback, which resolves an unclassified work
  item from a server default on first claim; it is still live and unchanged, and
  `domain.buildWorkItemUpdate`'s non-nil gate — untouched since 2026-08-11 and
  identical on both arms — means "already fixed" was never an available reading
  either. Nothing on this tool's path was broken, so nothing on it was changed. What
  made the misattribution cheap is recorded above: this tool's reply carries
  `requires_human_session` whether or not the call wrote it, so a caller cannot tell
  a value it read from a value it wrote.
- **§6.4 item 7 — CLOSED by `aihub#440`.** T2-1 left one sub-question open in
  **both** directions: whether `attrs` staying writable on a terminal work item is
  the defect or the feature. It is the **feature**, decided on traffic rather than
  taste. Measured over the 21-day transcript corpus (87 files, 738
  `pf_update_work_item` calls, 715 whose response carried a status): of the 49
  calls against a closed record, 28 carried `attrs_patch`, 20 `attrs`, 1
  `attrs_unset`, and **none** carried any other field. So the record tier is
  load-bearing — post-wrap decision and merge records are written through it,
  including by the batch that shipped this change — while the working tier's new
  refusal on a closed record breaks zero measured calls. `aihub#440` wrapped
  2026-09-08 and was re-checked the same day.
- **`milestone` is the one unexercised cell.** It appears in none of the 738
  measured calls, so its new terminal-state refusal rests on the tier argument
  alone rather than on observed traffic.
