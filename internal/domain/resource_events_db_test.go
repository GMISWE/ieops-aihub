package domain

// DB-gated integration tests for aihub#343: resource-lock mutations and
// declared_resources changes must be verifiable from pf_read_events alone.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@127.0.0.1:5432/aihub_test?sslmode=disable \
//	  go test ./internal/domain/ -run 'TestLockEventsDB' -count=1 -v
//
// # What the acceptance criterion actually demands
//
// aihub#343's criterion is VERIFIABILITY, not "an event was emitted": looking
// only at what pf_read_events returns, a reader must be able to decide whether a
// given lock should currently be held. So the assertions here never read
// resource_locks to check an event against it — they run
// lockVerdictFromEvents, a decision procedure with access to nothing but the
// event stream, and check that its verdict matches reality. A test that read the
// table would prove the event exists, not that it is sufficient.
//
// # Coverage: 11 execution sites, 11 covered
//
// COUNTED, not asserted. Two different numbers, and conflating them is how a
// coverage claim goes wrong:
//
//   - 7 lock-mutating SQL CONSTANTS, all in resource_events.go — the registry in
//     resource_events_test.go, pinned in both directions by
//     TestLockEvents_EveryAuditedStatementIsDeclaredAndRegistered.
//   - 11 EXECUTION SITES, i.e. calls to releaseLocks / acquireLockUpsert /
//     acquireLockIfFree: run_attempts.go has 9, gc.go 1, work_items.go 1.
//     Reproduce the count with:
//     grep -c 'releaseLocks(\|acquireLockUpsert(\|acquireLockIfFree(' on the
//     three non-test files, minus the three declarations.
//
// A statement count would UNDER-report, because three constants run from more
// than one site (lockDeleteByAttemptSQL from four; acquireLocksInsertSQL from
// two). Coverage has to be per site, since a site is what can stop calling the
// helper. Each of the 11 is reached here through its real entry point, never
// through the helper:
//
//	 site                                            cause                  arm
//	 1 FnClaimWorkItem     upsert                    claim                  claim upsert
//	 2 FnClaimWorkItem     prior-lock DELETE         claim_takeover         claim takeover release
//	 3 FnCompleteAttempt   DELETE all                attempt_terminal       complete attempt terminal
//	 4 FnCompleteAttempt   DELETE file_scope         attempt_paused         complete attempt paused
//	 5 FnForceTakeover     prior-lock DELETE         force_takeover         force takeover ...
//	 6 FnForceTakeover     re-derive upsert          force_takeover         force takeover ...
//	 7 FnAcquireLocks      DO NOTHING insert         acquire_locks          acquire locks insert
//	 8 FnAcquireLocks      orphan DELETE             orphan_reclaim         orphan reclaim ...
//	 9 FnAcquireLocks      orphan re-insert          orphan_reclaim         orphan reclaim ...
//	10 UpdateWorkItem      narrowing DELETE          declaration_narrowed   narrowing release
//	11 RunOrphanLockSweep  sweep DELETE              orphan_sweep           orphan sweep release
//
// plus two arms with no site of their own: the upsert's displaced-owner release
// (owner_replaced, a branch inside acquireLockUpsert) and the DO NOTHING insert
// that took nothing.
//
// Going through the entry points rather than the helper is the lesson of
// aihub#261: a test of the shared helper stays green when one CALL SITE stops
// using it, and "prevention that cannot reach the instances it was filed for" is
// how the previous four fixes in this area failed. Measured here too — mutant m6
// reverted site 3 alone to a raw tx.Exec and left the other ten arms green.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── The reader's decision procedure ────────────────────────────────────────

// lockEventFacts is the payload subset a reader can rely on.
type lockEventFacts struct {
	ResourceType string `json:"resource_type"`
	ResourceKey  string `json:"resource_key"`
	AttemptID    string `json:"attempt_id"`
	ClaimEpoch   int64  `json:"claim_epoch"`
	Cause        string `json:"cause"`
	OpID         string `json:"op_id"`
}

// lockVerdict is what a reader concludes about one lock key.
type lockVerdict struct {
	// Decidable is false when the stream says nothing about this key at all.
	// 🔴 It is reported SEPARATELY from Held because collapsing them is the
	// failure mode aihub#343 exists to prevent: "no events" read as "not held"
	// is exactly a reviewer taking an empty stream as proof nothing happened.
	Decidable bool
	Held      bool
	AttemptID string
	Cause     string
}

// lockVerdictFromEvents decides, from the event stream ALONE, whether a lock is
// currently held and by which attempt.
//
// This is aihub#343's acceptance criterion written as code. It gets a
// []EventRow — exactly what pf_read_events returns — and nothing else: no pool,
// no resource_locks read, no work item. If it cannot answer, the events are
// insufficient and the work item is not done.
// ⚠️ Its one unpinned premise: ListEvents orders by `created_at DESC` with NO
// tiebreaker (internal/domain/memory.go), and created_at defaults to
// clock_timestamp(). Two events on ONE key with an identical timestamp would
// order arbitrarily and the verdict would flip. Not reachable today — a batched
// multi-row INSERT never contains the same key twice, and two statements are
// always more than a clock tick apart — but it is a premise of the acceptance
// criterion rather than of this test, and it is pre-existing in ListEvents.
func lockVerdictFromEvents(events []EventRow, resourceType, resourceKey string) lockVerdict {
	// ListEvents returns newest-first; a state machine needs oldest-first.
	v := lockVerdict{}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.EventType != EventLockAcquired && e.EventType != EventLockReleased {
			continue
		}
		var f lockEventFacts
		if err := json.Unmarshal(e.Payload, &f); err != nil {
			continue
		}
		if f.ResourceType != resourceType || f.ResourceKey != resourceKey {
			continue
		}
		v.Decidable = true
		v.Cause = f.Cause
		if e.EventType == EventLockAcquired {
			v.Held = true
			v.AttemptID = f.AttemptID
			continue
		}
		// A release only clears the verdict when it is the CURRENT holder's
		// release. Without this a stale release for a superseded attempt would
		// wipe a live acquisition — the events are ordered, but two attempts of
		// one work item can interleave on the same key across a takeover.
		if v.AttemptID == "" || f.AttemptID == v.AttemptID {
			v.Held = false
			v.AttemptID = ""
		}
	}
	return v
}

