# pf_claim_work_item — contract card

```json
{
  "tool": "pf_claim_work_item",
  "description_sha256": "8ad92ad01ec4a74804859e887970087ba2f3572539186dc40b3b44c97c1b0048",
  "input_schema_sha256": "ab786059d010cc1b5040222e7deb72d9a314641e545cf3dfd63dedf3df60206f",
  "params": {
    "force_takeover": {
      "type": "boolean",
      "required": false
    },
    "idempotency_key": {
      "type": "string",
      "required": true
    },
    "requested_locks": {
      "type": "array",
      "required": false
    },
    "scenario_ref": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "acquired_locks",
    "attempt_id",
    "claim_epoch",
    "current_attempt_epoch",
    "ok",
    "project",
    "slug",
    "unrecognized_resources",
    "worktrees"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Five parameters. Two more were published here once and are gone by decision rather
than by oversight: `mode` (`aihub#394`) and, on the ready-queue side, the same plan-B
treatment. That history matters to a card because a withdrawn parameter is exactly
what a stale document keeps describing.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | id or slug |
| `idempotency_key` | string | yes | resending returns the EXISTING attempt and reuses its recorded secret |
| `requested_locks` | array | no | `{resource_type, resource_key}` — usually omit and let the server derive |
| `force_takeover` | boolean | no | takes over the WORK ITEM, not another work item's locks |
| `scenario_ref` | string | no | git SHA of the local scenario clone |

`mode` was `fresh|resume` from the first version of this tool and every hop existed
— published, forwarded, bound. What never existed was an effect: the complete set
of reads was one self-default and two audit fields, so `resume` restored exactly
what `fresh` restored and an out-of-vocabulary value was silently equal to `fresh`.
It was withdrawn rather than implemented because the promise made for it —
"restores step state from the previous attempt" — is true without it.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) builds the body for
`pkg/client/client.go` (`ClaimWorkItem`) → `POST /v1/work_items/<id>/claim`, bound by
`internal/server/router.go` (`handleClaimWorkItem`).

Three things are added at this hop that no parameter names:

- **`session_info.session_secret`.** Chosen BEFORE the request (protocol C6-2) and
  written to a partial state file first, because the server binds the attempt to
  the secret THIS request carries. On a REPLAY it must be the secret already
  recorded for that key — `recordedClaimSecret` reads it back — since the server's
  replay branch returns the existing attempt and never touches
  `session_secret_hash`. Minting a fresh one there left the state file holding S2
  while the row stored hash(S1), and every later credential-checked call answered
  "invalid session_secret".
- **`session_info.machine_id`**, from `POLYFORGE_MACHINE_ID` or the hostname. The
  server 400s without it.
- **`task_branches`**, computed by `claimTaskBranches`, so each declared repo's
  `git_branch` lock is keyed on the branch the claim predicts it will check out
  rather than on `declared_resources[].task_branch`.

`requested_locks` is forwarded verbatim when present; `force_takeover` only when
true; `scenario_ref` only when non-empty.

## hop 4 — what it actually does

- **The claim creates a run attempt, derives locks, and creates a git worktree per
  repo in the project.** The worktree half is non-fatal and the failures reach the
  RESPONSE, not just stderr: `worktree_problems` carries a directory that exists but
  that `verifyClaimWorktree` refuses. That verification is `rev-parse
  --show-toplevel` compared against the path, not `--git-dir`, because git searches
  parent directories and a polyforge workspace root is commonly itself a repository —
  measured, the naive check calls a directory with no `.git` at all healthy.
- **`force_takeover` does not steal another work item's lock.** A lock held by a
  running or paused attempt of a DIFFERENT work item answers 409
  `CONFLICT_LOCK_TAKEN`. `aihub#430` measured the commit-window interleaving that
  qualifier used to describe and found it comes back as 409
  `CONFLICT_SERIALIZATION_FAILURE` with the row unchanged, because this path opens
  SERIALIZABLE while `pf_force_takeover`'s opens READ COMMITTED. **The qualifier
  therefore belongs on that tool's card and not this one** — one sentence cannot be
  true of both isolation levels.
- **A failed local state write is NOT a no-op.** The claim already committed
  server-side, so the error message says so and names the recovery: replay THIS
  SAME `idempotency_key`, because that costs no epoch bump and no superseded
  attempt. A new key is the fallback, and only when the pre-claim record cannot be
  read back.

## hop 5 — what comes back

`internal/mcp/claim_response_slim.go` (`slimClaimResult`) is a **delete-list**:
everything the server sent minus the named keys. It used to be a keep-list, which
dropped `requires_human_session`, `wi_type`, `id` and `step_recovery_hint` in
silence — the first being the field post-claim routing branches on — while
faithfully copying `expires_at`, which v1.21 removed. `ok`, `attempt_id` and
`claim_epoch` are asserted by this handler from the state file it just wrote,
because those are what every later credential-checked call authenticates with.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape, and this is the tool
  where the keep-list failure was most expensive.
- **§6.2 T2-15** — the landed lock semantics are accepted; `deploy_env` and
  `git_branch` derivation retires under the de-locking ruling, shrinking the row to
  `file_scope`. Until that lands, this call still takes all three.
- **§6.2 T2-3** — one status code for "invalid attempt credential" across all
  tools; the replay hazard above is why the code matters here.

## Open

- **§6.4 item 6** — the de-locking group's open questions belong to `aihub#416`, not
  to this card: the generation probe registry, where an invalidation is recorded,
  and what `pf_predict_conflicts` reports for an advisory entry are open by design.
- A replay from a machine with no state file for the key is still left
  unauthenticated. Closing it needs the server to say "this was a replay"; stated in
  the `idempotency_key` description rather than fixed.
