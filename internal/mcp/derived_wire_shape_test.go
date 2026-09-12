package mcp_test

// aihub#350 — hop 1/2 gates for `derived` on the two tools that can wrap.
//
// The domain halves live in internal/domain (complete_attempt_derived_guard_test.go
// for shape and presence against a nil pool, complete_attempt_derived_db_test.go
// for storage and filed: existence). What THIS file holds is what only the tool
// layer can: where the refusal falls relative to the SIDE EFFECTS the tool
// performs around the completion call.
//
// The hazard is aihub#452's: pf_complete_attempt and pf_wrap both emit the
// closing note BEFORE the completion, because afterwards the credentials are
// gone - so a wrap refused by the SERVER for a missing derived would already
// have written a "wrapped: ..." note onto the timeline of an attempt that did
// not wrap, and pf_wrap would additionally have pushed and opened a PR. The
// pre-note refusals under test here are what keep the overwhelmingly common
// refusal - the pre-aihub#350 calling convention, i.e. every existing caller on
// the day this deploys - free of that cost.
//
// Deliberately NOT refused at this hop: a non-empty derived on a non-wrapped
// status. That combination is the server's refusal alone, because a hop-2 gate
// keyed on status would make this tool's other parameters unmeasurable to the
// aihub#419 G1 probe, whose status for this tool is pinned to "paused" while
// derived travels meaningfully only on "wrapped" - the first tool with two
// parameters on OPPOSITE branches of the same status switch. The cost is
// narrow and stated on the card: a note sent with that one malformed
// combination lands before the server's refusal.
//
// No database:
//
//	go test ./internal/mcp/ -run 'TestCompleteAttemptDerived|TestWrapDerived' -v

import (
	"strings"
	"testing"
	"time"
)

const derivedWIID = "wi_derived350"

// derivedRefusal drives pf_complete_attempt and hands back the refusal text and
// every path the tool touched. The empty path list is the whole point: these
// arms assert that a refusal wrote NOTHING - no note event, no completion.
func derivedRefusal(t *testing.T, args map[string]any) (text string, isErr bool, paths []string) {
	t.Helper()
	seedStateFile(t, derivedWIID)
	f := newFakeAihub(t)
	full := map[string]any{"work_item_id": derivedWIID}
	for k, v := range args {
		full[k] = v
	}
	result, isErr := callTool(t, f, "pf_complete_attempt", full)
	raw, _ := result["_raw"].(string)
	return raw, isErr, f.paths()
}

// TestCompleteAttemptDerivedIsRequiredBeforeTheNote is acceptance criterion 2
// at the tool hop, with the ordering that makes it cheap: the wrap is refused
// with the state file unresolved and the note unemitted, so the caller retries
// with derived added and the timeline shows one note, not two.
func TestCompleteAttemptDerivedIsRequiredBeforeTheNote(t *testing.T) {
	text, isErr, paths := derivedRefusal(t, map[string]any{
		"status": "wrapped",
		"note":   "wrapped: a note that must not land on a refused call",
	})
	if !isErr {
		t.Fatalf("pf_complete_attempt(status=\"wrapped\") with no derived succeeded: %q - the "+
			"server-side gate exists precisely because template-side defaults cannot refuse "+
			"anything, and this hop mirroring it is what spares the note below", text)
	}
	if len(paths) != 0 {
		t.Fatalf("the refusal fired after %v had already been requested.\nThe note precedes the "+
			"completion by necessity (the completion deletes the credentials), so any request "+
			"here means a timeline event for a wrap that was then refused - the exact aihub#452 "+
			"hazard this placement exists to avoid", paths)
	}
	for _, must := range []string{"derived", "folded", "filed:", "dropped:", "[]"} {
		if !strings.Contains(text, must) {
			t.Errorf("refusal = %q; want it to mention %q - on deploy day every existing caller "+
				"is this caller, and the message is the only migration guide they get", text, must)
		}
	}
}

// TestCompleteAttemptDerivedShapeIsRefusedBeforeTheNote holds the same ordering
// for a malformed list, through the same shared authority the server runs
// (domain.ValidateDerived) - so a list this hop forwards is a list the server's
// shape check will not bounce after the note has landed.
func TestCompleteAttemptDerivedShapeIsRefusedBeforeTheNote(t *testing.T) {
	text, isErr, paths := derivedRefusal(t, map[string]any{
		"status":  "wrapped",
		"note":    "wrapped: a note that must not land on a refused call",
		"derived": []any{"noted informally"},
	})
	if !isErr {
		t.Fatalf("a free-text derived entry passed the tool hop: %q", text)
	}
	if len(paths) != 0 {
		t.Fatalf("the shape refusal fired after %v had been requested; want no request at all", paths)
	}
	if !strings.Contains(text, "folded") || !strings.Contains(text, "dropped:") {
		t.Errorf("refusal = %q; want the three-form grammar in it", text)
	}
}