// projectEvents reads a project's whole event stream the way a reviewer does.
//
// Project scope, not work-item scope, on purpose: the replay in aihub#343's
// criterion spans TWO work items (one releases, the other tries to claim), and a
// reader who could only see one wi's timeline would be back to guessing.
func projectEvents(t *testing.T, pool *pgxpool.Pool, project string) []EventRow {
	t.Helper()
	resp, err := ListEvents(context.Background(), pool, &ListEventsFilter{
		Project: &project, Limit: 500,
	})
	if err != nil {
		t.Fatalf("ListEvents(%s): %v", project, err)
	}
	return resp.Events
}

// lockEventsOfCause filters a stream to lock events with a given cause.
func lockEventsOfCause(events []EventRow, eventType, cause string) []lockEventFacts {
	var out []lockEventFacts
	for _, e := range events {
		if e.EventType != eventType {
			continue
		}
		var f lockEventFacts
		if err := json.Unmarshal(e.Payload, &f); err != nil {
			continue
		}
		if cause != "" && f.Cause != cause {
			continue
		}
		out = append(out, f)
	}
	return out
}

func declaredPaths(paths ...string) json.RawMessage {
	entries := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, map[string]any{
			"type": "path", "uri": "file:" + p, "intent": "write",
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return raw
}

// ─── Tag A: the replay from the work item's acceptance criterion ─────────────

// TestLockEventsDB_ReplayTheUndecidableRelease is aihub#343's own replay set,
// and the arm that makes the work item done or not done.
//
// The recorded damage (instance 2): a note asserted a write lock had been
// released, citing pf_acquire_locks returning `already_held: []`. A day later a
// claim for the same path still returned 409 CONFLICT_LOCK_TAKEN from that same
// attempt. Whether the release never happened, or happened and the row outlived
// it, was undecidable — and the work item explicitly forbids using
// pf_acquire_locks' return as the verification instrument, because in that
// incident it was the side that contradicted the server.
//
// So: declare a path and claim it, remove the declaration, then have a second
// work item claim the same path. Both the RELEASED and the STILL-HELD worlds are
// exercised, and in each the verdict must come from the events alone and match
// what the server actually enforces.
func TestLockEventsDB_ReplayTheUndecidableRelease(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	t.Run("released is decidable as released", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		key := proj + ":internal/cli/init.go"

		holder := seedWIWithResources(t, pool, proj, uid, "holds init.go",
			declaredPaths("internal/cli/init.go"))
		claim, aerr := claimWI(t, pool, uid, holder.ID, "idem-343-rel-a")
		if aerr != nil {
			t.Fatalf("claim holder: %v", aerr)
		}

		// Events alone must now say: held, by this attempt.
		v := lockVerdictFromEvents(projectEvents(t, pool, proj), "file_scope", key)
		if !v.Decidable {
			t.Fatalf("after the claim the event stream says NOTHING about %s — "+
				"a reviewer is back to guessing, which is the whole defect", key)
		}
		if !v.Held || v.AttemptID != claim.AttemptID {
			t.Fatalf("events say held=%v by %q, want held by %q", v.Held, v.AttemptID, claim.AttemptID)
		}

		// Narrow the declaration: the aihub#264 release fires.
		if _, aerr := UpdateWorkItem(ctx, pool, holder.ID, uid, "admin",
			map[string]string{proj: "owner"},
			&UpdateWorkItemRequest{DeclaredResources: json.RawMessage(`[]`)}); aerr != nil {
			t.Fatalf("narrowing update: %v", aerr)
		}

		v = lockVerdictFromEvents(projectEvents(t, pool, proj), "file_scope", key)
		if !v.Decidable {
			t.Fatalf("the event stream lost track of %s after the narrowing", key)
		}
		if v.Held {
			t.Errorf("events say %s is still held by %q after its declaration was removed; "+
				"this is the exact undecidable state aihub#343 was filed for", key, v.AttemptID)
		}

		// And the server agrees: a second work item can take the path. This is the
		// hop the blocked caller in the incident actually saw.
		other := seedWIWithResources(t, pool, proj, uid, "wants init.go",
			declaredPaths("internal/cli/init.go"))
		if _, aerr := claimWI(t, pool, uid, other.ID, "idem-343-rel-b"); aerr != nil {
			t.Fatalf("second claim was refused although the events said the lock was free "+
				"— events and enforcement disagree: %v", aerr)
		}
	})

	t.Run("still held is decidable as held", func(t *testing.T) {
		// The negative control. Without it, "the events said not-held" proves
		// nothing: a server that emitted lock_released unconditionally, or emitted
		// no lock_acquired at all, would pass the arm above.
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		key := proj + ":internal/cli/init.go"

		holder := seedWIWithResources(t, pool, proj, uid, "keeps init.go",
			declaredPaths("internal/cli/init.go"))
		claim, aerr := claimWI(t, pool, uid, holder.ID, "idem-343-held-a")
		if aerr != nil {
			t.Fatalf("claim holder: %v", aerr)
		}
		// Touch declared_resources WITHOUT dropping the path, so the update path
		// runs and cannot be credited with the verdict.
		if _, aerr := UpdateWorkItem(ctx, pool, holder.ID, uid, "admin",
			map[string]string{proj: "owner"},
			&UpdateWorkItemRequest{
				DeclaredResources: declaredPaths("internal/cli/init.go", "internal/cli/doctor.go"),
			}); aerr != nil {
			t.Fatalf("widening update: %v", aerr)
		}

		v := lockVerdictFromEvents(projectEvents(t, pool, proj), "file_scope", key)
		if !v.Decidable {
			t.Fatalf("the event stream says nothing about %s", key)
		}
		if !v.Held || v.AttemptID != claim.AttemptID {
			t.Errorf("events say held=%v by %q, want held by %q — a still-declared lock "+
				"must not look released", v.Held, v.AttemptID, claim.AttemptID)
		}

		// The server agrees: a second work item is refused.
		other := seedWIWithResources(t, pool, proj, uid, "also wants init.go",
			declaredPaths("internal/cli/init.go"))
		_, aerr = claimWI(t, pool, uid, other.ID, "idem-343-held-b")
		if aerr == nil {
			t.Errorf("second claim SUCCEEDED although the events said the lock was held " +
				"— events and enforcement disagree")
		}
	})

	t.Run("an unrelated key stays undecidable", func(t *testing.T) {
		// The other control: lockVerdictFromEvents must not answer a question the
		// stream cannot answer. If it defaulted to "not held" for an unknown key,
		// the released arm above would pass on an empty event stream — the
		// "absence read as proof" failure aihub#343 is about, reproduced inside its
		// own test.
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "declares one path",
			declaredPaths("a.go"))
		if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-undec"); aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		v := lockVerdictFromEvents(projectEvents(t, pool, proj), "file_scope", proj+":never-declared.go")
		if v.Decidable {
			t.Errorf("the stream claims to know about a key nobody ever declared: %+v", v)
		}
	})
}

