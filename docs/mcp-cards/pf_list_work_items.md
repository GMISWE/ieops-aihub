# pf_list_work_items — contract card

```json
{
  "tool": "pf_list_work_items",
  "description_sha256": "09f84e65e171800db67951190ddfc5b44c9472f91353ac8bab6e42be7f5e6ba7",
  "input_schema_sha256": "829bee6820dfd3cffc6b21b5403cda9746fef079839b5abdc3d81f16015a4dc1",
  "params": {
    "claimed_by": {
      "type": "string",
      "required": false
    },
    "cursor": {
      "type": "string",
      "required": false
    },
    "ids": {
      "type": "array",
      "required": false
    },
    "include_step_state": {
      "type": "boolean",
      "required": false
    },
    "kind": {
      "type": "string",
      "required": false
    },
    "label": {
      "type": "string",
      "required": false
    },
    "limit": {
      "type": "string",
      "required": false
    },
    "milestone": {
      "type": "string",
      "required": false
    },
    "min_similarity": {
      "type": "string",
      "required": false
    },
    "order": {
      "type": "string",
      "required": false,
      "enum": [
        "asc",
        "desc"
      ]
    },
    "owner_display": {
      "type": "string",
      "required": false
    },
    "priority": {
      "type": "string",
      "required": false
    },
    "project": {
      "type": "string",
      "required": false
    },
    "query": {
      "type": "string",
      "required": false
    },
    "ready_only": {
      "type": "boolean",
      "required": false
    },
    "reporter_display": {
      "type": "string",
      "required": false
    },
    "scenario": {
      "type": "string",
      "required": false
    },
    "similar_to": {
      "type": "string",
      "required": false
    },
    "since": {
      "type": "string",
      "required": false
    },
    "sort": {
      "type": "string",
      "required": false,
      "enum": [
        "closed_at",
        "created_at"
      ]
    },
    "source": {
      "type": "string",
      "required": false
    },
    "status": {
      "type": "string",
      "required": false
    },
    "user_id": {
      "type": "string",
      "required": false
    },
    "wi_type": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "items",
    "next_cursor",
    "semantic"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Twenty-four parameters — the largest published surface in the toolset, and the one
whose InputSchema carries an explicit byte budget
(`internal/mcp/tools_list_wi_schema_size_test.go`, 5,798 B as of `aihub#656`) because
a schema sits in the prefix of every request.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | no | optional when `ids` or `similar_to` is given (`TestListWorkItems_IdsWithoutProjectIsAllowedAndScoped`); otherwise required (`TestListWorkItems_NoProjectAndNoIdsStillRejected`) |
| `ids` | array | no | ids or slugs; a CSV string also accepted; makes `project` optional |
| `status` | string | no | comma-separated; an array is also accepted (`TestListWorkItemsCSVArgAcceptsArrayAndString`) |
| `wi_type` | string | no | filter by type |
| `kind` | string | no | DEPRECATED alias for `wi_type`; an explicit `wi_type` wins |
| `priority` | string | no | `urgent\|high\|normal\|low` |
| `milestone` | string | no | filter by milestone |
| `scenario` | string | no | in practice always `coding` |
| `label` | string | no | filter by label |
| `user_id` | string | no | **REPORTER only** — not attempt owner, not watchers |
| `claimed_by` | string | no | attempt-owner half of `user_id`'s gap: exact match on the CURRENT/LATEST attempt's `run_attempts.actor_user_id`, via `wi.current_attempt_id`; watchers still uncovered (`aihub#652`) |
| `owner_display` | string | no | display-name CONTAINS match (case-insensitive `ILIKE`) on the CURRENT/LATEST attempt's `run_attempts.actor_display`; shares its `LEFT JOIN` with `claimed_by` (`aihub#656`) |
| `reporter_display` | string | no | display-name CONTAINS match (case-insensitive `ILIKE`) on `wi.reporter_display` — the display-name analogue of `user_id`'s exact reporter-id match (`aihub#656`) |
| `source` | string | no | filter by source |
| `ready_only` | boolean | no | same PREDICATE as the ready queue, different page |
| `include_step_state` | boolean | no | attaches `step_state`; ABSENT means "no step state" |
| `since` | string | no | filters CREATED_AT, not close time |
| `query` | string | no | semantic search over goal+content |
| `similar_to` | string | no | document→document recall from a stored vector |
| `min_similarity` | string | no | opt-in cosine floor, `[0,1]`, 0 means OFF |
| `limit` | string | no | default 50, ceiling 200 (`TestNormalizeListWorkItemsLimit`); a number is accepted (`TestListWorkItemsForwardsRealSkillCallShapes` sends one) |
| `cursor` | string | no | carries the value of the column named by `sort` (`TestListWorkItemsNextCursor_UsesSortColumn`) |
| `sort` | enum | no | `created_at` \| `closed_at` |
| `order` | enum | no | `asc` \| `desc` |

