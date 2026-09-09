# pf_update_work_item — contract card

```json
{
  "tool": "pf_update_work_item",
  "description_sha256": "49d4dfb79191528af8b91e23b4909b8d97b68eb9330c535cb62d1753f92c887c",
  "input_schema_sha256": "9675487a26bb93c26f17ff7a9ebc63ef72c089439caadb9bc565ac54a69f53ad",
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

**Since `aihub#495` the tool-level description carries the editability matrix
itself**, not just the parameter list it used to name. That is a hop-1 change with
no behaviour behind it: the matrix has been enforced since `aihub#440`, and it was
written down in the comment above `internal/domain/work_items.go`
(`wiEditTierByField`), in the table below, and in `docs/mcp-tools.md` — three
places, none of them on the wire. The description now states the three tiers, the
three status classes, the two 409s and the 403, the mixed-patch rule, and — named
individually, because it is the only cell that MOVED — that `labels`, `priority`,
`milestone`, `requires_human_session` and `declared_resources` used to succeed on a
terminal work item. `internal/mcp/update_wi_edit_matrix_publication_test.go`
anchors that text on `domain.WorkItemFieldsByEditTier` and
`domain.WorkItemStatusesByEditClass` rather than on a list retyped in the test, so
a seventh working-tier field cannot join the tier unpublished.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | id or slug |
| `goal` | string | no | single-line, **non-empty**, ≤500 chars; only while status is queued, paused or blocked, and only for the reporter / a maintainer / an admin |
| `goal_change_reason` | string | no | required with `goal` |
| `priority` | enum | no | from the domain list |
| `milestone` | string | no | updated milestone |
| `wi_type` | string | no | updated type; only while status is queued, paused or blocked — the same tier, predicate and status set as `goal`, and since `aihub#495` its description says so too |
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
- **`goal` carries two guards of its own, and since `aihub#474` (2026-09-09) they
  are the same two `pf_create_work_item` applies.** `internal/domain/work_item_fields.go`
  (`validateWorkItemGoalShape`) refuses a goal over 500 characters with a 400
  `BAD_REQUEST` and one containing `\n` or `\r` with `ErrGoalMultiline`, in that
  order, and BOTH work-item write paths call it. The checks sit BEHIND the matrix
  for the same reason `goal_change_reason`'s does — an edit refused on state is not
  first told its goal is multiline.
- **An EMPTY goal is refused since `aihub#507` (2026-09-09), and until then it was
  STORED.** `pf_create_work_item` had always answered `goal: ""` with a 400
  `BAD_REQUEST` "goal is required"; this tool wrote the empty string and returned
  200, leaving a work item that renders blank in every list and in the ready queue.
  Nothing downstream objected, and nothing was going to: `work_items.goal` is
  `TEXT NOT NULL` and `NOT NULL` admits the empty string, so unlike the length half
  below there was no constraint to turn it into even a bad error. The owner ruled
  on 2026-09-09 that this path mirrors create, and both now call one function,
  `internal/domain/work_item_fields.go` (`validateWorkItemGoalPresent`) — same
  code, same message, so the two doors cannot drift apart again. **The refusal
  breaks nobody**: measured on production 2026-09-09, 2,309 work items, zero with
  an empty goal and zero with a whitespace-only one, so "empty means clear the
  goal" was never a used affordance. ⚠️ **Clearing a goal is refused; leaving it
  alone is not.** Omitting `goal` and sending an explicit `null` are still the same
  no-op — the check sits inside the `req.Goal != nil` guard, which is the only
  thing that can tell `goal: ""` from no `goal` at all. And it is `""` ONLY: a
  whitespace-only goal is still accepted, because create accepts it and the ruling
  was to mirror create — refusing it here alone would close one asymmetry by
  opening another.
- **Required-ness is a SEPARATE function from the shape checks, on purpose.**
  `validateWorkItemGoalShape` is the declared Go mirror of `work_items_goal_check`
  (the correspondence `internal/domain/db_check_policy_test.go` asserts), and that
  CHECK permits the empty string. Folding the emptiness rule into it would make the
  mirror enforce something the database does not while that test kept passing, so
  the two rules stay two functions and each call site calls both.
