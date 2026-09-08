package mcp_test

// aihub#425 — four server-side names that no tool published, found by the
// aihub#419 layer-1 gate as BOUND_FIELD_UNPUBLISHED.
//
// ─── The work item's premise was wrong, and the correction is the design ───
//
// The wi says the SDK "drops undeclared arguments silently", so "the forwarding
// code is dead: the parameter is one line short of working". MEASURED instead,
// on a real in-memory MCP session against a recording HTTP server
// (recall_wire_query_test.go's queryRecorder and tools_fusion_test.go's
// fakeAihub — the same instruments the wire tests already use):
//
//	pf_recall       cursor       -> reached the wire UNPUBLISHED
//	pf_recall       recall_algo  -> reached the wire UNPUBLISHED
//	pf_remember     tags         -> reached the POST body UNPUBLISHED
//	pf_read_events  cursor       -> did NOT reach the wire
//
// So the four are not one defect. Three were fully wired end to end and merely
// undiscoverable — reachable today only by a caller who guesses a name no
// schema mentions — and one was the aihub#259 shape, bound by the server and
// never put on the wire by this process. Publishing alone fixes the first
// three; the fourth needed the forwarding line too.
//
// ─── And one of the four is NOT published, on purpose ─────────────────────
//
// recall_params_wiring_test.go's recallUnpublishedForwardedParams already held
// a reasoned decision for pf_recall's cursor AND recall_algo: both were
// forwarded-but-unpublished deliberately, with written reasons. A gate finding
// does not by itself overturn a design decision, so the two were separated by
// what could be MEASURED about each:
//
//   - cursor: next_cursor is returned TO THE MODEL in a pf_recall result
//     (verified through a real session against a server that returns one). The
//     exemption assumed the model would have to invent a cursor; in fact it is
//     handed one and given no way to spend it. Published.
//   - recall_algo: nothing in any response mentions it, so no caller is holding
//     a value it cannot use, and the reason — a deployment-level opt-in through
//     POLYFORGE_RECALL_ALGO — still stands. NOT published; recorded in the G4
//     allowlist instead, per the precedent aihub#424 set on the same gate.
//
// That distinction is worth stating because it changes what the fix has to be
// and what may be claimed about it. Had the premise been taken on trust, three
// of these would have been "fixed" by adding a forwarding line that was already
// there, and the resulting test would have passed on the unfixed tree.
//
// ⚠️ The same false premise is stated in attrs_patch_schema_test.go's header
// comment ("the MCP SDK drops arguments that the published InputSchema does not
// declare"). It is left alone here because that file is outside this work
// item's declared scope, and it is recorded rather than silently worked around.
// aihub#389's disclosure gate is independent evidence against it: the server
// can only report an unknown argument in request_adjusted because the argument
// reached the handler.
//
// No database needed:
//
//	go test ./internal/mcp/ -run 'TestBoundMemoryAndEventParams' -v

import (
	"encoding/json"
	"testing"
)

// boundParam is one (tool, parameter) pair this work item resolved.
type boundParam struct {
	tool  string
	param string
	// wiredBefore records whether the parameter already reached the wire
	// before this change. It is carried in the table so the asymmetry above is
	// visible at the assertion rather than only in prose: the wire arm is a
	// RED-then-GREEN proof for pf_read_events and a regression pin for the
	// other three, and a reader who cannot tell them apart will over-read the
	// evidence this file provides.
	wiredBefore bool
}

func boundParams() []boundParam {
	return []boundParam{
		{"pf_recall", "cursor", true},
		{"pf_remember", "tags", true},
		{"pf_read_events", "cursor", false},
	}
}

// TestBoundMemoryAndEventParamsArePublished is the arm that is RED for all four
// on the tree before this change.
//
// It reads the schema the server actually PUBLISHES over tools/list rather than
// the source text that produces it, so a parameter added to the wrong builder,
// or to a tool registered around the shared wrapper, fails here.
func TestBoundMemoryAndEventParamsArePublished(t *testing.T) {
	for _, bp := range boundParams() {
		t.Run(bp.tool+"."+bp.param, func(t *testing.T) {
			tool := publishedTool(t, bp.tool)
			if tool == nil {
				t.Fatalf("%s is not published at all", bp.tool)
			}
			// schemaProps (attrs_patch_schema_test.go) rather than a second
			// decoder: the published schema is one fact, and two readers of it
			// can disagree.
			prop, ok := schemaProps(t, tool)[bp.param]
			if !ok {
				t.Fatalf("%s does not publish %q. The server binds it, so the capability exists "+
					"and is reachable only by guessing a name no schema mentions — which is the "+
					"aihub#419 BOUND_FIELD_UNPUBLISHED finding this work item resolves.",
					bp.tool, bp.param)
			}
			// A published name with no description is published in name only: the
			// model reads the description, and every one of these four carries a
			// caveat (which paging path works, what lexical does, where tags are
			// visible) without which the parameter is a trap rather than a feature.
			if len(prop.Description) < 30 {
				t.Errorf("%s.%s is published with a %d-character description; these parameters "+
					"each carry a caveat a caller cannot infer from the name",
					bp.tool, bp.param, len(prop.Description))
			}
		})
	}
}

