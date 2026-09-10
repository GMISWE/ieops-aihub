# pf_pause_attempt — contract card

```json
{
  "tool": "pf_pause_attempt",
  "description_sha256": "fcfae2916f42bafa65ec2a703a2278ba4752aae19153db5edc845d913a161946",
  "input_schema_sha256": "b2a0b799191acc6ff59530c2dc1df8b535735fce5fe9b62b4d718a79380cc79e",
  "params": {
    "pause_reason": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "status"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters. The description carries the lock semantics because they differ from
every other terminal path: pausing **releases** `file_scope` locks acquired
mid-attempt and **retains** every other lock type for resume — the published
sentence is held against the live lock vocabulary by
`internal/mcp/pause_cancel_wire_shape_test.go`
(`TestPublishedPauseReleasesOneLockTypeFromTheLiveVocabulary`) and the release
itself by `internal/domain/run_attempts_test.go`
(`TestAcquireLocksReleasePausedSQL_FileScopeOnly`, with
`TestAcquireLocksReleasePausedSQL_NotAllLocks` refusing an unfiltered delete).

⚠️ Since `aihub#416` that retained set is normally EMPTY — `file_scope` is the only
lock the server derives, which `internal/domain/lock_derivation_retired_test.go`
(`TestResourceToLock_RepoAndServiceDeriveNoLock` and
`TestResourceToLock_PathStillDerivesFileScope`) holds per type and
`internal/domain/read_intent_scope_test.go`
(`TestReadIntentIsHonouredOnlyOnTheTypesThatDeriveALock`) holds over the whole
declared-type vocabulary — so the description says so rather than describing a
retention a caller will never observe. It is non-empty only for an attempt that
supplied `requested_locks` explicitly, the one remaining route to a
non-`file_scope` row and the subject of
`internal/domain/lock_derivation_retired_test.go`
(`TestDeriveClaimLocks_RepoAndServiceContributeNoLock`).

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | used to find the state file |
| `pause_reason` | string | no | optional reason |

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) resolves the state file
via `internal/config/state.go` (`ResolveStateFile`) and calls
`pkg/client/client.go` (`PauseAttempt`) → `POST /v1/work_items/<id>/pause`, bound by
`internal/server/routes_step.go` (`handlePauseAttempt`).

Two details:

- The request is addressed by `sf.WIID` — the **resolved canonical** id — not by the
  string the caller passed, so a slug-addressed pause still hits the right row,
  driven with a slug by `internal/mcp/pause_cancel_wire_shape_test.go`
  (`TestPauseAddressesTheCanonicalIDAndSendsAReasonOnlyWhenGiven`).
- `pause_reason` is forwarded only when non-empty, the same guard
  `pf_complete_attempt` applies for the same reason — both directions observed on
  the request in `internal/mcp/pause_cancel_wire_shape_test.go`
  (`TestPauseAddressesTheCanonicalIDAndSendsAReasonOnlyWhenGiven`).

## hop 4 — what it actually does

- The attempt's status becomes `paused` and the local state file is **kept**, which
  is the whole difference from a terminal completion: resume needs those
  credentials, and the contrast is driven both ways by
  `internal/mcp/pause_cancel_wire_shape_test.go`
  (`TestPauseKeepsTheStateFileAndATerminalCompletionDeletesIt`).
- **From that moment the server hard-rejects every call that goes through
  `verifyAttemptCredential`** with its own distinct code, `ErrAttemptPaused`
  ("attempt is paused; resume it before continuing"), deliberately different from a
  stale credential — the code, the 409 and that exact wire message are held by
  `internal/domain/attempt_paused_terminal_dbgated_test.go`
  (`TestPausedAttemptTerminal_CompleteOnAPausedAttemptChangesNothing`).
