package domain

import (
	"encoding/json"
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

// ─── requires_human_session defaulting (aihub#359) ────────────────────────────
//
// The structural gates live in rhs_classification_honesty_test.go; these two are the
// compiled half, asserting on the real values rather than on the source text.

// TestDefaultRequiresHumanSessionIsTrue pins the fail-safe. The owner ruled the BEHAVIOUR
// correct and out of scope for aihub#359: when nobody classified the work item, a human looks
// at it. Flipping this constant to false would silently hand unclassified work to an agent.
func TestDefaultRequiresHumanSessionIsTrue(t *testing.T) {
	if !defaultRequiresHumanSession {
		t.Error("defaultRequiresHumanSession is false. A claim on a work item with no " +
			"classification would then be handed straight to an agent with no human in the " +
			"loop. The server cannot read the scenario repo's per-wi_type defaults, so it has " +
			"nothing to base a narrower decision on and must fail safe.")
	}
}

// TestClassificationResolvedEventPayloadOmitsWIType asserts on the bytes that reach the
// agent_events row.
//
// The claim in the previous sentence is only true because classificationResolvedEventPayload
// does the marshalling itself and the call site stores its return value verbatim. An earlier
// cut of this test asserted on a map-returning builder while carrying the same sentence in its
// doc comment, and that sentence was false: review reintroduced the defect with
// json.Marshal(annotate(build(v), *wi.WIType)) at the call site and this test stayed green.
// The "bare call" half of the contract is enforced structurally by analyseEmission in
// rhs_classification_honesty_test.go; this half pins the bytes. Neither is sufficient alone.
func TestClassificationResolvedEventPayloadOmitsWIType(t *testing.T) {
	// Both values, not just the default. A builder that ignored its argument and hard-coded
	// true would otherwise satisfy a test that only ever passes the constant.
	for _, rhs := range []bool{true, false} {
		raw := classificationResolvedEventPayload(rhs)

		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("payload for rhs=%v is not a JSON object: %v (raw: %q)", rhs, err, raw)
		}

		// Anti-vacuity: an empty object trivially satisfies "does not contain wi_type".
		if len(got) == 0 {
			t.Fatalf("the payload for rhs=%v is empty, so the absence check below proves nothing", rhs)
		}
		if v, ok := got["requires_human_session"]; !ok || v != rhs {
			t.Errorf("payload must carry requires_human_session=%v, got %#v (whole payload: %s)",
				rhs, v, raw)
		}
		if v, ok := got["source"]; !ok || v != classificationSourceServerDefault {
			t.Errorf("payload must name where the value came from (source=%q), got %#v. Dropping "+
				"wi_type without naming the real source trades a misleading event for a silent one.",
				classificationSourceServerDefault, v)
		}
		if _, present := got["wi_type"]; present {
			t.Errorf("payload still carries wi_type: %s\n"+
				"This event is emitted only when the work item row has no classification, and on "+
				"that branch the value is defaultRequiresHumanSession — a constant. The wi_type is "+
				"never read, so naming it claims a derivation that did not happen (aihub#359).", raw)
		}
	}
}
