# pf_update_work_item — contract card

```json
{
  "tool": "pf_update_work_item",
  "description_sha256": "99ed434d8b00de46a4dc888ccef94b7d7a2c61689d2181398ae2483697a096ee",
  "input_schema_sha256": "3dd557c54830241af80bda9b3f99b7d0ba68358673728f83512ced8b533a4b88",
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
no behaviour behind it: the matrix has been enforced since `aihub#440` — every
(tier, status, actor) cell of it is driven by
`internal/domain/work_items_update_gate_test.go` (`TestUpdateGate`) — and it was
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
| `goal` | string | no | single-line, **non-empty**, ≤500 chars (`TestGoalShapeIsTheSameContractOnBothWritePaths`); only while status is queued, paused or blocked, and only for the reporter / a maintainer / an admin (`TestUpdateGate`) |
| `goal_change_reason` | string | no | required with `goal` |
| `priority` | enum | no | from the domain list |
| `milestone` | string | no | updated milestone |
| `wi_type` | string | no | updated type; only while status is queued, paused or blocked — the same tier, predicate and status set as `goal` (`TestUpdateGate`, `TestOnlyGoalAndWITypeCarryAPermissionGate`), and since `aihub#495` its description says so too |
| `requires_human_session` | boolean | no | sets `true` or `false`; **cannot reach the third state** — no way back to `NULL` |
| `reclassify_reason` | string | no | required with a `wi_type` change, min 10 chars |
| `labels` | array | no | max from the domain constant |
| `declared_resources` | array | no | the whole list, replaced |
| `resources_version` | integer | no | CAS guard — omitting it overwrites unconditionally |
| `attrs` | object | no | **REPLACES** the whole object; unsent keys are DELETED (`TestUpdateWorkItemAttrs_ReplaceStillDestroysUnsentKeys`); a non-object is a 400 (`TestStringifiedObjectParamIsRejected`) |
| `attrs_patch` | object | no | shallow merge (`TestUpdateWorkItemAttrsPatch_DoesNotDestroyOtherKeys`); `null` STORES a null rather than deleting (a `TestUpdateWorkItemAttrsUnset_DeletesNamedKeys` subtest); a non-object is a 400 (`TestStringifiedObjectParamIsRejected`) |
| `attrs_unset` | array | no | applied AFTER `attrs_patch`, so a key in both is deleted (`TestUpdateWorkItemAttrsUnset_DeletesNamedKeys`) |
| `content` | string | no | markdown ≤20000; not echoed back |
| `brief` | boolean | no | replaces the body with `content_len` |

`kind` is deliberately **absent**, and
`internal/mcp/tools_update_wi_schema_test.go`
(`TestUpdateWorkItemDoesNotPublishKind`) is what keeps it absent, with `wi_type` as
its control so an emptied schema cannot pass for a withdrawal. It was published here
and forwarded, but no server struct has a `kind` json tag, so the value died at bind:
200, `wi_type` untouched, no signal. It was measured live on a RUNNING work item — had
it bound to `wi_type`, the status gate would have rejected the call. Withdrawing the
promise is the fix; wiring it to `wi_type` would have opened a bypass around
`reclassify_reason`.
<!-- prose-only: because=counterfactual -->
`pf_list_work_items`' `kind` is a different parameter and stays, published there as a
deprecated alias for that tool's `wi_type` FILTER — both spellings and the deprecation
are held by `internal/mcp/tools_list_wi_schema_test.go`
(`TestListWorkItemsPublishesBothTypeSpellings`).

