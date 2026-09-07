package mcp_test

// aihub#424 — the attempt-lifecycle routes' bound-but-unpublished fields.
//
// aihub#419's G4 arm found five request fields the server binds that no MCP tool
// publishes. This file carries the two arms that are cheap to state directly; the
// other three are resolved by deletion or by a reasoned entry in
// serverNamesNoToolCanReach, and G4 itself is their gate.
//
// ⚠️ What "unreachable" means here, stated from what this package MEASURES rather
// than from the sentence that travels with this defect. G4's own message says
// "the SDK drops an argument the InputSchema does not declare" — go-sdk does not:
// unknown_params.go's header records that its pre-handler validation passes
// anything, because JSON Schema allows additional properties unless told
// otherwise, and the aihub#389 disclosure that names the argument back to the
// caller is proof the argument arrives. What is true, and sufficient, is that
// **nothing reads it**: a tool handler pulls named arguments out of the map, so an
// undeclared one is forwarded to nothing, echo's c.Bind then ignores what never
// arrived, and the call answers 200 having done less than it said. The first RED
// run of the pause_reason test below printed exactly that disclosure.
//
// ─── Arm 1: the blind spot that let `mode` survive its own withdrawal ───────
//
// aihub#394 withdrew `mode` from pf_claim_work_item's schema after proving it
// selected nothing, and left domain.ClaimRequest binding it. Its gate could not
// see that, and the reason is worth stating as a mechanism rather than as an
// anecdote: claim_param_contract_test.go quantifies over PUBLISHED parameters, so
// **the act of withdrawing a parameter removes it from that gate's field of
// view**. TestClaimPathActsOnNothingUnpublished closes half of the reverse
// direction — but only for fields the claim path ACTS on, and `mode`'s three
// reads were one self-default and two audit values, none of which is an action.
// A field that is bound, inert and unpublished therefore satisfied every gate in
// the package.
//
// TestClaimRequestBindsNothingUnreachable quantifies over the BOUND fields
// instead, which is the one quantifier that does not shrink when a parameter is
// withdrawn.
//
// ─── Arm 2: pausing through the other route could not say why ───────────────
//
// domain.CompleteAttemptRequest.PauseReason is written to run_attempts.pause_reason
// and into the attempt_completed event payload, on whichever route reaches
// FnCompleteAttempt. pf_pause_attempt published it; pf_complete_attempt did not,
// though its own `status` accepts "paused" — and the corpus says that path is
// used: of 605 observed pf_complete_attempt calls, 44 carried status="paused",
// and every one of them recorded a NULL reason, while 77 of 77 pf_pause_attempt
// calls supplied one (docs/audits/aihub-412-corpus-facts/param-types-vs-schema.md).
// So the same state was reachable two ways and only one of them could say why.
//
// No database needed:
//
//	go test ./internal/mcp/ -run 'TestClaimRequestBinds|TestCompleteAttempt' -v

import (
	"strings"
	"testing"
)

// TestClaimRequestBindsNothingUnreachable is the reverse of aihub#394's gate,
// quantified over what the SERVER binds rather than over what the tool publishes.
//
// It FAILS on the pre-fix tree, naming `mode`: bound by domain.ClaimRequest,
// published by nothing since aihub#394, and therefore settable by no MCP caller.
// A field in that state is worse than either half alone — the schema says the
// capability does not exist, and the server keeps a code path for it that only a
// non-MCP client could ever reach.
//
// The exemption list is claim_param_contract_test.go's own
// claimFieldsDeliberatelyUnpublished, reused rather than copied: a second list
// would be a second thing to keep true, and that file already checks its entries
// for staleness against package domain.
func TestClaimRequestBindsNothingUnreachable(t *testing.T) {
	published := publishedSchemaProps(t, claimTool)
	fields := claimRequestFields(t)

	checked := 0
	for jsonName := range fields {
		if _, ok := published[jsonName]; ok {
			checked++
			continue
		}
		if reason, exempt := claimFieldsDeliberatelyUnpublished[jsonName]; exempt {
			t.Logf("%s: bound but deliberately unpublished — %s", jsonName, reason)
			continue
		}
		t.Errorf("domain.ClaimRequest binds %q and %s publishes no such parameter.\n"+
			"No MCP caller can set it: the handler reads only the arguments it publishes, so an "+
			"undeclared one is forwarded to nothing and the field can only ever hold its zero "+
			"value. Note which gate this defeats — "+
			"TestClaimEveryPublishedParamIsActedOnByTheClaimPath quantifies over PUBLISHED "+
			"parameters, so withdrawing one removes it from that gate's field of view, and "+
			"TestClaimPathActsOnNothingUnpublished only sees fields the claim path ACTS on, "+
			"which an inert field by definition is not. Stop binding it, publish it, or record "+
			"it in claimFieldsDeliberatelyUnpublished with the reason.", jsonName, claimTool)
	}
	if checked == 0 {
		t.Fatal("not one ClaimRequest field was found published — the schema lookup is broken, " +
			"not the struct, and this test would report every field")
	}
	t.Logf("%d of %d ClaimRequest fields are published; %d exempt",
		checked, len(fields), len(claimFieldsDeliberatelyUnpublished))
}

