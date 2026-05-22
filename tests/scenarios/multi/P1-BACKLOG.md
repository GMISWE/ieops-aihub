# Multi-role P1 Backlog

Scenarios identified by Opus review as important but not yet written.

## P1-1 — Writer cross-user takeover rejected (403)
Bob (writer) tries to force_takeover Alice's RUNNING wi without admin/maintainer role.
Must return 403. Confirms cross-user takeover permission enforcement.
Reference: domain/run_attempts.go FnForceTakeover — cross-user requires maintainer/admin.

## P1-2 — Pause retains lock; second agent blocked
Alice pauses a wi (keeps lock); Bob tries to claim same resource → 409 CONFLICT_LOCK_TAKEN.
After Alice resumes, lock is re-acquired; Bob must wait again.
Reference: FnCompleteAttempt(paused) keeps resource_locks intact.

## P1-3 — Stale credential after force-takeover
After Admin force-takes over Alice's wi, Alice's old attempt credentials are stale.
Alice tries PATCH /step with old claim_epoch → 409 CONFLICT_EPOCH_MISMATCH.
Validates that takeover correctly invalidates the previous attempt's credentials.

## P1-4 — Admin visibility memory not visible to writer
Admin creates memory with visibility="admin".
Bob (writer) calls GET /memories — result should NOT contain the admin memory.
Admin calls GET /memories — result SHOULD contain it.
NOTE: Current server implementation does NOT filter admin visibility separately (server gap).
This test will FAIL until the server is fixed — useful as a regression-finding test.
Reference: domain/memory.go Recall — visibility filter incomplete.

## P1-5 — Multi-blocker dependency (AND semantics)
WI_C blocked_by=[WI_A, WI_B]. Only WI_A wraps; WI_C must stay blocked.
After WI_B also wraps, WI_C unblocks and appears in ready queue.
Reference: domain/run_attempts.go unblockDependentWI — checks all blockers resolved.

## P1-6 — Cross-project memory isolation
Alice writes memory with visibility="team" on project="marketplace".
Bob queries memories on project="aihub" (where he has no role) → 403 or empty.
Confirms team-visibility is still project-scoped.

## P1-7 — Cross-user cancel permission
Reporter can cancel their own wi; others need maintainer/admin.
Bob (writer) tries to cancel Alice's wi → 403.
Admin cancels Alice's wi → 200.
Reference: router.go handleCancelWorkItem — check project access level.

## P1-8 — emit_event cross-user (pf_emit_event)
Bob (writer) emits a note event on Alice's wi → 200 (writer can emit).
Carol (viewer) tries to emit a note → 403 (viewer cannot POST events).