`requires_human_session` publishes only two of the column's three states, and says
so. `domain.buildWorkItemUpdate` gates it behind a non-nil check, so omitting the
field and sending an explicit `null` compile to the same statement, argument for
argument — the comparison is
`internal/domain/update_wi_null_vs_omitted_test.go`
(`TestAnExplicitNullOnAnUpdateIsIndistinguishableFromAnOmittedField`), which carries a
real value beside each null so "identical" cannot be satisfied by a builder that
ignores the field — and this tool can therefore correct a classification but not
withdraw one. `aihub#447` measured both spellings against a
scratch work item on a live-era build and on `origin/main`; the stored value did not
move either time. That measurement is also what closes `§6.4` item 2 under **Open**
below — the `null` to `true` transition once attributed to an `attrs_patch`-only call
on this tool was written by the claim path.
<!-- prose-only: because=external-state -->

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) copies every argument
except `work_item_id` and `brief` into the body of
`PATCH /v1/work_items/<id>` via `pkg/client/client.go` (`UpdateWorkItem`), bound by
`internal/server/router.go` (`handleUpdateWorkItem`).

- **`resources_version` is coerced before the body is built** — the POSITION as well
  as the call, both read off the registration by
  `internal/mcp/resources_version_test.go`
  (`TestUpdateWorkItemHandlerCoercesResourcesVersion`), with the coerced value a
  JSON number in the marshalled body by (`TestNormalizeIntArgProducesJSONNumber`) —
  because it is an INT column
  and `*int` on the wire, so a quoted `"0"` from a mixed-version client becomes a
  JSON number here rather than failing bind two layers away as an opaque 400
  "invalid request body" — which is indistinguishable from the server not knowing
  the parameter at all.
- **`brief` is withheld on purpose** — observed on the request a fake aihub really
  received by `internal/mcp/wi_echo_test.go`
  (`TestUpdateBriefIsNotForwardedToTheServer`), which asserts in the same call that
  the rest of the body is unaffected — because it shapes this process's reply and means
  nothing to the server; forwarding a field the peer does not bind is how
  `expected_version` travelled the whole way and was discarded in silence.
- `internal/mcp/tools_update_wi_schema_test.go` is the class gate: it fails on any
  parameter this schema publishes that the server does not bind.

## hop 4 — what it actually does

- **`resources_version` is the only thing standing between two writers**
  (`TestDeclaredResourcesCASRetry_LosingWriteIsDiscardedWholeNotJustItsVersion`).
  Leaving it out overwrites unconditionally: a concurrent writer's list is silently discarded,
  locks and all, and the caller still gets a 200 — three arms, one per clause:
  `internal/domain/work_items_cas_test.go`
  (`TestBuildWorkItemUpdate_DeclaredResourcesAlwaysIncrementsVersion`) shows the
  compiled statement carries no precondition when the token is absent,
  `internal/domain/work_items_cas_db_test.go`
  (`TestUpdateWorkItemCASVersionAdvancesAcrossWrites`) drives a second writer that
  omits it against a real row and gets the 200 with the first list gone, and
  `internal/domain/declared_resources_release_db_test.go`
  (`TestNarrowingDeclaredResourcesReleasesItsLocks`) is the "locks and all" half,
  measured against the lock table. The token comes from
  `pf_get_work_item`, which is named in the description for the reason `aihub#260`
  gives about `members_version` — a guard whose input nobody can find is a guard
  nobody passes, so both halves are pinned together by
  `internal/mcp/update_wi_wire_shape_test.go`
  (`TestPublishedCASGuardNamesTheToolThatReturnsItsToken`): the description names that
  tool, and that tool's reply really carries the key, read off `domain.WorkItem`'s own
  json tags.
- **Narrowing `declared_resources` releases the corresponding `file_scope` locks**
  at the moment of the update (`aihub#264`). Any other lock type is not released that
  way and is held until the attempt ends. ⚠️ Since `aihub#416` there is normally no
  other type to hold: `repo` and `service` entries derive no lock
  (`internal/domain/commit_lock_type_test.go`,
  `TestCommitGateKeysFileScopeAndTheAdvisoryTypesDeriveNothing`, with the lock table's
  own answer in `internal/domain/delocking_db_test.go`,
  `TestDeLockingClaimTakesNoRepoOrServiceLock`), so narrowing a
  declaration to nothing releases everything the update path can reach.
