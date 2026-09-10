# pf_get_work_item — contract card

```json
{
  "tool": "pf_get_work_item",
  "description_sha256": "a3eca5e5ae430ceaee59a38c6683c39ec849c623957546282e99f9eb27268d21",
  "input_schema_sha256": "4d732bf5df271b72fd4e7474796cfc60c42f9aed6d1b6db7ce5beb0852a4da64",
  "params": {
    "brief": {
      "type": "boolean",
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

Two parameters. `aihub#385` audited this tool with **zero positive controls**, so
its "no defects found" verdict was explicitly recorded at reduced strength — that
is a property of the audit, not of the tool, and it is why this card leans on the
source rather than on that report.
<!-- prose-only: because=external-state -->

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | "Work item ID or slug" — both accepted |
| `brief` | boolean | no | "Omit the content field from the response (default false)" — the deletion held by `TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind` |

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) rejects an empty
`work_item_id` locally — `internal/mcp/get_work_item_shape_test.go`
(`TestGetWorkItemRefusesAnEmptyIdWithoutReachingTheServer`) asserts that on a request
COUNT of zero, because the router would answer the empty segment too and a status
alone cannot tell the two apart — and calls `pkg/client/client.go` (`GetWorkItem`) →
`GET /v1/work_items/<id>`, bound by `internal/server/router.go`
(`handleGetWorkItem`) and held routed by
`internal/mcp/universal_contract_gate_test.go`
(`TestContractEveryPathTheMCPLayerCallsIsRouted`). The id is a path segment.

**`brief` never leaves this process.** It is read here and applied to the decoded
result, and `internal/mcp/universal_contract_gate_test.go`
(`TestContractEveryPublishedParamLeavesTheProcess`) carries it as a named
local-consumption entry that is checked non-stale in both directions — an entry whose
parameter IS forwarded fails, and an entry naming a parameter no tool publishes fails
— while `internal/mcp/get_work_item_shape_test.go`
(`TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind`) drives the flag itself.
That is the correct landing point rather than a drop: the projection is a property of
what this process hands the model, and this process is the last hop before the model.
The same reasoning is written out at length for `pf_recall`'s `fields`, and the
contrast with `similarity_threshold` — which WAS published there, fully implemented in
domain, and carried by neither hop in between until both hops were wired to forward
it, which is why `pf_recall`'s own card states that contrast in the past tense — is
what makes the distinction worth stating.
<!-- prose-only: because=history -->

## hop 4 — what it actually does

The server resolves slug or canonical id — `internal/domain/work_item_ref_db_test.go`
(`TestGetWorkItemResolvesIDOrSlug`) drives both spellings including a slug that begins
with the id prefix, and `internal/domain/work_item_ref_policy_test.go`
(`TestNoWorkItemPrefixDispatch`) is the DB-free half that refuses any resolver
choosing a column by prefix — and returns the whole work-item record, including
`content`, `attrs`, `declared_resources` and `resources_version`.
`internal/mcp/get_work_item_shape_test.go`
(`TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind`) holds that in two
different directions rather than one: `id`, `slug`, `goal`, `attrs`,
`declared_resources` and `resources_version` must all survive the `brief` projection,
while `content` must come back on the plain call and must be ABSENT under `brief` —
deleting it is what the flag is for, so requiring it to survive both calls would be
requiring the flag not to work. Two consequences worth carding:

