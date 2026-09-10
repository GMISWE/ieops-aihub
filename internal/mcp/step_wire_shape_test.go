package mcp_test

// aihub#543 probe wave 1 — `docs/mcp-cards/pf_update_step.md`'s hop 2-3, the
// three claims about what leaves this process that the schema cannot show:
//
//	"Optional keys are forwarded only when non-empty"
//	    -> TestUpdateStepForwardsOptionalKeysOnlyWhenSet
//	"`heartbeat` returns early with a credentials-only body, so on that path
//	 `step_id`, `status`, `next_step` and everything else go nowhere"
//	    -> TestUpdateStepHeartbeatSendsNothingButCredentials
//	"Credentials … are injected from the local state file via
//	 `internal/config/state.go` (`ResolveStateFile`)"
//	    -> TestUpdateStepInjectsTheResolvedStateFileCredentials
//
// and the client half of hop 0-1's "`error_type` / `escalated` on a `completed`
// call are silently dropped":
//
//	    -> TestPublishedIgnoredOnCompletedSaysWhatTheStepHopsEnforce
//
// 🔴 `updateStepBody` carries a doc comment saying it was "extracted from the
// handler so a test can hold it against the struct that actually binds on the
// other side", and no test has ever called it: the tool handler is its only
// caller in the repo. The comparison it was extracted for happens in
// tools_step_contract_test.go against a HAND-WRITTEN map of argument names
// (stepBodyFieldFor), so the rename it records is asserted about the server's
// struct and never about the body this process really sends.
//
// This file does not call it either, deliberately. The verdicts below come from a
// request a fake aihub RECEIVED, which is the stronger claim: the sentences are
// about the wire, and every hop between the tool argument and the wire — strArg
// collapsing an absent argument onto "", the heartbeat early return, the
// state-file resolver — is part of what they assert, and a helper called directly
// skips all three.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestUpdateStep(ForwardsOptional|Heartbeat|Injects)' -count=1

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// stepWirePath is the only route pf_update_step PATCHes, and therefore the only
// one the body assertions below may read.
func stepWirePath(wiID string) string { return "/v1/work_items/" + wiID + "/step" }

// driveUpdateStep runs one real pf_update_step against a fake aihub in an
// isolated workspace and returns the body the server received.
//
// The FLOOR lives here rather than in each test: several assertions below are
// about a key that must be ABSENT, and an absent key cannot be told apart from a
// body that carried nothing at all. So the body is first required to carry the
// two keys this hop always renders — `status` and the resolved `attempt_id` —
// and a body missing either fails here instead of silently satisfying an absence
// assertion.
func driveUpdateStep(t *testing.T, wiID string, args map[string]any) map[string]any {
	t.Helper()

	f := newFakeAihub(t)
	f.on(stepWirePath(wiID), func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"status": "completed"}
	})

	args["work_item_id"] = wiID
	result, isErr := callToolBounded(t, f, "pf_update_step", args, 20*time.Second)
	if isErr {
		t.Fatalf("pf_update_step failed: %v — a refused call reaches the server with a body "+
			"nobody should draw conclusions from", result)
	}

	body := lastBodyFor(t, f, stepWirePath(wiID))
	if s, _ := body["status"].(string); s == "" {
		t.Fatalf("the step body carries no status (keys: %v) — the walk is broken and the absence "+
			"assertions below would pass against a body like this", sortedBodyKeys(body))
	}
	if s, _ := body["attempt_id"].(string); s == "" {
		t.Fatalf("the step body carries no attempt_id (keys: %v) — the credentials are injected by "+
			"this hop, so a body without them is not the body these tests are about",
			sortedBodyKeys(body))
	}
	return body
}