- **`attrs` vs `attrs_patch` is the difference between a merge and a wipe**, and both
  halves are driven against a real row in `internal/domain/work_items_attrs_db_test.go`
  — (`TestUpdateWorkItemAttrsPatch_DoesNotDestroyOtherKeys`) for the merge and
  (`TestUpdateWorkItemAttrs_ReplaceStillDestroysUnsentKeys`) for the wipe, which is
  retained deliberately because reinterpreting a field every existing caller already
  sends is a worse defect than the one `aihub#288` fixed. It is NOT the only way to
  delete a key: `attrs_unset` deletes named top-level keys, and
  (`TestUpdateWorkItemAttrsUnset_DeletesNamedKeys`) below is what holds it.
  `attrs_patch` is shallow: a top-level key replaces that key's stored value
  outright rather than merging into it recursively — a subtest of
  (`TestUpdateWorkItemAttrsPatch_DoesNotDestroyOtherKeys`) patches a nested object
  with a partial one, beside a sibling key that must survive so the shallow answer
  cannot come from a whole-column replace.
  `null` stores a JSON null, and
  (`TestUpdateWorkItemAttrsUnset_DeletesNamedKeys`) carries the subtest where a
  `null` is shown to be stored rather than treated as a delete.
- **Both are shape-checked, and `attrs` only since `aihub#465`**
  (`TestStringifiedObjectParamIsRejected` quantifies over both). `attrs_patch`
  had to be, because `jsonb || jsonb` silently does something else with an array;
  `attrs` is a plain column assignment, so Postgres stored whatever JSON arrived
  and answered 200 — including a JSON-encoded STRING of the object the caller
  meant, which two live calls did; both fields of this tool are rows in the
  guarded-site table of `internal/domain/json_object_params_test.go`
  (`TestStringifiedObjectParamIsRejected`), and every non-object shape an array, a
  scalar or a number can take is a case of `internal/domain/work_items_attrs_test.go`
  (`TestValidateAttrsPatch`). Both now reject any non-object with a 400
  naming the type and the byte length, and for a string they report through
  `details.string_decodes_to` whether the quoted text was valid JSON — the message
  is anchored at both ends and the details are asserted key by key in that same
  arm, with (`TestStringifiedObjectParamMessagesStayDistinguishable`) refusing two
  fields that answer the same sentence. Neither
  coerces: 18 of the 19 stringified payloads in the corpus are malformed, so
  decoding would rescue one and guess at the rest. A literal `null` keeps its
  existing meaning in both fields.
- **`goal` and `wi_type` are status-gated, permission-gated and reason-gated** —
  by the matrix below rather than by a guard of their own, and they are the ONLY two
  fields carrying the permission half, asserted over the whole field set by
  `internal/domain/work_items_update_gate_test.go`
  (`TestOnlyGoalAndWITypeCarryAPermissionGate`) with the status half in
  (`TestUpdateGate`).
- **`goal` carries two guards of its own, and since `aihub#474` (2026-09-09) they
  are the same two `pf_create_work_item` applies**
  (`TestGoalShapeIsTheSameContractOnBothWritePaths`). `internal/domain/work_item_fields.go`
  (`validateWorkItemGoalShape`) refuses a goal over 500 characters with a 400
  `BAD_REQUEST` and one containing `\n` or `\r` with `ErrGoalMultiline`, in that
  order, and BOTH work-item write paths call it —
  `internal/domain/work_item_goal_shape_test.go`
  (`TestGoalShapeIsTheSameContractOnBothWritePaths`) carries a case for each half and
  one for the order between them, and
  (`TestBothWorkItemWritePathsCallTheGoalShapeValidator`) is the wiring, without which
  the contract would be a rule with no callers. The checks sit BEHIND the matrix
  for the same reason `goal_change_reason`'s does — an edit refused on state is not
  first told its goal is multiline — which is an order
  `internal/domain/update_wi_check_order_test.go`
  (`TestTheEditMatrixIsCheckedBeforeTheGoalAndReasonRules`) reads off the parsed call
  positions inside `UpdateWorkItem`.
