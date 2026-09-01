package domain

import (
	"fmt"
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

// ─── Sweep cadence and alert idempotency (aihub#266) ─────────────────────────
//
// The DB-backed half of aihub#266 lives in gc_alert_idempotency_db_test.go: it
// measures the actual events-per-day, and it holds the negative control that
// fails on the pre-change build. What follows is what can be pinned without a
// server — the two constants' relationship, the shape of the sweep table, and
// the schedule's arithmetic — because each of those is a place where a plausible
// edit silently un-fixes the bug.

// TestGCAlertCadence_WindowIsTheCadenceAndPollingIsMuchFaster pins the
// relationship between the two constants, which is the one thing about them that
// is not free to change.
//
// The window is the user-visible cadence and the poll period is only a load
// knob, and the poll period has to be MUCH the smaller of the two. It is not
// enough for it merely to differ: an earlier draft of this change set the
// schedule to 24h and the window to 23h, which avoids the deadlock at
// window == period but leaves the SCHEDULE'S PHASE deciding the delivered
// cadence — and a restart randomises that phase. An instance restarting 22h
// after the last alert runs the sweep, the window correctly suppresses it, the
// schedule records the run anyway, and the next run is a full period later:
// measured alert gap ~46h, which is exactly the 2x-period failure the window was
// sized to avoid. Polling far more often than the window makes the window always
// the binding constraint, so phase can cost at most one poll period.
func TestGCAlertCadence_WindowIsTheCadenceAndPollingIsMuchFaster(t *testing.T) {
	if gcAlertPollPeriod >= gcAlertRepeatWindow {
		t.Fatalf("gcAlertPollPeriod (%v) must be much SHORTER than gcAlertRepeatWindow (%v), "+
			"or the schedule rather than the window decides how often a wi is alerted about, "+
			"and one restart at an unlucky phase stretches that to nearly twice the window",
			gcAlertPollPeriod, gcAlertRepeatWindow)
	}
	if gcAlertRepeatWindow <= 0 || gcAlertPollPeriod <= 0 {
		t.Fatalf("window=%v poll=%v; a non-positive window suppresses nothing and restores "+
			"the per-tick duplicate flood, and a non-positive poll period is gcEveryTick",
			gcAlertRepeatWindow, gcAlertPollPeriod)
	}

	// The delivered cadence is [window, window+poll]. It has to come out at or
	// under the specified "daily", or the sweep is no longer doing what §15 asks
	// however few duplicates it emits.
	if worst := gcAlertRepeatWindow + gcAlertPollPeriod; worst > gcSpecifiedAlertCadence {
		t.Errorf("worst-case alert-to-alert is %v (window %v + poll %v), which exceeds the "+
			"specified cadence of %v", worst, gcAlertRepeatWindow, gcAlertPollPeriod,
			gcSpecifiedAlertCadence)
	}
	// ...and not so far under it that the "daily" alert arrives several times a
	// day. Both bounds, so neither constant can drift alone.
	if gcAlertRepeatWindow > gcSpecifiedAlertCadence {
		t.Errorf("gcAlertRepeatWindow (%v) exceeds the specified cadence (%v), so a wi waits "+
			"longer than a day for its alert", gcAlertRepeatWindow, gcSpecifiedAlertCadence)
	}
	if gcAlertRepeatWindow < gcSpecifiedAlertCadence/2 {
		t.Errorf("gcAlertRepeatWindow (%v) is less than half the specified cadence (%v): a wi "+
			"would be alerted about %d times a day", gcAlertRepeatWindow,
			gcSpecifiedAlertCadence, gcSpecifiedAlertCadence/gcAlertRepeatWindow)
	}
}

// TestGCAlertRepeatWindowArg_IsTheWindowConstantInSeconds keeps the value
// actually sent to Postgres tied to the Go constant. These are the two halves of
// one number living in two grammars; a divergence here makes every window
// reasoning in gc.go describe a duration the database never sees.
func TestGCAlertRepeatWindowArg_IsTheWindowConstantInSeconds(t *testing.T) {
	want := fmt.Sprintf("%d seconds", int64(gcAlertRepeatWindow/time.Second))
	if gcAlertRepeatWindowArg != want {
		t.Fatalf("gcAlertRepeatWindowArg = %q, want %q", gcAlertRepeatWindowArg, want)
	}
	// A Duration that is not a whole number of seconds would be silently
	// truncated on the way into the interval literal.
	if gcAlertRepeatWindow%time.Second != 0 {
		t.Errorf("gcAlertRepeatWindow = %v is not a whole number of seconds, so %q loses "+
			"its sub-second part", gcAlertRepeatWindow, gcAlertRepeatWindowArg)
	}
}

// TestGCSweepTable_IsTheDocumentedCadenceForAllEightSweeps is criterion 4 of
// aihub#266 written down as a test rather than as prose in a PR description.
//
// The failure it guards is specific: RunAll is shared by eight sweeps, so the
// cheapest way to make two of them daily is to slow the whole thing down, which
// silently moves six sweeps that have documented reasons to run on every tick
// (see gcSweepTable's comment). Anyone doing that has to come here and say so.
func TestGCSweepTable_IsTheDocumentedCadenceForAllEightSweeps(t *testing.T) {
	want := map[string]time.Duration{
		sweepOrphanLockCleanup:      gcEveryTick,
		sweepMemoryExpiredArchive:   gcEveryTick,
		sweepMethodologyExpiry:      gcEveryTick,
		sweepEventPayloadTruncation: gcEveryTick,
		sweepUnblockDependentWI:     gcEveryTick,
		sweepPartitionCreate:        gcEveryTick,
		sweepNeedsHumanSessionAging: gcAlertPollPeriod,
		sweepUnclassifiedWIAlert:    gcAlertPollPeriod,
	}

	table := gcSweepTable()
	if len(table) != len(want) {
		t.Fatalf("gcSweepTable has %d sweeps, want %d — a sweep was added or removed "+
			"without deciding its cadence", len(table), len(want))
	}

	seen := make(map[string]bool, len(table))
	for _, s := range table {
		if s.Fn == nil {
			t.Errorf("sweep %q has a nil Fn", s.Name)
		}
		if seen[s.Name] {
			t.Errorf("sweep name %q appears twice; the schedule keys on the name, so two "+
				"sweeps sharing one would throttle each other", s.Name)
		}
		seen[s.Name] = true

		wantPeriod, known := want[s.Name]
		if !known {
			t.Errorf("sweep %q is not in this test's cadence table; add it with the reason "+
				"for its period", s.Name)
			continue
		}
		if s.Period != wantPeriod {
			t.Errorf("sweep %q period = %v, want %v", s.Name, s.Period, wantPeriod)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("sweep %q is missing from gcSweepTable, so RunDue and RunAll no longer "+
				"drive it at all", name)
		}
	}
}

// TestGCSchedule_ThrottlesASweepAndThenReleasesIt is the period gate in
// both directions without a database.
//
// The reverse half is the load-bearing one: a gate that returns false forever is
// a perfect fix for "too many runs" and a total failure of the sweep.
func TestGCSchedule_ThrottlesASweepAndThenReleasesIt(t *testing.T) {
	s := newGCSchedule()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// FORWARD: 1,440 minute-spaced ticks (one simulated day of the 60s ticker)
	// must let a daily sweep through exactly once. This is runDueAt's own
	// due/record pairing, for a sweep that completes cleanly every time.
	runs := 0
	for i := 0; i < 24*60; i++ {
		now := start.Add(time.Duration(i) * time.Minute)
		if s.due(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now) {
			runs++
			s.record(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now)
		}
	}
	if want := int(24 * time.Hour / gcAlertPollPeriod); runs != want {
		t.Errorf("the alert sweep ran %d times across one day of 60s ticks, want %d "+
			"(one per gcAlertPollPeriod of %v)", runs, want, gcAlertPollPeriod)
	}

	// REVERSE: after one period it must become due again. Without this the gate
	// could be "return false after the first call", which passes every duplicate
	// test. Checked on a schedule whose single recorded run is at a known
	// instant, rather than on `s` above, whose last recorded run is 23h into the
	// simulated day.
	fresh := newGCSchedule()
	fresh.record(sweepUnclassifiedWIAlert, gcAlertPollPeriod, start)
	if fresh.due(sweepUnclassifiedWIAlert, gcAlertPollPeriod, start.Add(gcAlertPollPeriod-time.Second)) {
		t.Error("a sweep was due one second BEFORE its period elapsed")
	}
	if !fresh.due(sweepUnclassifiedWIAlert, gcAlertPollPeriod, start.Add(gcAlertPollPeriod)) {
		t.Error("the alert sweep did not become due again after gcAlertPollPeriod; the " +
			"schedule silences it permanently rather than throttling it")
	}

	// REVERSE: an unrelated sweep name must not be throttled by this one's run.
	if !s.due(sweepNeedsHumanSessionAging, gcAlertPollPeriod, start) {
		t.Error("running one throttled sweep throttled a different one; the schedule is keyed " +
			"globally instead of per sweep")
	}

	// REVERSE: an attempt that is never recorded must stay due. runDueAt records
	// only a sweep that completed without an error, so a failing sweep has to
	// remain due on the next tick rather than going quiet for a whole period.
	unrecorded := newGCSchedule()
	for i := 0; i < 5; i++ {
		if !unrecorded.due(sweepUnclassifiedWIAlert, gcAlertPollPeriod, start.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("an unrecorded sweep stopped being due on attempt %d; due() is "+
				"recording on its own, so a sweep that errors would be silenced for a "+
				"whole period", i+1)
		}
	}
}

// TestGCSchedule_NeverThrottlesAnEveryTickSweep is the other reverse direction:
// the six sweeps aihub#266 did NOT touch must still run on every tick, including
// when two ticks land closer together than a nominal 60s.
//
// That last case is why gcEveryTick is 0 rather than 60*time.Second. With a
// literal 60s period, elapsed-since-last-run comparisons drop any tick that
// arrives a hair early — ordinary scheduling jitter — and six sweeps quietly run
// at half their documented cadence.
func TestGCSchedule_NeverThrottlesAnEveryTickSweep(t *testing.T) {
	s := newGCSchedule()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		if !s.due(sweepOrphanLockCleanup, gcEveryTick, now) {
			t.Fatalf("an every-tick sweep was throttled on call %d at an identical instant", i+1)
		}
		s.record(sweepOrphanLockCleanup, gcEveryTick, now)
	}
	// A tick 59.999s after the previous one — early, as real tickers are.
	if !s.due(sweepOrphanLockCleanup, gcEveryTick, now.Add(59999*time.Millisecond)) {
		t.Error("an every-tick sweep was throttled by a tick arriving under 60s after the " +
			"last one")
	}
	// record() must not have stored anything for a gcEveryTick sweep; if it did,
	// giving that sweep a period later would silently inherit a stale timestamp.
	if len(s.lastRun) != 0 {
		t.Errorf("record() stored %d entries for a gcEveryTick sweep, want 0", len(s.lastRun))
	}
}

