package mcp_test

// aihub#543 probe wave 1 — the two `docs/mcp-cards/pf_claim_work_item.md`
// sentences about what the tool PUBLISHES, bound to the live schema rather than
// to a copy of it.
//
//	"Two more were published here once and are gone by decision rather than by
//	 oversight: `mode` (`aihub#394`) and, on the ready-queue side, the same plan-B
//	 treatment."
//	    -> TestWithdrawnParamsStayUnpublished
//	"The design requires both (`H-R3-8`), and the description now says so…"
//	    -> TestPublishedIdempotencyKeyDescriptionDistinguishesTheHeader
//
// Both are read off a REAL session (`publishedTool` / `publishedSchemaProps`)
// rather than from the generated contract JSON, which carries no per-property
// descriptions and so cannot see the string the second test is about.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestWithdrawnParams|TestPublishedIdempotencyKey' -count=1

import (
	"encoding/json"
	"strings"
	"testing"
)

// withdrawnParams are the parameters a work item DECIDED to withdraw, with the
// decision, and a live parameter of the same tool used as the walk's floor.
//
// 🔴 The floor entry is not decoration. "Is `mode` absent?" is answered `true` by
// a lookup that returns an empty map — a renamed tool, a schema shape this test
// no longer parses, a registration that failed — and every one of those reads as
// compliance. The live parameter beside it fails first in all of those cases.
var withdrawnParams = []struct {
	tool, param, decision, floorParam string
}{
	{
		tool: "pf_claim_work_item", param: "mode", decision: "aihub#394",
		floorParam: "idempotency_key",
	},
	{
		tool: "pf_get_ready_queue", param: "non_conflicting", decision: "aihub#387",
		floorParam: "project",
	},
}

// TestWithdrawnParamsStayUnpublished is the ratchet under a withdrawal.
//
// A parameter is withdrawn when the promise made for it was true WITHOUT it —
// `mode`'s "restores step state from the previous attempt" is a property of
// wi_step_state, and `non_conflicting` was never read at hop 3 at all. Nothing
// stops either from being re-added "for symmetry": the published schema is one
// AddTool call away, and a re-added parameter that still has no effect is the
// exact defect both work items closed. The parameter contract arm next door
// catches a published parameter the claim path does not act on, so a re-added
// INERT `mode` would be red there — but a re-added `mode` wired to something
// would be green everywhere, while the card's sentence quietly became false.
//
// MUTANTS:
//
//	M15 enforcement: re-publish `mode` on pf_claim_work_item
//	                                            RED  pf_claim_work_item/mode
//	M16 enforcement: re-publish `non_conflicting` on pf_get_ready_queue
//	                                            RED  pf_get_ready_queue/non_conflicting
//	M17 enforcement: point the walk at a tool name that does not exist
//	                                            RED  the floor (publishedSchemaProps
//	                                                 fails on an unknown tool)
//	M18 publication: drop the citation from the card sentence
//	                                            RED  K12 DEBT_GROWTH. ⚠️ This arm
//	                                                 reads the SCHEMA, not the card,
//	                                                 so the publication side is held
//	                                                 by K12's citation binding
func TestWithdrawnParamsStayUnpublished(t *testing.T) {
	for _, w := range withdrawnParams {
		t.Run(w.tool+"/"+w.param, func(t *testing.T) {
			props := publishedSchemaProps(t, w.tool)
			if _, live := props[w.floorParam]; !live {
				t.Fatalf("%s publishes no %q (it publishes %v) — this walk is not reading the "+
					"schema it thinks it is, and every absence it reports would be an artefact",
					w.tool, w.floorParam, sortedPropNames(props))
			}
			if declared, present := props[w.param]; present {
				t.Errorf("%s publishes %q (declared %q) again. %s withdrew it: a parameter this "+
					"process publishes is a parameter callers will send, and the promise it was "+
					"published for is one the tool keeps without it.",
					w.tool, w.param, declared, w.decision)
			}
		})
	}
}

func sortedPropNames(props map[string]string) []string {
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	return out
}