- **An EMPTY goal is refused since `aihub#507` (2026-09-09), and until then it was
  STORED** (`TestBothWorkItemWritePathsRefuseAnEmptyGoal`). `pf_create_work_item` had always answered `goal: ""` with a 400
  `BAD_REQUEST` "goal is required"; this tool wrote the empty string and returned
  200, leaving a work item that renders blank in every list and in the ready queue —
  the refusal, its code and its message are pinned verbatim by
  `internal/domain/work_item_goal_required_test.go`
  (`TestEmptyGoalIsRefusedByTheFunctionBothWritePathsCall`).
  Nothing downstream objected, and nothing was going to: `work_items.goal` is
  `TEXT NOT NULL` and `NOT NULL` admits the empty string, so unlike the length half
  below there was no constraint to turn it into even a bad error — the DDL and the
  effective CHECK are read together, against the Go pair that now refuses what the
  column accepts, by `internal/domain/update_wi_null_vs_omitted_test.go`
  (`TestTheGoalColumnAdmitsTheEmptyStringThatGoNowRefuses`). The owner ruled
  on 2026-09-09 that this path mirrors create, and both now call one function,
  `internal/domain/work_item_fields.go` (`validateWorkItemGoalPresent`) — same
  code, same message, so the two doors cannot drift apart again, which is
  (`TestBothWorkItemWritePathsRefuseAnEmptyGoal`) for the wiring and
  (`TestCreateStillRefusesAnEmptyGoalInTheSameWords`) for the half that was already
  correct. **The refusal
  breaks nobody**: measured on production 2026-09-09, 2,309 work items, zero with
  an empty goal and zero with a whitespace-only one, so "empty means clear the
  goal" was never a used affordance. ⚠️ **Clearing a goal is refused; leaving it
  alone is not.** Omitting `goal` and sending an explicit `null` are still the same
  no-op — the check sits inside the `req.Goal != nil` guard, which is the only
  thing that can tell `goal: ""` from no `goal` at all; the guard's placement is
  asserted structurally by (`TestBothWorkItemWritePathsRefuseAnEmptyGoal`) and the
  two spellings are compiled and compared by
  `internal/domain/update_wi_null_vs_omitted_test.go`
  (`TestAnExplicitNullOnAnUpdateIsIndistinguishableFromAnOmittedField`). And it is
  `""` ONLY: a
  whitespace-only goal is still accepted, because create accepts it and the ruling
  was to mirror create — a space and a tab are both accepted rows of
  (`TestEmptyGoalIsRefusedByTheFunctionBothWritePathsCall`) — and refusing it here
  alone would close one asymmetry by opening another.
- **Required-ness is a SEPARATE function from the shape checks, on purpose.**
  `validateWorkItemGoalShape` is the declared Go mirror of `work_items_goal_check`
  (the correspondence `internal/domain/db_check_policy_test.go` asserts), and that
  CHECK permits the empty string. Folding the emptiness rule into it would make the
  mirror enforce something the database does not while that test kept passing, so
  the two rules stay two functions and each call site calls both.
- **The length half was missing here until `aihub#474`, and what it cost is not
  what the report assumed.**
  <!-- prose-only: because=history -->
  The filing said this tool STORED an over-length goal.
  It did not: `work_items_goal_check` caps `length(goal)` at 500 in the database
  (`internal/db/migrations/0002_work_items.sql`, unaltered by any later migration —
  `internal/domain/db_check_policy_test.go`
  (`TestDBCheckRegistry_MirrorsMatchTheMigration`) replays every Up section in order
  and compares the surviving bound against `domain.MaxWorkItemGoalRunes()`),
  so the write was refused by Postgres as SQLSTATE 23514 and reached the caller as a
  500 with a constraint name in it. So the defect was never data integrity; it was
  that the two tools answered the SAME illegal string two different ways, which is
  the `aihub#396` class exactly.
  <!-- prose-only: because=history -->
  The owner ruled the asymmetry an oversight on
  2026-09-09 rather than a deliberate exemption, and the fix is one shared function
  rather than a second copy of the check, so a future third write path cannot
  reintroduce the split. Adding the cap refuses no existing caller: measured on
  production 2026-09-09, 2,286 work items, `max(char_length(goal))` exactly 500,
  zero rows above it — which is also why the boundary value is pinned as ACCEPTED
  by `internal/domain/work_item_goal_shape_test.go`.

