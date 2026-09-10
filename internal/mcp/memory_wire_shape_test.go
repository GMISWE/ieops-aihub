package mcp_test

// aihub#543 wave 2, slice L6 — the hop 2-3 sentences of the four memory-mutation
// cards, asserted on the bytes each call really puts on the wire.
//
// ─── Why this file exists beside memory_tools_wire_test.go ───────────────────
//
// That file is aihub#325's guard and it walks one direction only: for every
// PUBLISHED property, a call carrying it must land its value where the table
// says. Every probe there sets the parameter. So the four cards' claims about
// what happens when a parameter is ABSENT — "`strength_delta` is forwarded only
// when present", "copies each of `updateMemoryPassthroughFields` only when the
// key is present", "sends exactly `{reason}` — no credentials at all",
// "`memory_id` is absent from the body on purpose" — are invisible to it, in
// exactly the way aihub#452 measured for the universal gate: a harness that
// forces one branch with its own probe values cannot see the other branch at
// all.
//
// The omission direction is not a smaller version of the same claim. A key that
// arrives when the caller did not send it is a value the caller never chose:
// `content: ""` on pf_update_memory overwrites a memory's body with nothing,
// where omitting it means "keep current". Those are different requests and the
// card says so; nothing checked it.
//
// 🔴 Exact key sets, never "does not contain X". A check for the absence of the
// three named credential keys stays green on the day a fourth starts riding
// along, which is the failure the heartbeat arm of aihub#459's slice records for
// the same shape. Both directions everywhere: each arm names what MUST arrive as
// well as what must not, because an assertion that a body lacks a key is
// satisfied perfectly by a body that is empty because the call never happened.
//
// Needs no database: the aihub server is the httptest recorder
// memory_tools_wire_test.go already builds.

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// bodyKeys is the request body's top-level key set, sorted. A nil body (no JSON
// at all was sent) is reported as nil rather than as an empty set, because
// "sent {}" and "sent nothing" are different requests and the activate arm below
// turns on the difference.
func bodyKeys(req *recordedRequest) []string {
	if req.body == nil {
		return nil
	}
	out := make([]string, 0, len(req.body))
	for k := range req.body {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// callAllowingError invokes a tool and reports whether the tool answered with an
// error, plus the request the recorder saw — which is nil when the handler
// refused before reaching the wire. s.call cannot be used for this: it Fatals on
// an error result and Fatals again when no request was made, and "no request was
// made" is the observation one arm below is about.
func (s *memoryWireStack) callAllowingError(t *testing.T, tool string, args map[string]any) (bool, *recordedRequest) {
	t.Helper()
	s.mu.Lock()
	s.last = nil
	s.mu.Unlock()

	res, err := s.session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s(%v): %v", tool, args, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return res.IsError, s.last
}

// ─── the omission direction ──────────────────────────────────────────────────

// optionalWireParam is one published-and-optional parameter, driven both ways.
type optionalWireParam struct {
	tool  string
	param string
	value any
}

// memoryOptionalParams are every optional parameter of the three memory tools
// whose cards claim a present-only forwarding rule.
//
// 🔴 The set is checked against the live schema below rather than trusted, in
// both directions: an entry naming a parameter the tool does not publish is a
// stale probe, and a published optional parameter missing from this list is a
// rule nobody is holding. That second direction is the one a hand-written list
// loses silently — it is how memory_tools_wire_test.go's own completeness arm is
// built, and for the same reason.
var memoryOptionalParams = []optionalWireParam{
	{"pf_reinforce_memory", "strength_delta", 2.0},
	{"pf_update_memory", "content", "a replacement body"},
	{"pf_update_memory", "visibility", "team"},
	{"pf_update_memory", "tags", []any{"alpha"}},
	{"pf_update_memory", "base_strength", 4.0},
}

// TestMemoryToolsForwardAnOptionalParamOnlyWhenItCarriesAValue is the omission
// half of the forwarding rule.
//
// Two calls per parameter: one that sets it, one that does not, with everything
// else identical. The set call is the control — without it a renderer that
// forwarded NOTHING would satisfy the absence assertion perfectly, and that is
// aihub#325's own defect wearing the opposite sign.
//
//	M1  drop the `if v, ok := args["strength_delta"]; ok` guard in
//	    buildReinforceMemoryBody and always write the key   RED (absent call)
//	M2  make buildUpdateMemoryBody copy every passthrough field
//	    unconditionally                                      RED (absent call)
//	M3  delete the copy loop in buildUpdateMemoryBody         RED (set call)
//	M4  drop `strength_delta` from reinforceMemorySchema      RED (SCHEMA_DRIFT:
//	                                                          the probe names a
//	                                                          parameter the live
//	                                                          registry does not
//	                                                          publish)
//	M5  add a published optional parameter to updateMemorySchema
//	    and no entry here                                     RED (UNCOVERED)
func TestMemoryToolsForwardAnOptionalParamOnlyWhenItCarriesAValue(t *testing.T) {
	s := newMemoryWireStack(t)
	published := s.publishedSchemas(t)

	// Both directions on the roster itself, before a single value is asserted.
	covered := map[string]bool{}
	for _, p := range memoryOptionalParams {
		covered[p.tool+"/"+p.param] = true
		props, ok := published[p.tool]
		if !ok {
			t.Errorf("K-L6 SCHEMA_DRIFT: this file probes %s, which the live registry does "+
				"not publish at all", p.tool)
			continue
		}
		if _, ok := props[p.param]; !ok {
			t.Errorf("K-L6 SCHEMA_DRIFT: this file probes %s.%s, which %s does not publish — "+
				"a stale probe asserts a rule about a parameter no caller can send",
				p.tool, p.param, p.tool)
		}
	}
	for _, tool := range []string{"pf_reinforce_memory", "pf_update_memory"} {
		wire, ok := memoryToolWire[tool]
		if !ok {
			t.Fatalf("%s has no wire table in memory_tools_wire_test.go, so this arm cannot "+
				"tell its required parameters from its optional ones", tool)
		}
		for name := range published[tool] {
			if _, isBase := wire.base[name]; isBase {
				continue // required: the tool refuses the call without it
			}
			if covered[tool+"/"+name] {
				continue
			}
			t.Errorf("K-L6 UNCOVERED: %s publishes the optional parameter %q and no entry here "+
				"drives it both ways, so nothing holds that omitting it keeps it off the wire. "+
				"That is the direction the aihub#325 gate cannot see.", tool, name)
		}
	}

	for _, p := range memoryOptionalParams {
		wire := memoryToolWire[p.tool]

		t.Run(p.tool+"/"+p.param+"/set", func(t *testing.T) {
			args := map[string]any{}
			for k, v := range wire.base {
				args[k] = v
			}
			args[p.param] = p.value
			req := s.call(t, p.tool, args)
			got, present := lookupPath(req.body, p.param)
			if !present {
				t.Fatalf("%s sent %s=%v and the body carries no %q at all: %s\nThe absence "+
					"assertion in the sibling subtest would pass vacuously against a renderer "+
					"that forwards nothing.", p.tool, p.param, p.value, p.param, mustJSON(t, req.body))
			}
			if mustJSON(t, got) != mustJSON(t, p.value) {
				t.Errorf("%s: %s=%v arrived as %s", p.tool, p.param, p.value, mustJSON(t, got))
			}
		})

		t.Run(p.tool+"/"+p.param+"/omitted", func(t *testing.T) {
			args := map[string]any{}
			for k, v := range wire.base {
				args[k] = v
			}
			req := s.call(t, p.tool, args)
			if got, present := lookupPath(req.body, p.param); present {
				t.Errorf("%s did not send %s and the body carries %q=%s anyway: %s\nThe card "+
					"says an absent optional key means \"keep current\" (pf_update_memory) or "+
					"\"do not move the strength\" (pf_reinforce_memory). A key the caller never "+
					"chose is a value the server will act on.",
					p.tool, p.param, p.param, mustJSON(t, got), mustJSON(t, req.body))
			}
		})
	}
}

// TestReinforceAndUpdateSendCredentialsAndTheWorkItemAndNoMemoryId is the base
// body of the two credentialed memory-mutation tools, as an EXACT key set.
//
// Two claims live in one assertion because they are two halves of one shape: the
// card says each tool "sends the three credentials plus `work_item_id`", and
// that `memory_id` is "absent from the body on purpose — it is the path
// segment". A subset check would hold the first and miss the second, and a
// check for `memory_id`'s absence alone would stay green on a body that had
// stopped carrying the credentials.
//
//	M6  add "memory_id": memID to buildReinforceMemoryBody   RED
//	M7  add "memory_id": memID to buildUpdateMemoryBody      RED
//	M8  drop session_secret from buildUpdateMemoryBody       RED
//	M9  point ReinforceMemory at /reinforce2                 RED (path)
func TestReinforceAndUpdateSendCredentialsAndTheWorkItemAndNoMemoryId(t *testing.T) {
	s := newMemoryWireStack(t)

	for _, tc := range []struct {
		tool   string
		method string
		suffix string
	}{
		{"pf_reinforce_memory", http.MethodPatch, "/reinforce"},
		{"pf_update_memory", http.MethodPatch, "/update"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			wire := memoryToolWire[tc.tool]
			args := map[string]any{}
			for k, v := range wire.base {
				args[k] = v
			}
			req := s.call(t, tc.tool, args)

			if req.method != tc.method {
				t.Errorf("%s issued %s, want %s", tc.tool, req.method, tc.method)
			}
			// The path segment, and the endpoint the card names.
			if want := "/v1/memories/" + probeMemory + tc.suffix; req.path != want {
				t.Errorf("%s reached %q, want %q — the card publishes that endpoint and the id "+
					"as its path segment", tc.tool, req.path, want)
			}

			want := []string{"attempt_id", "claim_epoch", "session_secret", "work_item_id"}
			if tc.tool == "pf_reinforce_memory" {
				// additional_context is required, so the base call carries it.
				want = append(want, "additional_context")
				sort.Strings(want)
			}
			got := bodyKeys(req)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s's base body is %v, want exactly %v.\nStated as an exact set, not "+
					"as \"contains the four\": a subset check goes green the day a fifth key "+
					"starts riding along, and `memory_id` appearing here is the specific "+
					"regression the card's \"absent from the body on purpose\" is about.",
					tc.tool, got, want)
			}
		})
	}
}

