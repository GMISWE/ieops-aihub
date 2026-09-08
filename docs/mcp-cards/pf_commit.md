# pf_commit — contract card

```json
{
  "tool": "pf_commit",
  "description_sha256": "5f44b1634c4eb4dfcbdce62c657b98cbe3cbb73e3fbd40238b7460fc5d04669e",
  "input_schema_sha256": "efb55cef0d5dd923b0e8c7447968212c86a2e60bdcf5342ee087f602af2d8545",
  "params": {
    "message": {
      "type": "string",
      "required": true
    },
    "paths": {
      "type": "array",
      "required": false
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
    "files",
    "lock_gate",
    "lock_gate_detail",
    "locks_acquired_for",
    "repo",
    "sha"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Five parameters, three required — and the longest warning on any non-fused tool,
because "commit" carries a heavier semantic here than the word normally does.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item's worktree |
| `repo` | string | yes | repository name |
| `message` | string | yes | commit message |
| `paths` | array | no | specific paths to stage (default: all) |
| `workspace_root` | string | no | workspace root path |

🔴 **This call can ACQUIRE LOCKS.** Before committing it lists the files the commit
would contain and compares them against the `file_scope` locks THIS ATTEMPT ACTUALLY
HOLDS — the live lock set, **not** `declared_resources`, and the description says so
because the two routinely disagree. Any changed file no held lock covers is locked
for this attempt automatically and stays locked until the attempt ends, so
committing WIDENS the lock set.

## hop 2-3 — what leaves this process, and what binds it

Two destinations, and neither is a "commit" endpoint:

1. **The lock gate** — `internal/mcp/tools_coding.go` (`commitLockGate`) hands the
   pending commit's paths to `pkg/client/client.go` (`ReconcileCommitLocks`) →
   `POST /v1/work_items/<id>/commit_locks`, bound by
   `internal/server/routes_step.go` (`handleReconcileCommitLocks`).
2. **The timeline event** — `internal/mcp/tools_coding.go` (`emitCodingEvent`) posts a
   `commit` event to `POST /v1/events`, **best-effort**: a failure there never fails
   the tool, because the git operation already succeeded by then.

The commit itself is local: `internal/coding/git_ops.go` (`GitCommitGated`) runs it
with the gate as a callback, so the gate runs between staging and committing rather
than before or after.

## hop 4 — what it actually does

- **A file held by another live attempt REFUSES the whole commit** with
  `CONFLICT_LOCK_TAKEN`: nothing is committed, the files stay staged, and the error
  names every blocked path plus its holder — actor, work item and attempt.
- **There is no pass-through.** A lock check that cannot reach the server fails the
  commit rather than allowing it. That direction is the decision: a gate that fails
  open is a gate whose absence is invisible.
- `lock_gate` reports which of three things happened on success: `covered` (every
  changed file already locked, nothing written), `acquired` (with
  `locks_acquired_for`), or `not_run` — reachable here only for a **merge commit**,
  which is made from `MERGE_HEAD` rather than from staged changes, and where `sha` is
  still the commit that was created.
- A refusal or a failed check comes back as a plain error string with **no
  `lock_gate` field at all**, so its absence is a signal rather than a default.

## hop 5 — what comes back

`jsonResult` of `{sha, repo, files}` plus the gate's report. The corpus record above
spans 1,082 calls at a 12.75% error rate — the fourth-busiest tool, and its error
rate is dominated by the refusals this gate is supposed to produce.

## Policy

- **§6.2 T2-15** — the landed lock semantics are accepted, and the de-locking ruling
  shrinks the affected row to `file_scope`, which is exactly the type this gate takes.
- **§6.1 T1-11** — **never pre-declare a ratchet file.** Let this commit-time gate
  take it. That rule exists because a pre-declared ratchet file blocks the very
  attempt that needs to update it, and the ruling is to write it into the two ratchet
  files' own headers where an executor will read it.

## Open

- **§6.4 item 6** — under the de-locking ruling the relationship between this
  automatic widening and an advisory declaration is `aihub#416`'s to define.