### The editability matrix (`aihub#440`)

`internal/domain/work_items.go` (`wiEditTierByField`) is the whole rule: **one**
matrix for every field this tool can write — every json field the request binds is
either tiered or a declared rider, checked in three directions by
`internal/domain/work_items_update_gate_test.go`
(`TestEveryWritableUpdateFieldHasATier`) — and **one error code per rejection
KIND**: 409 means the work item is in the wrong state, 403 means you are the
wrong caller, which is every cell of (`TestUpdateGate`) plus
(`TestUpdateGateStateIsCheckedBeforePermission`) for the case that fails both.

| tier | fields | `queued` · `paused` · `blocked` | `running` | `wrapped` · `failed` · `cancelled` |
|---|---|---|---|---|
| contract | `goal`, `wi_type` | reporter / project maintainer / admin only, else **403 `FORBIDDEN`** | **409 `CONFLICT_WI_ALREADY_CLAIMED`** — pause first | **409 `CONFLICT_TERMINAL_STATE`** |
| working | `content`, `labels`, `priority`, `milestone`, `requires_human_session`, `declared_resources` | allowed | allowed | **409 `CONFLICT_TERMINAL_STATE`** |
| record | `attrs`, `attrs_patch`, `attrs_unset` | allowed | allowed | **allowed** — the one exemption, and it is deliberate (`TestTerminalWorkItemKeepsTheAttrsWritePath`, `TestOnlyTheAttrsFieldsAreExemptOnATerminalWorkItem`) |

- **The strictest tier a patch touches governs the whole patch**, in both
  directions, by `internal/domain/work_items_update_gate_test.go`
  (`TestStrictestSuppliedEditTierGovernsTheWholePatch`). `attrs_patch`
  plus `labels` against a wrapped work item is refused whole rather than applied in
  part — driven against a real wrapped row, with the record-tier half asserted
  unchanged afterwards, by a subtest of
  `internal/domain/work_items_attrs_db_test.go`
  (`TestUpdateWorkItemAttrsPatch_WorksOnTerminalWorkItem`) — because
  a PATCH that wrote some fields and refused others would need a response shape
  that says which, and there is none.
- **`goal_change_reason`, `reclassify_reason` and `resources_version` carry no
  tier** — they write no column of their own, so sending one alone is a no-op
  rather than a refusal: the rider half of
  (`TestEveryWritableUpdateFieldHasATier`) compiles a request for each of the three
  and requires that it writes no column, and
  (`TestStrictestSuppliedEditTierGovernsTheWholePatch`) requires a riders-only patch
  to report no supplied tier at all, so there is nothing for the matrix to gate. The reason checks still run, and they run *after* the
  matrix: an edit refused on state is not first told its reason string is short.
- **Two codes were retired and are no longer produced anywhere:** 409
  `GOAL_CHANGE_NOT_ALLOWED` and 403 `WI_RECLASSIFY_FORBIDDEN` — "anywhere" is a
  claim about the tree, and it is measured over the tree by
  `internal/domain/update_wi_check_order_test.go`
  (`TestRetiredWorkItemErrCodesHaveNoProducerAnywhereInTheTree`), which walks every
  non-test `.go` file outside the one that declares them and carries a live code as
  its positive control; the narrower
  `internal/domain/work_items_update_gate_test.go`
  (`TestRetiredErrCodesAreNotProducedByTheUpdatePath`) is scoped to this one
  package. Each answered BOTH
  halves of its own field's gate, so a wrong CALLER on `goal` came back as a state
  conflict and a wrong STATE on `wi_type` came back as a permission failure — the
  conflation `aihub#242` had already removed from `pf_cancel_work_item`. The three
  codes in the table are that tool's own three, so one rule now covers both tools.
  The constants stay declared and mapped (`internal/domain/errors.go`
  (`ErrGoalChangeNotAllowed`)) so a stale client's branch still resolves.