Two of those descriptions are corrections rather than documentation. `user_id`'s
hop-4 predicate is `reporter_user_id` only, so "Filter by user ID" promised a
superset and the excess came back as a silent empty page — "which wis are mine"
returned the ones the caller filed and none they were working on;
`internal/domain/work_items_list_filters_test.go`
(`TestBuildListWorkItemsWhere_EveryFilterFieldReachesSQL`) binds the filter field to
that one column with its value bound as an argument, and
`internal/mcp/tools_update_wi_schema_test.go`
(`TestListWorkItemsUserIDDescriptionDisclosesReporterOnly`) requires the published
description to keep saying so. And `kind` is a deprecated FILTER alias here, a
different parameter from the `kind` that `pf_update_work_item` withdrew —
`internal/mcp/tools_list_wi_schema_test.go`
(`TestListWorkItemsPublishesBothTypeSpellings`) requires both spellings to be published
and `kind`'s own description to mark itself deprecated and point at `wi_type`, while
`internal/mcp/tools_update_wi_schema_test.go` (`TestUpdateWorkItemDoesNotPublishKind`)
holds the withdrawal on the other tool.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`buildListWorkItemsParams`) renders the arguments
into the query string for `pkg/client/client.go` (`ListWorkItems`) →
`GET /v1/work_items`, bound by `internal/server/router.go` (`handleListWorkItems`).

Three forwarding tables, and the split is the contract:

- `listWorkItemsStringParams` — forwarded verbatim via `scalarArg`, so a JSON number
  is accepted where a string is published, which
  `internal/mcp/tools_list_wi_schema_test.go`
  (`TestListWorkItemsForwardsEveryPublishedParamByValue`) drives one shape at a time
  off the `listWIWireProbes` table, including the `float64` spellings of `limit` and
  `min_similarity` that `strArg` would have dropped.
- `listWorkItemsBoolParams` — forwarded only when TRUE, and a value that is not a
  boolean is **rejected** rather than defaulted to false; both halves live in
  `internal/mcp/tools_list_wi_schema_test.go` — the omission
  (`TestListWorkItemsOmitsFalseBooleans`) and the refusal, in the `wantErr` rows
  `yes` and `2` that (`TestListWorkItemsForwardsEveryPublishedParamByValue`) runs.
  Defaulting is what made `ready_only: "true"` return the unfiltered list,
  indistinguishable from not sending it.
  <!-- prose-only: because=history -->
- `listWorkItemsCSVParams` (`ids`, `status`) — accept either a CSV string or a JSON
  array. Both shapes occur: every skill that filtered by status wrote
  `status=["wrapped"]`, `strArg` returned `""`, and the release flow listed the
  project's entire backlog instead of one release's worth.

Name agreement between those tables and the schema is hop 2 and is **not
sufficient** — it was green throughout the period `status=["wrapped"]` was being
discarded, because the name matched and the decoder could not read the shape.
<!-- prose-only: because=history -->
That is why `buildListWorkItemsParams` is a named function with a by-value test over
it: `internal/mcp/tools_list_wi_schema_test.go` calls it per shape
(`TestListWorkItemsForwardsEveryPublishedParamByValue`), per real skill call
(`TestListWorkItemsForwardsRealSkillCallShapes`) and per array-or-string decode
(`TestListWorkItemsCSVArgAcceptsArrayAndString`), and requires every published
parameter to have a value probe at all
(`TestListWorkItemsEveryPublishedParamHasAWireProbe`).