// TestUpdateStepForwardsOptionalKeysOnlyWhenSet pins the card's "Optional keys
// are forwarded only when non-empty".
//
// It is one claim with two observable halves, and the second is the one the card
// used to get WRONG. Sending `""` is byte-for-byte the same request as omitting
// the argument, because strArg cannot distinguish them and updateStepBody skips
// both — so what the rule buys is that a key which ARRIVES always carries a real
// value, NOT that the server can tell an omission from an explicit empty string.
// The card said the latter; measured here, it is the opposite way round, and the
// sentence was corrected in the same change that added this arm.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M6  enforcement: drop the `if v != ""` guard in updateStepBody, so every
//	    optional key is always forwarded    RED  the omitted-argument arm
//	M7  enforcement: forward `escalated` unconditionally rather than only when
//	    true                                RED  the omitted-argument arm
//	M8  publication: remove this arm's citation from the card sentence
//	                                        RED  K12 POPULATION_MOVED — with the
//	                                             citation goes the only backticked
//	                                             token in that sentence, so it also
//	                                             leaves the candidate population;
//	                                             either way the ledger stops matching
func TestUpdateStepForwardsOptionalKeysOnlyWhenSet(t *testing.T) {
	// The optional keys, with the value that makes each one appear. `escalated`
	// is a bool and its own branch, so it is listed with the others rather than
	// left to a reader to remember.
	optional := map[string]any{
		"step_attempt_id":  "sa_wire",
		"artifact_summary": "summary",
		"error_type":       "gate_failed",
		"escalated":        true,
	}

	t.Run("omitted arguments send no key", func(t *testing.T) {
		seedStateFile(t, "wi_wire_omitted")
		body := driveUpdateStep(t, "wi_wire_omitted", map[string]any{
			"step_id": "code_change", "status": "in_progress",
		})
		for key := range optional {
			if v, present := body[key]; present {
				t.Errorf("the body carries %q = %#v for an argument the caller never sent. The server "+
					"binds these as *string/bool, so a key that is always present makes \"the caller said "+
					"nothing\" indistinguishable from \"the caller said empty\" — and on error_type that is "+
					"the difference between a step that failed for a reason and one that did not fail.",
					key, v)
			}
		}
	})

	t.Run("an explicit empty string is the same request as an omission", func(t *testing.T) {
		seedStateFile(t, "wi_wire_empty")
		body := driveUpdateStep(t, "wi_wire_empty", map[string]any{
			"step_id": "code_change", "status": "in_progress",
			"step_attempt_id": "", "artifact_summary": "", "error_type": "",
		})
		for _, key := range []string{"step_attempt_id", "artifact_summary", "error_type"} {
			if v, present := body[key]; present {
				t.Errorf("the body carries %q = %#v for an argument sent as \"\". This is the half the "+
					"card got wrong for two waves: it claimed forwarding only non-empty values keeps an "+
					"omission DISTINGUISHABLE from an explicit \"\", and the measured effect is the "+
					"reverse — both reach the server as an absent field. If this key is now forwarded, "+
					"the distinction exists and the card sentence needs correcting back.", key, v)
			}
		}
	})

	t.Run("a value that was sent does arrive", func(t *testing.T) {
		seedStateFile(t, "wi_wire_present")
		args := map[string]any{"step_id": "code_change", "status": "failed"}
		for k, v := range optional {
			args[k] = v
		}
		body := driveUpdateStep(t, "wi_wire_present", args)
		for key, want := range optional {
			got, present := body[key]
			if !present {
				t.Errorf("CONTROL: %q was sent with %#v and never reached the server. Without this arm "+
					"the two above are satisfied by a body that forwards nothing at all, which would be "+
					"the aihub#290 silent drop rather than the documented rule.", key, want)
				continue
			}
			if got != want {
				t.Errorf("body[%q] = %#v, want %#v", key, got, want)
			}
		}
	})
}