// TestCompleteAttemptDerivedTravelsVerbatimIncludingEmpty is the forwarding
// half of criterion 2: [] is a statement, so it must reach the wire as [], not
// be dropped by a non-emptiness guard the way optional strings are. Both the
// empty and the mixed list are read back off the recorded body.
func TestCompleteAttemptDerivedTravelsVerbatimIncludingEmpty(t *testing.T) {
	drive := func(t *testing.T, derived []any) map[string]any {
		t.Helper()
		seedStateFile(t, derivedWIID)
		f := newFakeAihub(t)
		result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
			"work_item_id": derivedWIID,
			"status":       "wrapped",
			"derived":      derived,
		})
		if isErr {
			t.Fatalf("pf_complete_attempt failed: %v", result)
		}
		for _, c := range f.recorded() {
			if strings.HasSuffix(c.Path, "/complete") {
				return c.Body
			}
		}
		t.Fatalf("no /complete request recorded; paths=%v", f.paths())
		return nil
	}

	t.Run("empty_list_travels_as_empty_list", func(t *testing.T) {
		body := drive(t, []any{})
		got, present := body["derived"].([]any)
		if !present {
			t.Fatalf("the body carries no derived key on a wrap that declared []: %v.\nDropping "+
				"the empty list turns every honest \"no findings\" into the omission the server "+
				"refuses - the caller complied and the tool undid it", body)
		}
		if len(got) != 0 {
			t.Errorf("derived = %#v, want []", got)
		}
	})

	t.Run("entries_travel_verbatim", func(t *testing.T) {
		want := []any{"folded: a stale comment in helpers.go", "dropped:duplicate of a wrapped wi"}
		body := drive(t, want)
		got, _ := body["derived"].([]any)
		if len(got) != len(want) {
			t.Fatalf("derived = %#v, want %#v", body["derived"], want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("derived[%d] = %#v, want %#v", i, got[i], want[i])
			}
		}
	})
}

// TestCompleteAttemptSendsNoDerivedKeyWhenAbsent keeps nil and [] apart on the
// wire in the direction the forwarding test cannot see: a pause that never
// mentioned derived must produce a body with NO derived key, because the
// server's request struct reads absence as nil and nil is the value its guards
// key on. An unguarded `body["derived"] = ...` would send [] on every pause.
func TestCompleteAttemptSendsNoDerivedKeyWhenAbsent(t *testing.T) {
	seedStateFile(t, derivedWIID)
	f := newFakeAihub(t)
	result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
		"work_item_id": derivedWIID,
		"status":       "paused",
	})
	if isErr {
		t.Fatalf("pf_complete_attempt(paused) failed: %v", result)
	}
	for _, c := range f.recorded() {
		if strings.HasSuffix(c.Path, "/complete") {
			if v, present := c.Body["derived"]; present {
				t.Errorf("the pause body carries derived=%#v although the caller never sent the "+
					"field - absence is load-bearing at hop 3 and the tool just erased it", v)
			}
			return
		}
	}
	t.Fatalf("no /complete request recorded; paths=%v", f.paths())
}

// TestWrapDerivedIsRequiredBeforeThePushHalf is the pf_wrap mirror, and the
// stakes are higher than the note: pf_wrap pushes and opens a PR before it
// completes, so a refusal that fired server-side would follow a real push. The
// arm proves the ordering by construction - there is no git repo, no gh stub
// and no worktree here, so a call that got PAST the derived check would fail
// with a worktree error and at least resolve the state file; the derived
// refusal with zero requests and the state file untouched is only reachable
// from before all of it.
func TestWrapDerivedIsRequiredBeforeThePushHalf(t *testing.T) {
	seedStateFile(t, derivedWIID)
	f := newFakeAihub(t)
	result, isErr := callTool(t, f, "pf_wrap", map[string]any{
		"work_item_id": derivedWIID,
		"repo":         "aihub",
		"pr_title":     "a wrap with no dispositions",
		"pr_body":      "body",
		"note":         "wrapped: must not land",
	})
	if !isErr {
		t.Fatalf("pf_wrap with no derived succeeded: %v", result)
	}
	if paths := f.paths(); len(paths) != 0 {
		t.Fatalf("the refusal fired after %v had been requested; a refused wrap must have "+
			"delivered nothing", paths)
	}
	text, _ := result["_raw"].(string)
	if !strings.Contains(text, "derived") {
		t.Errorf("refusal = %q; want it to name derived - this tool's schema marks the "+
			"parameter required, and the runtime message is the half a caller who ignored "+
			"the schema actually reads", text)
	}
	if strings.Contains(text, "worktree") || strings.Contains(text, "state file") {
		t.Errorf("refusal = %q - it reads like the call got past the derived check and died "+
			"on the missing fixture, which would mean the check runs AFTER the resolution "+
			"work it is supposed to precede", text)
	}
}

// TestWrapDerivedRidesTheCompletionBody drives a real wrap through the full
// harness (repo, fake gh, fake aihub) and reads the completion body back: the
// dispositions the caller stated are exactly what reaches the server, on the
// same request that carries the credentials. This is the arm that makes the
// card's hop 2-3 sentence about derived a measurement rather than a paraphrase.
func TestWrapDerivedRidesTheCompletionBody(t *testing.T) {
	f := newWrapHarness(t)
	want := []any{"folded: wrap harness left a TODO in the fixture", "filed:aihub#350"}
	result, isErr := callToolBounded(t, f, "pf_wrap", map[string]any{
		"work_item_id": resolveCanonical,
		"repo":         "aihub",
		"pr_title":     "probe the derived forwarding",
		"pr_body":      "body",
		"derived":      want,
	}, 60*time.Second)
	if isErr {
		t.Fatalf("pf_wrap failed: %v", result)
	}
	body := lastBodyFor(t, f, wrapCompletePath)
	got, present := body["derived"].([]any)
	if !present {
		t.Fatalf("pf_wrap's completion body carries no derived (%v) - the tool required the "+
			"argument and then completed without it, which the server will refuse the day its "+
			"half deploys, stranding every wrap", body)
	}
	if len(got) != len(want) {
		t.Fatalf("derived = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("derived[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