// TestAlertSweepSQL_CandidateAndInsertCarryTheIdenticalGuard pins that each
// alert sweep's two statements share one rendered predicate.
//
// The hazard is directional and silent. If the candidate query's guard is LOOSER
// than the INSERT's, the sweep selects a wi, the INSERT refuses it, and
// Affected reports 0 — an alert that never arrives, with no error anywhere. A
// duplicates-only test cannot see that; this can.
func TestAlertSweepSQL_CandidateAndInsertCarryTheIdenticalGuard(t *testing.T) {
	for _, tc := range []struct {
		eventType            string
		candidate, insertSQL string
	}{
		{"wi_classification_missing", unclassifiedWIAlertCandidateSQL, unclassifiedWIAlertInsertSQL},
		{"wi_needs_attention", needsHumanSessionCandidateSQL, needsHumanSessionInsertSQL},
	} {
		// Rendered with each statement's own wi expression and interval
		// placeholder — everything else must match character for character.
		wantInCandidate := normSQL(alertNotRepeatedSQL("wi.id", tc.eventType, "$1"))
		wantInInsert := normSQL(alertNotRepeatedSQL("$2", tc.eventType, "$5"))

		if !strings.Contains(normSQL(tc.candidate), wantInCandidate) {
			t.Errorf("%s: the candidate query does not carry the repeat-window guard.\n"+
				"want substring: %s\ngot: %s", tc.eventType, wantInCandidate, normSQL(tc.candidate))
		}
		if !strings.Contains(normSQL(tc.insertSQL), wantInInsert) {
			t.Errorf("%s: the INSERT does not carry the repeat-window guard, so two "+
				"instances racing between the candidate query and the write both insert.\n"+
				"want substring: %s\ngot: %s", tc.eventType, wantInInsert, normSQL(tc.insertSQL))
		}
		// The INSERT must be the guarded INSERT ... SELECT form, not VALUES: a
		// WHERE NOT EXISTS cannot be attached to VALUES, so a revert to VALUES
		// would drop the guard while still looking like an insert of one row.
		if strings.Contains(normSQL(tc.insertSQL), "VALUES") {
			t.Errorf("%s: the INSERT uses VALUES, which cannot carry the WHERE NOT EXISTS "+
				"guard", tc.eventType)
		}
	}
}