// completeAttemptPauseReason drives the real pf_complete_attempt against a fake
// aihub and returns the body it POSTed to /complete.
func completeAttemptPauseReason(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	const wiID = "wi_pause424"
	seedStateFile(t, wiID)

	f := newFakeAihub(t)
	full := map[string]any{"work_item_id": wiID}
	for k, v := range args {
		full[k] = v
	}
	result, isErr := callTool(t, f, "pf_complete_attempt", full)
	if isErr {
		t.Fatalf("pf_complete_attempt failed: %v", result)
	}
	for _, c := range f.recorded() {
		if strings.HasSuffix(c.Path, "/complete") {
			return c.Body
		}
	}
	t.Fatalf("pf_complete_attempt made no /complete request; paths=%v", f.paths())
	return nil
}

// TestCompleteAttemptForwardsPauseReason is the gate for arm 2.
//
// It FAILS on the pre-fix tree: the handler forwards the four arguments it
// publishes and nothing else, so the body reaches the server without it — and the
// aihub#389 disclosure fires, which is what a caller sees today when they try.
// The assertion is on the RECORDED REQUEST rather than on the schema, because
// publishing a parameter this process then fails to forward would satisfy a
// schema-only test while changing nothing — that is aihub#419's G1 in miniature.
func TestCompleteAttemptForwardsPauseReason(t *testing.T) {
	const reason = "waiting on the owner to adjudicate the design table"
	body := completeAttemptPauseReason(t, map[string]any{
		"status":       "paused",
		"pause_reason": reason,
	})

	got, present := body["pause_reason"]
	if !present {
		t.Fatalf("pf_complete_attempt POSTed %v to /complete with no pause_reason.\n"+
			"domain.CompleteAttemptRequest binds it, FnCompleteAttempt writes it to "+
			"run_attempts.pause_reason and into the attempt_completed event, and this route is "+
			"one of the two ways to reach status=paused — so a caller pausing through this tool "+
			"records no reason at all while pf_pause_attempt's callers record one every time.",
			sortedAnyKeys(body))
	}
	if got != reason {
		t.Errorf("pause_reason = %#v, want %q", got, reason)
	}
	if body["status"] != "paused" {
		t.Errorf("status = %#v, want \"paused\" — the fixture is not exercising the pause path",
			body["status"])
	}
}

// TestCompleteAttemptOmitsPauseReasonWhenAbsent is the negative control, and it
// is not decoration: `body["pause_reason"] = strArg(args, "pause_reason")`
// without a guard would pass the test above and send `""` on every wrap, which
// FnCompleteAttempt writes to the column unconditionally — turning "this attempt
// was not paused" into "paused for no stated reason" on 560 wraps out of 605.
func TestCompleteAttemptOmitsPauseReasonWhenAbsent(t *testing.T) {
	body := completeAttemptPauseReason(t, map[string]any{"status": "wrapped"})
	if v, present := body["pause_reason"]; present {
		t.Errorf("pf_complete_attempt sent pause_reason=%#v on a wrap that supplied none — an "+
			"unguarded assignment writes an empty reason to run_attempts.pause_reason for every "+
			"terminal completion", v)
	}
}
