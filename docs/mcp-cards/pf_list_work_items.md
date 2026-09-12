# pf_list_work_items — contract card

```json
{
  "tool": "pf_list_work_items",
  "description_sha256": "4a2154c164801a0713044ec1702711b6535fd0a136fd4e0fd978d7b13809c183",
  "input_schema_sha256": "7d2ec8c0fba24eeb26e07d1706d57c6ff0e6ce940c50fd419460ef8a0a68dd70",
  "params": {
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

Twenty-one parameters — the largest published surface in the toolset, and the one
whose InputSchema carries an explicit byte budget
(`internal/mcp/tools_list_wi_schema_size_test.go`, 5,400 B) because a schema sits in
the prefix of every request.

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
anchor reproduces the failure family `aihub#367` measured WORST on this tool
(query= recall 0/6 at every N, 2026-09-06): the vector page fills with decoys,
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
  correction and the predicate agreeing. Widening it to cover attempt owner and
  watchers changes every existing caller's result set and is deliberately not done
  here.
