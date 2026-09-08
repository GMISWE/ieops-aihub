# pf_pr — contract card

```json
{
  "tool": "pf_pr",
  "description_sha256": "d98bc7d681ce9b2a0ef85aa0ddd2f616dd23d0774314b4fc529098e084b988d5",
  "input_schema_sha256": "43ced54eb8e2ed7a8090b2368e00a874792886c761df5155db3ab598a8f2ba73",
  "params": {
    "base": {
      "type": "string",
      "required": false
    },
    "body": {
      "type": "string",
      "required": true
    },
    "head": {
      "type": "string",
      "required": false
    },
    "repo": {
      "type": "string",
      "required": true
    },
    "title": {
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
    "baseRefName",
    "commits",
    "number",
    "state",
    "url"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Seven parameters, four required.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item's worktree |
| `repo` | string | yes | repository name |
| `title` | string | yes | PR title |
| `body` | string | yes | PR body |
| `head` | string | no | head branch (default: current) |
| `base` | string | no | base branch (default: the repo default branch) |
| `workspace_root` | string | no | workspace root path |

## hop 2-3 — what leaves this process, and what binds it

The PR is created by the **`gh` CLI**, not by aihub:
`internal/coding/gh_ops.go` (`GHCreatePR`) runs in the worktree. The only HTTP call
to aihub is the best-effort `pr_opened` event through
`internal/mcp/tools_coding.go` (`emitCodingEvent`) → `POST /v1/events`.

So there is no aihub hop 3 for `title`, `body`, `head` or `base`: they are `gh`
arguments. `internal/mcp/helpers.go` (`prPayload`) is what turns the `gh` result into
the event payload, which is the only aihub-side record that the PR exists.

## hop 4 — what it actually does

- Creates the PR and returns `gh`'s own JSON, which is why the observed response keys
  are GitHub's camelCase names (`baseRefName`, `commits`, `number`, `state`, `url`)
  rather than this repo's snake_case. **That is the only tool in the set whose
  response vocabulary is a third party's.**
- Failure to create the PR is a plain error result. Unlike `pf_ship`, there is no
  structured side-effect report, because a single-stage tool has nothing partial to
  report.
- The event is best-effort, so a PR can exist with no `pr_opened` on the timeline —
  which matters because after a wrap the timeline is the only durable record left.

## hop 5 — what comes back

`gh`'s object, unprojected. The corpus record above spans 662 calls at a 10.88%
error rate; `state` and `commits` in that union are GitHub's fields, not aihub's.

## Policy

- **§6.1 T1-5** — no projection, so nothing can be silently dropped here.
- **§6.2 T2-5** — `pr_opened` is another free-text event type with no published
  vocabulary; the ruling is to publish the vocabulary on `pf_emit_event`.

## Open

- Nothing this card can settle. The camelCase response is `gh`'s contract, not this
  repo's, and is not something an adjudicated row governs.
