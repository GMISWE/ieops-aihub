# pf_push — contract card

```json
{
  "tool": "pf_push",
  "description_sha256": "d5328e449ba0e0023b20062aac567545c614441a908da1bfe12798f4f4bc3ba8",
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

**It is a `--force-with-lease` push**, and it **refuses `main`/`master`/`dev`/`tot`**.
Both are on the tool description rather than in a skill, because a caller who thinks
this is an ordinary push will use it to "sync" a branch and lose someone else's
commits.

## hop 2-3 — what leaves this process, and what binds it

The push is local: `internal/coding/git_ops.go` (`GitCurrentBranch`) then
`internal/coding/git_ops.go` (`GitPush`). The only HTTP call is the best-effort
`push` event through `internal/mcp/tools_coding.go` (`emitCodingEvent`) →
`POST /v1/events`, bound by `internal/server/routes_memory.go` (`handleEmitEvent`).

No lock gate here: locks are taken at commit time, not at push time, so pushing
cannot widen the lock set.

## hop 4 — what it actually does

- Force-pushes the current branch of the work item's worktree with a lease, so a
  remote that moved under the caller rejects the push instead of overwriting it.
- **A base-moved rejection is not an error result.** It comes back as a JSON object
  carrying `error: "base_moved"` and advice to rebase and retry, so a caller keying on
  the marker keeps working. `internal/mcp/tools_coding.go` (`isBaseMoved`) recognises
  it, and is shared with `pf_ship` so the two cannot drift on the fragile half —
  recognising the condition — while deliberately not sharing a response shape.
- `base_sha_at_push` is returned so a later check can tell what the remote looked
  like at the moment of the push.

## hop 5 — what comes back

`{ok, branch, base_sha_at_push}` on success; `{error, advice}` on base-moved. The
corpus record above spans 527 calls at a 0.57% error rate, and `error`/`advice`
appear in the observed key union precisely because the base-moved path answers with
a success-shaped result.

## Policy

- **§6.1 T1-9** — the force-push and the branch refusal are published on the tool
  rather than described elsewhere, which is the standard that rule sets.
- **§6.2 T2-15** — no lock is taken here, which is why the de-locking ruling does not
  reach this tool.

## Open

- Nothing this card can settle.