- **`blocked` is new for the contract tier** (`TestUpdateGate` drives it beside
  queued and paused). `goal` and `wi_type` used to be
  refused there; a blocked work item has no live attempt to invalidate, which is
  the argument `aihub#242` already accepted for cancel.

## hop 5 — what comes back

The updated record, with one of two content treatments:
`internal/mcp/wi_echo_slim.go` (`dropContentEcho`) when `brief` is set, otherwise
(`suppressContentEcho`) which only removes a body the caller sent in this very call —
the three cases that separate those two are `internal/mcp/wi_echo_test.go`
(`TestUpdateBriefDropsContentTheCallerNeverSent`),
(`TestUpdateSuppressesTheContentItWasJustSent`) and
(`TestUpdateKeepsContentWhenTheStoredValueDiffers`).
`brief` is the WIDER rule — it drops the body whether or not the caller sent one and
whether or not what they sent matches what is stored — and that width, at the single
input where the equality gate alone would keep the body, is what
`internal/mcp/update_wi_wire_shape_test.go`
(`TestBriefDropsTheBodyWhereTheEqualityGateWouldKeepIt`) holds; branch ORDER is not
part of it, because that arm's own M22a mutant swapped the two branches and stayed
GREEN — either order ends in `dropContentEcho` once `brief` is set and the reply is
the same bytes.

`brief` here is **not** `pf_get_work_item`'s `brief`: this one reports
`content_len`, that one reports nothing, and both the published sentence saying so and
the two replies themselves are held by
(`TestPublishedBriefDifferenceFromGetIsTheEnforcedOne`), which drives both tools
against one served record. A work item with no body comes back as
`content: null` with no `content_len` (`TestDropContentEchoLeavesABodylessNullAloneAndReportsNoLength`),
so a missing `content_len` means "this wi has no body", never "the body was
withheld".

## Policy

- **§6.1 T1-9** — `kind`'s withdrawal is the rule applied: prose contradicting hop 3
  is a bug, and the legal dispositions are withdraw, fix, or file.
  <!-- prose-only: because=judgement -->
- **§6.1 T1-9, third application — `aihub#507` (2026-09-09).** The rule's other
  direction: not prose that outlived its behaviour, but a refusal that arrived
  without prose. The published `goal` description now says **non-empty**, because
  `""` went from stored to refused on this path and a caller holding the old
  contract would meet the new 400 by hitting it. The gate is
  `internal/mcp/goal_cap_publication_test.go` (`TestPublishedGoalCapIsTheEnforcedOne`),
  so the word cannot be edited out while the check stays. **Closed on the create side by `aihub#520`**
  (2026-09-09): `pf_create_work_item` and `pf_batch_create_work_items` share one
  `goal` string (`workItemFieldProps`) and now carry the word too, so all three
  tools that write this column publish the same refusal — which is the quantified
  loop of (`TestPublishedGoalCapIsTheEnforcedOne`) rather than three named tests,
  and it walks the live tool list so a fourth tool growing a `goal` is covered the
  day it appears. That was left as its own
  change because editing the shared string moves both of their
  `input_schema_sha256` and stales their cards, which its file scope had to
  include. The `aihub#507` gate was NAMED for this tool while it was the only one
  carrying the word; `aihub#520` folded it into the quantified loop above rather
  than leaving a second named test, which is what its own doc comment asked for.
  <!-- prose-only: because=history -->