## hop 4 — what it actually does

- `query` and `similar_to` are **mutually exclusive** and neither combines with
  `sort`/`order`/`cursor` — `internal/server/routes_wi_similar_to_db_test.go`
  (`TestSimilarTo_RejectionsAreExplicitNeverSilent`) requires a 400 carrying the words
  "mutually exclusive" for the pair and a 400 for each of the three ordering
  parameters. `similarity` compares only WITHIN one result set, and unless
  `min_similarity` sets a floor ANY input returns a full page: the same file's
  (`TestSimilarTo_MinSimilarityIsReachableAndDefaultsOff`) asserts the default floor
  is 0 and that an orthogonal row — cosine exactly 0 — still comes back on the page,
  and (`TestSimilarTo_SemanticBlockDistinguishesThePaths`) pins the query-relative
  scale the response publishes. Judge by `semantic.ranked_candidates` and by reading
  the goals.
- `min_similarity` above 0 without `query` or `similar_to` is a 400, never a silent
  no-op — `internal/server/routes_wi_similar_to_db_test.go`
  (`TestSimilarTo_SemanticBlockDistinguishesThePaths`) sends it once alongside a
  `query=` the server can only answer from ILIKE and once entirely alone, and requires
  400 for both. 0 is the default, means OFF, and is always accepted. No globally valid
  value exists — measured, garbage and real queries overlap on every
  similarity-derived statistic — so none is ever defaulted.
- `limit` above 200 is served as 200 **and reported in `request_adjusted`**; a
  non-integer is 400 — `internal/domain/work_items_status_vocab_test.go`
  (`TestNormalizeListWorkItemsLimit`) tables the clamp,
  `internal/mcp/request_adjusted_e2e_db_test.go`
  (`TestE2ERequestAdjustedDisclosesListLimitClamp`) drives the disclosure end to end,
  `internal/mcp/request_adjusted_wiring_test.go`
  (`TestListWorkItemsResponseCarriesRequestAdjusted`) holds that the projection keeps
  it, and `internal/server/queryparam_policy_test.go`
  (`TestPolicyRule1_MalformedParamsAreRejectedEverywhere`) covers `limit=abc`,
  `limit=12abc` and `limit=true`. That is both numeric rules honoured on one
  parameter, and it stopped being the only such parameter when `aihub#432` gave
  `pf_get_ready_queue`'s `max` a `request_adjusted` entry of its own:
  `internal/domain/ready_queue_disclosure_test.go`
  (`TestReadyQueueDisclosesTheMaxItAdjusted`) holds that clamp and the same
  `internal/server/queryparam_policy_test.go` table covers `max=abc` and `max=5x`.
- `ids` bounds the query to projects the caller can see. An inaccessible `project=`
  answers 404 while ids the caller cannot see are silently omitted, and **neither
  says whether the thing exists** — `internal/domain/work_items_list_filters_test.go`
  (`TestBuildListWorkItemsWhere_AccessibleProjectsBoundsAnUnscopedQuery`) holds that
  the allow-list reaches the SQL as a project clause, and
  `internal/server/project_visibility_gate_test.go`
  (`TestProjectVisibility_NonMemberGetsTheSharedNotFound`) holds that the denial is
  the shared 404 and does not name the project it refused.
- `cursor` is refused with a 400 when it is not a token this endpoint issued
  (`aihub#382`, moved onto the shared reader by `aihub#435`), and when it is, the
  caller's own string is forwarded — trimmed, never re-serialised;
  `internal/server/list_work_items_cursor_test.go` holds the refusal before any query
  runs (`TestListWorkItems_UnparseableCursorRejectedBeforeDB`) and the verbatim
  forwarding of a token `next_cursor` would emit
  (`TestListWorkItems_WellFormedCursorReachesTheFilterVerbatim`), while
  `internal/server/cursor_validation_test.go`
  (`TestCursor_EveryCursorParamGoesThroughOneReader`) is what keeps this endpoint on
  the shared reader. The `::timestamptz` cast belongs to the domain, and re-printing a
  parsed timestamp here would be a second conversion upstream of the one that counts.
  <!-- prose-only: because=counterfactual -->
