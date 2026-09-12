# pf_claim_work_item — contract card

```json
{
  "tool": "pf_claim_work_item",
  "description_sha256": "217fc4e7868d06b69e044249c0e221134a833d90801266adb11d8d2377a46527",
  "input_schema_sha256": "ed92323993421a21654e60262b99c28d844dc3b5a76f3e7a9a8b40053f6fb816",
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
treatment — both are held unpublished by `internal/mcp/claim_published_word_test.go`
(`TestWithdrawnParamsStayUnpublished`). That history matters to a card because a
withdrawn parameter is exactly what a stale document keeps describing.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | id or slug |
| `idempotency_key` | string | yes | a BODY param for DB dedup, **not** the HTTP `Idempotency-Key` header; resending returns the EXISTING attempt and reuses its recorded secret (`TestE2EClaimReplayKeepsTheSecretTheServerAccepts`) |
| `requested_locks` | array | no | `{resource_type, resource_key}` — usually omit and let the server derive (`TestResourceToLock_PathStillDerivesFileScope`, `TestDeriveClaimLocks_RepoAndServiceContributeNoLock`) |
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
turns into a 24h replay of the cached HTTP response, keyed `<api_key_id>:<key>` and
refused with 409 `IDEMPOTENCY_KEY_REUSED` if the same key arrives with a different
`method+target+body` — the header on every mutating request is
`pkg/client/client_test.go` (`TestMutatingRequestsCarryIdempotencyKey`), the
per-API-key scoping `internal/server/idempotency_test.go`
(`TestIdempotency_KeysAreScopedPerAPIKey`), the 409
`TestIdempotency_ReusedKeyDifferentRequestIsRejected`, and the window
`internal/server/idempotency_published_window_test.go`
(`TestPublishedReplayWindowIsTheEnforcedOne`). **That header is not this parameter**,
the client mints it per request, and a caller neither sees nor sets it. This parameter
dedups in the DATABASE on `run_attempts.idempotency_key` and answers with the existing
attempt, driven end to end by `internal/mcp/claim_idempotent_replay_e2e_db_test.go`
(`TestE2EClaimReplayKeepsTheSecretTheServerAccepts`). The design requires both
(`H-R3-8`), and the description now says so —
`TestPublishedIdempotencyKeyDescriptionDistinguishesTheHeader` reads it off a live
session and compares it with the header the same request carried — because the moment
a client started sending the header "idempotency" stopped being unambiguous at this
call site.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) builds the body for
`pkg/client/client.go` (`ClaimWorkItem`) → `POST /v1/work_items/<id>/claim`, bound by
`internal/server/router.go` (`handleClaimWorkItem`).

Three things are added at this hop that no parameter names:

- **`session_info.session_secret`.** Chosen BEFORE the request (protocol C6-2) and
  written to a partial state file first, because the server binds the attempt to the
  secret THIS request carries — `internal/mcp/claim_wire_shape_test.go`
  (`TestClaimPersistsTheSessionSecretBeforeTheServerSeesIt`) observes the file from
  inside the server's own handler, mid-request. On a REPLAY it must be the secret
  already recorded for that key — `recordedClaimSecret` reads it back — since the
  server's replay branch returns the existing attempt and never touches
  `session_secret_hash` (`TestE2EClaimReplayKeepsTheSecretTheServerAccepts`). Minting
  a fresh one there left the state file holding S2 while the row stored hash(S1), and
  every later credential-checked call answered "invalid session_secret".
- **`session_info.machine_id`**, from `POLYFORGE_MACHINE_ID` or the hostname. The
  server 400s without it, before any transaction is opened —
  `internal/domain/claim_required_fields_probe_test.go`
  (`TestClaimRefusesAMissingMachineIDBeforeTouchingDB`) drives the refusal with a nil
  pool, so a pass proves no state was touched.
- ⚠️ **`task_branches` is NO LONGER SENT** (`aihub#416`) —
  `internal/mcp/claim_wire_shape_test.go` (`TestClaimBodyCarriesNoTaskBranches`)
  drives a real claim and refuses the key anywhere in the body it sent. It existed for
  one consumer — the `git_branch` lock key a repo declaration derived — and that
  derivation is retired, so `claimTaskBranches` and its whole chain are gone along
  with the extra `GetWorkItem` round-trip they cost
  (`TestClaimMakesNoWorkItemReadBeforeItClaims`). This call is one request shorter
  than it was.