// TestGCSQL_PartitionKeyBoundsReadFromNowNotClockTimestamp pins the one rule
// that every statement in gc.go which READS agent_events.created_at has to obey.
//
// agent_events is RANGE partitioned on created_at. A lower bound the planner can
// evaluate lets it prune partitions; one it cannot forces a visit to every
// partition. now() is STABLE and prunes; clock_timestamp() is VOLATILE and does
// not. Measured on a fixture rebuilt to production's shape (199,221 events over
// seven monthly partitions, 111,221 of them on one work item, 78 unclassified
// wis): 1.1ms with now(), 181-197ms with clock_timestamp(), and "Subplans
// Removed: 4" appears only in the first plan.
//
// This is a rule about a whole file rather than one statement, and it is exactly
// the kind that erodes: every INSERT here correctly WRITES clock_timestamp(),
// so "make the timestamps consistent" reads like a tidy-up and costs two orders
// of magnitude. A mutation run confirmed the erosion is invisible otherwise —
// putting clock_timestamp() back into the backlog warning's window left every
// other test in this package green.
//
// The distinction the test encodes: `created_at > …` is a bound being SCANNED
// and must use now(); a created_at in a VALUES/SELECT list is a value being
// WRITTEN and must keep clock_timestamp(), which is why the check is anchored on
// the comparison rather than on the function name alone.
func TestGCSQL_PartitionKeyBoundsReadFromNowNotClockTimestamp(t *testing.T) {
	statements := map[string]string{
		"unclassifiedWIAlertCandidateSQL": unclassifiedWIAlertCandidateSQL,
		"unclassifiedWIAlertInsertSQL":    unclassifiedWIAlertInsertSQL,
		"needsHumanSessionCandidateSQL":   needsHumanSessionCandidateSQL,
		"needsHumanSessionInsertSQL":      needsHumanSessionInsertSQL,
		"defaultBacklogWarnSQL":           defaultBacklogWarnSQL,
	}

	for name, sql := range statements {
		norm := normSQL(sql)

		if strings.Contains(norm, "created_at > clock_timestamp()") {
			t.Errorf("%s bounds created_at with clock_timestamp(), which is VOLATILE: the "+
				"planner cannot prune agent_events' partitions with it, so this statement "+
				"visits every partition. Use now().\n%s", name, norm)
		}
		if !strings.Contains(norm, "created_at > now()") {
			t.Errorf("%s no longer bounds created_at with now(). Either the window was "+
				"removed — in which case the duplicate flood is back — or it was rewritten "+
				"in a shape this rule can no longer see, which is worse.\n%s", name, norm)
		}

		// REVERSE: the statements that WRITE a row must still write
		// clock_timestamp(). A blanket clock_timestamp()->now() replacement would
		// satisfy everything above while quietly changing every emitted event's
		// timestamp from wall-clock to transaction-start.
		if strings.Contains(norm, "INSERT INTO agent_events") &&
			!strings.Contains(norm, "clock_timestamp()") {
			t.Errorf("%s inserts a row without clock_timestamp(): the written created_at "+
				"was replaced along with the scanned bound.\n%s", name, norm)
		}
	}

	// The rule is over a SET, so it has to fail when the set shrinks — otherwise
	// deleting an entry above is a silent way to exempt a statement from it.
	if len(statements) != 5 {
		t.Fatalf("this test covers %d statements; every named SQL in gc.go that reads "+
			"agent_events.created_at must be listed", len(statements))
	}
}