// TestRedactMemoryBodyCarriesOnlyTheReason holds the whole hop 2-3 sentence of
// the pf_redact_memory card: exactly `{reason}`, no credentials at all, PATCH,
// and the id as the path segment.
//
// It is also the positive control the aihub#325 credential arm cannot supply.
// TestMemoryToolsSendCredentialsWithTheirWorkItem `continue`s on any tool whose
// body has no attempt_id, so pf_redact_memory has always been SKIPPED by it.
//
// ⚠️ Corrected 2026-09-10 (aihub#543): this comment, and the card, had the blind
// spot INVERTED. Credentials with NO work_item_id is precisely the pair that
// invariant refuses, so that day would move this tool from "skipped" to RED —
// mutant M10 below is that day, and it reddens there too. The shape neither of
// them can see is credentials arriving WITH a work_item_id: it satisfies the
// invariant, and it is what would quietly turn a role-authorized path into one a
// reader takes for attempt-gated. The card calls the absence deliberate; the
// exact key set here is what makes that a checked claim rather than a
// description of whatever the renderer happens to do.
//
//	M10  add attempt_id to buildRedactMemoryBody          RED
//	M11  make buildRedactMemoryBody return an empty map   RED (reason missing)
//	M12  rename the body key to redaction_reason          RED (exact key set)
//	M13  change RedactMemory to POST                      RED (method)
func TestRedactMemoryBodyCarriesOnlyTheReason(t *testing.T) {
	s := newMemoryWireStack(t)

	req := s.call(t, "pf_redact_memory", map[string]any{
		"memory_id": probeMemory,
		"reason":    "superseded by a newer note",
	})

	if req.method != http.MethodPatch {
		t.Errorf("pf_redact_memory issued %s, want PATCH", req.method)
	}
	if want := "/v1/memories/" + probeMemory + "/redact"; req.path != want {
		t.Errorf("pf_redact_memory reached %q, want %q", req.path, want)
	}
	if got := bodyKeys(req); len(got) != 1 || got[0] != "reason" {
		t.Fatalf("pf_redact_memory's body is %v, want exactly [reason].\nThe card states the "+
			"absence of attempt credentials as a design decision — authorization here is by "+
			"role — and says building unread credentials into the body would make a reader "+
			"conclude the path is attempt-gated when it is not. An exact set is the only "+
			"form of that claim a fourth key cannot walk past.", got)
	}
	if got, _ := lookupPath(req.body, "reason"); got != "superseded by a newer note" {
		t.Errorf("pf_redact_memory sent reason=%v, want the caller's value", got)
	}
}

