# pf_acquire_locks — contract card

```json
{
  "tool": "pf_acquire_locks",
  "description_sha256": "fbd2cbf926935ea2cab1195a2801bc6e648e199625ede7fb0a7bef350c114bcf",
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
- **`already_held` includes locks with no live declaration behind them**: locks taken
  from a client-supplied `requested_locks`, `file_scope` locks predating
  `aihub#264`, and — on a database with rows older than `aihub#416` —
  `git_branch`/`deploy_env` rows that nothing derives any more. So the two lists
  answer different questions, and only their union answers "what am I holding".
  ⚠️ That population USED to be universal (every attempt declaring a repo held a
  `git_branch` lock) and is now rare. Rare makes reporting it more important, not
  less: a non-`file_scope` key here has no declaration anywhere to explain it from.
- **Removing a path from `declared_resources` DOES release its `file_scope` lock**,
  at the moment of the `pf_update_work_item` call — not here, and not at attempt
  end. Any surviving non-`file_scope` row is not released that way and is held until
  the attempt ends.
- This is not the only way locks are acquired: `pf_commit` and `pf_ship` widen the
  set automatically for every file a commit contains.

## hop 5 — what comes back

`jsonResult`, no projection, so `acquired` and `already_held` reach the model as the
server sent them. The corpus record above spans 320 calls at a 4.38% error rate,
which is consistent with a tool whose refusal is a normal outcome.

A **third** key can appear: `unrecognized_resources`, added by `aihub#509`
(2026-09-09), one human-readable line per `declared_resources` entry the lock mapper
cannot understand. It is `omitempty` and a healthy work item never carries it. Read
it as a report on the **declarations**, not as a third lock list — `acquired` and
`already_held` stay a partition of the attempt's lock set, and this names entries
that are in neither because they derive nothing. It is the same producer and the
same key `pf_claim_work_item` uses, deliberately: an entry that derives no target
used to fall out of both lists silently, so the two came back complete and correct
while saying nothing about the declaration that contributed to neither.

⚠️ It is **not** in `response_keys_observed` above, and that is not an omission.
That list is copied from `aihub#412`'s generated corpus, whose window closed before
the key existed; and because the key is `omitempty` and every work item the K10 live
walk drives declares no resources, no live response carries it either, so it has no
entry in `live-response-keys.json` — an invented entry there fails that arm as
readily as a missing one. Both files are therefore correct as they stand.

## Policy

- **§6.2 T2-12** — repo and service entries become **advisory** and derive no lock.
  Landed by `aihub#416` (2026-09-09), which is why the `already_held` sentence above
  no longer names `git_branch`/`deploy_env` as a live population. The row's other
  half — the unmappable-entry report this tool used to skip in silence — landed by
  `aihub#509` (2026-09-09) as the `unrecognized_resources` key described in hop 5,
  and the row is `landed` rather than `landed-in-part` from that date.
- **§6.2 T2-13** — the empty-`repo` lock-key form must stay byte-identical. Nobody
  should tidy the two shapes into one three-segment key: doing so strands every live
  lock, because a key with no repo segment and a key with an empty one are different
  strings.
- **§6.2 T2-15** — `deploy_env` and `git_branch` derivation retires, shrinking the
  row this tool reports to `file_scope`. Landed by `aihub#416` (2026-09-09);
  migration `0038` deleted the existing rows of both types and left one
  `lock_released` event per row with `cause=derivation_retired`.

## Open

- **§6.4 item 6 is CLOSED for this tool as of `aihub#416` (2026-09-09).** The "of
  every type" clause is kept deliberately rather than narrowed to `file_scope`: the
  server still ENFORCES a `requested_locks` row of any type in the CHECK vocabulary,
  so a description promising only `file_scope` would under-report exactly the
  population `aihub#345` exists to surface.