- **This is the tool that returns `content`** — and the list tool's projection never
  carries it, held by `TestWorkItemListSelectsCarryTheCASTokenAndNotTheBody`.
  `pf_list_work_items` will not do: its
  response is projected and its `content` is null by design, held by
  `internal/domain/list_work_items_select_columns_test.go`
  (`TestWorkItemListSelectsCarryTheCASTokenAndNotTheBody`), which censuses both
  full-record SELECTs and requires neither to project `wi.content`. ⚠️ CORRECTED
  2026-09-10: this bullet used to say this was the tool that returns
  `resources_version`, the compare-and-set token `pf_update_work_item` consumes, and
  that `pf_list_work_items` would not do for it. Measured wrong — both list queries
  project `wi.resources_version` and the projection keeps it deliberately
  (`internal/mcp/list_wi_slim_e2e_test.go`,
  `TestListWorkItemsResponseKeepsEveryConsumedField`, whose row for that field reads
  "compare-and-set guard for a `declared_resources` write read from this same
  response"), and the same census arm above is what now holds BOTH halves. `content`
  is the field `pf_list_work_items` cannot supply at all, and it is the reason the
  coding scenario's spec step names this tool.
  <!-- prose-only: because=cross-repo -->
- **`brief=true` deletes `content` outright and reports no length**
  (`TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind`). It is a
  different operation from `pf_update_work_item`'s `brief`, which replaces the body
  with `content_len` — `internal/mcp/get_work_item_shape_test.go`
  (`TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind`) asserts the ABSENCE of
  a `content_len` key as well as the absence of `content`, because reporting the
  length while deleting the body is the other tool's operation and is exactly the
  change a reader unifying the two flags would make, and it reads both published
  descriptions off a live session and requires them to promise different operations.
  A caller told the two are equivalent would apply that tool's "no `content_len`
  means no body" rule here and conclude a work item with a 4 KB body is empty.
  <!-- prose-only: because=counterfactual -->
  The two descriptions say so explicitly for that reason.

## hop 5 — what comes back

`jsonResult` with at most one key deleted. No slim function — unlike
`pf_list_work_items`, whose items drop every null-valued key. So on this tool an
absent key means the server did not send it, while on that one it means null. The
corpus record above spans 2,063 calls, the highest-volume tool in the census, at a
1.60% error rate.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape. This tool's `brief` is
  a delete, which is why it needs no keep-list maintenance:
  `internal/mcp/get_work_item_shape_test.go`
  (`TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind`) names the fields that
  must survive it, so a delete that widened past `content` is red without any list of
  what to keep.
- **§6.1 T1-1** — an argument no tool publishes is reported rather than rejected:
  sending `expected_version` here comes back under `request_adjusted` with `param:
  "unknown_params"`, held for every published tool at once by
  `internal/mcp/unknown_params_test.go`
  (`TestEveryRegisteredToolDisclosesUnknownParams`) with its negative control beside
  it (`TestUnknownParamsDisclosureIsSilentOnACleanCall`), which is what stops a field
  that always fires from counting as a disclosure. That is the only
  **unknown-argument** disclosure the repo makes and one of several
  `request_adjusted` disclosures, which is the population
  `internal/mcp/request_adjusted_writers_test.go`
  (`TestRequestAdjustedHasOneClampAppenderAndOneUnknownArgumentWriter`) enumerates.
  The mechanism has **five** writers, all of them censused by
  `internal/mcp/request_adjusted_writers_test.go`
  (`TestRequestAdjustedHasOneClampAppenderAndOneUnknownArgumentWriter`), which reports
  a fourth clamp — or a sixth writer of any shape — by name rather than absorbing it:
  three are clamp disclosures sharing one appender, `internal/domain/memory.go`
  (`Recall`) for `top_k`, `internal/domain/work_items.go` (`ListWorkItems`) for
  `limit` and `internal/domain/work_items.go` (`newReadyQueue`) for `max`, carded on
  `pf_recall`, `pf_list_work_items` and `pf_get_ready_queue` respectively; the fourth
  is this unknown-argument writer; and the fifth is neither kind and is carded on
  `pf_update_step` instead, `internal/server/routes_step.go` (`handleUpdateStep`)
  hand-building its own `domain.RequestAdjustment` entries for the `step_id` and
  `status` a heartbeat DISCARDS — a value the server declined to act on rather than
  one it clamped or one it did not recognise, and reachable only by a direct HTTP
  caller. The unknown-argument writer is its
  own site, `internal/mcp/unknown_params.go` (`unknownParamsField`), and the three
  clamps share `internal/domain/request_adjusted.go` (`appendIntAdjustment`), and
  `internal/mcp/request_adjusted_writers_test.go`
  (`TestRequestAdjustedHasOneClampAppenderAndOneUnknownArgumentWriter`) requires that
  separation in both directions as well as requiring the two kinds' `param` values to
  stay distinct. So a caller reading `request_adjusted` on any tool cannot infer it
  was sent an unknown argument; the `param` value is what tells the two apart, which
  `internal/mcp/unknown_params_test.go`
  (`TestUnknownParamsDisclosureKeepsTheServersOwnAdjustments`) holds on a single
  response carrying one entry of each kind.

## Open

- Nothing this card can settle. The `aihub#385` audit's control gap is recorded
  above so the "no defects" line is read at the strength it was given.
  <!-- prose-only: because=external-state --> That audit
  wrapped 2026-09-07; re-checked 2026-09-08.
