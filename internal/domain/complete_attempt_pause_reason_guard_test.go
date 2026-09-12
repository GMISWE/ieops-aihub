package domain

import (
	"context"
	"strings"
	"testing"
)

// aihub#452 — hop 3/4 gate for "pause_reason is read only when status=paused".
//
// The schema published that sentence the day pause_reason landed (aihub#424) and
// it was false at both remaining hops: the MCP handler forwarded the field on any
// status when non-empty, and FnCompleteAttempt wrote it into
// run_attempts.pause_reason and into the attempt_completed payload for every
// status. The MCP half is gated in internal/mcp; this file gates the domain half,
// which is the one a direct HTTP caller reaches without passing through the tool.
//
// # Why there is no database here, and why that is not a weaker test
//
// FnCompleteAttempt validates the request before it opens a transaction, so a
// refusal is decided from the request alone. This file drives the REAL exported
// function with a nil *pgxpool.Pool: reaching the pool at all is a nil-pointer
// panic, so "returned an error" and "got as far as the database" are two states
// this can tell apart without AIHUB_TEST_DB — which CI does not set. A helper
// extracted for testability would have been the easier route and a worse one: it
// would still pass with nothing calling it, which is the false green this whole
// programme keeps finding.
//
// So the nil pool is the instrument, not a shortcut. It measures the property the
// fix actually claims — the refusal happens before any database work — and the
// panic it recovers is the evidence of the opposite verdict, never a crash that
// takes the package's other tests with it.

// completeAttemptVerdict is what one call to FnCompleteAttempt did with a request
// it could decide on its own.
type completeAttemptVerdict struct {
	// Refused is non-empty when the call returned an error before touching the
	// pool. It holds that error's message.
	Refused string
	// ReachedPool is true when the call ran past the request checks and tried to
	// open a transaction on the nil pool.
	ReachedPool bool
}

// runCompleteAttemptRequestChecks calls FnCompleteAttempt with a nil pool and
// reports which of the two outcomes happened. It never fails the test itself:
// both outcomes are legitimate answers and each arm below decides which one it
// wanted.
func runCompleteAttemptRequestChecks(t *testing.T, req *CompleteAttemptRequest) (v completeAttemptVerdict) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			// The only way past the request checks is BeginTx on the nil pool.
			v = completeAttemptVerdict{ReachedPool: true}
		}
	}()
	if aerr := FnCompleteAttempt(context.Background(), nil, "wi_452guard", req, nil, ""); aerr != nil {
		if aerr.Code != ErrBadRequest {
			t.Fatalf("FnCompleteAttempt refused with code %q, want %q — the refusal has to be a "+
				"request error the caller can act on, not a server fault", aerr.Code, ErrBadRequest)
		}
		return completeAttemptVerdict{Refused: aerr.Message}
	}
	t.Fatalf("FnCompleteAttempt returned nil with a nil pool, which is impossible on the success "+
		"path: it neither refused the request nor tried to open a transaction. req=%+v", req)
	return completeAttemptVerdict{}
}

// TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus is the RED arm: on the
// pre-fix tree both cases run straight into the UPDATE and stamp the reason onto
// a terminal attempt row, so both report ReachedPool.
//
// The message assertion is anchored at the start of the string rather than done
// with a substring search, because the value under test is itself a status string
// that the message quotes — a Contains check would pass on a message that merely
// echoed the caller's input back.
func TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus(t *testing.T) {
	const wantPrefix = `pause_reason is read only when status="paused"`

	for _, status := range []string{"wrapped", "failed"} {
		t.Run(status, func(t *testing.T) {
			v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
				Status:      status,
				PauseReason: strp("owner adjudication pending"),
			})
			if v.ReachedPool {
				t.Fatalf("FnCompleteAttempt(status=%q, pause_reason=%q) reached the database.\n"+
					"The schema publishes pause_reason as read only when status=\"paused\"; writing it "+
					"on a terminal completion stamps a pause reason onto a row whose only reader — "+
					"GetReadyQueue's paused[] segment — filters on wi.status='paused' and will never "+
					"look at it.", status, "owner adjudication pending")
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

// TestCompleteAttemptAcceptsPauseReasonOnPause is the green control that sits
// between the refusing arms and the narrowness arms. It is what separates "the
// guard works" from "the guard rejects everything": if this one ever reports a
// refusal, the fix has broken the capability it was supposed to protect.
func TestCompleteAttemptAcceptsPauseReasonOnPause(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:      "paused",
		PauseReason: strp("waiting on the owner to adjudicate the design table"),
	})
	if !v.ReachedPool {
		t.Fatalf("FnCompleteAttempt refused a pause carrying a reason: %q.\nThat is the one "+
			"combination the column exists for (migration 0027).", v.Refused)
	}
}

// TestCompleteAttemptDoesNotRequirePauseReason is the narrowness arm that matters
// most in practice. The tempting over-wide reading of "pause_reason is read only
// when status=paused" is the converse — "a pause must carry a reason" — and that
// version would reject legitimate calls on the tool every executor in the
// workspace ends its run with. nil and "" are both no-reason pauses and both must
// pass.
func TestCompleteAttemptDoesNotRequirePauseReason(t *testing.T) {
	for name, reason := range map[string]*string{"nil": nil, "empty": strp("")} {
		t.Run(name, func(t *testing.T) {
			v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
				Status:      "paused",
				PauseReason: reason,
			})
			if !v.ReachedPool {
				t.Fatalf("FnCompleteAttempt refused a pause with a %s reason: %q.\n"+
					"pause_reason is optional; the guard must fire on a reason sent where it cannot "+
					"be read, never on the absence of one.", name, v.Refused)
			}
		})
	}
}

// TestCompleteAttemptAllowsEmptyPauseReasonOnTerminal is the other narrowness
// arm: an empty reason on a terminal status states nothing, so there is nothing
// to misplace and nothing to refuse. Widening the guard to "pause_reason present"
// would fail every caller that sets the field to its zero value — and the empty
// case is handled where it belongs, by the write normalising it to NULL rather
// than the empty string, so that "not paused" stays distinguishable from
// "paused, reason not given".
func TestCompleteAttemptAllowsEmptyPauseReasonOnTerminal(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:      "wrapped",
		PauseReason: strp(""),
		// aihub#350: a wrap must carry a derived list or it is refused before
		// this arm's subject - the pause_reason residue - is ever reached.
		Derived: []string{},
	})
	if !v.ReachedPool {
		t.Fatalf("FnCompleteAttempt refused a wrap carrying an EMPTY pause_reason: %q.\n"+
			"An empty reason carries no statement to put in the wrong place. Refusing it makes the "+
			"guard's decision surface wider than the ambiguity it exists to resolve.", v.Refused)
	}
}

// TestCompleteAttemptStatusValidationStillFirst guards the ordering the arms
// above depend on. An unknown status must be refused for BEING unknown, not for
// its pause_reason, or a caller who typo'd the status gets told to drop a field
// that was never their problem.
func TestCompleteAttemptStatusValidationStillFirst(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:      "wraped",
		PauseReason: strp("a reason, on a status that does not exist"),
	})
	const wantPrefix = "status must be wrapped, failed, or paused"
	if !strings.HasPrefix(v.Refused, wantPrefix) {
		t.Errorf("refusal for an unknown status = %q (reached_pool=%v), want it to start with %q",
			v.Refused, v.ReachedPool, wantPrefix)
	}
}
