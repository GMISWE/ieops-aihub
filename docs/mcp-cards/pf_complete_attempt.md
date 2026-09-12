# pf_complete_attempt — contract card

```json
{
  "tool": "pf_complete_attempt",
  "description_sha256": "3071ac903f2a848c7ce3531253ea0c9b665d68260c8dede7cb7eb66adada0ba1",
  "input_schema_sha256": "ed855b04f59b06b43b339c55136f01c13dd45b784008bfdb4f3e317a00b8c89e",
  "params": {
    "force_terminate_step": {
      "type": "boolean",
      "required": false
    },
    "note": {
      "type": "string",
      "required": false
    },
    "pause_reason": {
      "type": "string",
      "required": false
    },
    "status": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "note_emitted",
    "ok",
    "worktrees"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Five parameters. The description's second sentence is an ordering constraint rather
than a convenience note: this call deletes the credentials `pf_emit_event` needs, so
a note emitted afterwards cannot authenticate — the published statement and the
order the tool really uses are compared in one arm by
`internal/mcp/complete_attempt_wire_shape_test.go`
(`TestPublishedNoteOrderingIsTheOrderTheToolUses`), on top of the enforced half in
`internal/mcp/tools_fusion_test.go`
(`TestFusedNoteReachesTimelineBeforeTerminalCall`).

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | used to find the state file |
| `status` | string | yes | `wrapped` \| `failed` \| `paused` |
| `force_terminate_step` | boolean | no | force-terminate an in-progress step |
| `note` | string | no | closing note recorded BEFORE the attempt is completed (`TestPublishedNoteOrderingIsTheOrderTheToolUses`) |
| `pause_reason` | string | no | read only when `status="paused"`, and refused with any other status |

`note` exists because the closing note and the terminal call were always two
round-trips in a fixed order — 201 measured adjacent pairs, 0.325% of billed input —
and the second read nothing out of the first.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) resolves the state file
through `internal/config/state.go` (`ResolveStateFile`), emits the note via
`internal/mcp/tools_events.go` (`emitNote`) → `POST /v1/events`, then calls
`pkg/client/client.go` (`CompleteAttempt`) → `POST /v1/work_items/<id>/complete`,
bound by `internal/server/router.go` (`handleCompleteAttempt`).

- **`status` and `force_terminate_step` are forwarded**; the three credentials come
  from the state file — all four observed on the body a fake aihub received, in
  `internal/mcp/complete_attempt_wire_shape_test.go`
  (`TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote`).
- **`pause_reason` is refused on any status but `paused`, and forwarded only when
  non-empty** (`TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus`;
  `TestCompleteAttemptForwardsPauseReason`,
  `TestCompleteAttemptOmitsPauseReasonWhenAbsent`). The refusal (`aihub#452`) is raised before the state file is resolved
  and before the note below is emitted, so a declined call writes nothing at all —
  driven with a note attached, and the empty request list asserted, by
  `internal/mcp/attempt_lifecycle_param_contract_test.go`
  (`TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus`).
  The non-emptiness half still earns its keep on the pause path: the server writes
  whatever it is given, so an unguarded assignment would send `""` and turn "paused,
  reason not given" into "paused for no stated reason" — the pause path is driven
  both ways in the same file (`TestCompleteAttemptDoesNotRequirePauseReasonOnPause`
  for the empty and absent cases, `TestCompleteAttemptStillPausesWithAReason` for
  the value the column exists for).
- **`note` never reaches this endpoint.** It becomes its own `note` event on the
  other call, which is the difference from `pause_reason`: one is a timeline event
  whatever the status, the other is a column read on one status — the absent body key
  is asserted by `internal/mcp/complete_attempt_wire_shape_test.go`
  (`TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote`) and the event it
  becomes instead by `internal/mcp/tools_fusion_test.go`
  (`TestFusedNoteReachesTimelineBeforeTerminalCall`).

## hop 4 — what it actually does

- **Terminal statuses delete the local state file**, by the resolved canonical key
  and best-effort by the key the caller passed, so a slug-addressed completion also
  clears a stale slug-keyed stub. `paused` keeps it, because resume needs the
  credentials.
- **The note is emitted first and its failure does not abort the call.** The outcome
  is reported instead: `note_emitted` on success, and on failure the error text gains
  a clause saying whether the note already landed — because the caller is about to
  decide whether to retry, and retrying re-sends the note, which
  `internal/mcp/tools_fusion_test.go` (`TestFusedNoteFailureIsReportedNotSwallowed`,
  `TestFusedNoteAbsentMeansNoEvent`) and `internal/mcp/tools_events_note_test.go`
  (`TestNoteOutcomeSuffix`, `TestApplyNoteResultDistinguishesAbsentFromFailed`) hold
  between them.
- **This is NOT exactly-once for the note.** A retry after a failed completion
  records it twice. That is documented rather than solved; an idempotency key on
  events is a bigger change.