- **The length half was missing here until `aihub#474`, and what it cost is not
  what the report assumed.** The filing said this tool STORED an over-length goal.
  It did not: `work_items_goal_check` caps `length(goal)` at 500 in the database
  (`internal/db/migrations/0002_work_items.sql`, unaltered by any later migration),
  so the write was refused by Postgres as SQLSTATE 23514 and reached the caller as a
  500 with a constraint name in it. So the defect was never data integrity; it was
  that the two tools answered the SAME illegal string two different ways, which is
  the `aihub#396` class exactly. The owner ruled the asymmetry an oversight on
  2026-09-09 rather than a deliberate exemption, and the fix is one shared function
  rather than a second copy of the check, so a future third write path cannot
  reintroduce the split. Adding the cap refuses no existing caller: measured on
  production 2026-09-09, 2,286 work items, `max(char_length(goal))` exactly 500,
  zero rows above it — which is also why the boundary value is pinned as ACCEPTED
  by `internal/domain/work_item_goal_shape_test.go`.

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
- **§6.1 T1-9, third application — `aihub#507` (2026-09-09).** The rule's other
  direction: not prose that outlived its behaviour, but a refusal that arrived
  without prose. The published `goal` description now says **non-empty**, because
  `""` went from stored to refused on this path and a caller holding the old
  contract would meet the new 400 by hitting it. The gate is
  `internal/mcp/goal_cap_publication_test.go` (`TestPublishedGoalCapIsTheEnforcedOne`),
  so the word cannot be edited out while the check stays. **Closed on the create side by `aihub#520`**
  (2026-09-09): `pf_create_work_item` and `pf_batch_create_work_items` share one
  `goal` string (`workItemFieldProps`) and now carry the word too, so all three
  tools that write this column publish the same refusal. That was left as its own
  change because editing the shared string moves both of their
  `input_schema_sha256` and stales their cards, which its file scope had to
  include. The `aihub#507` gate was NAMED for this tool while it was the only one
  carrying the word; `aihub#520` folded it into the quantified loop above rather
  than leaving a second named test, which is what its own doc comment asked for.
- **§6.1 T1-9, second application — CLOSED by `aihub#474` (2026-09-09).** The
  published `goal` description used to state the status gate and nothing else,
  while `pf_create_work_item`'s stated both of its shape constraints. The
  disposition at the time was a split: document the refusal that existed, FILE the
  cap that did not. The filing came back "oversight" rather than "deliberate
  exemption", so the cap now exists on this path and the description states both
  constraints — and it builds the number from `domain.MaxWorkItemGoalRunes()`
  rather than retyping it, which is the T1-9 failure mode one level up: a published
  limit that no longer tracks the enforced one reads as true and is not.
- **§6.1 T1-9, fourth application — `aihub#495` (2026-09-09).** The third
  application's direction again, one scope up: not one parameter's refusal
  arriving without prose, but the whole matrix. `aihub#440` moved five fields from
  *writable on a wrapped work item* to 409 `CONFLICT_TERMINAL_STATE` — the only
  200 → 409 transition in that batch — and published nothing, while `wi_type`'s
  description still read `Updated wi_type` though `goal`, its partner in the same
  tier under the same predicate, had been rewritten to state the status gate. Both
  are on the wire now. The gate is
  `internal/mcp/update_wi_edit_matrix_publication_test.go`, and it reads
  `internal/domain/work_items.go` (`WorkItemFieldsByEditTier`) and
  (`WorkItemStatusesByEditClass`) rather than a list retyped in the test — a third
  copy of the matrix would go green on precisely the day a sixth working-tier
  field joined it unpublished, which is the event the gate exists for. It checks
  both contract-tier parameters, not only the one that was wrong, because the
  defect was the PAIR disagreeing.
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