- **§6.1 T1-9, second application — CLOSED by `aihub#474` (2026-09-09).** The
  published `goal` description used to state the status gate and nothing else,
  while `pf_create_work_item`'s stated both of its shape constraints. The
  disposition at the time was a split: document the refusal that existed, FILE the
  cap that did not. The filing came back "oversight" rather than "deliberate
  exemption", so the cap now exists on this path and the description states both
  constraints — and (`TestPublishedGoalCapIsTheEnforcedOne`) builds the number it
  looks for from `domain.MaxWorkItemGoalRunes()`
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

- **§6.4 item 2 — CLOSED by `aihub#447`, and this is the card it lands on.**
  <!-- prose-only: because=external-state -->
  T2-9's
  live side effect was a `pf_update_work_item` call sending **only** `work_item_id`
  and `attrs_patch` that moved `requires_human_session` from `null` to `true` and
  persisted it, with no field of that name anywhere in the request.
  Both parameters
  involved are published by this tool and documented at length above, so the README
  rule — a card touching a `§6.4` item says so here — points at this card.
  <!-- prose-only: because=judgement -->
  **It did
  not reproduce, and the write has a different author.** The audit's recipe was run
  on both arms it asks for: the same sequence (create with the field omitted,
  `attrs_patch`-only update through the MCP layer, then claim) against a server built
  from `origin/main` and against one built from a live-era commit, on a real
  database.
  <!-- prose-only: because=history -->
  The update left the column `NULL` on both; the CLAIM set it `true` on
  both, emitting `wi_classification_resolved` (`source: server_default`)
  sub-millisecond before `attempt_started`. That is the same ordered pair
  `aihub#411`'s own live timeline carries, one minute after it was filed and **51
  minutes before** the update it was attributed to.
  <!-- prose-only: because=external-state -->
  So the mechanism is
  `domain.FnClaimWorkItem`'s C-R9-12 fallback, which resolves an unclassified work
  item from a server default on first claim; it is still live and unchanged, and the
  value it resolves to is a constant pinned by
  `internal/domain/run_attempts_test.go` (`TestDefaultRequiresHumanSessionIsTrue`)
  and the event names that source rather than a derivation in
  (`TestClassificationResolvedEventPayloadOmitsWIType`) — while
  `domain.buildWorkItemUpdate`'s non-nil gate — untouched since 2026-08-11,
  identical on both arms, and the thing
  `internal/domain/update_wi_null_vs_omitted_test.go`
  (`TestAnExplicitNullOnAnUpdateIsIndistinguishableFromAnOmittedField`) compiles both
  spellings against — means "already fixed" was never an available reading
  either. Nothing on this tool's path was broken, so nothing on it was changed. What
  made the misattribution cheap is recorded above: this tool's reply carries
  `requires_human_session` whether or not the call wrote it, so a caller cannot tell
  a value it read from a value it wrote — two calls, one writing the field and one
  not, are compared key set against key set by
  `internal/mcp/update_wi_wire_shape_test.go`
  (`TestTheUpdateEchoCarriesRequiresHumanSessionEitherWay`).
- **§6.4 item 7 — CLOSED by `aihub#440`.** T2-1 left one sub-question open in
  **both** directions: whether `attrs` staying writable on a terminal work item is
  the defect or the feature.
  <!-- prose-only: because=judgement -->
  It is the **feature**, decided on traffic rather than
  taste. Measured over the 21-day transcript corpus (87 files, 738
  `pf_update_work_item` calls, 715 whose response carried a status): of the 49
  calls against a closed record, 28 carried `attrs_patch`, 20 `attrs`, 1
  `attrs_unset`, and **none** carried any other field. So the record tier is
  load-bearing — post-wrap decision and merge records are written through it,
  including by the batch that shipped this change — while the working tier's new
  refusal on a closed record breaks zero measured calls. `aihub#440` wrapped
  2026-09-08 and was re-checked the same day.
- **`milestone` is the one unexercised cell.**
  <!-- prose-only: because=measurement -->
  It appears in none of the 738
  measured calls, so its new terminal-state refusal rests on the tier argument
  alone rather than on observed traffic.
