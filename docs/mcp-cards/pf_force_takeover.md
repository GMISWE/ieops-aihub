# pf_force_takeover — contract card

```json
{
  "tool": "pf_force_takeover",
  "description_sha256": "c5d5bbc866691e6bbb516b506b96195d99e657f9a512eb552da070cce0e1d3cc",
  "input_schema_sha256": "a86fd1afda060007271b6cbc1fe3481e26d2c714bcdc91b567b3eac2622c510a",
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

`aihub#451` then measured the exception itself on THIS path, in
`internal/domain/force_takeover_commit_window_db_test.go`. A foreign work item whose
claim is in flight — its `run_attempts` row and its lock row committed together,
inside the takeover's window — is displaced: the takeover returns success, the
displaced agent is told nothing, and no `lock_released` is recorded either. Commit
only the lock row inside that window and the takeover is refused instead, which is
what "narrow" means here. The clause is a measured statement, not a hedge.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) generates a fresh
session secret, builds `{reason, session_info{session_secret, machine_id}}` and calls
`pkg/client/client.go` (`ForceTakeover`) → `POST /v1/work_items/<id>/force_takeover`,
bound by `internal/server/router.go` (`handleForceTakeover`).

**No `task_branches` are sent**, and since `aihub#416` neither does a claim — the
whole mechanism is gone with the `git_branch` derivation it keyed. This paragraph
used to record an asymmetry between the two calls (a takeover re-derived a repo's
lock from the declaration while a claim overrode it); there is no longer a repo lock
for either to derive, so the asymmetry is gone rather than resolved.

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

`unrecognized_resources` was added to the response by `aihub#509` (2026-09-09) and
reaches the model for free, which is the delete-list working as designed: nobody had
to add it to a keep-list. It is one line per `declared_resources` entry the lock
mapper cannot understand — the same producer and key `pf_claim_work_item` carries —
and it is `omitempty`, so a healthy work item never sees it. Until then a takeover
re-derived this work item's locks from stored declarations, skipped the ones it
could not map, and said nothing; the standing defence was that a takeover is always
followed by a fresh claim, which does report, and that is a property of the
polyforge skill flow rather than of this endpoint.

⚠️ It is **not** in `response_keys_observed` above. That list is copied from
`aihub#412`'s generated corpus, whose 18 takeovers all predate the key; and since
`omitempty` plus a K10 walk whose work items declare no resources means no live
response carries it, it has no `live-response-keys.json` entry either — an invented
entry fails that arm as readily as a missing one.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape.
- **§6.2 T2-15** — the landed lock semantics are accepted and the race qualifier is
  filed; `deploy_env` and `git_branch` derivation retires under the de-locking
  ruling, shrinking the row to `file_scope`. Landed by `aihub#416` (2026-09-09), so
  the locks this call re-derives for the new attempt are `file_scope` only.
- **§6.2 T2-8 — LANDED** (`aihub#443`). Who may call this is a role question, and
  the two disagreeing role ladders it was about are now one shared map.
- **§6.2 T2-12 — LANDED** (`aihub#509`, 2026-09-09). The half `aihub#416` left open
  was this response's silence about declarations the mapper cannot understand; it is
  now the `unrecognized_resources` key described in hop 5.

## Open

- **§6.4 item 6 is CLOSED for this tool as of `aihub#416` (2026-09-09)**: the
  de-locking ruling changed what this call re-derives, not how. The commit-window
  race above is documented, not closed, and `aihub#416` did not touch it; the full
  analysis lives beside the lock upsert statement in
  `internal/domain/resource_events.go`.