// TestUpdateStepHeartbeatSendsNothingButCredentials pins the card's
// "`heartbeat` returns early with a credentials-only body, so on that path
// `step_id`, `status`, `next_step` and everything else go nowhere".
//
// The exact key SET, not a list of keys to look for. A test that checked only
// the four keys the card names would stay green when a fifth started riding
// along, and the card's claim is about "everything else" — which is a claim
// about the whole body and can only be asserted as one.
//
// This is also the hop that decides what the server's own aihub#442 disclosure
// can ever say: the card's hop 5 states that `request_adjusted`'s step_id/status
// entries are reachable only by a DIRECT HTTP caller, and that is TRUE precisely
// because this branch sends neither. The two sentences are one fact seen from
// both ends.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M9  enforcement: build the heartbeat body with updateStepBody plus
//	    heartbeat=true, the natural "reuse the renderer" refactor
//	                                        RED  the exact-key-set assertion
//	M10 enforcement: add step_id to the heartbeat body only
//	                                        RED  the exact-key-set assertion
//	M11 publication: remove this arm's citation from the card sentence
//	                                        RED  K12 — the sentence lands back in
//	                                             the debt column and the ledger row
//	                                             stops matching
func TestUpdateStepHeartbeatSendsNothingButCredentials(t *testing.T) {
	const wiID = "wi_wire_heartbeat"
	seedStateFile(t, wiID)

	f := newFakeAihub(t)
	f.on(stepWirePath(wiID), func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"status": "heartbeat_ok"}
	})

	// Everything a caller might send alongside heartbeat=true. status and
	// step_id are what the taught producer really sends (the pf-execute skill
	// text says "add heartbeat=true to an in_progress call"), and the terminal
	// pair is the shape that reads as a completion and completes nothing.
	result, isErr := callToolBounded(t, f, "pf_update_step", map[string]any{
		"work_item_id": wiID, "heartbeat": true,
		"step_id": "code_change", "status": "completed",
		"step_attempt_id": "sa_hb", "artifact_summary": "not a completion",
		"error_type": "gate_failed", "escalated": true,
	}, 20*time.Second)
	if isErr {
		t.Fatalf("a heartbeat carrying a terminal status must still be a heartbeat, not an error: %v", result)
	}

	body := lastBodyFor(t, f, stepWirePath(wiID))
	got := sortedBodyKeys(body)
	want := []string{"attempt_id", "claim_epoch", "heartbeat", "session_secret"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("the heartbeat body carries keys %v, want exactly %v — every extra key is a value "+
			"the caller believes was acted on, and this branch acts on none of them", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the heartbeat body carries keys %v, want exactly %v", got, want)
		}
	}
	// FLOOR: the flag itself really is what the server received, so "exactly four
	// keys" is a heartbeat request and not some other request with four keys.
	if body["heartbeat"] != true {
		t.Errorf("the body's heartbeat flag = %#v, want true", body["heartbeat"])
	}
}

