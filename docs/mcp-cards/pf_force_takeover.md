# pf_force_takeover — contract card

```json
{
  "tool": "pf_force_takeover",
  "description_sha256": "c5d5bbc866691e6bbb516b506b96195d99e657f9a512eb552da070cce0e1d3cc",
  "params": {
    "reason": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "new_attempt_id",
    "new_claim_epoch",
    "ok",
    "prior_actor_display",
    "prior_attempt_id"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters, and a description whose last clause is the whole reason this tool
and `pf_claim_work_item`'s `force_takeover` flag carry **different** wording.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | id or slug |
| `reason` | string | yes | recorded on the timeline |

"No flag displaces another work item's lock, **except in a narrow commit-window
race**" lives here and only here. `aihub#430` measured the same interleaving on the
claim path and found it comes back as a retryable 409 with the row untouched,
because that path opens SERIALIZABLE while this one opens READ COMMITTED. It is one
statement about two different guarantees, so it must not be copied back.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) generates a fresh
session secret, builds `{reason, session_info{session_secret, machine_id}}` and calls
`pkg/client/client.go` (`ForceTakeover`) → `POST /v1/work_items/<id>/force_takeover`,
bound by `internal/server/router.go` (`handleForceTakeover`).

**No `task_branches` are sent**, unlike a claim. That is why a
`declared_resources[].task_branch` value survives a takeover and can win the
lock-key derivation for a repo the claim would have predicted a branch for.

## hop 4 — what it actually does

- The prior attempt becomes `superseded`, its `resource_locks` rows are deleted, a
  `force_takeover` event is written, a new running attempt is created carrying THIS
  caller's secret hash, and `current_attempt_id`/epoch advance.
- **It requires the work item to be `running`** and admits a self-takeover, which is
  what makes re-running the exact same call safe: after the first success the caller
  owns the current attempt. Its idempotency key is synthesised server-side per
  attempt, so unlike a claim there is no replay branch to fall into.
- **A failed local state write is NOT a no-op, and is worse here than on a claim.**
  The takeover already committed, so the previous holder is evicted; and unlike the
  claim path — which persists its secret before calling the server — this handler
  generates the secret in memory and writes it nowhere until that write. The error
  message says exactly that and names the recovery.
- The worktree map is carried over from whatever state file this machine already
  holds, keyed on the canonical id, because nothing in the takeover response carries
  one and only a claim ever creates worktrees. Without that, the next `pf_ship` /
  `pf_diff` / `pf_commit` would find no worktrees and, with no `workspace_root`
  argument to reconstruct a path from, fail outright.

## hop 5 — what comes back

`internal/mcp/force_takeover_response_slim.go` (`slimForceTakeoverResult`) is a
delete-list. It used to be a keep-list holding five named keys, which dropped `id`,
`slug` and `project` in silence — the three fields added so that a SLUG-addressed
takeover could key its state file canonically. This handler reads all three itself
and then told the model none of them, so the answer withheld the identity the call
had just established.

`new_attempt_id` and `new_claim_epoch` are asserted from the state file just
written, not relayed: relaying would report a credential this machine does not hold
whenever the two disagree. `ok` stays relayed, because on this route the server's own
field is the only statement of success.

Under a delete-list, `expires_at` needs no entry: v1.21 removed it, the response
does not carry it, and naming it here would be the same rot that left it in the old
CLAIM keep-list.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape.
- **§6.2 T2-15** — the landed lock semantics are accepted and the race qualifier is
  filed; `deploy_env` and `git_branch` derivation retires under the de-locking
  ruling, shrinking the row to `file_scope`.
- **§6.2 T2-8 — LANDED** (`aihub#443`). Who may call this is a role question, and
  the two disagreeing role ladders it was about are now one shared map.

## Open

- **§6.4 item 6** — the de-locking group's open questions belong to `aihub#416`. The
  commit-window race above is documented, not closed; the full analysis lives beside
  the lock upsert statement in `internal/domain/resource_events.go`.
