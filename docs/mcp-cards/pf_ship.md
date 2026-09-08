# pf_ship — contract card

```json
{
  "tool": "pf_ship",
  "description_sha256": "4872cf049830e50729a25ac0cb0da301eaf7116985cd2f7e3dfed62192061004",
  "input_schema_sha256": "5133a04b1c0c49344a0ecc3bf3872bd72aaed84170905bb081ae4a8021bc698e",
  "params": {
    "message": {
      "type": "string",
      "required": true
    },
    "paths": {
      "type": "array",
      "required": false
    },
    "pr_base": {
      "type": "string",
      "required": false
    },
    "pr_body": {
      "type": "string",
      "required": true
    },
    "pr_title": {
      "type": "string",
      "required": true
    },
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
    "branch",
    "commit_sha",
    "committed",
    "error",
    "head_sha",
    "lock_gate",
    "lock_gate_detail",
    "locks_acquired_for",
    "ok",
    "pr",
    "pr_action",
    "pushed",
    "pushed_sha",
    "repo",
    "side_effects",
    "stage"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Eight parameters, five required. The description opens with a capitalised warning
because the fusion hides a force-push behind a word that does not imply one.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item's worktree |
| `repo` | string | yes | repository name |
| `message` | string | yes | commit message |
| `pr_title` | string | yes | PR title |
| `pr_body` | string | yes | PR body |
| `paths` | array | no | specific paths to stage (default: all) |
| `pr_base` | string | no | base for a NEW PR; unused when an open PR already covers the branch |
| `workspace_root` | string | no | workspace root path |

**Why a separate tool rather than `pf_commit(push=true, open_pr=true)`:** `pr_title`
and `pr_body` would then be required only when a flag is set, and `objectSchema`
renders a flat `required` array that cannot say so — the published schema would
understate its own contract. A tool named "commit" that force-pushes to origin also
hides exactly the property that most needs to stay visible.

## hop 2-3 — what leaves this process, and what binds it

`internal/coding/ship.go` (`Ship`) drives all three stages locally, with the same
lock gate `pf_commit` uses passed in as a callback. HTTP calls:

- `POST /v1/work_items/<id>/commit_locks` — the gate, bound by
  `internal/server/routes_step.go` (`handleReconcileCommitLocks`).
- `POST /v1/events` — up to three best-effort events (`commit`, `push`, `pr_opened`),
  so shipping in one call leaves the same timeline as shipping in three. Strictly a
  superset: this `push` event also carries `sha`, which `pf_push`'s does not.

## hop 4 — what it actually does

- **Idempotent by re-derivation, not by a stored cursor.** A retry commits only if
  something is staged and skips the push when a PR already covers HEAD, so retrying
  after a failure never duplicates a commit. An existing open PR on the branch is
  pushed to and reused rather than duplicated.
- **On failure the response is a JSON object, not an error string.** `stage` says
  which of commit/push/pr failed, and `side_effects` lists what already happened —
  typically a local commit that was never pushed. That mapping is
  `internal/mcp/tools_coding.go` (`shipPayload`), a plain function precisely because
  it is the whole load-bearing contract of the failure path: it stands between "push
  failed" and a caller that cannot tell whether a commit is sitting unpushed in its
  worktree.
- **`lock_gate` has five values here, two more than `pf_commit`'s three**: `covered`,
  `acquired`, `not_run`, `refused`, and `could_not_run` — the last meaning nothing
  was checked, with `lock_gate_detail` saying whether the check itself failed or the
  commit stage died before reaching it. `internal/mcp/tools_coding.go`
  (`commitStageErr`) narrows the failure to the commit stage, because a ship that
  staged nothing and failed at the push also leaves the gate un-invoked and there
  `not_run` is the true answer.
- `internal/mcp/tools_coding.go` (`shipSideEffects`) reports the retry case
  explicitly: no new commit was needed, but worktree HEAD is not on origin. Reporting
  "none" there would be a flat denial of undelivered work at the moment the caller is
  deciding whether to redo it.

## hop 5 — what comes back

Always a JSON object; the corpus record above is the union over 25 calls and
includes both the success keys and the failure keys, which is what "the failure path
is the deliverable" looks like in the data.

## Policy

- **§6.2 T2-15** — same `file_scope` gate as `pf_commit`, same de-locking ruling.
- **§6.1 T1-11** — never pre-declare a ratchet file; let this gate take it.
- **§6.1 T1-5** — no projection.

## Open

- **§6.4 item 6** — what the gate does with an advisory declaration is `aihub#416`'s
  question.