// ─── Tag B: every mutation site emits ───────────────────────────────────────

// TestLockEventsDB_EveryMutationSiteEmits reaches all eleven lock-mutating
// statements through their real entry points.
//
// Deleting the releaseLocks / acquireLockUpsert / acquireLockIfFree call at ONE
// site leaves the other ten arms green, which is the property that makes this a
// coverage gate rather than a smoke test.
func TestLockEventsDB_EveryMutationSiteEmits(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	t.Run("claim upsert", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "claim emits", declaredPaths("a.go"))
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-claim")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		got := lockEventsOfCause(projectEvents(t, pool, proj), EventLockAcquired, lockCauseClaim)
		if len(got) != 1 || got[0].ResourceKey != proj+":a.go" || got[0].AttemptID != claim.AttemptID {
			t.Errorf("claim emitted %+v, want one lock_acquired for %q by %q",
				got, proj+":a.go", claim.AttemptID)
		}
	})

	t.Run("claim takeover release", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "takeover emits", declaredPaths("a.go"))
		first, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-tko-1")
		if aerr != nil {
			t.Fatalf("first claim: %v", aerr)
		}
		// Same user re-claiming a running wi is an implicit takeover, which
		// supersedes the prior attempt and deletes its locks.
		if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-tko-2"); aerr != nil {
			t.Fatalf("re-claim: %v", aerr)
		}
		got := lockEventsOfCause(projectEvents(t, pool, proj), EventLockReleased, lockCauseClaimTakeover)
		if len(got) != 1 || got[0].AttemptID != first.AttemptID {
			t.Errorf("claim takeover emitted %+v, want one lock_released for the superseded "+
				"attempt %q", got, first.AttemptID)
		}
	})

	t.Run("complete attempt terminal release", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "wrap emits", declaredPaths("a.go"))
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-wrap")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		if aerr := FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
			AttemptID: claim.AttemptID, ClaimEpoch: claim.ClaimEpoch,
			SessionSecret: testSecret, Status: "wrapped",
		}); aerr != nil {
			t.Fatalf("complete: %v", aerr)
		}
		got := lockEventsOfCause(projectEvents(t, pool, proj), EventLockReleased, lockCauseAttemptTerminal)
		if len(got) != 1 || got[0].ResourceKey != proj+":a.go" {
			t.Errorf("terminal completion emitted %+v, want one lock_released for %q",
				got, proj+":a.go")
		}
	})

	t.Run("complete attempt paused release", func(t *testing.T) {
		// The pause branch releases file_scope ONLY, and the distinction has to be
		// readable from the stream: "the attempt ended and the lock is still there"
		// is correct on pause and a leak on terminal.
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		declared, err := json.Marshal([]map[string]any{
			{"type": "repo", "uri": "repo:repo-343", "intent": "write", "task_branch": "pf343-pause"},
			{"type": "path", "uri": "file:a.go", "intent": "write"},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		wi := seedWIWithResources(t, pool, proj, uid, "pause emits", declared)
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-pause")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		if aerr := FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
			AttemptID: claim.AttemptID, ClaimEpoch: claim.ClaimEpoch,
			SessionSecret: testSecret, Status: "paused",
		}); aerr != nil {
			t.Fatalf("complete paused: %v", aerr)
		}
		events := projectEvents(t, pool, proj)
		got := lockEventsOfCause(events, EventLockReleased, lockCauseAttemptPaused)
		if len(got) != 1 || got[0].ResourceType != "file_scope" {
			t.Fatalf("pause emitted %+v, want exactly one file_scope lock_released", got)
		}
		// The retained git_branch lock must still read as held from the stream —
		// otherwise a reader would conclude resume is free to be taken over.
		v := lockVerdictFromEvents(events, "git_branch", "repo-343/pf343-pause")
		if !v.Decidable || !v.Held {
			t.Errorf("after pause the events say git_branch repo-343/pf343-pause is %+v; "+
				"pause retains branch locks, so it must still read as held", v)
		}
	})

	t.Run("force takeover release and re-acquire", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "force takeover emits", declaredPaths("a.go"))
		first, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-ft")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		ft, aerr := FnForceTakeover(ctx, pool, wi.ID, uid, "taker", "admin",
			map[string]string{proj: "owner"},
			&ForceTakeoverRequest{Reason: "aihub#343 coverage",
				SessionInfo: SessionInfo{MachineID: "m-343", SessionSecret: testSecret}})
		if aerr != nil {
			t.Fatalf("force_takeover: %v", aerr)
		}
		events := projectEvents(t, pool, proj)
		rel := lockEventsOfCause(events, EventLockReleased, lockCauseForceTakeover)
		if len(rel) != 1 || rel[0].AttemptID != first.AttemptID {
			t.Errorf("force_takeover release emitted %+v, want one for the prior attempt %q",
				rel, first.AttemptID)
		}
		acq := lockEventsOfCause(events, EventLockAcquired, lockCauseForceTakeover)
		if len(acq) != 1 || acq[0].AttemptID != ft.NewAttemptID {
			t.Errorf("force_takeover re-acquire emitted %+v, want one for the new attempt %q",
				acq, ft.NewAttemptID)
		}
		// End to end: the stream must land on "held by the new attempt".
		v := lockVerdictFromEvents(events, "file_scope", proj+":a.go")
		if !v.Held || v.AttemptID != ft.NewAttemptID {
			t.Errorf("after force_takeover the events say %+v, want held by %q", v, ft.NewAttemptID)
		}
	})

	t.Run("acquire locks insert", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "acquire_locks emits", json.RawMessage(`[]`))
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-al")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		mustExec(t, pool, `UPDATE work_items SET declared_resources='`+
			string(declaredPaths("mid.go"))+`'::jsonb WHERE id='`+wi.ID+`'`)
		if _, aerr := FnAcquireLocks(ctx, pool, wi.ID, &AcquireLocksRequest{
			AttemptID: claim.AttemptID, ClaimEpoch: claim.ClaimEpoch, SessionSecret: testSecret,
		}); aerr != nil {
			t.Fatalf("acquire_locks: %v", aerr)
		}
		got := lockEventsOfCause(projectEvents(t, pool, proj), EventLockAcquired, lockCauseAcquireLocks)
		if len(got) != 1 || got[0].ResourceKey != proj+":mid.go" {
			t.Errorf("acquire_locks emitted %+v, want one lock_acquired for %q",
				got, proj+":mid.go")
		}
	})

	t.Run("acquire locks emits nothing when it took nothing", func(t *testing.T) {
		// The honesty arm: a DO NOTHING insert over a key this attempt already
		// holds changed nothing, so it must record nothing. This is the property
		// that separates an audit trail from a log of intentions — pf_acquire_locks
		// already shipped a field built from the re-derived TARGET set instead of
		// the table, and it reported `already_held: []` for a lock the server was
		// still enforcing (aihub#345).
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "idempotent acquire", declaredPaths("a.go"))
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-noop")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		before := len(lockEventsOfCause(projectEvents(t, pool, proj), EventLockAcquired, ""))
		for i := 0; i < 3; i++ {
			if _, aerr := FnAcquireLocks(ctx, pool, wi.ID, &AcquireLocksRequest{
				AttemptID: claim.AttemptID, ClaimEpoch: claim.ClaimEpoch, SessionSecret: testSecret,
			}); aerr != nil {
				t.Fatalf("acquire_locks #%d: %v", i, aerr)
			}
		}
		after := len(lockEventsOfCause(projectEvents(t, pool, proj), EventLockAcquired, ""))
		if after != before {
			t.Errorf("three no-op acquire_locks calls added %d lock_acquired events; "+
				"an acquisition that did not happen must not be recorded", after-before)
		}
	})

	t.Run("orphan reclaim release and re-acquire", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		// A dead attempt holding a lock: claim a work item, then wrap it while
		// forcing the lock row to survive, which is what an un-swept orphan is.
		dead := seedWIWithResources(t, pool, proj, uid, "dies holding orphan.go",
			declaredPaths("orphan.go"))
		deadClaim, aerr := claimWI(t, pool, uid, dead.ID, "idem-343-site-orphan-dead")
		if aerr != nil {
			t.Fatalf("claim dead: %v", aerr)
		}
		mustExec(t, pool, `UPDATE run_attempts SET status='failed' WHERE id='`+deadClaim.AttemptID+`'`)

		live := seedWIWithResources(t, pool, proj, uid, "reclaims orphan.go", json.RawMessage(`[]`))
		liveClaim, aerr := claimWI(t, pool, uid, live.ID, "idem-343-site-orphan-live")
		if aerr != nil {
			t.Fatalf("claim live: %v", aerr)
		}
		mustExec(t, pool, `UPDATE work_items SET declared_resources='`+
			string(declaredPaths("orphan.go"))+`'::jsonb WHERE id='`+live.ID+`'`)
		if _, aerr := FnAcquireLocks(ctx, pool, live.ID, &AcquireLocksRequest{
			AttemptID: liveClaim.AttemptID, ClaimEpoch: liveClaim.ClaimEpoch, SessionSecret: testSecret,
		}); aerr != nil {
			t.Fatalf("acquire_locks reclaim: %v", aerr)
		}
		events := projectEvents(t, pool, proj)
		rel := lockEventsOfCause(events, EventLockReleased, lockCauseOrphanReclaim)
		if len(rel) != 1 || rel[0].AttemptID != deadClaim.AttemptID {
			t.Fatalf("orphan reclaim released %+v, want one for the dead attempt %q",
				rel, deadClaim.AttemptID)
		}
		acq := lockEventsOfCause(events, EventLockAcquired, lockCauseOrphanReclaim)
		if len(acq) != 1 || acq[0].AttemptID != liveClaim.AttemptID {
			t.Errorf("orphan reclaim acquired %+v, want one for %q", acq, liveClaim.AttemptID)
		}
		// 🔴 This is the ONLY arm that reaches acquireLockIfFree with the key
		// already taken, so it is the only place the "emit what CHANGED, not what
		// was attempted" rule is observable. The plain DO NOTHING insert ran first
		// here and took nothing — the row was the dead attempt's — so it must have
		// recorded nothing under cause=acquire_locks; only the reclaim after the
		// orphan delete really acquired anything.
		//
		// Measured: without this assertion a mutant that emits lock_acquired
		// unconditionally in acquireLockIfFree left the WHOLE suite green,
		// including the three-repeat-calls arm below — because FnAcquireLocks'
		// collision SELECT short-circuits before the insert when the caller
		// already owns the key, so that arm never reaches the helper at all.
		if plain := lockEventsOfCause(events, EventLockAcquired, lockCauseAcquireLocks); len(plain) != 0 {
			t.Errorf("the DO NOTHING insert hit an existing row and took nothing, yet emitted %+v "+
				"— an acquisition that did not happen must never be recorded", plain)
		}
		// 🔴 The release must be filed on the DEAD work item's timeline, not the
		// reclaimer's. An orphan row can belong to a different work item, and the
		// only reader with a reason to look is the one wondering where their lock
		// went — filing it under the reclaimer hides it from exactly them.
		deadStream, err := ListEvents(ctx, pool, &ListEventsFilter{WorkItemID: &dead.ID, Limit: 200})
		if err != nil {
			t.Fatalf("ListEvents(dead wi): %v", err)
		}
		if got := lockEventsOfCause(deadStream.Events, EventLockReleased, lockCauseOrphanReclaim); len(got) != 1 {
			t.Errorf("the dead work item's own timeline has %d orphan_reclaim releases, want 1 — "+
				"the release was filed under the reclaimer instead of the holder", len(got))
		}
	})

	t.Run("narrowing release", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "narrowing emits",
			declaredPaths("keep.go", "drop.go"))
		if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-narrow"); aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		if _, aerr := UpdateWorkItem(ctx, pool, wi.ID, uid, "admin",
			map[string]string{proj: "owner"},
			&UpdateWorkItemRequest{DeclaredResources: declaredPaths("keep.go")}); aerr != nil {
			t.Fatalf("narrowing update: %v", aerr)
		}
		got := lockEventsOfCause(projectEvents(t, pool, proj), EventLockReleased, lockCauseDeclarationNarrowed)
		if len(got) != 1 || got[0].ResourceKey != proj+":drop.go" {
			t.Errorf("narrowing emitted %+v, want exactly one lock_released for %q "+
				"(and none for the still-declared keep.go)", got, proj+":drop.go")
		}
	})

	t.Run("orphan sweep release", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "swept",
			declaredPaths("swept.go", "swept2.go", "swept3.go"))
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-site-sweep")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		// Kill the attempt without releasing, exactly the state the sweep exists
		// for. The sweep is the one lock mutation with no caller behind it, which
		// is why "my lock vanished and nothing I did removed it" had no answer.
		mustExec(t, pool, `UPDATE run_attempts SET status='failed' WHERE id='`+claim.AttemptID+`'`)
		var orphanCount int64
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM resource_locks rl
			JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
			WHERE ra.work_item_id = $1`, wi.ID).Scan(&orphanCount); err != nil {
			t.Fatalf("count orphans: %v", err)
		}
		res := RunOrphanLockSweep(ctx, pool)
		if res.Error != "" {
			t.Fatalf("sweep: %s", res.Error)
		}
		// 🔴 EXACT count, three locks not one. GCResult.Affected used to be the
		// DELETE's own RowsAffected and is now len(the reporting query's rows), and
		// those are only equal because both LEFT JOINs are on a primary key. An
		// off-by-N here would misreport how much the GC tick did while every event
		// still looked right, so the number is pinned rather than tested for
		// non-zero — and with more than one row, since 1 == 1 would hold under any
		// duplication or truncation bug.
		if res.Affected != orphanCount {
			t.Fatalf("sweep reported Affected=%d but %d orphan locks existed; the reporting "+
				"wrapper is not returning exactly the rows the DELETE removed",
				res.Affected, orphanCount)
		}
		if orphanCount < 2 {
			t.Fatalf("fixture produced %d orphan locks; need >1 or the count assertion "+
				"above cannot discriminate", orphanCount)
		}
		events := projectEvents(t, pool, proj)
		got := lockEventsOfCause(events, EventLockReleased, lockCauseOrphanSweep)
		if len(got) < 1 {
			t.Fatalf("sweep emitted no lock_released; it removed %d rows", res.Affected)
		}
		found := false
		for _, f := range got {
			if f.ResourceKey == proj+":swept.go" && f.AttemptID == claim.AttemptID {
				found = true
			}
		}
		if !found {
			t.Errorf("sweep events %+v do not include %q held by %q",
				got, proj+":swept.go", claim.AttemptID)
		}
		v := lockVerdictFromEvents(events, "file_scope", proj+":swept.go")
		if !v.Decidable || v.Held {
			t.Errorf("after the sweep the events say %+v, want decidably not held", v)
		}
	})

	t.Run("upsert displacing an owner emits both halves", func(t *testing.T) {
		// The statement with no call site of its own. ON CONFLICT DO UPDATE can
		// rewrite an un-swept orphan row's owner; recording only the acquisition
		// would leave a reader following the previous owner with an unmatched
		// lock_acquired, reading as "still held" for a lock that changed hands.
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		key := proj + ":contested.go"

		dead := seedWIWithResources(t, pool, proj, uid, "orphan owner of contested.go",
			declaredPaths("contested.go"))
		deadClaim, aerr := claimWI(t, pool, uid, dead.ID, "idem-343-site-disp-dead")
		if aerr != nil {
			t.Fatalf("claim dead: %v", aerr)
		}
		mustExec(t, pool, `UPDATE run_attempts SET status='failed' WHERE id='`+deadClaim.AttemptID+`'`)

		taker := seedWIWithResources(t, pool, proj, uid, "claims contested.go",
			declaredPaths("contested.go"))
		takerClaim, aerr := claimWI(t, pool, uid, taker.ID, "idem-343-site-disp-take")
		if aerr != nil {
			t.Fatalf("claim taker: %v", aerr)
		}
		events := projectEvents(t, pool, proj)
		disp := lockEventsOfCause(events, EventLockReleased, lockCauseOwnerReplaced)
		if len(disp) != 1 || disp[0].AttemptID != deadClaim.AttemptID || disp[0].ResourceKey != key {
			t.Fatalf("displacement emitted %+v, want one lock_released for %q held by %q",
				disp, key, deadClaim.AttemptID)
		}
		v := lockVerdictFromEvents(events, "file_scope", key)
		if !v.Held || v.AttemptID != takerClaim.AttemptID {
			t.Errorf("after the displacement the events say %+v, want held by %q",
				v, takerClaim.AttemptID)
		}
	})
}

// ─── wi_resources_updated ───────────────────────────────────────────────────

// TestLockEventsDB_ResourcesUpdatedSettlesTheVersionArgument is aihub#343's
// FIRST recorded instance, replayed.
//
// A step summary said declared_resources had been "reduced to CLAUDE.md(read)".
// A review called it fabricated because the update carried
// `resources_version: 0`. The producer answered that 0 was the compare-and-set
// GUARD and not an entry count. Nothing recorded either number, so neither side
// could be checked. The assertion here is that all four numbers are now on the
// record and cannot be confused for one another.
func TestLockEventsDB_ResourcesUpdatedSettlesTheVersionArgument(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	wi := seedWIWithResources(t, pool, proj, uid, "version argument",
		declaredPaths("a.go", "b.go", "c.go"))
	if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-resver"); aerr != nil {
		t.Fatalf("claim: %v", aerr)
	}
	// The reduction from the incident: three write paths down to one read path.
	// The surviving entry justifies NO lock, so the entry count and the lock
	// count differ — which is the other half of the same argument.
	reduced := json.RawMessage(`[{"type":"path","uri":"file:CLAUDE.md","intent":"read"}]`)
	if _, aerr := UpdateWorkItem(ctx, pool, wi.ID, uid, "admin",
		map[string]string{proj: "owner"},
		&UpdateWorkItemRequest{DeclaredResources: reduced}); aerr != nil {
		t.Fatalf("reduction update: %v", aerr)
	}

	events := projectEvents(t, pool, proj)
	var body map[string]any
	found := 0
	for _, e := range events {
		if e.EventType != EventResourcesUpdated {
			continue
		}
		found++
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
	}
	if found != 1 {
		t.Fatalf("got %d wi_resources_updated events, want 1", found)
	}

	// 🔴 Four separate numbers. A payload carrying one number is what made the
	// original argument unresolvable.
	for field, want := range map[string]float64{
		"prior_resources_version": 0,
		"new_resources_version":   1,
		"prior_entry_count":       3,
		"new_entry_count":         1,
	} {
		got, ok := body[field].(float64)
		if !ok {
			t.Errorf("payload is missing %q: %v", field, body)
			continue
		}
		if got != want {
			t.Errorf("payload[%q] = %v, want %v", field, got, want)
		}
	}

	// The locked-path sets say what the entry counts cannot: the one surviving
	// entry is intent=read and justifies no lock at all.
	priorPaths, _ := body["prior_locked_paths"].([]any)
	newPaths, _ := body["new_locked_paths"].([]any)
	removed, _ := body["removed_locked_paths"].([]any)
	releasedKeys, _ := body["released_file_scope_keys"].([]any)
	if len(priorPaths) != 3 {
		t.Errorf("prior_locked_paths = %v, want 3 paths", priorPaths)
	}
	if len(newPaths) != 0 {
		t.Errorf("new_locked_paths = %v, want none (the surviving entry is intent=read)", newPaths)
	}
	if len(removed) != 3 {
		t.Errorf("removed_locked_paths = %v, want 3", removed)
	}
	// released_file_scope_keys is not a subtraction — it is what the DELETE's
	// RETURNING clause reported, so it must agree with the lock_released events.
	if len(releasedKeys) != 3 {
		t.Errorf("released_file_scope_keys = %v, want 3", releasedKeys)
	}

	// The op_id ties the declaration change to the releases it caused, so a
	// reader does not have to infer causation from adjacent timestamps.
	opID, _ := body["op_id"].(string)
	if opID == "" {
		t.Fatalf("wi_resources_updated has no op_id")
	}
	tied := 0
	for _, f := range lockEventsOfCause(events, EventLockReleased, lockCauseDeclarationNarrowed) {
		if f.OpID == opID {
			tied++
		}
	}
	if tied != 3 {
		t.Errorf("%d lock_released events share the declaration change's op_id, want 3", tied)
	}
}

// TestLockEventsDB_ResourcesUpdatedAgreesWithTheReleaseItDescribes is the arm an
// independent review of this change had to add, because the suite above could
// not see the defect it found.
//
// wi_resources_updated used to recompute its added/removed sets from the derived
// KEYS while releaseUndeclaredFileScopeLocks subtracts on the declared PATHS.
// Every fixture above omits a {"type":"repo"} entry, so the two subtractions
// coincide and the whole suite stayed green.
//
// 🔴 This arm uses the flow aihub#261 calls THE ORDINARY POLYFORGE FLOW: a work
// item claimed with a repo entry holds repo-qualified locks, and the very next
// /pf-plan rewrites declared_resources as path entries only. The paths are
// untouched, so nothing is released — but every derived key changes. Measured on
// the key-based version:
//
//	removed_file_scope_keys = [proj:repo-a:go.mod, proj:repo-a:go.sum]  (still HELD)
//	added_file_scope_keys   = [proj:go.mod,        proj:go.sum]         (held by NOBODY)
//	lock_released events     = 0
//
// A reader running this work item's own decision procedure over that would
// conclude either "declared away but never released ⇒ leak, delete them" — which
// destroys the protection aihub#261 restored — or "this wi is now unprotected".
// The assertion is therefore agreement: what the record says was removed must be
// exactly what the release actually released.
func TestLockEventsDB_ResourcesUpdatedAgreesWithTheReleaseItDescribes(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	withRepo, err := json.Marshal([]map[string]any{
		{"type": "repo", "uri": "repo:repo-a", "intent": "write", "task_branch": "pf343-agree"},
		{"type": "path", "uri": "file:go.mod", "intent": "write"},
		{"type": "path", "uri": "file:go.sum", "intent": "write"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wi := seedWIWithResources(t, pool, proj, uid, "repo entry then paths only", withRepo)
	claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-agree")
	if aerr != nil {
		t.Fatalf("claim: %v", aerr)
	}
	held := fileScopeKeys(claim.AcquiredLocks)
	if len(held) != 2 {
		t.Fatalf("claim took %v, want two repo-qualified file_scope locks", held)
	}

	// The next /pf-plan: path entries only, repo entry dropped.
	if _, aerr := UpdateWorkItem(ctx, pool, wi.ID, uid, "admin",
		map[string]string{proj: "owner"},
		&UpdateWorkItemRequest{DeclaredResources: declaredPaths("go.mod", "go.sum")}); aerr != nil {
		t.Fatalf("paths-only update: %v", aerr)
	}

	events := projectEvents(t, pool, proj)
	var body map[string]any
	for _, e := range events {
		if e.EventType == EventResourcesUpdated {
			if err := json.Unmarshal(e.Payload, &body); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
		}
	}
	if body == nil {
		t.Fatalf("no wi_resources_updated event")
	}

	releases := lockEventsOfCause(events, EventLockReleased, lockCauseDeclarationNarrowed)
	removed, _ := body["removed_locked_paths"].([]any)
	added, _ := body["added_locked_paths"].([]any)
	released, _ := body["released_file_scope_keys"].([]any)

	// Nothing was declared away, so nothing may be reported as removed or added,
	// and nothing may be released.
	if len(removed) != 0 || len(added) != 0 {
		t.Errorf("the paths were untouched but the record says removed=%v added=%v. "+
			"The audit is subtracting on derived KEYS while the release subtracts on "+
			"declared PATHS (aihub#261) — a checkable, wrong record.", removed, added)
	}
	if len(releases) != 0 {
		t.Errorf("%d lock_released(declaration_narrowed) events for a narrowing that "+
			"removed nothing: %+v", len(releases), releases)
	}
	if len(released) != 0 {
		t.Errorf("released_file_scope_keys = %v, want none", released)
	}
	// And the record must AGREE with the events, in both directions.
	if len(removed) != len(releases) {
		t.Errorf("the record names %d removed paths but %d lock_released events fired; "+
			"the audit and the enforcement it describes must be one computation",
			len(removed), len(releases))
	}

	// The control: the locks really are still held, so "nothing was released" is
	// the correct answer and not the symptom of a release that silently failed.
	for _, key := range held {
		v := lockVerdictFromEvents(events, "file_scope", key)
		if !v.Decidable || !v.Held {
			t.Errorf("events say %s is %+v after a narrowing that dropped nothing; "+
				"want still held", key, v)
		}
	}
}

// TestLockEventsDB_RejectedCASRecordsNothing: a failed compare-and-set returns
// 409 and rolls back, so it changed nothing — and an event for it would describe
// a state the database never held. That is worse than a missing event: it is a
// checkable, wrong record, and the next argument would be about that.
//
// ⚠️ What this arm can and cannot detect, because the first version of this
// comment claimed more than the code proves. It does NOT pin the ORDERING of the
// emit against the CAS check: UpdateWorkItem's deferred tx.Rollback discards a
// savepoint that already committed into the transaction, so moving
// emitResourcesUpdated ABOVE the CAS check leaves this green. The one mutant it
// really catches — and the one that matters, because it is the only way an event
// can outlive a rolled-back update — is emitting on the POOL instead of the
// transaction.
func TestLockEventsDB_RejectedCASRecordsNothing(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	wi := seedWIWithResources(t, pool, proj, uid, "rejected cas", declaredPaths("a.go"))
	if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-343-cas"); aerr != nil {
		t.Fatalf("claim: %v", aerr)
	}
	wrongVersion := 99
	_, aerr := UpdateWorkItem(ctx, pool, wi.ID, uid, "admin", map[string]string{proj: "owner"},
		&UpdateWorkItemRequest{
			DeclaredResources: json.RawMessage(`[]`),
			ResourcesVersion:  &wrongVersion,
		})
	if aerr == nil {
		t.Fatalf("the update with a stale resources_version was ACCEPTED; fixture is wrong")
	}

	events := projectEvents(t, pool, proj)
	for _, e := range events {
		if e.EventType == EventResourcesUpdated {
			t.Errorf("a rejected CAS emitted wi_resources_updated: %s", e.Payload)
		}
	}
	if got := lockEventsOfCause(events, EventLockReleased, lockCauseDeclarationNarrowed); len(got) != 0 {
		t.Errorf("a rejected CAS emitted %d narrowing releases: %+v", len(got), got)
	}
	// And the lock is still readable as held, which is the state the server is in.
	v := lockVerdictFromEvents(events, "file_scope", proj+":a.go")
	if !v.Held {
		t.Errorf("after a rejected CAS the events say %+v, want still held", v)
	}
}

// ─── A dropped event must not fail the operation ────────────────────────────

// TestLockEventsDB_FailedEventInsertDoesNotAbortTheOperation measures the claim
// emitResourceEvent's comment makes, in both directions.
//
// 🔴 The negative control is the point. `_, _ = tx.Exec(ctx, "INSERT INTO
// agent_events ...")` appears all over this package and LOOKS like "ignore a
// failed event". It is not: a failed statement puts a Postgres transaction into
// the aborted state, so every later statement fails with 25P02 and Commit
// returns an error. Discarding the Go error converts a dropped EVENT into a
// failed OPERATION.
//
// Both arms run the same failing insert against the same transaction. Only the
// savepoint arm can commit afterwards.
func TestLockEventsDB_FailedEventInsertDoesNotAbortTheOperation(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	wi := seedWIWithResources(t, pool, proj, uid, "event failure containment", declaredPaths("a.go"))

	// A work_item_id that violates agent_events' FK is a real, reachable class of
	// event-insert failure and needs no fault injection hook.
	badEvent := resourceEvent{
		EventType:  EventLockReleased,
		WorkItemID: "wi_does_not_exist_343",
		Project:    proj,
		Payload:    []byte(`{"probe":true}`),
	}

	t.Run("without a savepoint the operation dies", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, `UPDATE work_items SET priority='high' WHERE id=$1`, wi.ID); err != nil {
			t.Fatalf("the operation's own write failed: %v", err)
		}
		// The naive "ignore the error" form.
		// Written out here rather than reusing a production const: this arm is
		// imitating the NAIVE form that the rest of this package uses, not calling
		// ours, so borrowing our statement would blur what is under test.
		_, _ = tx.Exec(ctx, `
			INSERT INTO agent_events (
				id, work_item_id, run_attempt_id, actor_user_id, actor_display,
				api_key_id, event_type, payload, project
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			NewID("evt"), badEvent.WorkItemID, nil, nil, "", "",
			badEvent.EventType, badEvent.Payload, proj)
		if err := tx.Commit(ctx); err == nil {
			t.Errorf("committed after a failed event insert with no savepoint — " +
				"then emitResourceEvent's savepoint is unnecessary and its comment is wrong")
		}
	})

	t.Run("with the savepoint the operation survives", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, `UPDATE work_items SET priority='low' WHERE id=$1`, wi.ID); err != nil {
			t.Fatalf("the operation's own write failed: %v", err)
		}
		emitResourceEvent(ctx, tx, badEvent) // must not poison tx
		if _, err := tx.Exec(ctx, `UPDATE work_items SET milestone='m343' WHERE id=$1`, wi.ID); err != nil {
			t.Fatalf("the transaction was poisoned by a dropped event: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit failed after a dropped event: %v", err)
		}
		var priority string
		if err := pool.QueryRow(ctx, `SELECT priority FROM work_items WHERE id=$1`, wi.ID).
			Scan(&priority); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if priority != "low" {
			t.Errorf("priority = %q, want %q — the operation did not commit", priority, "low")
		}
	})

	t.Run("a batch past the bind-parameter ceiling still lands", func(t *testing.T) {
		// 🔴 The batching that makes releases cheap is also a way to lose them all.
		// The extended query protocol allows 65535 bind parameters per statement
		// and pgx enforces it client-side, so at 9 parameters per row ONE statement
		// dies at 7282 rows — and because the savepoint contains the failure, the
		// operation commits and the whole tick's events vanish with a line on
		// stderr. Measured with a probe of the exact statement shape:
		//
		//	7281 rows (65529 params) -> ok
		//	7282 rows (65538 params) -> "extended protocol limited to 65535 parameters"
		//
		// gc.go's orphan sweep is the one genuinely unbounded producer (no LIMIT,
		// one statement), so a mass attempt failure reaches it. This arm sends more
		// than the ceiling through the real entry point and requires every row.
		const n = 7300
		evs := make([]resourceEvent, 0, n)
		for i := 0; i < n; i++ {
			evs = append(evs, resourceEvent{
				EventType:  EventLockReleased,
				WorkItemID: wi.ID,
				Project:    proj,
				Payload:    []byte(`{"cause":"ceiling_probe","resource_type":"file_scope"}`),
			})
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		emitResourceEvents(ctx, tx, evs)
		var got int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM agent_events
			WHERE work_item_id=$1 AND payload->>'cause'='ceiling_probe'`, wi.ID).Scan(&got); err != nil {
			t.Fatalf("count: %v", err)
		}
		if got != n {
			t.Errorf("emitResourceEvents(%d events) landed %d; a batch above the 65535 "+
				"bind-parameter ceiling was silently dropped", n, got)
		}
		// Rolled back: this arm must not leave 7300 rows behind for the others.
	})

	t.Run("an unrecordable release does not poison the transaction", func(t *testing.T) {
		// ⚠️ NOT end to end — this drives emitResourceEvent directly on a
		// hand-built transaction, and no claim or lock mutation happens here. It
		// covers only the drop branch: an event that cannot be recorded at all (no
		// resolvable work item) must leave the caller's tx usable.
		// emitResourceEvents refuses to issue that INSERT, because agent_events
		// rejects a NULL work_item_id for these types.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		emitResourceEvent(ctx, tx, resourceEvent{
			EventType: EventLockReleased, WorkItemID: "", Payload: []byte(`{}`),
		})
		if _, err := tx.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatalf("an unrecordable event poisoned the transaction: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit failed after an unrecordable event: %v", err)
		}
	})
}
