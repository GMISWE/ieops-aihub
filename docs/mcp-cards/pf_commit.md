# pf_commit — contract card

```json
{
  "tool": "pf_commit",
  "description_sha256": "babfa24510b2031eeac37d0f4f8c029e0238944e08abf2ef66b9733a8b89ad81",
  "input_schema_sha256": "efb55cef0d5dd923b0e8c7447968212c86a2e60bdcf5342ee087f602af2d8545",
  "params": {
    "message": {
      "type": "string",
      "required": true
    },
    "paths": {
      "type": "array",
      "required": false
    },
    "repo": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": true
    },
    "workspace_root": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "files",
    "lock_gate",
    "lock_gate_detail",
    "locks_acquired_for",
    "repo",
    "sha"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Five parameters, three required — and the longest warning on any non-fused tool,
because "commit" carries a heavier semantic here than the word normally does.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item's worktree |
| `repo` | string | yes | repository name |
| `message` | string | yes | commit message |
| `paths` | array | no | specific paths to stage (default: all) |
| `workspace_root` | string | no | workspace root path |

🔴 **This call can ACQUIRE LOCKS.** Before committing it lists the files the commit
would contain and compares them against the `file_scope` locks THIS ATTEMPT ACTUALLY
HOLDS — the live lock set, **not** `declared_resources`, and the description says so
because the two routinely disagree. Any written file no held lock covers is locked
for this attempt automatically and stays locked until the attempt ends, so
committing WIDENS the lock set.

🔴 **"Writes" is narrower than "contains" for a merge commit** (aihub#662), and
`internal/coding/commit_gate_test.go`
(`TestGitPendingCommitPaths_MergeCountsOnlyWhatDiffersFromBothParents`) measures both
directions of that criterion on one worktree: a clean merge inherits every path from
one parent or the other and writes none of them, while a conflicted merge writes
exactly the hand-resolved path and still leaves the carried file out. The reading it
replaces was the staged set, which for a merge is everything the other side brought
in, and which refused `aihub#654` over three files it had never touched
(`TestCommitGateWire_MergeSendsOnlyWhatItWrites` drives that shape through the tool).
`TestGitMergeParents_ReadsEveryParentNotJustTheFirst` holds the parent enumeration an
octopus merge needs, since `git rev-parse MERGE_HEAD` answers one sha and exits 0.

## hop 2-3 — what leaves this process, and what binds it

Two destinations, and neither is a "commit" endpoint:

1. **The lock gate** — `internal/mcp/tools_coding.go` (`commitLockGate`) hands the
   pending commit's paths to `pkg/client/client.go` (`ReconcileCommitLocks`) →
   `POST /v1/work_items/<id>/commit_locks`, bound by
   `internal/server/routes_step.go` (`handleReconcileCommitLocks`).
2. **The timeline event** — `internal/mcp/tools_coding.go` (`emitCodingEvent`) posts a
   `commit` event to `POST /v1/events`, **best-effort**: a failure there never fails
   the tool, because the git operation already succeeded by then.

The commit itself is local: `internal/coding/git_ops.go` (`GitCommitGated`) runs it
with the gate as a callback, so the gate runs between staging and committing rather
than before or after — both edges are held in
`internal/coding/commit_gate_test.go`, by
`TestGitCommitGated_GateSeesExactlyWhatTheCommitContains` (the gate is handed the
staged change set, so it runs after staging) and by
`TestCommitGate_AbsentVersusPresent` (HEAD does not move when it refuses, so it runs
before committing).

## hop 4 — what it actually does

- **A file held by another live attempt REFUSES the whole commit** with
  `CONFLICT_LOCK_TAKEN`: nothing is committed, the files stay staged, and the error
  names every blocked path plus its holder — actor, work item and attempt, all four
  clauses driven through the real tool by
  `internal/mcp/commit_refusal_shape_test.go`
  (`TestRefusedCommitLeavesTheIndexPopulatedAndNamesEveryHolder`), which reads the
  promised holder fields off the live description rather than listing them, and with
  the envelope those fields travel in held by
  `internal/domain/commit_locks_test.go` (`TestCommitLockConflictErr_NamesEveryHolder`).