// TestPublishedIdempotencyKeyDescriptionDistinguishesTheHeader pins the card's
// "One word, two mechanisms" paragraph where it makes a checkable promise: that
// the published description SAYS SO.
//
// 🔴 The header name is taken from the request the client really sent, not
// written down here. That is the difference between this arm and a spell-check:
// if the transport ever renames the header, the description has to be reworded
// in the same change or this goes red — which is the direction the card's own
// argument runs, since the paragraph exists because "idempotency" stopped being
// unambiguous at this call site the day a client started sending the header.
//
// The two values are compared as well as the two names: a description that
// distinguishes a body parameter from a header is describing two mechanisms, and
// two mechanisms that always carried the same value would be one.
//
// MUTANTS:
//
//	M19 enforcement: drop "NOT the HTTP Idempotency-Key header" from the
//	    published description                    RED  names_the_header
//	M20 enforcement: stop minting the header (setStandardHeaders no longer sets
//	    it)                                      RED  the wire floor — no header
//	                                                  observed to name
//	M21 enforcement: mint the header FROM the body — `Idempotency-Key` set to the
//	    body's `idempotency_key` when present, the "why mint a random one when the
//	    body already has a key" refactor    RED  two_distinct_values
//	M22 publication: drop the citation from the card sentence
//	                                             RED  K12 DEBT_GROWTH
func TestPublishedIdempotencyKeyDescriptionDistinguishesTheHeader(t *testing.T) {
	const bodyParam = "idempotency_key"

	// What the wire really carries, observed through the same fake the omission
	// probes use.
	body, calls := driveClaim(t, nil)
	var headerName, headerValue string
	for _, c := range calls {
		if c.Path != claimWirePath {
			continue
		}
		for name, values := range c.Header {
			if strings.Contains(strings.ToLower(name), "idempotency") && len(values) > 0 {
				headerName, headerValue = name, values[0]
			}
		}
	}
	if headerName == "" {
		t.Fatalf("the claim request carried no idempotency header at all, so there is no "+
			"second mechanism for the description to distinguish. The card says the client mints "+
			"one per request (aihub#436); headers seen: %v", headerNames(calls))
	}

	sentBody, _ := body[bodyParam].(string)
	if sentBody == "" {
		t.Fatalf("the claim body carried no %s, so the two mechanisms cannot be compared", bodyParam)
	}

	t.Run("two_distinct_values", func(t *testing.T) {
		if headerValue == sentBody {
			t.Errorf("the %s header and the %s body parameter both carried %q. The card calls "+
				"them two mechanisms with different guarantees — one a 24h replay of a cached HTTP "+
				"response, the other a DB dedup on run_attempts — and a caller who set the body "+
				"parameter would be silently choosing the header's cache key too.",
				headerName, bodyParam, redactSecret(sentBody))
		}
	})

	t.Run("names_the_header", func(t *testing.T) {
		tool := publishedTool(t, "pf_claim_work_item")
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal pf_claim_work_item InputSchema: %v", err)
		}
		desc, present := publishedParamDescription(t, schema, bodyParam)
		if !present {
			t.Fatalf("pf_claim_work_item publishes no %s property at all", bodyParam)
		}
		if len(desc) < 40 {
			t.Fatalf("the published %s description is %d characters (%q) — too short to be the "+
				"description this test is about, and a substring check against it would mean nothing",
				bodyParam, len(desc), desc)
		}
		if !strings.Contains(desc, headerName) {
			t.Errorf("the published %s description never names the %s header the client sends "+
				"with this very request:\n    %s\nThe card says the description \"now says so\", and "+
				"H-R3-8 requires BOTH mechanisms — so a caller reading only this string cannot tell "+
				"which one they are setting.", bodyParam, headerName, desc)
		}
		// The other half of "two mechanisms": where THIS one dedups. Named because
		// a description that mentions the header without saying what the parameter
		// itself does has moved the ambiguity rather than removed it.
		if !strings.Contains(desc, "run_attempts") {
			t.Errorf("the published %s description names the header but not the DB dedup this "+
				"parameter performs (`run_attempts.idempotency_key`):\n    %s", bodyParam, desc)
		}
	})
}

// headerNames lists the header names seen on the claim route, so a failure says
// what WAS there rather than only what was missing.
func headerNames(calls []recordedCall) []string {
	var out []string
	for _, c := range calls {
		if c.Path != claimWirePath {
			continue
		}
		for name := range c.Header {
			out = append(out, name)
		}
	}
	return out
}
