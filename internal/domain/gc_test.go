package domain

import (
	"regexp"
	"strings"
	"testing"
	"time"
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

// ─── Partition creator month arithmetic (aihub#268) ──────────────────────────
//
// agent_events is PARTITION BY RANGE (created_at) and nearly every aihub action
// writes it, so a month with no partition is a total write-path outage
// (`no partition of relation "agent_events" found for row`), not a lost audit
// row. partitionMonthsAhead is the whole of the sweep's decision about which
// months must exist, and it is pure — these tests pin its contract without a
// live DB, which is what the rest of this suite can also do (see the note at the
// top of this file: no testcontainers are wired into the worktree).

// specNames is the partition table names of specs, in order.
func specNames(specs []partitionSpec) []string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	return names
}

func TestPartitionMonthsAhead_MonthEndDoesNotSkipAMonth(t *testing.T) {
	// The bug: the sweep stepped months with now.AddDate(0, i, 0) on now's own
	// day-of-month, and AddDate normalises overflow instead of clamping. Asked
	// on 2026-08-31, i=1 gave 2026-09-31 → 2026-10-01, so it requested
	// August, October, October and never September.
	got := specNames(partitionMonthsAhead(time.Date(2026, 8, 31, 23, 59, 0, 0, time.UTC), 6))
	want := []string{
		"agent_events_2026_08",
		"agent_events_2026_09",
		"agent_events_2026_10",
		"agent_events_2026_11",
		"agent_events_2026_12",
		"agent_events_2027_01",
		"agent_events_2027_02",
	}
	if len(got) != len(want) {
		t.Fatalf("partitionMonthsAhead(2026-08-31, 6) returned %d specs, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("spec[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestPartitionMonthsAhead_JanuaryEndCoversFebruary(t *testing.T) {
	// The nastiest instance of the same overflow: on 2027-01-31, Jan+1 month
	// normalises to 2027-03-03, so February — the shortest month, and the one
	// no other day-of-month can reach past — was skipped entirely.
	got := specNames(partitionMonthsAhead(time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC), 2))
	want := []string{"agent_events_2027_01", "agent_events_2027_02", "agent_events_2027_03"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("partitionMonthsAhead(2027-01-31, 2) = %v, want %v", got, want)
		}
	}
}

func TestPartitionMonthsAhead_EveryDayOfTwoYearsYieldsConsecutiveMonths(t *testing.T) {
	// Property form of the two cases above, so no future rewrite can reintroduce
	// a day-of-month dependence for some date nobody thought to enumerate: for
	// EVERY day in a two-year window, the specs must start at that day's own
	// month and step one calendar month at a time, with each range's end equal
	// to the next range's start.
	const ahead = 6
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	for ; day.Before(end); day = day.AddDate(0, 0, 1) {
		specs := partitionMonthsAhead(day, ahead)
		if len(specs) != ahead+1 {
			t.Fatalf("%s: got %d specs, want %d", day.Format("2006-01-02"), len(specs), ahead+1)
		}

		wantStart := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
		for i, s := range specs {
			expStart := wantStart.AddDate(0, i, 0)
			expEnd := expStart.AddDate(0, 1, 0)
			if s.Start != expStart.Format(partitionBoundLayout) || s.End != expEnd.Format(partitionBoundLayout) {
				t.Fatalf("%s: spec[%d] = [%s, %s), want [%s, %s)", day.Format("2006-01-02"), i,
					s.Start, s.End, expStart.Format(partitionBoundLayout), expEnd.Format(partitionBoundLayout))
			}
			expName := "agent_events_" + expStart.Format("2006_01")
			if s.Name != expName {
				t.Fatalf("%s: spec[%d].Name = %q, want %q", day.Format("2006-01-02"), i, s.Name, expName)
			}
			// Contiguity: no gap and no overlap between adjacent partitions.
			if i > 0 && specs[i-1].End != s.Start {
				t.Fatalf("%s: gap between spec[%d].End=%s and spec[%d].Start=%s",
					day.Format("2006-01-02"), i-1, specs[i-1].End, i, s.Start)
			}
		}
	}
}

func TestPartitionMonthsAhead_CoversTheMonthThatBrokeAihub268(t *testing.T) {
	// Concrete regression anchor. Production had partitions only through
	// 2026_10 (upper bound 2026-11-01 00:00 UTC), so from any day in August the
	// live constant must already be asking for 2026_11 — the month whose absence
	// was the reported outage.
	specs := partitionMonthsAhead(time.Date(2026, 8, 27, 16, 19, 0, 0, time.UTC), partitionLookaheadMonths)
	var found bool
	for _, s := range specs {
		if s.Name == "agent_events_2026_11" {
			found = true
			if s.Start != "2026-11-01 00:00:00+00" || s.End != "2026-12-01 00:00:00+00" {
				t.Errorf("agent_events_2026_11 bounds = [%s, %s), want [2026-11-01 00:00:00+00, 2026-12-01 00:00:00+00)", s.Start, s.End)
			}
		}
	}
	if !found {
		t.Errorf("with partitionLookaheadMonths=%d, a sweep run on 2026-08-27 does not reach agent_events_2026_11; specs=%v",
			partitionLookaheadMonths, specNames(specs))
	}
}

func TestPartitionLookaheadMonths_LeavesRunwayToNoticeAFailure(t *testing.T) {
	// The failure this sweep guards against is silent by nature, so the
	// lookahead is the runway an operator has to notice and fix it. Two months
	// (the pre-aihub#268 value) is not runway. This is a floor, not the value:
	// raising the constant is fine, dropping back toward 2 is the regression.
	if partitionLookaheadMonths < 3 {
		t.Errorf("partitionLookaheadMonths = %d, want >= 3 months of runway (aihub#268)", partitionLookaheadMonths)
	}
}

func TestCreatePartitionSQL_AttachesTheSpecToAgentEvents(t *testing.T) {
	spec := partitionSpec{
		Name:  "agent_events_2026_11",
		Start: "2026-11-01 00:00:00+00",
		End:   "2026-12-01 00:00:00+00",
	}
	got := normSQL(createPartitionSQL(spec))

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS agent_events_2026_11",
		"PARTITION OF agent_events",
		"FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')",
	} {
		if !strings.Contains(got, normSQL(want)) {
			t.Errorf("createPartitionSQL missing fragment %q.\n got: %q", want, got)
		}
	}
}

func TestPartitionMonthsAhead_BoundsCarryAnExplicitUTCOffset(t *testing.T) {
	// A range bound written as a bare 'YYYY-MM-01' is cast to timestamptz using
	// the SESSION TimeZone, not UTC. Migration 0006 created its bounds under a
	// UTC session (production shows `TO ('2026-11-01 00:00:00+00')`), so a
	// server running in, say, +08 would compute a boundary 8 hours off and
	// either collide with the neighbouring partition or open a gap that routes
	// nothing. Pin the literal shape: date, time, and an explicit +00.
	boundRE := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} 00:00:00\+00$`)
	for _, s := range partitionMonthsAhead(time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC), 3) {
		if !boundRE.MatchString(s.Start) {
			t.Errorf("%s Start = %q, want an explicit-UTC timestamptz literal", s.Name, s.Start)
		}
		if !boundRE.MatchString(s.End) {
			t.Errorf("%s End = %q, want an explicit-UTC timestamptz literal", s.Name, s.End)
		}
	}

	// Non-UTC input must still yield UTC-offset literals for the calendar month
	// the caller is in (RunPartitionCreate passes time.Now().UTC(); this pins
	// that the formatting itself cannot leak a local offset).
	shanghai := time.FixedZone("CST", 8*3600)
	for _, s := range partitionMonthsAhead(time.Date(2026, 8, 31, 23, 0, 0, 0, shanghai), 1) {
		if !boundRE.MatchString(s.Start) || !boundRE.MatchString(s.End) {
			t.Errorf("non-UTC now leaked into bounds: %s = [%s, %s)", s.Name, s.Start, s.End)
		}
	}
}