// TestBoundMemoryAndEventParamsReachTheWire measures the other end.
//
// Publishing a name the process then drops is the aihub#148/#259 defect wearing
// the opposite sign, and it is not hypothetical for these four: pf_read_events'
// cursor was exactly that until this change, bound by the server and never sent.
func TestBoundMemoryAndEventParamsReachTheWire(t *testing.T) {
	t.Run("pf_recall cursor and the still-unpublished recall_algo", func(t *testing.T) {
		q := newQueryRecorder(t)
		callToolAgainstRecorder(t, q, "pf_recall", map[string]any{
			"project": "aihub", "query": "anything",
			"cursor": "CURSOR_TOKEN_425", "recall_algo": "lexical",
		})
		got := q.last(t)
		if got.Get("cursor") != "CURSOR_TOKEN_425" {
			t.Errorf("cursor on the wire = %q, want %q", got.Get("cursor"), "CURSOR_TOKEN_425")
		}
		if got.Get("recall_algo") != "lexical" {
			t.Errorf("recall_algo on the wire = %q, want %q — it stays UNPUBLISHED but must "+
				"keep working for the plugin builds that pass it", got.Get("recall_algo"), "lexical")
		}
	})

	// THE red-then-green arm. Everything else in this file about pf_read_events
	// would be satisfied by publishing the name alone.
	t.Run("pf_read_events cursor", func(t *testing.T) {
		q := newQueryRecorder(t)
		callToolAgainstRecorder(t, q, "pf_read_events", map[string]any{
			"project": "aihub", "cursor": "EVENT_CURSOR_425",
		})
		if got := q.last(t).Get("cursor"); got != "EVENT_CURSOR_425" {
			t.Errorf("cursor on the wire = %q, want %q.\n"+
				"    The server has always bound this and ListEvents has always returned "+
				"next_cursor, so while it was not forwarded the second page of any event "+
				"stream was unreachable from MCP — and the first page looked complete.",
				got, "EVENT_CURSOR_425")
		}
	})

	t.Run("pf_remember tags", func(t *testing.T) {
		f := newFakeAihub(t)
		callTool(t, f, "pf_remember", map[string]any{
			"project": "aihub", "type": "experience.debug", "content": "c",
			"visibility": "project", "tags": []any{"alpha", "beta"},
		})
		calls := f.recorded()
		if len(calls) != 1 {
			t.Fatalf("expected exactly one HTTP call, got %d", len(calls))
		}
		raw, _ := json.Marshal(calls[0].Body["tags"])
		if string(raw) != `["alpha","beta"]` {
			t.Errorf("tags in the POST body = %s, want [\"alpha\",\"beta\"]", raw)
		}
	})
}

// TestUnsuppliedCursorIsNotSentAsEmpty is the negative control for the
// forwarding line added to pf_read_events.
//
// The obvious way to write that line — params.Set("cursor", strArg(...)) —
// forwards an EMPTY cursor when the caller supplies none, and handleListEvents
// reads `if cursor := c.QueryParam("cursor"); cursor != ""`, so an empty value
// happens to be harmless there today. Harmless-by-luck at the far end is not a
// contract: the arm below pins that the parameter is absent rather than empty,
// so a later server change that distinguishes "" from absent cannot break this
// tool silently. Without it, "always forwards" satisfies every positive arm.
func TestUnsuppliedCursorIsNotSentAsEmpty(t *testing.T) {
	for _, tool := range []string{"pf_read_events", "pf_recall"} {
		t.Run(tool, func(t *testing.T) {
			q := newQueryRecorder(t)
			callToolAgainstRecorder(t, q, tool, map[string]any{"project": "aihub"})
			if _, present := q.last(t)["cursor"]; present {
				t.Errorf("%s put a cursor on the wire when the caller supplied none: %v",
					tool, q.last(t))
			}
		})
	}
}

// TestRecallAlgoStaysUnpublishedOnPurpose pins the half of this work item that
// is a decision NOT to change something.
//
// The aihub#419 gate flagged recall_algo exactly as it flagged the other three,
// and the difference is not visible from the gate's output — it is visible only
// in what each name does for a caller. Without this arm, the next person to read
// the baseline diff sees three names published and one silently dropped, which
// is indistinguishable from an oversight.
func TestRecallAlgoStaysUnpublishedOnPurpose(t *testing.T) {
	tool := publishedTool(t, "pf_recall")
	if _, published := schemaProps(t, tool)["recall_algo"]; published {
		t.Errorf("pf_recall now publishes recall_algo. That may well be right, but it reverses a " +
			"written decision (recall_params_wiring_test.go's recallUnpublishedForwardedParams, " +
			"and the G4 allowlist entry handleRecall.recall_algo) — change those together with " +
			"it, or the tree carries a reason that contradicts the code.")
	}
}
