# pf_remove_dependency — contract card

```json
{
  "tool": "pf_remove_dependency",
  "description_sha256": "ae3ab1d207bbfc5ba3b44275554cc4c0df02336a63789bf8dd4cc2a63ee10a2d",
  "input_schema_sha256": "fd8248e6f947f18b6d02e8546a1c3e8a4ad86b2f2380af51e62a6fd0b54f15a4",
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
    }
  },
  "response_keys_observed": [
    "ok"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three parameters, all required, and no `note`: removing an edge records no reason.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `blocked_wi_id` | string | yes | the work item that is blocked |
| `blocking_wi_id` | string | yes | the work item that is blocking |
| `kind` | string | yes | `blocks\|supersedes\|related` |

"Authorized by project writer on the blocked item's project; **no run-attempt
credential is involved**" — a narrower statement than the create side's, because
removal needs no permission on the blocking item.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_dependency.go` (`registerDependencyTools`) passes all three as
**positional arguments** to `pkg/client/client.go` (`RemoveDependency`), which issues
`DELETE /v1/work_items/<blocked>/dependencies/<blocking>/<kind>`, bound by
`internal/server/router.go` (`handleDeleteDependency`).

Every parameter is a **path segment**; there is no body at all. That is what made
this tool the sharper half of the credential defect: the old code built
`attempt_id`, `claim_epoch` and `session_secret` into a body that `RemoveDependency`
never put on the wire — it issued the DELETE with a nil body and the client only
marshals a non-nil one — so the three fields were constructed and dropped inside the
same function.

## hop 4 — what it actually does

- Deletes the named edge. **Removing the last unfinished blocker requeues the blocked
  work item**, which is the effect a caller is usually after and which the create
  side's `blocked_by` description states.
- The requeue sweep is synchronous with the removal, not a background job, so the
  status change is visible on the next read.
- Deleting an edge that does not exist is not an error the schema warns about; the
  three-segment address means a typo in `kind` addresses a different edge rather than
  failing to parse.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.1 T1-9** — same application as the create side: fields that reached no reader
  were withdrawn, not re-described. Here they did not even reach the wire.
- **§6.2 T2-3** — outside the attempt-credential class by design.

## Open

- Whether a no-op delete should be distinguishable from a successful one is not
  covered by any adjudicated row, and the response does not say.
