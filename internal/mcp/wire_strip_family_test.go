package mcp_test

import (
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// aihub#586 — the family half of the strip, per tool, on real requests.
//
// wire_strip_test.go (package mcp) pins the roster and the census;
// remember_wire_shape_test.go holds pf_remember's own arm in depth, key by key.
// This file drives ALL THREE family tools through the real handler with the
// same attack shape — a bogus name plus `rendered_html`, the measured
// aihub#586 key — and requires, for each:
//
//	absence     neither name reaches the recorded body (for pf_remember that
//	            is the fix; for pf_save_artifact and pf_update_memory it is
//	            the boundary guarantee their whitelist builders already gave,
//	            now held where the builders cannot un-give it);
//	presence    a published sibling from the same call still lands, so the
//	            strip is a projection and this harness is reading the request
//	            it thinks it is;
//	disclosure  the response's request_adjusted names both stripped keys
//	            under unknown_params — the ruling is strip AND report.
//
// One extra arm for pf_save_artifact: a caller-supplied `attempt_id` must not
// displace the state file's. That was already true (buildSaveArtifactBody
// writes the credential unconditionally), but before the strip the caller's
// value at least ARRIVED at the builder's input; now it dies at the boundary,
// and the arm pins which of the two values the wire carries.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestWireStrippedFamily' -count=1
//
// MUTANTS:
//
//	M18 enforcement: delete pf_update_memory from wireStrippedTools
//	                                          RED  the roster pin in
//	                                               wire_strip_test.go — NOT this
//	                                               file's absence arms, which stay
//	                                               green because that tool's
//	                                               builder whitelists on its own.
//	                                               That is why the roster pin
//	                                               exists: for the two builder
//	                                               tools the strip is the boundary
//	                                               guarantee, and only the pin
//	                                               notices the boundary opening
//	                                               while the builders still hold.
//	M19 enforcement: delete pf_remember from wireStrippedTools
//	                                          RED  this file's pf_remember absence
//	                                               arm (both keys land) AND the
//	                                               roster pin
//	M20 enforcement: strip only the bogus key, forward rendered_html
//	                                          RED  every absence arm's
//	                                               rendered_html half
//	M21 publication: delete the family sentence's citation from any of the
//	    three cards                           RED  K12
func TestWireStrippedFamilyDropsUnpublishedKeysAndDisclosesThem(t *testing.T) {
	const (
		bogusKey    = "bogus_probe_aihub_586_never_published"
		smuggledKey = "rendered_html"
	)

	// One published sibling per tool, asserted present so an empty or misread
	// body cannot satisfy the absence arms by accident.
	sibling := map[string]string{
		"pf_remember":      "content",
		"pf_save_artifact": "content",
		"pf_update_memory": "work_item_id",
	}

	for _, tool := range []string{"pf_remember", "pf_save_artifact", "pf_update_memory"} {
		t.Run(tool, func(t *testing.T) {
			s := newMemoryWireStack(t)
			args := map[string]any{}
			for k, v := range memoryToolWire[tool].base {
				args[k] = v
			}
			args[bogusKey] = "smuggled value"
			args[smuggledKey] = "<p>must never reach the wire through this tool</p>"

			text, rec := s.callWithText(t, tool, args)

			for _, key := range []string{bogusKey, smuggledKey} {
				if got, present := rec.body[key]; present {
					t.Errorf("%s forwarded unpublished %q = %#v — the aihub#586 boundary is "+
						"open for this tool", tool, key, got)
				}
			}
			if _, present := rec.body[sibling[tool]]; !present {
				t.Fatalf("%s's published %q is missing from the body (%v), so the absence "+
					"assertions above are about a request this harness misread",
					tool, sibling[tool], rec.body)
			}

			var result map[string]any
			if err := json.Unmarshal([]byte(text), &result); err != nil {
				t.Fatalf("%s's result is not a JSON object: %v (%q)", tool, err, text)
			}
			entries, _ := result["request_adjusted"].([]any)
			named := map[string]bool{}
			for _, e := range entries {
				entry, _ := e.(map[string]any)
				if entry["param"] != "unknown_params" {
					continue
				}
				requested, _ := entry["requested"].([]any)
				for _, name := range requested {
					if s, ok := name.(string); ok {
						named[s] = true
					}
				}
			}
			for _, key := range []string{bogusKey, smuggledKey} {
				if !named[key] {
					t.Errorf("%s stripped %q without naming it in request_adjusted — the "+
						"ruling is strip AND report, and a silent strip is the aihub#389 "+
						"defect resurrected one hop earlier: %s", tool, key, text)
				}
			}
		})
	}

	t.Run("pf_save_artifact keeps the state file's credential, not the caller's", func(t *testing.T) {
		s := newMemoryWireStack(t)
		args := map[string]any{}
		for k, v := range memoryToolWire["pf_save_artifact"].base {
			args[k] = v
		}
		args["attempt_id"] = "ra_attacker"
		_, rec := s.callWithText(t, "pf_save_artifact", args)
		if got := rec.body["attempt_id"]; got != "ra_probe" {
			t.Errorf("pf_save_artifact's wire attempt_id = %#v, want the state file's "+
				"\"ra_probe\" — a caller-supplied credential either displaced the injected "+
				"one or the injection stopped", got)
		}
	})
}

// callWithText invokes a tool and returns both the result text and the request
// the recorder saw. The memoryWireStack.call helper returns only the request;
// the aihub#586 arms need the response too, because the disclosure lives there.
func (s *memoryWireStack) callWithText(t *testing.T, tool string, args map[string]any) (string, *recordedRequest) {
	t.Helper()
	s.mu.Lock()
	s.last = nil
	s.mu.Unlock()

	res, err := s.session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s(%v): %v", tool, args, err)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError {
		t.Fatalf("call %s(%v) returned an error, so no request reached the wire: %s", tool, args, text)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		t.Fatalf("call %s(%v) made no HTTP request at all", tool, args)
	}
	return text, s.last
}
