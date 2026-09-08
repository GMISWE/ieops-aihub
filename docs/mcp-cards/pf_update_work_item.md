# pf_update_work_item — contract card

```json
{
  "tool": "pf_update_work_item",
  "description_sha256": "29b3c7434085f3bd45cf9acebf95466a7fe1ae8757a6d86786d03886c6687c30",
  "input_schema_sha256": "0300196485444292f9a9e0a482a4140cc7c998fde93784f65c368b88b165a8ff",
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
| `goal` | string | no | only while status is queued or paused |
| `goal_change_reason` | string | no | required with `goal` |
| `priority` | enum | no | from the domain list |
| `milestone` | string | no | updated milestone |
| `wi_type` | string | no | updated type |
| `requires_human_session` | boolean | no | updated flag |
| `reclassify_reason` | string | no | required with a `wi_type` change, min 10 chars |
| `labels` | array | no | max from the domain constant |
| `declared_resources` | array | no | the whole list, replaced |
| `resources_version` | integer | no | CAS guard — omitting it overwrites unconditionally |
| `attrs` | object | no | **REPLACES** the whole object; unsent keys are DELETED |
| `attrs_patch` | object | no | shallow merge; `null` STORES a null rather than deleting |
| `attrs_unset` | array | no | applied AFTER `attrs_patch`, so a key in both is deleted |
| `content` | string | no | markdown ≤20000; not echoed back |
| `brief` | boolean | no | replaces the body with `content_len` |

`kind` is deliberately **absent**. It was published here and forwarded, but no
server struct has a `kind` json tag, so the value died at bind: 200, `wi_type`
untouched, no signal. It was measured live on a RUNNING work item — had it bound to
`wi_type`, the status gate would have rejected the call. Withdrawing the promise is
the fix; wiring it to `wi_type` would have opened a bypass around
`reclassify_reason`. `pf_list_work_items`' `kind` is a different parameter and stays.

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
  at the moment of the update (`aihub#264`). `git_branch` and `deploy_env` locks are
  not released that way and are held until the attempt ends.
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
- **`goal` and `wi_type` are status-gated and reason-gated.** A goal change on a
  running work item is refused, which is also why a terminal work item accepts
  `attrs` writes and almost nothing else.

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
- **§6.2 T2-1** — one editability matrix for the whole struct, one error code per
  rejection KIND (409 state, 403 permission), and no field silently exempt.
- **§6.1 T1-5** — both content treatments are deletes.

## Open

- **§6.4 item 7** — T2-1 leaves one sub-question open in **both** directions:
  whether `attrs` staying writable on a terminal work item is the defect or the
  feature. Existing tooling depends on that write path. The wi states it rather than
  choosing, and so does this card.