- `claimed_by` matches the CURRENT/LATEST attempt only — the predicate is
  `ra.id = wi.current_attempt_id` rather than `ra.work_item_id = wi.id`, a distinction
  pinned by mutation in `internal/server/routes_wi_list_params_db_test.go`
  (`TestListWorkItemsParams_EndToEnd`, `"claimed_by excludes a superseded claimant after
  reclaim"`), which reclaims a fixture item to a second actor and requires the original
  claimant's count to drop to 0 while the reclaimer's count becomes exactly 1 — the
  assertion the wrong predicate flips. It is exact equality rather than `ILIKE`, an id
  filter rather than a display-name search — the `ClaimedByUserID` case in
  `internal/domain/work_items_list_filters_test.go`
  (`TestBuildListWorkItemsWhere_EveryFilterFieldReachesSQL`) pins the rendered predicate
  as `ra.actor_user_id = $2`. It shares its `LEFT JOIN run_attempts` with `OwnerDisplay`
  behind one guard, so setting both filters at once does not double the join:
  `internal/domain/work_items_list_filters_test.go`
  (`TestBuildListWorkItemsWhere_ClaimedByAndOwnerDisplaySharesOneJoin`).
- `owner_display`/`reporter_display` (`aihub#656`) are CONTAINS matches rather than
  exact matches: `run_attempts.actor_display` and `wi.reporter_display` are each compared with
  `ILIKE` against a `%`-wrapped value rather than tested for equality, pinned by
  `internal/domain/work_items_list_filters_test.go`
  (`TestBuildListWorkItemsWhere_EveryFilterFieldReachesSQL`'s `ReporterDisplay` case
  and `TestBuildListWorkItemsWhere_ClaimedByAndOwnerDisplaySharesOneJoin`'s
  `OwnerDisplay` assertion). Both reached hop 3 for the first time in `aihub#656`:
  before it, `internal/server/router.go`'s hop-3 binding table had zero entries for
  either field (nor for `WatcherUserID` — see the Open section below), so a caller
  of the raw `/v1/work_items` endpoint got a 200 with an unfiltered page and no
  error — this wi's own defect, not a repeat of one `aihub#652` had already found.
  `claimed_by` is a shape worth copying (field, hop-3 wiring and MCP publication
  landed together in one commit, `b784a18`) but not a precedent for THIS silent-drop:
  that commit introduced `ClaimedByUserID` as a brand-new field, so there was never
  a point where it existed in the domain layer unwired at hop 3 — unlike
  `OwnerDisplay`/`ReporterDisplay`/`WatcherUserID` here, which did.
  <!-- prose-only: because=history -->

## hop 5 — what comes back

`internal/mcp/list_wi_slim.go` (`slimListWorkItemsResult`) drops every item key whose
value is null — `content` plus six that mean "none" — losslessly, by a per-value
check rather than by assertion, which `internal/mcp/list_wi_slim_e2e_test.go` holds in
both directions: the seven deletions with the reason each is lossless
(`TestListWorkItemsResponseDropsReconstructibleFields`) and the reverse half a
delete-everything projection would fail
(`TestListWorkItemsResponseKeepsEveryConsumedField`), with
`internal/mcp/list_wi_slim_test.go`
(`TestSlimListWorkItems_KeepsNonNullValuesOfNullDroppedFields`) covering the guard
those two are blind to. So on this tool **an absent key means null**, which the
description states as an invariant rather than as a field list: a checked-in list of
droppable fields would rot exactly as quietly as the response shape it describes.
`seq` and `scenario` are deliberately NOT among them despite passing the same rule,
held together in `internal/mcp/list_wi_slim_test.go`
(`TestSlimListWorkItems_KeepsValueGatedCandidates`) so that keeping one and dropping
the other — the revision this card is the record of — is red.

