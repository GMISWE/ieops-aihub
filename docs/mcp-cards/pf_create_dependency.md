# pf_create_dependency — contract card

```json
{
  "tool": "pf_create_dependency",
  "description_sha256": "1ad7c74d33d5532547250635a85b879f3b9345891d941582ed7f8aa4ed474a70",
  "input_schema_sha256": "2b6ea1a4abb0e47acbeb9e505a981235633faf0a76843c3f874c4d98d444c42f",
  "params": {
    "blocked_wi_id": {
      "type": "string",
      "required": true
    },
    "blocking_wi_id": {
      "type": "string",
      "required": true
    },
    "kind": {
      "type": "string",
      "required": true
    },
    "note": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "ok"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Four parameters, three required. The description states the authorization model
inline, which is unusual and deliberate.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `blocked_wi_id` | string | yes | the work item that is blocked |
| `blocking_wi_id` | string | yes | the work item that is blocking |
| `kind` | string | yes | `blocks\|supersedes\|related` |
| `note` | string | no | optional note |

"Authorized by project role (writer on the blocked item's project, viewer on the
blocking item's if they differ); **no run-attempt credential is involved**" is the
sentence that matters. `kind` is prose rather than an enum, so an out-of-vocabulary
value is not refused before the handler runs.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_dependency.go` (`registerDependencyTools`) checks the three
required fields locally, builds `{blocked_wi_id, blocking_wi_id, kind}` plus `note`
when non-empty, and calls `pkg/client/client.go` (`CreateDependency`) →
`POST /v1/work_items/<blocked>/dependencies`, bound by
`internal/server/router.go` (`handleCreateDependency`).

**Both handlers in this file used to build `attempt_id`, `claim_epoch` and
`session_secret` into the request, and took a required `work_item_id` whose only
purpose was naming the state file to read them out of. Nothing consumed any of it.**
On the remove side the client did not even put the body on the wire. Those fields
were deleted rather than made real: a credential that nothing checks is worse than
no credential at all, because a reader — including a reviewer — concludes the path is
attempt-gated and the code shape agrees with them.

## hop 4 — what it actually does

- Creates a real dependency edge and records a `dependency_created` event on the
  timeline. A `blocks` edge with an unfinished blocker puts the blocked item in
  `status=blocked`; removing the last unfinished blocker requeues it.
- **The same effect is reachable at creation time** through `pf_create_work_item`'s
  `blocked_by`, which exists precisely so a plan can create child work items in
  topological order without needing cross-project dependency permissions afterwards.
- Cross-project edges are permitted with `viewer` on the blocking side; the read side
  folds items the caller cannot see rather than erroring.
- The model in force — project `writer` on the blocked item's project, nothing else —
  is pinned by `internal/mcp/dependency_authz_e2e_db_test.go`.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.1 T1-9** — the credential removal is that rule's clearest application: the
  prose said "attempt-gated", hop 3 read nothing, and the fix was to withdraw the
  fields rather than to soften the description.
- **§6.2 T2-3** — this path is deliberately outside the attempt-credential class, so
  a single credential status code does not apply to it.

## Open

- `kind` is not an enum and the server does not reject an unknown value before the
  handler. Whether it should be is not covered by any adjudicated row.
