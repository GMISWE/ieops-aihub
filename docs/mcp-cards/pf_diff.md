# pf_diff — contract card

```json
{
  "tool": "pf_diff",
  "description_sha256": "68b8ca01da9df6d8b77d37685932f706fa0537731eb233f6ec241a180df32b54",
  "input_schema_sha256": "7c248aa4ad5c48f0ecff2e62a1789471eb3e4828e2a78e1eb62596ace8afa60f",
  "params": {
    "repo": {
      "type": "string",
      "required": true
    },
    "vs_base": {
      "type": "boolean",
      "required": false
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
  "response_keys_observed": [],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Four parameters, two required. This is the only coding tool that makes **no HTTP
call at all**.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item's worktree |
| `repo` | string | yes | repository name |
| `workspace_root` | string | no | workspace root path |
| `vs_base` | boolean | no | diff against the base branch instead of HEAD |

## hop 2-3 — what leaves this process, and what binds it

Nothing leaves. `internal/mcp/tools_coding.go` (`registerCodingTools`) resolves a
path with `internal/coding/scenario.go` (`WorktreePath`) and runs
`internal/coding/git_ops.go` (`GitDiff`) locally.

There is **no hop 3** for this tool: no route binds it, no server field reads it, and
no credential is injected. `workspace_root` is the resolver's **fallback**, not its
primary: `WorktreePath` prefers the worktree map the claim recorded in the state file
and only reconstructs `pf.<project>-<seq>/<repo>` from `workspace_root` when that map
has no entry for this repo. So a `pf_force_takeover` that lost the map makes this tool
fail outright **unless** the caller supplies `workspace_root`, which is why the
takeover handler carries the map over rather than rebuilding the state file from
scratch.

The universal contract gate counts tools that make at least one HTTP call; this one
is in the minority that does not, and its floor is set below that count for exactly
that reason.

## hop 4 — what it actually does

- `vs_base=false` (default) diffs the working tree against HEAD — uncommitted work.
  `vs_base=true` diffs against the base branch, which is what a reviewer wants and
  what a "what did this wi change" question means.
- **The result is returned as raw text content, not JSON.** This is the one tool in
  the set whose successful result is a `TextContent` that is deliberately not a JSON
  object, so a caller parsing every result as JSON gets nothing from it.
- A path that exists but is not a usable git worktree fails here rather than being
  repaired. Claim-time verification is where that condition is diagnosed and
  reported, in `internal/mcp/tools_lifecycle.go` (`verifyClaimWorktree`).

## hop 5 — what comes back

Raw diff text. **`response_keys_observed` is an empty list, not `null`** — the
corpus has records for this tool, and the union of top-level JSON keys over them is
empty because every successful result is prose. The corpus README states that
explicitly: an empty list means "not measurable this way", not "returns nothing".

## Policy

- **§6.1 T1-5** — no projection; nothing to keep or delete.
- **§6.1 T1-9** — the description is one line and matches hop 4 exactly, so no
  disposition is needed.

## Open

- Nothing this card can settle.
