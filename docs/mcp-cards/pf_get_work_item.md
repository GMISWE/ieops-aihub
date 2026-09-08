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

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | "Work item ID or slug" — both accepted |
| `brief` | boolean | no | "Omit the content field from the response (default false)" |

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) rejects an empty
`work_item_id` locally and calls `pkg/client/client.go` (`GetWorkItem`) →
`GET /v1/work_items/<id>`, bound by `internal/server/router.go`
(`handleGetWorkItem`). The id is a path segment.

**`brief` never leaves this process.** It is read here and applied to the decoded
result. That is the correct landing point rather than a drop: the projection is a
property of what this process hands the model, and this process is the last hop
before the model. The same reasoning is written out at length for `pf_recall`'s
`fields`, and the contrast with `similarity_threshold` — published, implemented in
domain, and carried by neither hop in between — is what makes the distinction worth
stating.

## hop 4 — what it actually does

The server resolves slug or canonical id and returns the whole work-item record,
including `content`, `attrs`, `declared_resources` and `resources_version`. Two
consequences worth carding:

- **This is the tool that returns `resources_version`,** which is the compare-and-set
  token `pf_update_work_item` consumes. `pf_list_work_items` will not do: its
  response is projected and its `content` is null by design, which is also why the
  spec step of the coding scenario says to use this call and not that one.
- **`brief=true` deletes `content` outright and reports no length.** It is NOT the
  same operation as `pf_update_work_item`'s `brief`, which replaces the body with
  `content_len`. A caller told the two are equivalent would apply that tool's "no
  `content_len` means no body" rule here and conclude a work item with a 4 KB body
  is empty. The two descriptions say so explicitly for that reason.

## hop 5 — what comes back

`jsonResult` with at most one key deleted. No slim function — unlike
`pf_list_work_items`, whose items drop every null-valued key. So on this tool an
absent key means the server did not send it, while on that one it means null. The
corpus record above spans 2,063 calls, the highest-volume tool in the census, at a
1.60% error rate.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape. This tool's `brief` is
  a delete, which is why it needs no keep-list maintenance.
- **§6.1 T1-1** — an argument no tool publishes is reported, not rejected: sending
  `expected_version` here comes back under `request_adjusted` with `param:
  "unknown_params"`, and that disclosure is the only one the repo makes.

## Open

- Nothing this card can settle. The `aihub#385` audit's control gap is recorded
  above so the "no defects" line is read at the strength it was given.