// TestUpdateStepInjectsTheResolvedStateFileCredentials pins the card's
// "Credentials (`attempt_id`, `claim_epoch`, `session_secret`) are injected from
// the local state file via `internal/config/state.go` (`ResolveStateFile`)".
//
// 🔴 Addressed by SLUG, with a pre-claim stub on disk beside the canonical file.
// That is what makes the sentence's "via ResolveStateFile" observable rather
// than decorative: a handler that read `<argument>.json` directly would find the
// stub, whose session_secret is a different string and whose attempt_id is
// empty — the value the server answers 409 CONFLICT_EPOCH_MISMATCH to. Reading
// the canonical file is the resolver's whole job.
//
// What existed before, stated precisely because "uncovered" is easy to overclaim.
// stale_credential_delete_test.go drives both of this tool's branches by slug and
// checks the canonical secret reached the wire — but as a FIXTURE check on the way
// to asserting a deletion, and with NO stub on disk (it asserts the stub's absence,
// which is what makes its own delete-by-slug a no-op). Without a stub, a handler
// reading the argument directly finds nothing and fails loudly; with one, it finds
// a different credential and succeeds quietly. state_resolve_wiring_test.go covers
// the same resolver for pf_ship and pf_wrap, and not for this tool.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M12 enforcement: replace config.ResolveStateFile with config.ReadStateFile in
//	    the pf_update_step handler          RED  both arms (the stub's credentials
//	                                             reach the wire)
//	M13 enforcement: re-read the file with config.ReadStateFile INSIDE the
//	    heartbeat branch (the resolve is shared, so the branch has to be given its
//	    own read to mutate it alone)        RED  the heartbeat arm, GREEN the
//	                                             ordinary one — which is why both
//	                                             arms are here
//	M14 publication: remove this arm's citation from the card sentence
//	                                        RED  K12 — same shape
func TestUpdateStepInjectsTheResolvedStateFileCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"ordinary branch", map[string]any{
			"step_id": "code_change", "status": "completed", "step_attempt_id": "sa_resolved",
		}},
		{"heartbeat branch", map[string]any{
			"step_id": "code_change", "status": "in_progress", "heartbeat": true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newResolveWorkspace(t)
			writeResolveStub(t)
			writeResolveCanonical(t, nil)

			// ⚠️ Registered under the SLUG, because this tool builds the URL from
			// the caller's own argument and only the CREDENTIALS come from the
			// resolved file. (pf_wrap addresses /complete by sf.WIID; the server
			// resolves a slug in the path either way, aihub#127.) Asserted below
			// rather than left implicit, so the day the URL changes source this
			// arm says so instead of failing on a missing body.
			f := newFakeAihub(t)
			f.on(stepWirePath(resolveSlug), func(map[string]any) (int, any) {
				return http.StatusOK, map[string]any{"status": "completed"}
			})

			args := map[string]any{"work_item_id": resolveSlug}
			for k, v := range tc.args {
				args[k] = v
			}
			result, isErr := callToolBounded(t, f, "pf_update_step", args, 20*time.Second)
			if isErr {
				t.Fatalf("pf_update_step failed: %v\n"+
					"Read through the pre-claim stub, sf.WIID is %q and the stub carries no attempt_id at all",
					result, resolveSlug)
			}

			// One request, addressed by the caller's own string. A body assertion
			// against a request that never happened would be vacuous, so the path
			// is resolved from the recorder rather than assumed.
			if paths := f.paths(); len(paths) != 1 || paths[0] != stepWirePath(resolveSlug) {
				t.Fatalf("expected exactly one PATCH to %s, got %v — the URL is built from the "+
					"caller's argument (the server resolves the slug); if it now carries the canonical "+
					"id this arm still holds, but read the credentials assertion below against the new path",
					stepWirePath(resolveSlug), paths)
			}
			body := lastBodyFor(t, f, stepWirePath(resolveSlug))
			assertCanonicalCredentials(t, "tools_step.go pf_update_step ("+tc.name+")", body)

			// The three keys the card NAMES, present under those names. The helper
			// above asserts the values; this asserts the sentence's vocabulary,
			// which is what a caller reading a transcript matches against.
			for _, key := range []string{"attempt_id", "claim_epoch", "session_secret"} {
				if _, present := body[key]; !present {
					t.Errorf("the body carries no %q (keys: %v) — the card names these three, so a "+
						"credential travelling under another name makes the published sentence false "+
						"even where the call still works", key, sortedBodyKeys(body))
				}
			}
			// FLOOR: the stub really was on disk and really was a different
			// credential. Without this the assertions above pass in a workspace
			// where there was only ever one file to read.
			stub, err := config.ReadStateFile(resolveSlug)
			if err != nil {
				t.Fatalf("FLOOR: the pre-claim stub is not readable (%v), so this arm is not "+
					"exercising the resolver at all", err)
			}
			if stub.SessionSecret == resolveSecret {
				t.Fatalf("FLOOR: the stub's session_secret equals the canonical file's, so reading " +
					"either file would satisfy every assertion above")
			}
		})
	}
}

