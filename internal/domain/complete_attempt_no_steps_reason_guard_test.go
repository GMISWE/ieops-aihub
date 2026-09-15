package domain

import (
	"strings"
	"testing"
)

// aihub#684 — AC4's guard half: "no_steps_reason is read only when
// status=\"wrapped\"" (the field is the escape hatch for a gate that only
// fires on that status), so a non-empty value sent with any other status is
// refused rather than silently ignored.
//
// Same instrument as complete_attempt_pause_reason_guard_test.go (aihub#452)
// and the same reason for it: FnCompleteAttempt decides this refusal from the
// request alone, before BeginTx, so driving the real exported function with a
// nil *pgxpool.Pool tells "refused" and "reached the database" apart without
// AIHUB_TEST_DB. completeAttemptVerdict and runCompleteAttemptRequestChecks
// are defined in that file and reused here unchanged — this is the domain
// half; the MCP forwarding half (internal/mcp/tools_lifecycle.go) is gated
// separately.
//
// The polarity is inverted from pause_reason's: that guard fires on
// status != "paused", this one fires on status != "wrapped". Every arm below
// is pause_reason's five-function shape with that inversion carried through,
// including which statuses need Derived: []string{} to clear the OTHER
// guard (aihub#350) that sits next to this one and would otherwise refuse a
// "wrapped" request before this guard is ever exercised.

// TestCompleteAttemptRefusesNoStepsReasonOnNonWrappedStatus is the RED arm:
// on the pre-fix tree both cases run straight into the UPDATE and stamp the
// reason onto a paused or failed attempt row, so both report ReachedPool.
func TestCompleteAttemptRefusesNoStepsReasonOnNonWrappedStatus(t *testing.T) {
	const wantPrefix = `no_steps_reason is read only when status="wrapped"`

	for _, status := range []string{"failed", "paused"} {
		t.Run(status, func(t *testing.T) {
			v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
				Status:        status,
				NoStepsReason: strp("raw git push landed this; no step graph was ever opened for it"),
			})
			if v.ReachedPool {
				t.Fatalf("FnCompleteAttempt(status=%q, no_steps_reason=%q) reached the database.\n"+
					"no_steps_reason is read only when status=\"wrapped\" — it is the escape hatch for "+
					"a gate that only fires on a wrap (aihub#684); writing it on a paused or failed "+
					"completion stamps a reason nothing will ever read it back from.",
					status, "raw git push landed this; no step graph was ever opened for it")
			}
			if !strings.HasPrefix(v.Refused, wantPrefix) {
				t.Errorf("refusal message = %q, want it to start with %q", v.Refused, wantPrefix)
			}
			if !strings.Contains(v.Refused, status) {
				t.Errorf("refusal message = %q, want it to name the offending status %q — the caller "+
					"sent two fields and cannot tell which one the server objected to", v.Refused, status)
			}
			if !strings.Contains(v.Refused, "note") {
				t.Errorf("refusal message = %q, want it to name `note`, the field that does record on "+
					"every status; a refusal with no alternative just moves the caller's problem",
					v.Refused)
			}
		})
	}
}

// TestCompleteAttemptAcceptsNoStepsReasonOnWrapped is the green control that
// sits between the refusing arm above and the narrowness arms below: if this
// one ever reports a refusal, the fix has broken the capability it was
// supposed to protect. Derived: []string{} is required alongside — a wrap
// with no derived list is refused by the aihub#350 guard before this one is
// ever reached.
func TestCompleteAttemptAcceptsNoStepsReasonOnWrapped(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:        "wrapped",
		NoStepsReason: strp("raw git push landed this; no step graph was ever opened for it"),
		Derived:       []string{},
	})
	if !v.ReachedPool {
		t.Fatalf("FnCompleteAttempt refused a wrap carrying a no_steps_reason: %q.\n"+
			"\"wrapped\" is the one status the field is read on (aihub#684).", v.Refused)
	}
}

// TestCompleteAttemptDoesNotRequireNoStepsReason is the narrowness arm that
// matters most in practice: the tempting over-wide reading of "no_steps_reason
// is read only when status=wrapped" is the converse — "every wrap must carry
// one" — and that version would refuse the overwhelming majority of wraps,
// which never trip the code-produced-with-no-step gate at all and so have
// nothing to explain. nil and "" are both no-reason wraps and both must pass
// this guard (whether the DB-side gate itself then fires is a separate
// concern - see complete_attempt_no_steps_recorded_db_test.go).
func TestCompleteAttemptDoesNotRequireNoStepsReason(t *testing.T) {
	for name, reason := range map[string]*string{"nil": nil, "empty": strp("")} {
		t.Run(name, func(t *testing.T) {
			v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
				Status:        "wrapped",
				NoStepsReason: reason,
				Derived:       []string{},
			})
			if !v.ReachedPool {
				t.Fatalf("FnCompleteAttempt refused a wrap with a %s no_steps_reason: %q.\n"+
					"no_steps_reason is optional on this guard; it must fire on a reason sent where it "+
					"cannot be read, never on the absence of one.", name, v.Refused)
			}
		})
	}
}

// TestCompleteAttemptAllowsEmptyNoStepsReasonOnNonWrapped is the other
// narrowness arm: an empty reason on a paused completion states nothing, so
// there is nothing to misplace and nothing to refuse. Widening the guard to
// "no_steps_reason present" would fail every caller that sets the field to
// its zero value.
func TestCompleteAttemptAllowsEmptyNoStepsReasonOnNonWrapped(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:        "paused",
		NoStepsReason: strp(""),
	})
	if !v.ReachedPool {
		t.Fatalf("FnCompleteAttempt refused a pause carrying an EMPTY no_steps_reason: %q.\n"+
			"An empty reason carries no statement to put in the wrong place. Refusing it makes the "+
			"guard's decision surface wider than the ambiguity it exists to resolve.", v.Refused)
	}
}

// TestCompleteAttemptNoStepsReasonStatusValidationStillFirst guards the
// ordering the arms above depend on. An unknown status must be refused for
// BEING unknown, not for its no_steps_reason, or a caller who typo'd the
// status gets told to drop a field that was never their problem.
func TestCompleteAttemptNoStepsReasonStatusValidationStillFirst(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:        "wraped",
		NoStepsReason: strp("a reason, on a status that does not exist"),
	})
	const wantPrefix = "status must be wrapped, failed, or paused"
	if !strings.HasPrefix(v.Refused, wantPrefix) {
		t.Errorf("refusal for an unknown status = %q (reached_pool=%v), want it to start with %q",
			v.Refused, v.ReachedPool, wantPrefix)
	}
}
