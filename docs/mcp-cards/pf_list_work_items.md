# pf_list_work_items — contract card

```json
{
  "tool": "pf_list_work_items",
  "description_sha256": "907e2546b613d754fe8d31d24223eded70811856dec589b33f42fd29d65d64f4",
  "input_schema_sha256": "5ea984a67dd4a0af8d723b6859fdc45f6335557b8124c2412f38ceb7e8666d7d",
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
| `project` | string | no | optional when `ids` or `similar_to` is given; otherwise required |
| `ids` | array | no | ids or slugs; a CSV string also accepted; makes `project` optional |
| `status` | string | no | comma-separated; an array is also accepted |
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
| `limit` | string | no | default 50, ceiling 200; a number is accepted |
| `cursor` | string | no | carries the value of the column named by `sort` |
| `sort` | enum | no | `created_at` \| `closed_at` |
| `order` | enum | no | `asc` \| `desc` |

Two of those descriptions are corrections rather than documentation. `user_id`'s
hop-4 predicate is `reporter_user_id` only, so "Filter by user ID" promised a
superset and the excess came back as a silent empty page — "which wis are mine"
returned the ones the caller filed and none they were working on. And `kind` is a
deprecated FILTER alias here, which is a different parameter from the `kind` that
`pf_update_work_item` withdrew.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`buildListWorkItemsParams`) renders the arguments
into the query string for `pkg/client/client.go` (`ListWorkItems`) →
`GET /v1/work_items`, bound by `internal/server/router.go` (`handleListWorkItems`).

Three forwarding tables, and the split is the contract:

- `listWorkItemsStringParams` — forwarded verbatim via `scalarArg`, so a JSON number
  is accepted where a string is published.
- `listWorkItemsBoolParams` — forwarded only when TRUE, and a value that is not a
  boolean is **rejected** rather than defaulted to false. Defaulting is what made
  `ready_only: "true"` return the unfiltered list, indistinguishable from not
  sending it.
- `listWorkItemsCSVParams` (`ids`, `status`) — accept either a CSV string or a JSON
  array. Both shapes occur: every skill that filtered by status wrote
  `status=["wrapped"]`, `strArg` returned `""`, and the release flow listed the
  project's entire backlog instead of one release's worth.

Name agreement between those tables and the schema is hop 2 and is **not
sufficient** — it was green throughout the period `status=["wrapped"]` was being
discarded, because the name matched and the decoder could not read the shape. That
is why `buildListWorkItemsParams` is a named function with a by-value test over it.

## hop 4 — what it actually does

- `query` and `similar_to` are **mutually exclusive** and neither combines with
  `sort`/`order`/`cursor`. `similarity` compares only WITHIN one result set; there is
  no relevance filter, so ANY input returns a full page. Judge by
  `semantic.ranked_candidates` and by reading the goals.
- `min_similarity` above 0 without `query` or `similar_to` is a 400, never a silent
  no-op. 0 is the default, means OFF, and is always accepted. No globally valid
  value exists — measured, garbage and real queries overlap on every
  similarity-derived statistic — so none is ever defaulted.
- `limit` above 200 is served as 200 **and reported in `request_adjusted`**; a
  non-integer is 400. That is both numeric rules honoured on one parameter, and it
  is the shape `pf_get_ready_queue`'s `max` does not have.
- `ids` bounds the query to projects the caller can see. An inaccessible `project=`
  answers 404 while ids the caller cannot see are silently omitted, and **neither
  says whether the thing exists**.
- `cursor` is refused with a 400 when it is not a token this endpoint issued
  (`aihub#382`, moved onto the shared reader by `aihub#435`), and when it is, the
  caller's own string is forwarded — trimmed, never re-serialised. The
  `::timestamptz` cast belongs to the domain, and re-printing a parsed timestamp
  here would be a second conversion upstream of the one that counts.

## hop 5 — what comes back

`internal/mcp/list_wi_slim.go` (`slimListWorkItemsResult`) drops every item key whose
value is null — `content` plus six that mean "none" — losslessly, by a per-value
check rather than by assertion. So on this tool **an absent key means null**, which
the description states as an invariant rather than as a field list: a checked-in
list of droppable fields would rot exactly as quietly as the response shape it
describes. `seq` and `scenario` are deliberately NOT among them despite passing the
same rule.

That projection is why `content` is always null here and why the spec step is told
to read a work item with `pf_get_work_item` instead.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape; this is the tool it was
  proved on.
- **§6.1 T1-2 / T1-12** — `limit` is the reference implementation of both numeric
  rules: 400 on unparseable, clamp-and-disclose on out of range.
- **§6.1 T1-6 — LANDED.** This endpoint got there first (`aihub#382`); `aihub#435`
  moved the check into `internal/server/queryparam.go` (`queryCursor`) and gave
  `pf_recall` and `pf_read_events`, which had none, the same one.
- **§6.1 T1-10** — the resident schema budget is a property of the whole
  `tools/list` payload; this tool carries the only per-tool ceiling that existed
  before that ruling, and the ruling is to derive per-tool ceilings from a payload
  budget rather than to add 47 more constants.

## Open

- **§6.4 item 8** — the `user_id` predicate is corrected in the description only.
  Widening it to cover attempt owner and watchers changes every existing caller's
  result set and is deliberately not done here.
