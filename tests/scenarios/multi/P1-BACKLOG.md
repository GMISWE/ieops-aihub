# Multi-role P1 Scenario Ledger

Originally a backlog of scenarios identified by review as important but not yet
written. That framing has rotted: **as of 2026-09-11 every item below has an
implementation file in this directory** (verified per item — see each Status
line). This file is kept as the per-item rationale and code-reference ledger,
not as a to-do list. If a new P1 gap is found, add it here WITH a Status line
saying "not implemented" so the header claim stays checkable.

## P1-1 — Writer cross-user takeover rejected (403)
Bob (writer) tries to force_takeover Alice's RUNNING wi without admin/maintainer role.
Must return 403. Confirms cross-user takeover permission enforcement.
Reference: `internal/domain/run_attempts.go` (`FnForceTakeover`) — cross-user requires maintainer/admin.
Status (2026-09-11): implemented — P1-01-writer-takeover-rejected.md.

## P1-2 — Pause retains lock; second agent blocked
Alice pauses a wi while holding a lock she asked for in `requested_locks`; Bob tries to
claim that same lock → 409 CONFLICT_LOCK_TAKEN. After Alice resumes the lock is still
hers; Bob must wait again.
Reference: pause does NOT keep every lock — `acquireLocksReleasePausedSQL` deletes
`file_scope` rows and retains every other type. And since aihub#416 (2026-09-09) a
`repo` declaration derives no lock at all, so the two agents must contend on a lock one
of them explicitly requested; declaring the same repo no longer blocks anyone.
Realized as P1-02-pause-retains-lock.md, which carries the same caveat in full.

## P1-3 — Stale credential after force-takeover
After Admin force-takes over Alice's wi, Alice's old attempt credentials are stale.
Alice tries PATCH /step with old claim_epoch → 409 CONFLICT_EPOCH_MISMATCH.
Validates that takeover correctly invalidates the previous attempt's credentials.
Status (2026-09-11): implemented — P1-03-stale-cred-after-takeover.md.

## P1-4 — Admin visibility memory not visible to writer
Admin creates memory with visibility="admin".
Bob (writer) calls GET /memories — result should NOT contain the admin memory.
Admin calls GET /memories — result SHOULD contain it.
NOTE: Admin visibility filter implemented in internal/domain/memory.go
(`recallText`; the forward-relations enrichment applies the same filter in
`loadForwardRelations`). This scenario now passes.
Reference: `internal/domain/memory.go` (`Recall`) — visibility filter complete.
Status (2026-09-11): implemented — P1-04-admin-visibility-memory.md.

## P1-5 — Multi-blocker dependency (AND semantics)
WI_C blocked_by=[WI_A, WI_B]. Only WI_A wraps; WI_C must stay blocked.
After WI_B also wraps, WI_C unblocks and appears in ready queue.
Reference: `internal/domain/run_attempts.go` (`unblockDependentWI`) — checks all blockers resolved.
Status (2026-09-11): implemented — P1-05-multi-blocker-and-semantics.md.

## P1-6 — Cross-project memory isolation
Alice writes memory with visibility="team" on project="marketplace".
Bob queries memories on project="aihub" (where he has no role) → 403 or empty.
Confirms team-visibility is still project-scoped.
Status (2026-09-11): implemented — P1-06-cross-project-memory-isolation.md.

## P1-7 — Cross-user cancel permission
Reporter can cancel their own wi; others need maintainer/admin.
Bob (writer) tries to cancel Alice's wi → 403.
Admin cancels Alice's wi → 200.
Reference: router.go handleCancelWorkItem — check project access level.
Status (2026-09-11): implemented — P1-07-cancel-permission.md.

## P1-8 — emit_event cross-user (pf_emit_event)
Bob (writer) emits a note event on Alice's wi → 200 (writer can emit).
Carol (viewer) tries to emit a note → 403 (viewer cannot POST events).
Status (2026-09-11): implemented — P1-08-emit-event-permissions.md.