Since `aihub#360` (2026-09-12) a list that carries a `query` returns a SECOND
top-level section, `lexical`: verbatim-substring retrieval — every whitespace
token of the query must appear, case-insensitively, in goal+content — over the
same filtered scope, parallel to `items` and never merged into it
(`internal/domain/wi_lexical_db_test.go`). Its hits
carry no `similarity`, its `total` is explicit even at 0, and it is present
exactly when the request carried a non-empty `query=` (`similar_to` has no
query text and never gets one), whichever path served `items` — driven against
a live pgvector database by `internal/domain/wi_lexical_db_test.go`
(`TestListWorkItemsLexicalSectionRetrievesWhatTheVectorPathCannot`), whose
anchor reproduces the failure family `aihub#367` first measured on this tool
(the `0/6 at every N` figure it took on 2026-09-06 is withdrawn by
`aihub#677` — measured through the `aihub#648` serving defect, and after
`aihub#650` repaired it the same six frozen queries read 3/6 at @1 and 6/6 at
@5 per `aihub#660`; the anchor's geometry is built by a fake provider and never
rested on that reading): the vector page fills with decoys,
the parent work item is outside it, and the lexical section retrieves it — and
whose garbage-query arm holds the published advice, a full semantic page next
to an explicit `lexical.total: 0`. The last arm of
`TestListWorkItemsLexicalSectionRetrievesWhatTheVectorPathCannot` holds that
the caller's filters still scope the section (a `status` filter that excludes
the target empties it). A hit's `snippet` is an evidence line of at most 160 runes;
`wi.content` is read server-side to compute it and never travels, so the
"absent key means null" contract on `items` and the no-bodies property above
are both untouched — the hop-5 projection keeps forwarding unknown top-level
keys (`internal/mcp/list_wi_slim_test.go`,
`TestSlimListWorkItems_KeepsUnknownTopLevelKeys`), and the section's K10
declaration lives in `live-response-keys.json` because the generated corpus
predates the key (`internal/mcp/card_response_keys_live_e2e_db_test.go`).

That projection is why `content` is always null here:
`internal/domain/list_work_items_select_columns_test.go`
(`TestWorkItemListSelectsCarryTheCASTokenAndNotTheBody`) censuses both full-record
SELECTs, the paging one and the vector one, and requires neither to project
`wi.content`. The coding scenario's spec step is told to read a work item with
`pf_get_work_item` instead for that reason.
<!-- prose-only: because=cross-repo -->

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape; this is the tool it was
  proved on.
- **§6.1 T1-2 / T1-12** — `limit` is the reference implementation of both numeric
  rules: 400 on unparseable, clamp-and-disclose on out of range, held by the four arms
  the hop-4 bullet above names, of which `internal/server/queryparam_policy_test.go`
  (`TestPolicyRule1_MalformedParamsAreRejectedEverywhere`) is the one that sweeps the
  rule across every endpoint rather than this parameter alone.
- **§6.1 T1-6 — LANDED.** This endpoint got there first (`aihub#382`); `aihub#435`
  moved the check into `internal/server/queryparam.go` (`queryCursor`) and gave
  `pf_recall` and `pf_read_events`, which had none, the same one.
- **§6.1 T1-10** — the resident schema budget is a property of the whole
  `tools/list` payload, and `internal/mcp/tools_list_payload_budget_test.go`
  (`TestToolsListPayloadStaysWithinItsWireBudget`) is where that is now enforced,
  bounding the whole serialised payload and the largest single tool's share of it;
  this tool carries the only per-tool ceiling that existed before that ruling
  (`internal/mcp/tools_list_wi_schema_size_test.go`,
  `TestListWorkItemsSchemaStaysWithinItsWireBudget`), `pf_update_step` gained the
  second one in `aihub#543`'s first probe wave, and the ruling is to derive per-tool
  ceilings from a payload budget rather than to add 47 more constants.

## Open

