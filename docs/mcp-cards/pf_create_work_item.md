# pf_create_work_item — contract card

```json
{
  "tool": "pf_create_work_item",
  "description_sha256": "2978a1542ea8458bd057eea171e82bbd04eecdec320281d9b37b3ce3c14b9d69",
  "input_schema_sha256": "bd546eb788a6c4f5062d6aa03698211e70f7a6b2b4b6ae2aa7983194026b8a19",
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
| `goal` | string | yes | single-line, non-empty, ≤500 chars |
| `scenario` | string | no | default `coding` |
| `priority` | enum | no | `urgent\|high\|normal\|low`, from the domain list |
| `wi_type` | string | no | `fix_bug`, `feature`, `chore`, … |
| `requires_human_session` | boolean | no | **three states**: `true`, `false`, or omitted (= `NULL`, *unclassified*) |
| `milestone` | string | no | milestone name |
| `labels` | array | no | max from the domain constant |
| `declared_resources` | array | no | `{type, uri, intent}` + optional `repo` |
| `parent_work_item_id` | string | no | parent wi |
| `source` | enum | no | how it was filed; a closed vocabulary |
| `attrs` | object | no | additional attributes; a non-object — including a JSON-encoded string of one — is a 400 |
| `blocked_by` | array | no | creates real `blocks` edges and sets status=blocked |
| `content` | string | no | markdown ≤20000 chars; **not echoed back** |
| `force_create` | boolean | no | bypass the duplicate check |
| `force_reason` | string | no | required by the server with `force_create` |

Two of those are enums rather than prose *because* prose failed: `priority` was a
pipe-separated string in a description, and `source` read as free text while being a
closed vocabulary — sending `jira` instead of `sync_jira` was a 500.

⚠️ The enum is what a caller is OFFERED, not what stops it. `aihub#396` recorded that
"the SDK validates an enum before the handler runs and cannot validate prose", and
that reason is **false for this codebase** — corrected here by `aihub#496`
(2026-09-09). `aihub#463` measured it on go-sdk v1.6.0 (2026-09-08):
`applySchema -> resolved.Validate` is reached only from the generic
`AddTool[In, Out]`, while polyforge registers through the untyped
`(*mcp.Server).AddTool`, whose `callTool` hands the request straight to the handler
with no schema step. An out-of-vocabulary value still arrives in the `POST` body.

What the enum is actually worth is unchanged and still worth having: it is how a
caller — an LLM reading `tools/list`, and any client that validates before sending —
learns the set without spending a round trip. The refusal is the server-side Go
validator, which answers 400 naming the field. Both halves are required; publishing
the enum alone would move a 500 nowhere.

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

`declared_resources` entries deliberately carry NEITHER `base_branch` NOR
`task_branch`. Each was published and read by nothing at the point it was withdrawn:
`base_branch` never had a reader at all (`aihub#395`), and `task_branch` lost its
only one when `aihub#416` retired the `git_branch` lock a repo entry derived. Both
struct fields and their decoder stay, so stored payloads carrying either value keep
round-tripping — deleting a field would drop it from every stored payload that has
one, which is a data loss to fix a documentation defect.

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
  it is the field that determines whether an agent is ever dispatched to it — and it
  has **three** states, not the two its `boolean` type suggests (`aihub#411` T2-9,
  published by `aihub#447`). Omitting it stores `NULL`, which is not a default of
  `false`: the work item goes to `unclassified[]` rather than `items[]`, and
  `ready_only` excludes it. `NULL` is also not permanent — `domain.FnClaimWorkItem`
  resolves it to `true` from a server default on the FIRST claim, writes it back and
  emits `wi_classification_resolved`, so a work item that has ever been claimed
  cannot still be unclassified. That claim-path write is what `aihub#411` T2-9
  recorded as an unexplained side effect of an unrelated `pf_update_work_item` call;
  see this card's Policy section.
- **`attrs` must be a JSON object** (`aihub#465`). It binds to a bare
  `json.RawMessage` and is assigned straight into the jsonb column, so before that
  work item a JSON-encoded STRING of an object was stored verbatim under a 200 and
  every reader that expects an object got a string. The rejection is the same one
  the PATCH path gives — type, byte length, and `details.string_decodes_to` saying
  whether the quoted text was itself valid JSON — and nothing is coerced. An
  omitted `attrs` is still defaulted to `{}`, and a literal `null` is unchanged.

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
- **§6.1 T1-9 — `aihub#520` (2026-09-09).** The `goal` description now says
  **non-empty**. The refusal is not new here, only unpublished: the handler rejects
  `goal: ""` locally with `goal is required` before the request leaves the process,
  and `internal/domain/work_item_fields.go` (`validateWorkItemGoalPresent`) answers
  400 with the same text on every path that writes the column. But `required` in a
  published schema means the property must be PRESENT, not that its value must be
  non-empty, so a caller reading the schema could satisfy it with `""` and be
  refused anyway. `aihub#507` made the behaviour identical on
  `pf_update_work_item` and stated the word only there, leaving this tool and
  `pf_batch_create_work_items` — which share one string via `workItemFieldProps` —
  to a change that could carry their two cards.
  `internal/mcp/goal_cap_publication_test.go` (`TestPublishedGoalCapIsTheEnforcedOne`)
  now quantifies the word over every tool publishing `goal`, so the three
  descriptions cannot drift apart again.
- **§6.2 T2-6** — the memory-type leniency question is adjacent: an enum in a schema
  the SDK will not enforce should not be called an enum. Here the SDK **does**
  enforce `priority` and `source`, which is the difference.
- **§6.2 T2-9** — the ruling was *state the third state in the schema (`omit ⇒
  unclassified, not dispatched`)*, and the description above now does. Its second
  half — the one-shot live observation that an `attrs_patch`-only
  `pf_update_work_item` moved a `NULL` to `true` — was settled by `aihub#447` and
  **did not reproduce on either arm**. Same sequence, twice: create with the field
  omitted, `attrs_patch`-only update through the MCP layer, then claim; run against a
  server built from `origin/main` and against one built from a live-era commit. The
  update left the column `NULL` both times and the claim set it `true` both times,
  emitting `wi_classification_resolved` sub-millisecond before `attempt_started` —
  the same ordered pair the live timeline of `aihub#411` itself carries, 51 minutes
  BEFORE the update it was attributed to. So the write is real, has a named
  mechanism, and is not on the update path; nothing was "fixed", because nothing on
  that path was broken.

## Open

- Nothing this card can settle. `scenario` accepting values no row can hold is
  recorded rather than fixed: narrowing it is a contract change.
- The claim-path resolution itself is recorded, not re-opened. It is a constant
  (`domain.defaultRequiresHumanSession`), the server has no second source to check a
  classification against, and `domain.ErrRequiresHumanSessionMismatch` documents at
  length why the 409 that was supposed to police it never fired. Whether an
  unclassified work item should be resolvable by a claim at all is a design
  question this card only makes visible.