// TestActivateMemoryPostsTheIdInThePathWithNoBody holds pf_activate_memory's
// whole hop 2-3 sentence: the handler refuses an empty value, and a real one
// becomes POST /v1/memories/<id>/activate with nothing else sent.
//
// The refusal half is asserted as "no request reached the wire", not as "an
// error came back". A handler that forwarded the empty id and let the server
// answer 404 would produce an error result too, and the card's claim is about
// where the refusal happens — it is what makes the id a path segment safe to
// build a URL from.
//
//	M14  drop the memID == "" guard in the activate handler   RED (a request
//	                                                          reaches the wire)
//	M15  pass an empty body map instead of nil to ActivateMemory  RED (body)
//	M16  change ActivateMemory to PATCH                       RED (method)
func TestActivateMemoryPostsTheIdInThePathWithNoBody(t *testing.T) {
	s := newMemoryWireStack(t)

	isErr, req := s.callAllowingError(t, "pf_activate_memory", map[string]any{"memory_id": ""})
	if !isErr {
		t.Error("pf_activate_memory accepted an empty memory_id")
	}
	if req != nil {
		t.Errorf("pf_activate_memory sent %s %s for an empty memory_id: the card says the "+
			"handler rejects the empty value, and a refusal that still makes the round trip "+
			"is a different contract — it builds a URL out of nothing", req.method, req.path)
	}

	req = s.call(t, "pf_activate_memory", map[string]any{"memory_id": probeMemory})
	if req.method != http.MethodPost {
		t.Errorf("pf_activate_memory issued %s, want POST", req.method)
	}
	if want := "/v1/memories/" + probeMemory + "/activate"; req.path != want {
		t.Errorf("pf_activate_memory reached %q, want %q", req.path, want)
	}
	if keys := bodyKeys(req); keys != nil {
		t.Errorf("pf_activate_memory sent a body carrying %v; the card says a POST with a nil "+
			"body — the id is the path segment and nothing else is sent, which is what lets "+
			"the Memory-First recall step activate what it read without a claimed work item", keys)
	}
}