- **§6.4 item 8** — the `user_id` predicate is corrected in the description only,
  and the two arms the hop-1 paragraph above names — the description
  (`TestListWorkItemsUserIDDescriptionDisclosesReporterOnly`) and the SQL
  (`TestBuildListWorkItemsWhere_EveryFilterFieldReachesSQL`) — are what keep the
  correction and the predicate agreeing. Widening user_id itself to cover attempt
  owner and watchers would still change every existing caller's result set and is
  deliberately not done. `aihub#652` (re-checked 2026-09-13) instead added
  `claimed_by` as a separate, additive parameter for the attempt-owner half —
  exact match on `run_attempts.actor_user_id` for the CURRENT/LATEST attempt,
  sharing one JOIN guard with `OwnerDisplay`
  (`TestBuildListWorkItemsWhere_ClaimedByAndOwnerDisplaySharesOneJoin`) so the two
  filters never double the `LEFT JOIN` when both are set. Watchers remain
  uncovered by any published filter, a scope decision rather than a defect.

  **Superseded in part by `aihub#656` (2026-09-13) — read this update, not just
  the paragraph above.** `aihub#656` found that `internal/server/router.go`'s
  hop-3 binding table had ZERO entries for `OwnerDisplay`, `ReporterDisplay`
  **and** `WatcherUserID`:
  all three were silently dropped over the raw `/v1/work_items` endpoint despite
  full domain-layer support already existing for each (`buildListWorkItemsWhere`).
  `OwnerDisplay`/`ReporterDisplay` are now both wired at hop 3
  (`internal/server/router_list_wi_params_test.go`'s
  `TestListWorkItems_EveryFilterParamReachesTheFilter`) *and* published as
  `owner_display`/`reporter_display` above, because neither has a contrary scope
  ruling. `WatcherUserID` is DIFFERENT: it is now wired at hop 3 too — the raw
  HTTP `watcher_user_id` query param reaches the filter (`buildListWorkItemsWhere`'s
  `wi_watches` semi-join, `TestBuildListWorkItemsWhere_WatcherIsAndedWithProjectScope`
  in `internal/domain/work_items_watching_test.go`) and is no longer silently
  dropped there — but it is deliberately **NOT** added as an `pf_list_work_items`
  tool parameter. The scope ruling in the paragraph above stands unchanged and is
  the reason: publishing a watcher filter here was a decision `aihub#652` declined
  to make, not an oversight it left behind, and fixing the unrelated hop-3 gap is
  not grounds to reverse that decision by accident.
  <!-- prose-only: because=judgement -->

  One more thing worth stating explicitly, since it is a fact about the HTTP
  surface rather than the schema decision above: at raw `/v1/work_items`,
  `watcher_user_id` now accepts an **arbitrary** user id — any string, not just
  the caller's own — which is a genuinely NEW capability this wi introduces at
  hop 3, not a restored one.
  <!-- prose-only: because=judgement -->
  No hop-0/1 (MCP schema) contract ever promised
  watcher-filtering over HTTP, and the only pre-existing consumer (`/ui`'s
  internal handler) always hardcoded the session's own user id, never an
  arbitrary one. This follows the same precedent `user_id`/`claimed_by` already
  set for accepting arbitrary ids at `/v1`, and it is not a privilege escalation:
  the result is still ANDed with the caller's project-access scope, same as
  every other filter.
  <!-- prose-only: because=judgement -->
  `internal/mcp/tools_list_wi_schema_test.go`
  (`TestListWorkItemsPublishesDisplayFiltersNotWatcher`) pins both halves of this
  — `owner_display`/`reporter_display` published, `watcher_user_id` not — so a
  future accidental addition of the latter shows up as a failing test rather than
  a silent schema change. See `internal/server/list_wi_filter_completeness_gate_test.go`
  for the general recurrence gate `aihub#656` added: every `ListWorkItemsFilter`
  field must be written to by `handleListWorkItems` (hop 3 exists for it), which
  proves a field is wired to *something* but never that it is wired under the
  *correct* param name — that half is still this file's and the hop-3/hop-4 test
  files' job.
