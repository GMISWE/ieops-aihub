package domain

import (
	"strings"
	"testing"
)

// ─── AcquireLocks SQL contracts (aihub#190) ───────────────────────────────────
//
// Pure-unit tests (no live DB) that assert on the SQL literal constants used
// by FnAcquireLocks and the pause branch of FnCompleteAttempt.  Following the
// same pattern as gc_test.go / orphanLockSweepSQL.

// TestAcquireLocksInsertSQL_NoSteal verifies that the INSERT used by
// FnAcquireLocks uses DO NOTHING (never steals a lock from another attempt).
func TestAcquireLocksInsertSQL_NoSteal(t *testing.T) {
	got := normSQL(acquireLocksInsertSQL)

	// Must contain DO NOTHING — the non-stealing variant.
	if !strings.Contains(got, "DO NOTHING") {
		t.Errorf("acquire-locks INSERT must use ON CONFLICT ... DO NOTHING to avoid stealing.\n got: %q", got)
	}

	// Must NOT contain DO UPDATE — that is the stealing variant used by claim.
	if strings.Contains(got, "DO UPDATE") {
		t.Errorf("acquire-locks INSERT must NOT use DO UPDATE (that would steal locks).\n got: %q", got)
	}
}

// TestAcquireLocksCollisionSQL_LiveAttemptPredicate verifies that the collision
// SELECT matches both running and paused attempts — consistent with the claim-time
// check and the orphan-sweep contract.
func TestAcquireLocksCollisionSQL_LiveAttemptPredicate(t *testing.T) {
	got := normSQL(acquireLocksCollisionSQL)

	want := "ra.status IN ('running', 'paused')"
	if !strings.Contains(got, normSQL(want)) {
		t.Errorf("collision SELECT predicate must match running and paused attempts.\n got: %q\nwant substring: %q", got, want)
	}
}

// TestAcquireLocksCollisionSQL_NotRunningOnly is a regression guard: the
// bare equality `ra.status = 'running'` would miss paused-attempt locks.
func TestAcquireLocksCollisionSQL_NotRunningOnly(t *testing.T) {
	got := normSQL(acquireLocksCollisionSQL)

	// Simple substring: if the IN clause is absent and a bare = 'running' appears
	// instead, that is a regression.
	if strings.Contains(got, "status = 'running'") && !strings.Contains(got, "IN") {
		t.Errorf("collision SELECT uses bare = 'running' without IN clause; paused-attempt locks would be ignored.\n got: %q", got)
	}
}

// TestAcquireLocksReleasePausedSQL_FileScopeOnly verifies that the SQL released
// on pause targets only file_scope locks, leaving git_branch/deploy_env for resume.
func TestAcquireLocksReleasePausedSQL_FileScopeOnly(t *testing.T) {
	got := normSQL(acquireLocksReleasePausedSQL)

	// Must delete from resource_locks.
	if !strings.Contains(got, "DELETE FROM resource_locks") {
		t.Errorf("pause-release SQL must DELETE FROM resource_locks.\n got: %q", got)
	}

	// Must filter by resource_type = 'file_scope'.
	wantFilter := "resource_type='file_scope'"
	if !strings.Contains(got, normSQL(wantFilter)) {
		t.Errorf("pause-release SQL must filter to file_scope only.\n got: %q\nwant substring: %q", got, wantFilter)
	}
}

// TestAcquireLocksReleasePausedSQL_NotAllLocks ensures the pause-release does NOT
// unconditionally delete all locks (which would break resume for branch/env locks).
func TestAcquireLocksReleasePausedSQL_NotAllLocks(t *testing.T) {
	got := normSQL(acquireLocksReleasePausedSQL)

	// A bare DELETE without a resource_type filter would remove all locks.
	// Confirm the predicate is present.
	if !strings.Contains(got, "resource_type") {
		t.Errorf("pause-release SQL is missing resource_type predicate — would delete ALL locks on pause.\n got: %q", got)
	}
}
