# pf_acquire_locks — contract card

```json
{
  "tool": "pf_acquire_locks",
  "description_sha256": "929a4a1abe5aa9b2fc32c5a81e0cf14ee7885a7ebad64778bccc383e97736646",
  "input_schema_sha256": "9ec2816b6ac4d1a38dbba45cadc97d1817b4a4491844acb11cc17cabe4a2fa77",
  "params": {
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "acquired",
    "already_held"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One parameter and a three-sentence description that is a **fix**, not decoration.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | used to find the state file |

Before `aihub#345` this tool reported only the locks it had just re-derived from
`declared_resources`, so `already_held: []` read as "this attempt holds no locks"
while the server went on enforcing locks it had not mentioned — and an execute agent
published exactly that conclusion as a *correction* to a premise that had been
right. The description now states the partition: `acquired` is what THIS call took,
`already_held` is every other lock the attempt holds of every type read from the
lock table, the two are disjoint, and together they are the attempt's full set.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) resolves the state file
and sends the three credentials — and nothing else — to
`pkg/client/client.go` (`AcquireLocks`) → `POST /v1/work_items/<id>/acquire_locks`,
bound by `internal/server/routes_step.go` (`handleAcquireLocks`). Addressed by the
resolved canonical `sf.WIID`.

There is no lock list on the wire: the server reads the work item's own
`declared_resources`. That is what makes this a *reconcile*, not a request.

## hop 4 — what it actually does

- **It blocks on conflict and never steals.** A path held by another live attempt
  comes back `CONFLICT_LOCK_TAKEN` with the holder named; nothing is taken
  partially.
- **`already_held` includes locks with no live declaration behind them**:
  `git_branch` and `deploy_env` locks, locks taken from a client-supplied
  `requested_locks`, and `file_scope` locks predating `aihub#264`. So the two lists
  answer different questions, and only their union answers "what am I holding".
- **Removing a path from `declared_resources` DOES release its `file_scope` lock**,
  at the moment of the `pf_update_work_item` call — not here, and not at attempt
  end. `git_branch` and `deploy_env` are not released that way and are held until the
  attempt ends.
- This is not the only way locks are acquired: `pf_commit` and `pf_ship` widen the
  set automatically for every file a commit contains.

## hop 5 — what comes back

`jsonResult`, no projection, so `acquired` and `already_held` reach the model as the
server sent them. The corpus record above spans 320 calls at a 4.38% error rate,
which is consistent with a tool whose refusal is a normal outcome.

## Policy

- **§6.2 T2-12** — repo and service entries become **advisory** and derive no lock.
  The surviving half — an unmappable-entry report on the two lock paths that stay
  silent — rides on `aihub#416`'s spec.
- **§6.2 T2-13** — the empty-`repo` lock-key form must stay byte-identical. Nobody
  should tidy the two shapes into one three-segment key: doing so strands every live
  lock, because a key with no repo segment and a key with an empty one are different
  strings.
- **§6.2 T2-15** — `deploy_env` and `git_branch` derivation retires, shrinking the
  row this tool reports to `file_scope`.

## Open

- **§6.4 item 6** — under the de-locking ruling the "of every type" clause above
  will describe fewer types. What replaces it, and what an advisory entry reports, is
  `aihub#416`'s call — still open (`paused`) at the last re-check, 2026-09-08.
