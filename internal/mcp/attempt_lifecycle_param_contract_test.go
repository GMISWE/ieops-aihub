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
	"encoding/json"
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

// completeAttemptRefusal drives the real pf_complete_attempt against a fake
// aihub and hands back the refusal text together with every path the tool
// touched. Unlike completeAttemptPauseReason above it does NOT require a
// /complete request: a refusal is the subject of the aihub#452 arms below, and
// what those arms need to see is that nothing was requested at all.
func completeAttemptRefusal(t *testing.T, args map[string]any) (text string, isErr bool, paths []string) {
	t.Helper()
	const wiID = "wi_pause452"
	seedStateFile(t, wiID)

	f := newFakeAihub(t)
	full := map[string]any{"work_item_id": wiID}
	for k, v := range args {
		full[k] = v
	}
	result, isErr := callTool(t, f, "pf_complete_attempt", full)
	if raw, ok := result["_raw"].(string); ok {
		return raw, isErr, f.paths()
	}
	b, _ := json.Marshal(result)
	return string(b), isErr, f.paths()
}

// TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus is the aihub#452 hop-2
// gate, and it FAILS on the pre-fix tree: the handler's only condition on
// pause_reason was non-emptiness, so `status:"failed"` plus a reason was
// forwarded to /complete and written to the attempt row.
//
// The two assertions are separate properties and both are load-bearing. The
// refusal is what the caller sees; the EMPTY path list is what makes it a
// refusal rather than a late complaint about something already recorded. A
// rejection placed after the note block would have emitted a /events request for
// a call it then declined — the same class of defect as the one being fixed,
// introduced by its own fix.
func TestCompleteAttemptRefusesPauseReasonOnNonPausedStatus(t *testing.T) {
	const wantPrefix = `pause_reason is read only when status="paused"`

	for _, status := range []string{"wrapped", "failed"} {
		t.Run(status, func(t *testing.T) {
			text, isErr, paths := completeAttemptRefusal(t, map[string]any{
				"status":       status,
				"pause_reason": "owner adjudication pending",
				"note":         "wrapped: this note must not be emitted",
			})
			if !isErr {
				t.Fatalf("pf_complete_attempt(status=%q, pause_reason=...) succeeded, result=%s, "+
					"requests=%v.\nThe schema publishes pause_reason as read only when "+
					"status=\"paused\"; forwarding it on a terminal status records a pause reason on a "+
					"row whose only reader filters on wi.status='paused'.", status, text, paths)
			}
			if !strings.HasPrefix(text, wantPrefix) {
				t.Errorf("refusal = %q, want it to start with %q", text, wantPrefix)
			}
			if !strings.Contains(text, status) {
				t.Errorf("refusal = %q, want it to name the offending status %q: the caller sent two "+
					"fields and cannot tell which one was objected to", text, status)
			}
			if len(paths) != 0 {
				t.Errorf("pf_complete_attempt refused the call but still made requests: %v.\nA "+
					"refusal has to happen before the note is emitted, or the tool has written a "+
					"timeline event for a call it declined.", paths)
			}
		})
	}
}

// TestCompleteAttemptStillPausesWithAReason is the green control between the
// refusing arm above and the narrowness arms below: the combination the column
// exists for must still reach /complete carrying the reason.
func TestCompleteAttemptStillPausesWithAReason(t *testing.T) {
	const reason = "waiting on the owner to adjudicate the design table"
	body := completeAttemptPauseReason(t, map[string]any{
		"status":       "paused",
		"pause_reason": reason,
	})
	if body["pause_reason"] != reason {
		t.Errorf("pause_reason = %#v, want %q — the aihub#452 guard must not cost the pause path "+
			"the field aihub#424 added for it", body["pause_reason"], reason)
	}
}

// TestCompleteAttemptDoesNotRequirePauseReasonOnPause is the narrowness arm for
// the over-wide reading of the same sentence: "read only when status=paused"
// does NOT mean "paused must carry a reason". This tool is the call every
// executor in the workspace ends its run with, so a guard written as the
// converse would refuse ordinary pauses.
func TestCompleteAttemptDoesNotRequirePauseReasonOnPause(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"absent": {"status": "paused"},
		"empty":  {"status": "paused", "pause_reason": ""},
	} {
		t.Run(name, func(t *testing.T) {
			body := completeAttemptPauseReason(t, args)
			if body["status"] != "paused" {
				t.Fatalf("status = %#v, want \"paused\"", body["status"])
			}
			if v, present := body["pause_reason"]; present {
				t.Errorf("pf_complete_attempt sent pause_reason=%#v on a pause that supplied none "+
					"(%s) — '' and nil must stay distinguishable on the column", v, name)
			}
		})
	}
}

// TestCompleteAttemptWrapWithEmptyPauseReasonIsNotRefused is the other
// narrowness arm, and it is the one that protects the wrap path this whole
// workspace depends on. An empty reason states nothing, so there is nothing to
// put in the wrong place: refusing it would make the guard's decision surface
// wider than the ambiguity it exists to resolve, and would fail any caller that
// sets the field to its zero value.
func TestCompleteAttemptWrapWithEmptyPauseReasonIsNotRefused(t *testing.T) {
	body := completeAttemptPauseReason(t, map[string]any{
		"status":       "wrapped",
		"pause_reason": "",
	})
	if body["status"] != "wrapped" {
		t.Errorf("status = %#v, want \"wrapped\"", body["status"])
	}
	if v, present := body["pause_reason"]; present {
		t.Errorf("pf_complete_attempt forwarded pause_reason=%#v on a wrap", v)
	}
}
