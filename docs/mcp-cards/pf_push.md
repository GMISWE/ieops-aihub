# pf_push — contract card

```json
{
  "tool": "pf_push",
  "description_sha256": "d5328e449ba0e0023b20062aac567545c614441a908da1bfe12798f4f4bc3ba8",
  "input_schema_sha256": "0fe775364e2ef42df20532529ebac1a47967019cfcd0020bcad30b8908de893b",
  "params": {
    "repo": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": true
    },
    "workspace_root": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "advice",
    "base_sha_at_push",
    "branch",
    "error",
    "ok"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three parameters, two required, and a one-line description carrying two facts a
caller must not miss.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item's worktree |
| `repo` | string | yes | repository name |
| `workspace_root` | string | no | workspace root path |

**It is a `--force-with-lease` push whenever `origin` already holds the branch**, and
it **refuses `main`/`master`/`dev`/`tot`** — the published list and the set
`internal/coding/git_ops.go` really refuses are held equal in both directions by
`internal/mcp/push_lease_contract_test.go`
(`TestPublishedPushRefusalIsTheEnforcedSet`), and the refusal is an error result that
leaves the remote byte-for-byte alone (`TestPushRefusesAProtectedBranchAndLeavesOriginUntouched`).
Both are on the tool description rather than in a skill, because a caller who thinks
this is an ordinary push will use it to "sync" a branch and lose someone else's
commits.
**When `origin` has no such branch yet the push carries no lease**, deliberately: the
lease would be taken against the stale remote-tracking ref a delete-branch-on-merge
leaves behind and git refuses that push as `stale info`, which is how a wi whose PR
merged could never deliver a later commit (`aihub#226`, 2026-09-10 re-derived) — held
by `internal/mcp/push_lease_contract_test.go`
(`TestPushOfABranchDeletedOnOriginIsRecreatedWithoutALease`), whose fixture deletes the
branch through a second clone so the stale ref survives.

## hop 2-3 — what leaves this process, and what binds it

The push is local: `internal/coding/git_ops.go` (`GitCurrentBranch`) then
`internal/coding/git_ops.go` (`GitPush`) — driven end to end by
`internal/mcp/push_lease_contract_test.go`
(`TestPushEmitsOnePushEventAndMakesNoOtherRequest`), which counts the requests the
call really makes. The only HTTP call is the best-effort `push` event through
`internal/mcp/tools_coding.go` (`emitCodingEvent`) → `POST /v1/events`, bound by
`internal/server/routes_memory.go` (`handleEmitEvent`) — and "best-effort" is held to
its meaning by `internal/mcp/push_lease_contract_test.go`
(`TestPushEmitsOnePushEventAndMakesNoOtherRequest`), which drives that sink to a 500
and requires the call to answer `ok` anyway, because the push has already landed by
then.

No lock gate here: locks are taken at commit time, not at push time, so pushing
cannot widen the lock set.

## hop 4 — what it actually does

- Force-pushes the current branch of the work item's worktree with a lease, so a
  remote that moved under the caller rejects the push instead of overwriting it.
- **A base-moved rejection is not an error result.** It comes back as a JSON object
  carrying `error: "base_moved"` and advice to rebase and retry, so a caller keying on
  the marker keeps working; `internal/mcp/push_lease_contract_test.go`
  (`TestPushWhoseLeaseIsStaleAnswersBaseMovedAndOverwritesNothing`) drives a genuinely
  stale lease and reads the marker out of this sentence, and it asserts the other half
  of what the lease is for: the commit somebody else pushed is still on the remote
  afterwards.
- `internal/mcp/tools_coding.go` (`isBaseMoved`) recognises it, and is shared with
  `pf_ship` so the two cannot drift on the fragile half — recognising the condition —
  while deliberately keeping two response shapes, both of which
  `internal/mcp/push_lease_contract_test.go`
  (`TestBaseMovedRecognitionIsSharedByPushAndShipWithDifferentShapes`) drives from one
  fixture and requires to differ.
- `base_sha_at_push` is the worktree HEAD this push delivered, which is what
  `origin/<branch>` points at once it succeeds, so a later check can tell what the
  remote looked like at the moment of the push; the identity is held in both
  directions by `internal/mcp/push_lease_contract_test.go`
  (`TestPushBaseShaAtPushIsTheHeadItDelivered`), including the direction the key's own
  name invites: it is never the sha `origin` held BEFORE the push.

## hop 5 — what comes back

`{ok, branch, base_sha_at_push}` on success; `{error, advice}` on base-moved.
The corpus record above spans 527 calls at a 0.57% error rate.
`error`/`advice` appear in the observed key union precisely because the base-moved
path answers with a success-shaped result, and
`internal/mcp/push_lease_contract_test.go`
(`TestPushWhoseLeaseIsStaleAnswersBaseMovedAndOverwritesNothing`) holds that path's
keys inside the union the machine block declares.

## Policy

- **§6.1 T1-9** — the force-push and the branch refusal are published on the tool
  rather than described elsewhere, which is the standard that rule sets.
- **§6.2 T2-15** — no lock is taken here, which is why the de-locking ruling does not
  reach this tool.

## Open

- Nothing this card can settle.