- **A paused attempt retains the right to write timeline events, and that is the
  contract** (owner ruling ②, 2026-09-10, `aihub#585`; driven end to end by
  `TestPausedAttemptStillWritesTimelineEvents`, and the asymmetry itself was
  measured by `aihub#583`, which corrected this bullet's earlier claim that every
  credential-checked call is refused). `pf_emit_event` authenticates through the
  lighter verifier `verifyAttemptCredentialSimple` in `internal/domain/memory.go`,
  whose whole check is the current attempt id, the claim epoch and the secret hash
  — the attempt's status is never consulted, and a pause moves none of those three
  — which `internal/domain/paused_refusal_scope_test.go`
  (`TestOnlyOneCredentialVerifierRefusesAPausedAttempt`) holds as a census: it
  enumerates the credential verifiers out of the AST and requires exactly one of
  them, and never the Simple one, to answer `ATTEMPT_PAUSED`. The grant is driven
  end-to-end by `internal/domain/paused_attempt_emit_event_dbgated_test.go`
  (`TestPausedAttemptStillWritesTimelineEvents`): pause through the production
  path, then `EmitEvent` succeeds on the paused attempt's own credentials while a
  wrong secret on the same paused attempt still answers `ATTEMPT_MISMATCH`. The
  retained right is load-bearing rather than residue: pausing hands a wi to a
  human, and the note that says WHY often lands after the pause — the 2026-09-10
  close-out paused `aihub#543` and then wrote its checkpoint note through exactly
  this path.
  <!-- prose-only: because=judgement -->
  Unifying the two verifiers is therefore overturning a ruling rather
  than finishing a cleanup, and both arms above go RED against such a diff.
- So a step loop that pauses cannot corrupt step state — it simply cannot advance —
  but it can walk into a cascade of surprise credential errors if it retries.
- `internal/mcp/tools_step.go` (`classifyStepUpdateErr`) is the client-side half:
  `ATTEMPT_PAUSED` keeps the state file and points at resume, while a genuine stale
  credential deletes it, which `internal/mcp/error_code_classification_test.go`
  (`TestClassifyStepUpdateErr_ClassifiesByCodeNotByMessageText`) holds as a pair of
  controls on the real classifier. Getting that classification wrong is destructive
  in one direction only.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of keys real
callers have been handed.

## Policy

- **§6.2 T2-3 — LANDED (`aihub#441`).** One status code for "invalid attempt
  credential" across all tools, and it is **403 `ATTEMPT_MISMATCH`** — the two
  verifiers are driven with one wrong secret and required to answer the same code,
  status and message by
  `internal/domain/attempt_status_vocabulary_dbgated_test.go`
  (`TestAttemptStatusVocabulary_OneCodeForOneInvalidSecret`). The invalid
  `session_secret` refusal used to answer 401 `UNAUTHORIZED` from
  `verifyAttemptCredential` (this tool's path, plus `pf_complete_attempt`,
  `pf_wrap`, `pf_commit`, `pf_acquire_locks`, `pf_update_step`, `pf_save_artifact`)
  and 403 `ATTEMPT_MISMATCH` from `pf_emit_event`, for the same wrong secret.
  `ATTEMPT_PAUSED` being distinct from that class is the property this tool depends
  on, and it is the reason the ruling says *one* code for the credential class rather
  than one code for everything — it is unchanged, still 409, and still reached only
  by a caller whose secret is VALID, so the unification cannot shadow it: the
  branch's position below the constant-time comparison is held by
  `internal/domain/paused_refusal_scope_test.go`
  (`TestOnlyOneCredentialVerifierRefusesAPausedAttempt`) and the resulting answer —
  a wrong secret on a paused attempt coming back `ATTEMPT_MISMATCH` — by
  `internal/domain/attempt_status_vocabulary_dbgated_test.go`
  (`TestAttemptStatusVocabulary_ACancelledAttemptAnswersMismatchNotPaused`).
- **§6.2 T2-15** — which lock types survive a pause is exactly the row the
  de-locking ruling shrinks to `file_scope`, which
  `internal/domain/run_attempts_test.go`
  (`TestAcquireLocksReleasePausedSQL_FileScopeOnly` and
  `TestAcquireLocksReleasePausedSQL_NotAllLocks`) holds on the DELETE's predicate.
  Landed by `aihub#416` (2026-09-09) with the pause SQL byte-unchanged — it always
  named `file_scope` explicitly — so what that work item changed is upstream of this
  DELETE: `repo` and `service` entries derive no lock for it to retain, which
  `internal/domain/commit_lock_type_test.go`
  (`TestCommitGateKeysFileScopeAndTheAdvisoryTypesDeriveNothing`) holds.

## Open

- **§6.4 item 6 is CLOSED for this tool as of `aihub#416` (2026-09-09).**
  <!-- prose-only: because=external-state -->
  The
  prediction this bullet made came true — "retained for resume" now usually
  describes an empty set — and the resolution was to say so in the description
  rather than to drop the clause. Dropping it would be wrong in the one case that
  still reaches it: an attempt holding a `requested_locks` row keeps it across a
  pause, and a caller told otherwise would expect a release that does not happen.