`requested_locks` is forwarded verbatim when present; `force_takeover` only when
true; `scenario_ref` only when non-empty — every one of those driven BOTH ways by
`internal/mcp/claim_wire_shape_test.go`
(`TestClaimForwardsAnOptionalParamOnlyWhenItCarriesAValue`).

## hop 4 — what it actually does

- **The claim creates a run attempt, derives locks, creates a git worktree per repo in
  the project, and then records a `repo_pin` per worktree** — the attempt and its
  locks against a real database by `internal/mcp/claim_idempotent_replay_e2e_db_test.go`
  (`TestE2EClaimReplayKeepsTheSecretTheServerAccepts`), the worktree and pin halves by
  `internal/mcp/repo_pins_wiring_test.go` (`TestClaimRecordsRepoPins`). The pin half is
  `aihub#416`: after the worktrees exist, the tool reads `git rev-parse HEAD` in each
  and POSTs them to `/v1/work_items/<id>/repo_pins`, which stores them on
  `run_attempts.repo_pins` (migration `0037`), which
  `internal/mcp/repo_pins_wiring_test.go` (`TestClaimRecordsRepoPins`) drives against
  a real worktree and `internal/domain/delocking_db_test.go`
  (`TestDeLockingMigration0038AndRepoPins`) round-trips through the column. They come
  back on this response as `repo_pins` (the echo asserted by `TestClaimRecordsRepoPins`)
  and on `pf_get_step`, whose passthrough `internal/mcp/get_step_wire_shape_test.go`
  holds by name for this key.
  🔴 **A pin is PROVENANCE, not a constraint.** It answers "which tree was this
  conclusion reached on". Nothing enforces it, nothing notices a mid-attempt `git
  pull`, and it does not expire. A repo whose worktree could not be built is ABSENT
  from the map, the claim still succeeds, and the absence is reported in
  `worktree_problems` (`TestClaimDoesNotAdoptADirectoryWithADanglingGitPointer`) — a
  conclusion drawn in such a repo has to say it has no provenance rather than borrow
  the credibility of the pins beside it.
  ⚠️ It is a SECOND request, not a claim field, because the worktrees do not exist
  when the claim goes out. Predicting them beforehand is what `aihub#356` did for
  branch names and what `aihub#416` deleted.
  <!-- prose-only: because=history -->
- **Locks derived at claim are `file_scope` only** since `aihub#416`, which
  `internal/domain/lock_derivation_retired_test.go`
  (`TestResourceToLock_PathStillDerivesFileScope`) holds at the mapper. A `repo` or
  `service` entry in `declared_resources` derives nothing; `acquired_locks` on this
  response is empty for a work item that declares no path
  (`TestDeriveClaimLocks_RepoAndServiceContributeNoLock`). The worktree half is
  non-fatal and the failures reach the RESPONSE rather than only stderr:
  `worktree_problems` carries a directory that exists but that `verifyClaimWorktree`
  refuses (`internal/mcp/claim_worktree_adopt_test.go`,
  `TestClaimDoesNotAdoptADirectoryWithADanglingGitPointer`). That verification is
  `rev-parse --show-toplevel` compared against the path rather than `--git-dir`,
  because git searches parent directories and a polyforge workspace root is commonly
  itself a repository — measured by
  `TestClaimDoesNotAdoptAHalfCheckedOutDirectoryInsideAGitWorkspace`, where the naive
  check calls a directory with no `.git` at all healthy.
