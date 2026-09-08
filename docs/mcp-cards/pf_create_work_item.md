# pf_create_work_item — contract card

```json
{
  "tool": "pf_create_work_item",
  "description_sha256": "2978a1542ea8458bd057eea171e82bbd04eecdec320281d9b37b3ce3c14b9d69",
  "params": {
    "attrs": {
      "type": "object",
      "required": false
    },
    "blocked_by": {
      "type": "array",
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
    "force_create": {
      "type": "boolean",
      "required": false
    },
    "force_reason": {
      "type": "string",
      "required": false
    },
    "goal": {
      "type": "string",
      "required": true
    },
    "labels": {
      "type": "array",
      "required": false
    },
    "milestone": {
      "type": "string",
      "required": false
    },
    "parent_work_item_id": {
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
    "project": {
      "type": "string",
      "required": true
    },
    "requires_human_session": {
      "type": "boolean",
      "required": false
    },
    "scenario": {
      "type": "string",
      "required": false
    },
    "source": {
      "type": "string",
      "required": false,
      "enum": [
        "admin",
        "auto_debug",
        "auto_execute",
        "auto_review",
        "human",
        "sync_github",
        "sync_jira"
      ]
    },
    "wi_type": {
      "type": "string",
      "required": false
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

Sixteen top-level parameters, fifteen of which come from
`internal/mcp/tools_lifecycle.go` (`workItemFieldProps`) — one definition shared with
`pf_batch_create_work_items`, because two hand-maintained copies would drift and a
field present on one tool but not the other is the same silent drop the batch tool
exists downstream of.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | project to file under |
| `goal` | string | yes | single-line, ≤500 chars |
| `scenario` | string | no | default `coding` |
| `priority` | enum | no | `urgent\|high\|normal\|low`, from the domain list |
| `wi_type` | string | no | `fix_bug`, `feature`, `chore`, … |
| `requires_human_session` | boolean | no | routes the wi to a human rather than the auto queue |
| `milestone` | string | no | milestone name |
| `labels` | array | no | max from the domain constant |
| `declared_resources` | array | no | `{type, uri, intent}` + optional `repo` |
| `parent_work_item_id` | string | no | parent wi |
| `source` | enum | no | how it was filed; a closed vocabulary |
| `attrs` | object | no | additional attributes |
| `blocked_by` | array | no | creates real `blocks` edges and sets status=blocked |
| `content` | string | no | markdown ≤20000 chars; **not echoed back** |
| `force_create` | boolean | no | bypass the duplicate check |
| `force_reason` | string | no | required by the server with `force_create` |

Two of those are enums rather than prose *because* prose failed: `priority` was a
pipe-separated string in a description, and `source` read as free text while being a
closed vocabulary — sending `jira` instead of `sync_jira` was a 500. The SDK
validates an enum before the handler runs and cannot validate prose.

`blocked_by` states what it DOES, not merely what it is a list of, because
`aihub#357` was filed on the belief that it only flipped `status`.

## hop 2-3 — what leaves this process, and what binds it

The handler checks `project` and `goal` locally, applies `applyForceReasonDefault`
(the server requires ≥10 chars of reason and rejects without one), and passes the
**whole argument map** to `pkg/client/client.go` (`CreateWorkItem`) →
`POST /v1/work_items`, bound by `internal/server/router.go` (`handleCreateWorkItem`).

Because the map is forwarded wholesale there is no forwarding table to drift from —
every published property is on the wire by construction. The risk on this tool is
therefore at hops 1 and 4, not hop 2.

`declared_resources` entries carry `task_branch`, which since `aihub#356` is only a
FALLBACK for lock-key derivation, and deliberately no longer carry `base_branch`:
that field was published and read by nothing, so a caller who set it got a worktree
off the hard-coded `origin/main` with no error. The struct field and decoder stay,
so stored payloads keep round-tripping.

## hop 4 — what it actually does

- **Duplicate detection runs by default** and a 409 DUPLICATE/CANDIDATES is a normal
  outcome, not a fault. `force_create` bypasses it and needs a reason.
- **`blocked_by` is transactional with the create**: each entry becomes a real
  `blocks` edge and a `dependency_created` event on the new wi's timeline, a
  non-empty list makes the wi `status=blocked`, and removing the last unfinished
  blocker requeues it. An entry naming no work item is rejected **and the wi is NOT
  created**.
- **`scenario` is effectively fixed.** The column is CHECKed to
  `coding|writing|data` and creation rejects everything but `coding`, so the
  parameter accepts a value no row can hold.
- **`requires_human_session` decides which ready-queue section the wi lands in**, so
  it is the field that determines whether an agent is ever dispatched to it.

## hop 5 — what comes back

The whole created record, minus one thing: `internal/mcp/wi_echo_slim.go`
(`suppressContentEcho`) removes `content` when the caller sent it. The caller sent
that body one line ago, so echoing it is pure cost; the record is otherwise
complete. There is no `brief` counterpart here because there is nothing for it to do
— a work item's content at creation is whatever the caller supplied, so an unsent
content is an absent one.

## Policy

- **§6.1 T1-4** — the ruling is to adopt `aihub#396`'s policy row verbatim and gate
  it by **enumerating DB CHECKs, not fields**; `scenario` above is a live instance of
  a CHECK the published schema does not reflect.
- **§6.1 T1-5** — the content suppression is a delete, not a keep-list.
- **§6.2 T2-6** — the memory-type leniency question is adjacent: an enum in a schema
  the SDK will not enforce should not be called an enum. Here the SDK **does**
  enforce `priority` and `source`, which is the difference.

## Open

- Nothing this card can settle. `scenario` accepting values no row can hold is
  recorded rather than fixed: narrowing it is a contract change.