- 🔴 **The refusal ships a DIFFERENT remedy on a merge**, because following the
  ordinary one there destroys data: `internal/mcp/commit_gate_wire_test.go`
  (`TestCommitGateWire_MergeRefusalIsNotTheDestructiveRemedy`) executes the ordinary
  recipe against a real refused merge and requires `git diff HEAD^2 HEAD` to report a
  DELETION of the file the other parent contributed, invisible in the merge's own
  diff, before it requires the shipped merge advice to be a different string. The same
  arm pins that the `merge` flag travels over the wire, which it must because
  `MERGE_HEAD` lives in the caller's worktree and the server never sees it, and its
  third subtest holds an ordinary commit to the executable remedy. The shape of the
  merge advice — a prohibition with no numbered recipe for anything to execute — is
  held by `internal/domain/commit_locks_test.go`
  (`TestCommitLockConflictErr_MergeShipsADifferentRemedy`).
- **There is no pass-through.** A lock check that cannot reach the server fails the
  commit rather than allowing it. That direction is the decision: a gate that fails
  open is a gate whose absence is invisible.
- `lock_gate` reports which of three things happened on success: `covered` (every
  written file already locked, nothing written), `acquired` (with
  `locks_acquired_for`), or `not_run` — this commit writes nothing a lock could
  protect, reachable here only for a **merge commit**, which is created from
  `MERGE_HEAD` rather than from a change set, and where `sha` is still the commit that
  was created — every value this description advertises is
  driven through the tool by `internal/mcp/commit_gate_wire_test.go`
  (`TestCommitGateWire_CommitAdvertisesOnlyReachableLockGateValues`), with the merge
  path measured by `TestCommitGateWire_CommitCanReportNotRunOnlyViaAMergeCommit` and,
  since aihub#662, also reached by a merge that carries both sides across and
  contributes no path of its own (`TestCommitGateWire_MergeSendsOnlyWhatItWrites`).
- A refusal or a failed check comes back as a plain error string with **no
  `lock_gate` field at all**, so its absence is a signal rather than a default —
  `internal/mcp/commit_refusal_shape_test.go`
  (`TestRefusedOrUncheckedCommitCarriesNoLockGateField`) drives both of those
  failures and binds the field's name to the one a SUCCESS really carries, because
  an absence check alone would go green the day the field is renamed.

## hop 5 — what comes back

`jsonResult` of `{sha, repo, files}` plus the gate's report, checked in both
directions — every named key present, and nothing beyond them and the gate's own
fields — by `internal/mcp/commit_refusal_shape_test.go`
(`TestCommitSuccessAnswersTheCardsThreeKeysPlusTheGateReport`). The corpus record above
spans 1,082 calls at a 12.75% error rate — the fourth-busiest tool, and its error
rate is dominated by the refusals this gate is supposed to produce.

## Policy

- **§6.2 T2-15** — the landed lock semantics are accepted, and the de-locking ruling
  shrinks the affected row to `file_scope`, which is exactly the type this gate takes:
  `internal/domain/commit_lock_type_test.go`
  (`TestCommitGateKeysFileScopeAndTheAdvisoryTypesDeriveNothing`) reads that type out
  of this card and requires it of both the gate's derivation and the query it reads
  the attempt's held keys back with.
- **§6.1 T1-11** — **never pre-declare a ratchet file.** Let this commit-time gate
  take it. That rule exists because a pre-declared ratchet file blocks the very
  attempt that needs to update it, and the ruling is to write it into the two ratchet
  files' own headers where an executor will read it.

## Open

- **§6.4 item 6 is CLOSED for this tool as of `aihub#416` (2026-09-09)**, and the
  answer is that there is no relationship to define.
  <!-- prose-only: because=external-state --> This gate takes `file_scope`
  locks for the PATHS a commit contains; an advisory `repo` or `service` entry
  derives no lock at all, so the two operate on disjoint sets — the disjointness by
  `internal/domain/commit_lock_type_test.go`
  (`TestCommitGateKeysFileScopeAndTheAdvisoryTypesDeriveNothing`), the advisory half
  at the mapper by `internal/domain/lock_derivation_retired_test.go`
  (`TestResourceToLock_RepoAndServiceDeriveNoLock`) and against a database by
  `internal/domain/delocking_db_test.go`
  (`TestDeLockingClaimTakesNoRepoOrServiceLock`), and "the PATHS a commit contains"
  by `internal/coding/commit_gate_test.go`
  (`TestGitCommitGated_GateSeesExactlyWhatTheCommitContains`).
  `aihub#416` changed nothing here — the gate is byte-unchanged and its non-goals
  name `file_scope` explicitly.
  <!-- prose-only: because=history -->
