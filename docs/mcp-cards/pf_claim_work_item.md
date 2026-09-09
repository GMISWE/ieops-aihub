# pf_claim_work_item — contract card

```json
{
  "tool": "pf_claim_work_item",
  "description_sha256": "8ad92ad01ec4a74804859e887970087ba2f3572539186dc40b3b44c97c1b0048",
  "input_schema_sha256": "87a9ba1141422c042e4762145752878c7d79485166bef069385b5a7e7e1def67",
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
| `idempotency_key` | string | yes | a BODY param for DB dedup, **not** the HTTP `Idempotency-Key` header; resending returns the EXISTING attempt and reuses its recorded secret |
| `requested_locks` | array | no | `{resource_type, resource_key}` — usually omit and let the server derive |
| `force_takeover` | boolean | no | takes over the WORK ITEM, not another work item's locks |
| `scenario_ref` | string | no | git SHA of the local scenario clone |

`mode` was `fresh|resume` from the first version of this tool and every hop existed
— published, forwarded, bound. What never existed was an effect: the complete set
of reads was one self-default and two audit fields, so `resume` restored exactly
what `fresh` restored and an out-of-vocabulary value was silently equal to `fresh`.
It was withdrawn rather than implemented because the promise made for it —
"restores step state from the previous attempt" — is true without it.

**One word, two mechanisms — and only one of them is this parameter.** `aihub#436`
made `pkg/client` (`setStandardHeaders`) mint an `Idempotency-Key` HTTP header on
every POST/PATCH, which `internal/server/idempotency.go` (`IdempotencyMiddleware`)
turns into a 24h replay of the cached HTTP response, keyed
`<api_key_id>:<key>` and refused with 409 `IDEMPOTENCY_KEY_REUSED` if the same key
arrives with a different `method+target+body`. **That header is not this
parameter**, the client mints it per request, and a caller neither sees nor sets
it. This parameter dedups in the DATABASE on `run_attempts.idempotency_key` and
answers with the existing attempt. The design requires both (`H-R3-8`), and the
description now says so, because the moment a client started sending the header
"idempotency" stopped being unambiguous at this call site.

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
- ⚠️ **`task_branches` is NO LONGER SENT** (`aihub#416`). It existed for one
  consumer — the `git_branch` lock key a repo declaration derived — and that
  derivation is retired, so `claimTaskBranches` and its whole chain are gone along
  with the extra `GetWorkItem` round-trip they cost. This call is one request
  shorter than it was.

`requested_locks` is forwarded verbatim when present; `force_takeover` only when
true; `scenario_ref` only when non-empty.

## hop 4 — what it actually does

- **The claim creates a run attempt, derives locks, creates a git worktree per repo
  in the project, and then records a `repo_pin` per worktree.** The pin half is
  `aihub#416`: after the worktrees exist, the tool reads `git rev-parse HEAD` in each
  and POSTs them to `/v1/work_items/<id>/repo_pins`, which stores them on
  `run_attempts.repo_pins` (migration `0037`). They come back on this response as
  `repo_pins` and on `pf_get_step`.
  🔴 **A pin is PROVENANCE, not a constraint.** It answers "which tree was this
  conclusion reached on". Nothing enforces it, nothing notices a mid-attempt `git
  pull`, and it does not expire. A repo whose worktree could not be built is ABSENT
  from the map, the claim still succeeds, and the absence is reported in
  `worktree_problems` — a conclusion drawn in such a repo has to say it has no
  provenance rather than borrow the credibility of the pins beside it.
  ⚠️ It is a SECOND request, not a claim field, because the worktrees do not exist
  when the claim goes out. Predicting them beforehand is what `aihub#356` did for
  branch names and what `aihub#416` deleted.
- **Locks derived at claim are `file_scope` only** since `aihub#416`. A `repo` or
  `service` entry in `declared_resources` derives nothing; `acquired_locks` on this
  response is empty for a work item that declares no path. The worktree half is non-fatal and the failures reach the
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
  `file_scope`. Landed by `aihub#416` (2026-09-09): this call now takes `file_scope`
  only, unless a caller supplies `requested_locks` explicitly.
- **§6.2 T2-3** — one status code for "invalid attempt credential" across all
  tools; the replay hazard above is why the code matters here.

## Open

- **§6.4 item 6 is PARTLY closed as of `aihub#416` (2026-09-09).** What that work
  item settled and this card now describes: repo/service derive no lock, and a claim
  records `repo_pins`. What it deliberately left for the scenario repo, so this card
  cannot answer it: the generation probe registry lives in `services.yaml` there, on
  its own lifecycle, and is not part of this tool's contract.
- A replay from a machine with no state file for the key is still left
  unauthenticated. Closing it needs the server to say "this was a replay"; stated in
  the `idempotency_key` description rather than fixed.