// TestPublishedIgnoredOnCompletedSaysWhatTheStepHopsEnforce is the publication
// half of the card's "`error_type` / `escalated` on a `completed` call are
// silently dropped, which is documented rather than changed because rejecting
// them is a behaviour change".
//
// The server half — the row carrying neither field, the work item unstalled, and
// the 200 — is driven against a database by
// internal/server/routes_step_dbgated_test.go's
// TestHandleUpdateStep_EscalatedSurvivesToTheHistoryRead. This arm holds the two
// things that make that a CONTRACT rather than an accident:
//
//   - the schema still SAYS so, in the words a caller reads before deciding what
//     to send; and
//   - this hop still forwards both fields on a completed call, so the drop is the
//     server's documented decision and not a client-side refusal wearing the same
//     clothes. That distinction is invisible from the server side and invisible
//     from the schema; only the request shows it.
//
// 🔴 Why the second half is not redundant with the first. If the client began
// refusing the combination — the change routes_step.go argues for on next_step,
// "every combination that cannot be honoured is REJECTED rather than ignored" —
// the DB arm would keep passing (nothing reaches it), the schema would keep
// saying "ignored (not refused)", and the published sentence would be false with
// every gate green. That is exactly the shape aihub#290 was filed about, so the
// probe for it has to observe a request.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M26 publication: drop "(not refused)" from error_type's description
//	                                        RED  the published-word half
//	M27 publication: reword escalated's description to say the field is refused
//	    on completed                        RED  the published-word half
//	M28 enforcement: skip error_type/escalated in updateStepBody when
//	    status=="completed" — the "don't send what is not read" refactor
//	                                        RED  the forwarded-on-completed half
func TestPublishedIgnoredOnCompletedSaysWhatTheStepHopsEnforce(t *testing.T) {
	tool := publishedTool(t, "pf_update_step")
	props := schemaProps(t, tool)

	// The published words. Each is checked for what it PROMISES, not for a
	// sentence: a reworded description that still says the field is read only on
	// failed and not refused elsewhere is the same contract.
	for _, c := range []struct {
		param string
		wants []string
		why   string
	}{
		{"error_type", []string{"failed", "ignored", "not refused"},
			"a caller who reads \"ignored\" without \"not refused\" has no way to know whether " +
				"sending one costs it the transition"},
		{"escalated", []string{"failed"},
			"escalated has a side effect on the FAILED branch (it blocks the work item), so which " +
				"branch reads it is the whole contract"},
	} {
		desc := props[c.param].Description
		if strings.TrimSpace(desc) == "" {
			t.Errorf("pf_update_step publishes %q with no description at all — %s", c.param, c.why)
			continue
		}
		for _, want := range c.wants {
			if !strings.Contains(desc, want) {
				t.Errorf("pf_update_step's %s description does not say %q — %s.\nGot: %s",
					c.param, want, c.why, desc)
			}
		}
	}

	// And the request. Both fields reach the server on a COMPLETED call, which is
	// what makes the drop the server's decision.
	seedStateFile(t, "wi_wire_ignored")
	body := driveUpdateStep(t, "wi_wire_ignored", map[string]any{
		"step_id": "code_change", "status": "completed", "step_attempt_id": "sa_ignored",
		"error_type": "gate_failed", "escalated": true,
	})
	if got := body["error_type"]; got != "gate_failed" {
		t.Errorf("body[\"error_type\"] = %#v on a completed call, want it forwarded. If this hop now "+
			"drops or refuses it, the published \"ignored (not refused)\" describes a decision that is no "+
			"longer the server's, and the card sentence about it is a statement about code that has moved",
			got)
	}
	if got := body["escalated"]; got != true {
		t.Errorf("body[\"escalated\"] = %#v on a completed call, want it forwarded — same reason", got)
	}
	if got, _ := body["status"].(string); got != "completed" {
		t.Fatalf("FLOOR: the request this arm reads is not a completed one (status = %q)", got)
	}
}
