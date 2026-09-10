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
| `blocked_wi_id` | string | yes | the work item that is blocked — the direction driven end to end by `TestBlockedByIsMachineReadable` |
| `blocking_wi_id` | string | yes | the work item that is blocking — same arm, same edge, other end (`TestBlockedByIsMachineReadable`) |
| `kind` | string | yes | `blocks\|supersedes\|related` |

"Authorized by project writer on the blocked item's project; **no run-attempt
credential is involved**" — a narrower statement than the create side's, because
removal needs no permission on the blocking item.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_dependency.go` (`registerDependencyTools`) passes all three as
**positional arguments** to `pkg/client/client.go` (`RemoveDependency`), which issues
`DELETE /v1/work_items/<blocked>/dependencies/<blocking>/<kind>`, bound by
`internal/server/router.go` (`handleDeleteDependency`).

Every parameter is a **path segment**; there is no body at all, which
`internal/mcp/dependency_card_claims_test.go`
(`TestDependencyToolsAddressEdgesByPathAndSendNoCredential`) reads off the request
this tool really makes — the three ids in that order, and a nil body.
That absence is what made this tool the sharper half of the credential defect: the
old code built `attempt_id`, `claim_epoch` and `session_secret` into a body that
`RemoveDependency` never put on the wire, so the three fields were constructed and
dropped inside the same function.
<!-- prose-only: because=history -->

## hop 4 — what it actually does

- Deletes the named edge. **Removing the last unfinished blocker requeues the blocked
  work item**, which is the effect a caller is usually after and which the create
  side's `blocked_by` description states — held with its own remaining-blocker control
  by `internal/domain/dependencies_requeue_test.go`
  (`TestDeleteDependency_LastBlockerRemoved_Requeues` and
  `TestDeleteDependency_OtherBlockerRemains_StaysBlocked`).
- The requeue sweep is synchronous with the removal, not a background job, so the
  status change is visible on the next read.
- **Deleting an edge that matches no row answers `NOT_FOUND`** (both subtests of
  `TestDeleteDependency_OtherBlockerRemains_StaysBlocked`). The schema does not
  warn about it, and the three-segment address means a typo in `kind` addresses an
  edge that does not exist rather than failing to parse — so the answer is the same
  404 either way, and the real edge is untouched — both subtests of
  `internal/domain/dependencies_requeue_test.go`
  (`TestDeleteDependency_OtherBlockerRemains_StaysBlocked`), and the reason a `kind`
  is never refused on this side is that `DeleteDependency` validates nothing, unlike
  the create side — a difference held by
  `internal/domain/dependency_kind_refusal_test.go`
  (`TestDependencyKindIsValidatedOnCreateAndNotOnDelete`).

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.1 T1-9** — same application as the create side: fields that reached no reader
  were withdrawn, not re-described. Here they did not even reach the wire.
- **§6.2 T2-3** — outside the attempt-credential class by design.

## Open

- Whether a no-op delete should be distinguishable from a successful one is not
  covered by any adjudicated row. **This bullet used to add "and the response does not
  say", which was measured false on 2026-09-10** (`aihub#584`): a delete matching no
  row is `NOT_FOUND`, which this tool returns as an error result rather than as an
  `ok`, so the two are already distinguishable — what is unadjudicated is whether that
  is the right answer for a caller retrying a partially-failed cleanup.