- **A step still `in_progress` fails the completion on `wrapped` and `failed`** unless
  `force_terminate_step` is set — but **`paused` does not need the flag**
  (`TestTheStepInProgressRefusalIsGatedOnPausedOrTheFlagAlone`). The H-R9-11
  block in `internal/domain/run_attempts.go` (`FnCompleteAttempt`) force-terminates a
  live step when `status="paused"` OR the flag is set, and refuses with
  `ErrConflictStepInProgress` only otherwise, so the flag is load-bearing on the two
  terminal statuses alone — the disjunction, the else branch's code, the status
  vocabulary the subtraction runs over and the absence of any second read of the flag
  are all read out of that block by
  `internal/domain/complete_attempt_step_gate_test.go`
  (`TestTheStepInProgressRefusalIsGatedOnPausedOrTheFlagAlone`).
  `internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`)
  forwards it without a second gate of its own, so there is no other refusal to hit —
  the flag reaching the wire on every status is asserted by
  `internal/mcp/complete_attempt_wire_shape_test.go`
  (`TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote`).
  The terminal half is why the fused `pf_wrap`, which completes as `wrapped` and never
  sets the flag, is refused while a step is still live — and every retry re-sends its
  note, both driven against a refusing server by
  `internal/mcp/wrap_note_retry_test.go`
  (`TestWrapCompletesAsWrappedWithNoFlagAndEveryRetryResendsItsNote`).
- **`pause_reason` is written on `paused` and on nothing else** — the refusal on any
  other status by `TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus`, the
  terminal-path nil by `TestTheTerminalPathNilsThePauseReasonBeforeTheWrite`.
  `internal/domain/run_attempts.go`
  (`FnCompleteAttempt`) refuses a non-empty reason on any other status next to the
  status check, before it opens a transaction — exercised against a nil pool, which is
  what proves the refusal precedes the transaction, in
  `internal/domain/complete_attempt_pause_reason_guard_test.go`
  (`TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus`,
  `TestCompleteAttemptStatusValidationStillFirst`) — and normalises an empty one to
  `NULL` rather than `''` at the write, which
  `internal/domain/complete_attempt_step_gate_test.go`
  (`TestTheTerminalPathNilsThePauseReasonBeforeTheWrite`) pins in place and in order.
  Both the column and the `attempt_completed` payload
  read the same normalised value, so the row and the event cannot disagree about
  whether a reason was recorded. The capability of recording a reason on a wrapped or
  failed attempt was decided against rather than narrowed by accident: the column's
  only reader is `internal/domain/work_items.go` (`GetReadyQueue`), whose paused
  segment filters `wi.status = 'paused'`, and `internal/db/migrations/0027_run_attempts_pause_reason.sql`
  scopes the column to `complete_attempt(status=paused)` — the reader census and its
  filter are held by `internal/domain/complete_attempt_step_gate_test.go`
  (`TestPauseReasonHasExactlyOneReader`), the reader's own behaviour by
  `internal/domain/step_pause_stall_test.go` (`TestPauseReasonSurfacedInReadyQueue`,
  `TestPauseReasonOmittedWhenNil`) and the migration's scope by
  `TestMigration0027_UpDown`. `note` is the field that
  records on every status, and the refusal names it, which
  `internal/mcp/complete_attempt_wire_shape_test.go`
  (`TestTheRefusalNamesNoteAsTheFieldThatRecordsOnEveryStatus`) drives on all three
  statuses and against the refusal's own text.

## hop 5 — what comes back

The server's completion result, plus `note_emitted` / `note_error` when a note was
requested, plus the worktree paths read out of the state file this call is about to
delete — surfaced for all statuses so a caller does not have to have read that file
itself, and with no slim function, both driven per status by
`internal/mcp/complete_attempt_wire_shape_test.go`
(`TestCompleteAttemptResultCarriesTheWorktreesAndIsNotProjected`).

## Policy

- **§6.2 T2-3** — one status code for "invalid attempt credential" across all tools;
  retire `lost` or give it a writer; and add the cancelled-attempt status the cancel
  path says it needs.
- **§6.1 T1-9** — the `note` ordering is published on the tool rather than left in a
  skill, because the hazard is invisible from the schema alone; that it is published
  on both the tool description and the `note` property, and that the tool follows it,
  is held by `internal/mcp/complete_attempt_wire_shape_test.go`
  (`TestPublishedNoteOrderingIsTheOrderTheToolUses`).
- **§6.1 T1-9, second application** — `pause_reason` shipped with "read only when
  `status="paused"`" while both later hops read it on every status. Disposition 2
  (fix the code so the prose becomes true) was taken over rewriting the sentence,
  because the capability the sentence denied had no reader; `aihub#452`. The prose
  gained the one thing a guard adds that "read only" does not say — that the
  mismatch is refused rather than ignored.

## Open

- The double-note on retry is a known, stated behaviour rather than a settled
  design. Nothing in the response distinguishes a first note from a duplicate.
