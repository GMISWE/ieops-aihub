package domain

import (
	"regexp"
	"strings"
	"testing"
)

// ─── Orphan Lock Sweep predicate (aihub#145) ─────────────────────────────────
//
// The domain test suite is pure-unit (no live DB / testcontainers wired into
// the worktree), so RunOrphanLockSweep cannot be exercised against a real
// pool here. Instead we assert on the sweep SQL itself: the retention contract
// is that a lock is kept while its owner attempt is 'running' OR 'paused'
// (FnCompleteAttempt keeps locks on paused for resume — N4 / C5-3 invariant;
// the claim conflict-check matches IN ('running','paused')). These tests pin
// the sweep predicate to that contract and guard against a regression back to
// the too-strict 'running'-only predicate that deleted paused attempts' locks.

// normSQL collapses all whitespace so the assertions are insensitive to
// formatting/indentation of the embedded SQL literal.
func normSQL(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestOrphanLockSweepSQL_RetainsPausedLocks(t *testing.T) {
	got := normSQL(orphanLockSweepSQL)

	// The predicate must match the retention contract: running OR paused.
	wantPredicate := "ra.status IN ('running', 'paused')"
	if !strings.Contains(got, normSQL(wantPredicate)) {
		t.Errorf("orphan-lock sweep predicate does not match lock-retention contract.\n got: %q\nwant substring: %q", got, wantPredicate)
	}
}

func TestOrphanLockSweepSQL_NotRunningOnly(t *testing.T) {
	got := normSQL(orphanLockSweepSQL)

	// Regression guard: the old, too-strict predicate `ra.status = 'running'`
	// (with no IN clause) deleted the locks a paused attempt deliberately
	// retains, breaking resume and enabling lock theft (aihub#145). Make sure
	// we never ship a bare equality on 'running'.
	badPredicate := regexp.MustCompile(`ra\.status\s*=\s*'running'`)
	if badPredicate.MatchString(got) {
		t.Errorf("orphan-lock sweep still uses the too-strict `ra.status = 'running'` predicate; it must retain paused locks via IN ('running','paused'). got: %q", got)
	}
}

func TestOrphanLockSweepSQL_DeletesGenuineOrphans(t *testing.T) {
	got := normSQL(orphanLockSweepSQL)

	// The sweep must still DELETE locks (genuinely orphaned ones: wrapped /
	// failed / cancelled / no-attempt) via a NOT EXISTS anti-join on
	// run_attempts. This proves we did not neuter the sweep — only widened the
	// retention set.
	for _, want := range []string{
		"DELETE FROM resource_locks",
		"WHERE NOT EXISTS",
		"FROM run_attempts ra",
		"ra.id = rl.owner_attempt_id",
	} {
		if !strings.Contains(got, normSQL(want)) {
			t.Errorf("orphan-lock sweep SQL missing expected fragment %q.\n got: %q", want, got)
		}
	}
}