- **`force_takeover` does not steal another work item's lock.** A lock held by a
  running or paused attempt of a DIFFERENT work item answers 409 `CONFLICT_LOCK_TAKEN`
  — the refusal measured in `internal/domain/force_takeover_lock_steal_db_test.go`
  (`TestForceTakeoverLockSteal_ClaimRefusesAForeignHolder`), the status pair in
  `internal/domain/claim_foreign_holder_statuses_test.go`
  (`TestPublishedForeignHolderStatusesAreTheEnforcedOnes`). `aihub#430` measured the
  commit-window interleaving that qualifier used to describe: it comes back as 409
  `CONFLICT_SERIALIZATION_FAILURE` with the row unchanged. That refusal exists because
  this path pins SERIALIZABLE while `pf_force_takeover`'s opens a bare `pool.Begin` —
  no isolation level pinned there; it follows the `default_transaction_isolation` the
  database, the role or the DSN sets (`aihub#497`) — the split pinned at the source by
  `internal/domain/txn_isolation_probe_test.go`
  (`TestClaimOpensSerializableAndTakeoverOpensBareBegin`). **The qualifier
  therefore belongs on that tool's card and not this one** — one sentence cannot be
  true of both sides of that split.
- **A failed local state write is NOT a no-op.** The claim already committed
  server-side, so the error message says so and names the recovery: replay THIS SAME
  `idempotency_key`, because that costs no epoch bump and no superseded attempt —
  `internal/mcp/claim_committed_side_effect_test.go`
  (`TestClaimErrorDisclosesTheClaimItAlreadyMade`) asserts the disclosure and the
  order of the two recoveries. A new key is the fallback, and only when the pre-claim
  record cannot be read back.

## hop 5 — what comes back

`internal/mcp/claim_response_slim.go` (`slimClaimResult`) is a **delete-list**:
everything the server sent minus the named keys, which
`internal/mcp/claim_response_projection_test.go`
(`TestClaimResultPassesThroughAFieldTheStructDoesNotHaveYet`) states as the shape a
keep-list cannot pass. It used to be a keep-list, which
dropped `requires_human_session`, `wi_type`, `id` and `step_recovery_hint` in
silence — the first being the field post-claim routing branches on — while
faithfully copying `expires_at`, which v1.21 removed. `ok`, `attempt_id` and
`claim_epoch` are asserted by this handler from the state file it just wrote,
because those are what every later credential-checked call authenticates with
(`TestClaimResultKeepsTheStateFileAuthoritativeKeys`).

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape, and this is the tool
  where the keep-list failure was most expensive.
- **§6.2 T2-15** — the landed lock semantics are accepted; `deploy_env` and
  `git_branch` derivation retires under the de-locking ruling, shrinking the row to
  `file_scope` (`TestResourceToLock_RepoAndServiceDeriveNoLock`). Landed by
  `aihub#416` (2026-09-09): this call now takes `file_scope` only, unless a caller
  supplies `requested_locks` explicitly.
- **§6.2 T2-3** — one status code for "invalid attempt credential" across all
  tools; the replay hazard above is why the code matters here.

## Open

- **§6.4 item 6 is PARTLY closed as of `aihub#416` (2026-09-09).**
  <!-- prose-only: because=external-state -->
  What that work item
  settled and this card now describes: repo/service derive no lock, and a claim
  records `repo_pins` — both held, by
  `TestDeriveClaimLocks_RepoAndServiceContributeNoLock` and
  `TestClaimRecordsRepoPins`. What it deliberately left for the scenario repo, so this
  card cannot answer it: the generation probe registry lives in `services.yaml` there,
  on its own lifecycle, and is not part of this tool's contract.
  <!-- prose-only: because=cross-repo -->
- A replay from a machine with no state file for the key is still left
  unauthenticated. Closing it needs the server to say "this was a replay"; stated in
  the `idempotency_key` description rather than fixed.
