# pf_complete_attempt — contract card

```json
{
  "tool": "pf_complete_attempt",
  "description_sha256": "4ec095b52008327a117469cabd90cc52a66dbc1c51cd2fe75b089849e084a580",
  "input_schema_sha256": "ded51dfef13981256d43f3cdc2717a824be84b048a0d8b5d4048d07e1d822c2c",
  "params": {
    "derived": {
      "type": "array",
      "required": false
    },
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

Six parameters. The description's ordering sentence is a constraint rather than a
convenience note: this call deletes the credentials `pf_emit_event` needs, so
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
| `derived` | array | on wrap | required when `status="wrapped"` and refused if omitted — dispositions for findings the attempt did not fix; `[]` legal, absence refused (`TestCompleteAttemptDerivedIsRequiredBeforeTheNote`) |

`note` exists because the closing note and the terminal call were always two
round-trips in a fixed order — 201 measured adjacent pairs, 0.325% of billed input —
and the second read nothing out of the first.

`derived` (`aihub#350`) exists because of a measured deposition mechanism: of 24
internally-derived open work items on 2026-09-02, 18 had a parent that was already
wrapped — the queue grows as residue left at exactly this transition, and filing a
new wi cost one call while folding a finding into the parent's record cost
remembering not to make it. The field's entry grammar is the friction inverted:
`folded` travels bare (with `folded:<text>` optional), `filed:<wi id or slug>` must
name a work item that resolves, `dropped:<reason>` must state its reason — the
cheapest legal entry is now the one that leaves no queue residue, held with the
real domain function by `internal/domain/complete_attempt_derived_guard_test.go`
(`TestFoldedIsTheCheapestLegalDerivedEntry`). The gate is server-side rather than
template-side because a third of the measured derived inflow crosses projects
(16 of 49), and a wrap in another project never meets an aihub template.

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
- **`derived` is refused at this hop, before the note, when a wrap omits it or
  shapes it wrongly** — the aihub#452 placement, so a declined call writes nothing —
  driven with a note attached and the empty request list asserted by
  `internal/mcp/derived_wire_shape_test.go`
  (`TestCompleteAttemptDerivedIsRequiredBeforeTheNote`,
  `TestCompleteAttemptDerivedShapeIsRefusedBeforeTheNote`). The shape check is
  `internal/domain/run_attempts.go` (`ValidateDerived`), the server's own function,
  and that the two hops share one authority is held by
  `internal/domain/complete_attempt_derived_guard_test.go`
  (`TestValidateDerivedIsTheSharedShapeAuthority`).
- **When supplied, `derived` is forwarded verbatim, INCLUDING empty** — `[]` is the
  explicit "no findings" declaration and must reach hop 3 as `[]`, while a call that
  never sent the field produces a body with no `derived` key at all, both read off
  the recorded body by `internal/mcp/derived_wire_shape_test.go`
  (`TestCompleteAttemptDerivedTravelsVerbatimIncludingEmpty`,
  `TestCompleteAttemptSendsNoDerivedKeyWhenAbsent`).
- **A non-empty `derived` sent with `paused` or `failed` is refused by the SERVER's
  pre-transaction guard alone**, held by
  `internal/domain/complete_attempt_derived_guard_test.go`
  (`TestDerivedIsRefusedOnNonWrappedStatuses`); this hop deliberately leaves that
  combination to the server, because a hop-2 gate keyed on it would make this tool's
  other parameters unmeasurable to the aihub#419 G1 probe, whose `status` for this
  tool is pinned to `"paused"` while `derived` travels meaningfully only on
  `"wrapped"`. The cost of that placement is stated in
  `internal/mcp/derived_wire_shape_test.go`'s package comment rather than restated
  here.

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
- **A wrap that omits `derived` is refused, never defaulted** (`aihub#350`) — nil
  and `[]` are different statements, only the second is one the caller can be held
  to, and both directions are exercised against a nil pool by
  `internal/domain/complete_attempt_derived_guard_test.go`
  (`TestCompleteAttemptRefusesAWrapWithoutDerived`,
  `TestCompleteAttemptAcceptsAnExplicitlyEmptyDerived`). The refusal sits before
  `BeginTx` next to the status and pause_reason guards, with the status check still
  first (`TestDerivedStatusValidationStillFirst`).
- **Every `filed:<ref>` must resolve to a work item the caller can see, or the wrap
  refuses WHOLE** — attempt still running, work item untouched, no
  `attempt_completed` event — held with a real database by
  `internal/domain/complete_attempt_derived_db_test.go`
  (`TestWrapFiledMustNameAnExistingWorkItem`). Resolution goes through
  `internal/domain/work_items.go` (`resolveVisibleRefOnTx`), the same statement
  `blocked_by` resolves through, so a hidden work item answers exactly like an
  absent one and the field is not a new existence oracle; cross-project refs — a
  third of the measured inflow — resolve exactly when the caller holds a role in
  the target project (`TestWrapFiledCrossProjectIsScopedByCallerRoles`). Which
  entries the loop resolves, and with what trimming, is pinned by
  `internal/domain/complete_attempt_derived_guard_test.go`
  (`TestDerivedFiledRefsExtraction`).
- **On wrapped, the list lands on `run_attempts.derived` (migration 0040) and in the
  `attempt_completed` payload, from one value** — so the row and the timeline cannot
  disagree; `[]` is stored as `[]`, never normalised to `NULL`, and a pause stores
  `NULL` whatever the request carried — all three held by
  `internal/domain/complete_attempt_derived_db_test.go`
  (`TestWrapDerivedFoldedLeavesNoNewWorkItem`,
  `TestWrapDerivedExplicitEmptyIsStoredAsEmptyNotNull`,
  `TestWrapDerivedIsNotWrittenOnNonWrappedCompletions`). A column rather than
  `attrs`, because a plain `attrs` write is a whole-column REPLACE that destroys
  every key it does not resend — held by
  `internal/domain/work_items_attrs_db_test.go`
  (`TestUpdateWorkItemAttrs_ReplaceStillDestroysUnsentKeys`) — so a disposition
  parked there is one careless write away from vanishing.
- **The default disposition provably leaves no queue residue**: a wrap whose only
  entry is a fold succeeds and moves the project's work-item count by zero, driven
  end to end by `internal/domain/complete_attempt_derived_db_test.go`
  (`TestWrapDerivedFoldedLeavesNoNewWorkItem`).
- 🔴 **What the gate does NOT hold: honesty.** The cheapest compliant call would be
  `derived: []` from an attempt that found three things, and catching it would need
  a read of the note's prose this transaction does not make.
  The gate moves which disposition is cheapest — the same posture
  polyforge-scenario#11's A/B/C wrap tags take on the template side — and the
  parameter description says "The server cannot check the list against the note's
  prose, so its honesty is yours" rather than implying an enforcement that is not
  there.

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
