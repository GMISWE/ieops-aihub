package mcp_test

// aihub#543 probe wave 1, slice C — the `docs/mcp-cards/pf_complete_attempt.md`
// sentences about the NOTE ORDERING, about what leaves this process, and about
// what comes back.
//
//	"The description's second sentence is an ordering constraint rather than a
//	 convenience note: this call deletes the credentials `pf_emit_event` needs,
//	 so a note emitted afterwards cannot authenticate."
//	    -> TestPublishedNoteOrderingIsTheOrderTheToolUses
//	"**§6.1 T1-9** — the `note` ordering is published on the tool rather than
//	 left in a skill, because the hazard is invisible from the schema alone."
//	    -> TestPublishedNoteOrderingIsTheOrderTheToolUses
//	"**`status` and `force_terminate_step` are forwarded**; the three
//	 credentials come from the state file."
//	    -> TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote
//	"**`note` never reaches this endpoint.**…"
//	    -> TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote
//	"`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) forwards it
//	 without a second gate of its own, so there is no other refusal to hit."
//	    -> TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote
//	"`note` is the field that records on every status, and the refusal names it."
//	    -> TestTheRefusalNamesNoteAsTheFieldThatRecordsOnEveryStatus
//	"The server's completion result, plus `note_emitted` / `note_error` …, plus
//	 the worktree paths read out of the state file this call is about to delete
//	 — surfaced for all statuses…"
//	    -> TestCompleteAttemptResultCarriesTheWorktreesAndIsNotProjected
//
// 🔴 WHAT THE EXISTING ARMS DO AND DO NOT COVER. TestFusedNoteReachesTimeline
// BeforeTerminalCall holds the ENFORCED ordering — /v1/events, then /complete —
// and nothing compares it against the DESCRIPTION that publishes it, which is
// the entire point of T1-9: the hazard is invisible from the schema, so it was
// written into the prose, and prose with no arm over it is what this wave
// exists to find. TestTerminalToolsPublishNote is the other candidate and it
// checks only that `note` is published AS A STRING, which a description that
// said nothing about ordering would satisfy.
//
// The refusal arm next door (TestCompleteAttemptRefusesPauseReasonOnNonPaused
// Status) asserts the message's prefix and that it names the offending status.
// It does not assert that it names `note`, and naming the alternative is the
// half the card claims: a caller told only "not here" has nowhere to put the
// sentence it was trying to record.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedNoteOrdering|TestCompleteAttemptBody|TestTheRefusalNamesNote|TestCompleteAttemptResult' -count=1

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

const completeWIID = "wi_01JCOMPLETEATTEMPT"

const completeWirePath = "/v1/work_items/" + completeWIID + "/complete"

const notesPath = "/v1/events"

// completeAttemptCredentials is what the state file contributes to the body.
// The status and the flag are the caller's and are asserted separately, so this
// is the set that must be there whatever the caller sent.
var completeAttemptCredentials = []string{"attempt_id", "claim_epoch", "session_secret"}

// seedCompleteState writes a claimed state file carrying worktrees, in a fresh
// workspace. Terminal statuses delete it, so anything driving more than one
// status re-seeds between calls rather than sharing one file.
func seedCompleteState(t *testing.T, worktrees map[string]string) {
	t.Helper()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          completeWIID,
		Project:       "aihub",
		AttemptID:     "ra_complete",
		ClaimEpoch:    3,
		SessionSecret: "complete-secret",
		Claimed:       true,
		Worktrees:     worktrees,
	}); err != nil {
		t.Fatalf("write state file: %v", err)
	}
}

// TestPublishedNoteOrderingIsTheOrderTheToolUses is the T1-9 claim in both
// directions at once: the description states the ordering, and the tool obeys
// it, so neither can move without the other.
//
// The two halves are one arm on purpose. Split apart, "the prose says the note
// goes first" is satisfied by prose over a handler that emits it last, and "the
// note went first" is satisfied by a handler whose description never warned
// anybody — and T1-9's whole finding is that the second is what the caller is
// left with when the ordering lives in a skill instead of on the tool.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M18 enforcement: move the `if note != ""` emitNote block in
//	    internal/mcp/tools_lifecycle.go's pf_complete_attempt handler to AFTER
//	    the s.client.CompleteAttempt call
//	                                            RED  the_tool_emits_the_note_first
//	M19 publication: delete " — which is the only order that works, since this
//	    call deletes the credentials pf_emit_event needs" from the description
//	                                            RED  the_description_states_the
//	                                                 _ordering, on both the order
//	                                                 word and the reason
//	M20 publication: drop "before the attempt is completed" from the `note`
//	    property description                    RED  the_note_property_states_the
//	                                                 _ordering
//	M21 publication: delete the citation clause from the card's T1-9 bullet
//	                                            RED  K12 DEBT_GROWTH
//	G2  control:     reword the card's hop 0-1 ordering sentence, leaving the
//	    description and the handler alone      GREEN  this arm reads the live
//	                                                 session and the wire, not the
//	                                                 card; the card's text is K12's
//	                                                 half
func TestPublishedNoteOrderingIsTheOrderTheToolUses(t *testing.T) {
	tool := publishedTool(t, "pf_complete_attempt")

	t.Run("the_description_states_the_ordering", func(t *testing.T) {
		desc := tool.Description
		// pf_emit_event by name, because the hazard is not "notes are ordered" but
		// "the OTHER call you would have used stops working here", and a caller
		// cannot infer which call that is.
		if !strings.Contains(desc, "pf_emit_event") {
			t.Errorf("the pf_complete_attempt description never names pf_emit_event.\nT1-9's "+
				"finding is that the ordering hazard is invisible from the schema, so it is "+
				"published here rather than left in a skill — and the hazard is specifically that "+
				"pf_emit_event afterwards cannot authenticate. Description:\n%s", desc)
		}
		// One of these has to carry the ORDER. Checked as a set rather than as a
		// fixed phrase so a rewording that keeps the claim keeps the arm green,
		// while a rewording that drops it does not.
		if !containsAny(desc, "beforehand", "before", "only order") {
			t.Errorf("the pf_complete_attempt description states no ORDER between the note and "+
				"the completion.\nWithout it the fused `note` reads as a convenience — pass it if "+
				"you like — and a caller that emits its own note afterwards gets an "+
				"unauthenticated request against a deleted state file. Description:\n%s", desc)
		}
		if !containsAny(desc, "deletes the credentials", "credentials pf_emit_event needs") {
			t.Errorf("the pf_complete_attempt description states the ordering without its REASON. "+
				"A rule with no reason is one a caller reorders when it is inconvenient; the reason "+
				"is that this call deletes the credentials the other one needs. Description:\n%s", desc)
		}
	})

	t.Run("the_note_property_states_the_ordering", func(t *testing.T) {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal InputSchema: %v", err)
		}
		desc, ok := publishedParamDescription(t, schema, "note")
		if !ok {
			t.Fatalf("pf_complete_attempt publishes no `note` property — the card's hop 0-1 table " +
				"describes it as the closing note recorded BEFORE the attempt is completed")
		}
		if !strings.Contains(desc, "before the attempt is completed") {
			t.Errorf("the `note` property description is %q and does not say when the note is "+
				"recorded.\nThe card publishes that ordering on the PARAMETER as well as in the "+
				"tool description, because a caller reading one property's help text is the reader "+
				"most likely to be composing the two calls by hand", desc)
		}
	})

	t.Run("the_tool_emits_the_note_first", func(t *testing.T) {
		seedCompleteState(t, nil)
		f := newFakeAihub(t)

		result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
			"work_item_id": completeWIID,
			"status":       "wrapped",
			"note":         "wrapped: the ordering the description publishes",
			"derived":      []any{},
		})
		if isErr {
			t.Fatalf("pf_complete_attempt failed: %v", result)
		}

		paths := f.paths()
		// FLOOR: both hops must have happened, or "the note came first" is a
		// statement about a request that was never made.
		noteAt, completeAt := indexOfPath(paths, notesPath), indexOfPath(paths, completeWirePath)
		if noteAt < 0 || completeAt < 0 {
			t.Fatalf("the tool made %v; this arm needs both %s and %s to have happened, or the "+
				"ordering assertion is about nothing", paths, notesPath, completeWirePath)
		}
		if noteAt > completeAt {
			t.Errorf("the note landed after the completion (%v).\nThe completion deletes the "+
				"state file and with it the credentials the note event authenticates with, so a "+
				"note emitted second is a note the timeline never receives — which is the exact "+
				"hazard this tool's description publishes", paths)
		}
		if result["note_emitted"] != true {
			t.Errorf("note_emitted = %v, want true; the description ends by telling the caller "+
				"this key is how it finds out", result["note_emitted"])
		}
	})
}

// TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote is the card's
// hop 2-3 body claim: what is forwarded, what is not, and that nothing in this
// process refuses the flag before the server sees it.
//
// The absent-key half is what no existing gate reaches. TestContractEvery
// PublishedParamLeavesTheProcess drives each published parameter with a value
// it chose and asserts the key ARRIVES, so `note` — a published parameter that
// must NOT arrive on this route — is a violation from its point of view and
// lives in that gate's local-consumption table. That table records the fact;
// nothing observes it on the wire.
//
// MUTANTS:
//
//	M22 enforcement: add `body["note"] = note` to the pf_complete_attempt
//	    handler                                 RED  note_never_reaches_this
//	                                                 _endpoint
//	M23 enforcement: replace the `if boolArg(args, "force_terminate_step")`
//	    guard with an unconditional assignment  RED  the_flag_is_omitted_when
//	                                                 _false
//	M24 enforcement: add a local refusal — `if boolArg(args,
//	    "force_terminate_step") && status != "paused" { return errResult(...) }`
//	                                            RED  no_local_gate_refuses_the
//	                                                 _flag, on wrapped and failed
//	M25 publication: delete the citation clause from the card's forwarding bullet
//	                                            RED  K12 DEBT_GROWTH; this arm
//	                                                 reads the wire, so K12's
//	                                                 citation binding is its
//	                                                 publication side
func TestCompleteAttemptBodyForwardsTheFlagUngatedAndCarriesNoNote(t *testing.T) {
	drive := func(t *testing.T, args map[string]any) map[string]any {
		t.Helper()
		seedCompleteState(t, nil)
		f := newFakeAihub(t)
		full := map[string]any{"work_item_id": completeWIID}
		for k, v := range args {
			full[k] = v
		}
		result, isErr := callTool(t, f, "pf_complete_attempt", full)
		if isErr {
			t.Fatalf("pf_complete_attempt%v failed: %v — this arm is about the body a SUCCESSFUL "+
				"call sends", args, result)
		}
		body := lastBodyFor(t, f, completeWirePath)
		// FLOOR, per call: the credentials the state file contributes must be
		// there, or every absence assertion below is satisfied by an empty body.
		for _, k := range completeAttemptCredentials {
			if v, present := body[k]; !present || v == nil || v == "" {
				t.Fatalf("the complete body carries no usable %q (keys=%v) — the walk is broken",
					k, sortedBodyKeys(body))
			}
		}
		return body
	}

	t.Run("note_never_reaches_this_endpoint", func(t *testing.T) {
		body := drive(t, map[string]any{
			"status":  "wrapped",
			"note":    "wrapped: this text belongs on the timeline, not on the completion",
			"derived": []any{},
		})
		if v, present := body["note"]; present {
			t.Errorf("the complete body carries note=%#v.\nThe card draws the whole distinction "+
				"from pause_reason here: `note` becomes its own timeline event on the OTHER call, "+
				"whatever the status, while pause_reason is a column this endpoint reads on one "+
				"status. A note on this body is a second place the same sentence could be "+
				"recorded, and the two would not have to agree.", v)
		}
		want := append([]string{"status", "derived"}, completeAttemptCredentials...)
		sort.Strings(want)
		if got := sortedBodyKeys(body); len(got) != len(want) {
			t.Errorf("the complete body carries %v, want exactly %v for a call that supplied only "+
				"a status, a note and a derived list", got, want)
		}
	})

	t.Run("the_flag_is_forwarded_when_true", func(t *testing.T) {
		body := drive(t, map[string]any{"status": "wrapped", "force_terminate_step": true, "derived": []any{}})
		if body["force_terminate_step"] != true {
			t.Errorf("force_terminate_step = %#v, want true. It is the one parameter on this tool "+
				"that decides anything server-side on a terminal status, so a caller that sets it "+
				"and does not have it forwarded gets ErrConflictStepInProgress with no way to "+
				"proceed", body["force_terminate_step"])
		}
	})

	t.Run("the_flag_is_omitted_when_false", func(t *testing.T) {
		body := drive(t, map[string]any{"status": "wrapped", "force_terminate_step": false, "derived": []any{}})
		if v, present := body["force_terminate_step"]; present {
			t.Errorf("the complete body carries force_terminate_step=%#v on a call that sent "+
				"false.\nBoth directions are the claim: a handler that always forwards satisfies "+
				"\"forwarded\" and says nothing about \"forwarded\", and only the pair pins what "+
				"the card describes.", v)
		}
	})

	t.Run("no_local_gate_refuses_the_flag", func(t *testing.T) {
		// The flag is load-bearing on wrapped and failed alone — paused
		// force-terminates whatever the caller sent. So the combination most at
		// risk of a well-meaning local guard is exactly this one: the flag set on
		// a terminal status. The card says this process adds no second gate, and
		// the way to observe that is that the request happens at all.
		for _, status := range []string{"wrapped", "failed", "paused"} {
			t.Run(status, func(t *testing.T) {
				args := map[string]any{"status": status, "force_terminate_step": true}
				if status == "wrapped" {
					// aihub#350: a wrap without derived is refused at this hop,
					// before the request this arm exists to observe.
					args["derived"] = []any{}
				}
				body := drive(t, args)
				if body["status"] != status {
					t.Errorf("status = %#v, want %q", body["status"], status)
				}
				if body["force_terminate_step"] != true {
					t.Errorf("force_terminate_step = %#v on status=%q, want true — a refusal or a "+
						"quiet drop here would be a second gate, and the card tells a caller there "+
						"is no other refusal to hit", body["force_terminate_step"], status)
				}
			})
		}
	})
}

// TestTheRefusalNamesNoteAsTheFieldThatRecordsOnEveryStatus is the card's
// closing sentence on the pause_reason guard, and both halves are load-bearing.
//
// "The refusal names it" is the part with a reader: a caller that sent
// pause_reason on a wrap is trying to record a sentence, and a refusal that
// only says where the sentence cannot go leaves it with nowhere to put it. The
// "records on every status" half is what makes naming `note` honest rather than
// a redirection to a field with its own status rule.
//
// MUTANTS:
//
//	M26 enforcement: drop "; drop pause_reason or use note, which is recorded on
//	    every status" from the handler's refusal message
//	                                            RED  the_refusal_names_note
//	M27 enforcement: guard emitNote with `if note != "" && status == "wrapped"`
//	                                            RED  a_note_records_on_every
//	                                                 _status/failed and /paused
//	M28 publication: delete the citation clause from the card's closing sentence
//	                                            RED  K12 DEBT_GROWTH
func TestTheRefusalNamesNoteAsTheFieldThatRecordsOnEveryStatus(t *testing.T) {
	t.Run("the_refusal_names_note", func(t *testing.T) {
		for _, status := range []string{"wrapped", "failed"} {
			t.Run(status, func(t *testing.T) {
				seedCompleteState(t, nil)
				f := newFakeAihub(t)
				result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
					"work_item_id": completeWIID,
					"status":       status,
					"pause_reason": "the caller had something to record",
				})
				if !isErr {
					t.Fatalf("pf_complete_attempt(status=%q, pause_reason=…) succeeded: %v — the "+
						"aihub#452 guard refuses that combination", status, result)
				}
				text, _ := result["_raw"].(string)
				if text == "" {
					t.Fatalf("the refusal carried no text: %v", result)
				}
				if !strings.Contains(text, "note") {
					t.Errorf("the refusal is %q and never names `note`.\nThe caller sent a sentence "+
						"it wanted recorded; refusing the field without naming the one that records "+
						"on every status turns a redirect into a dead end, and the field it was "+
						"reaching for is the one this tool already publishes.", text)
				}
			})
		}
	})

	t.Run("a_note_records_on_every_status", func(t *testing.T) {
		for _, status := range []string{"wrapped", "failed", "paused"} {
			t.Run(status, func(t *testing.T) {
				seedCompleteState(t, nil)
				f := newFakeAihub(t)
				args := map[string]any{
					"work_item_id": completeWIID,
					"status":       status,
					"note":         status + ": recorded whatever the status",
				}
				if status == "wrapped" {
					// aihub#350: a wrap without derived is refused before the
					// note this arm counts is ever emitted.
					args["derived"] = []any{}
				}
				result, isErr := callTool(t, f, "pf_complete_attempt", args)
				if isErr {
					t.Fatalf("pf_complete_attempt(status=%q, note=…) failed: %v", status, result)
				}
				if indexOfPath(f.paths(), notesPath) < 0 {
					t.Errorf("status=%q emitted no note event (%v).\nThe refusal above tells a "+
						"caller to use `note` BECAUSE it records on every status; a status that "+
						"silently drops it makes that advice false exactly where the caller was "+
						"sent", status, f.paths())
				}
				if result["note_emitted"] != true {
					t.Errorf("status=%q reported note_emitted=%v, want true", status, result["note_emitted"])
				}
			})
		}
	})
}

// TestCompleteAttemptResultCarriesTheWorktreesAndIsNotProjected is the card's
// hop 5, which makes two claims a caller acts on: the worktree paths are
// surfaced for ALL statuses out of a file this call is about to delete, and
// there is no slim function, so the server's own keys reach the model as sent.
//
// The worktrees half has one reader and one moment: after a terminal
// completion, the state file is gone, so a caller that did not get the paths in
// this response cannot get them at all. "For all statuses" is the part a
// narrowing would silently break — a guard added for paused would leave the
// wrap path, which is where the paths are actually needed, answering nothing.
//
// MUTANTS:
//
//	M29 enforcement: move the addWorktrees call inside `if status == "paused"`
//	                                            RED  worktrees_for_every_status
//	                                                 /wrapped and /failed
//	M30 enforcement: replace jsonResult(result) with a projection copying only
//	    ok/worktrees/note_emitted               RED  no_slim_function
//	M31 publication: delete the citation clause from the card's hop 5 paragraph
//	                                            RED  K12 DEBT_GROWTH
func TestCompleteAttemptResultCarriesTheWorktreesAndIsNotProjected(t *testing.T) {
	const serverOnlyKey = "closed_at"
	const worktreePath = "/tmp/pf.aihub-543/aihub"

	drive := func(t *testing.T, status string) map[string]any {
		t.Helper()
		seedCompleteState(t, map[string]string{"aihub": worktreePath})
		f := newFakeAihub(t)
		f.on(completeWirePath, func(map[string]any) (int, any) {
			// A key the server sends and no local struct declares. If anything
			// projected this response it would be dropped here.
			return http.StatusOK, map[string]any{"ok": true, serverOnlyKey: "2026-09-10T00:00:00Z"}
		})
		args := map[string]any{
			"work_item_id": completeWIID,
			"status":       status,
		}
		if status == "wrapped" {
			// aihub#350: a wrap without derived is refused before any request.
			args["derived"] = []any{}
		}
		result, isErr := callTool(t, f, "pf_complete_attempt", args)
		if isErr {
			t.Fatalf("pf_complete_attempt(status=%q) failed: %v", status, result)
		}
		return result
	}

	t.Run("worktrees_for_every_status", func(t *testing.T) {
		for _, status := range []string{"wrapped", "failed", "paused"} {
			t.Run(status, func(t *testing.T) {
				result := drive(t, status)
				wt, ok := result["worktrees"].(map[string]any)
				if !ok {
					t.Fatalf("status=%q returned worktrees=%#v, want an object.\nOn a terminal "+
						"status the state file these paths came from is deleted by this very call, "+
						"so a caller that does not receive them here has no second chance",
						status, result["worktrees"])
				}
				if wt["aihub"] != worktreePath {
					t.Errorf("status=%q returned worktrees=%v, want aihub -> %q", status, wt, worktreePath)
				}
			})
		}
	})

	t.Run("no_slim_function", func(t *testing.T) {
		result := drive(t, "wrapped")
		if _, present := result[serverOnlyKey]; !present {
			t.Errorf("the server sent %q and the result does not carry it (keys=%v).\nThe card "+
				"says there is no slim function on this tool: the completion result reaches the "+
				"model as the server sent it, plus the note outcome and the worktrees. A "+
				"projection here would drop a field the server added without anybody editing this "+
				"tool.", serverOnlyKey, sortedBodyKeys(result))
		}
	})
}

// containsAny reports whether s contains any of the alternatives. Used where
// the claim is a property of the sentence rather than one phrasing of it, so a
// reword that keeps the claim keeps the arm green.
func containsAny(s string, alternatives ...string) bool {
	for _, a := range alternatives {
		if strings.Contains(s, a) {
			return true
		}
	}
	return false
}

// indexOfPath returns the position of the first request to path, or -1. The
// POSITION rather than a boolean, because two of the claims here are about
// order and a membership test cannot express one.
func indexOfPath(paths []string, path string) int {
	for i, p := range paths {
		if p == path {
			return i
		}
	}
	return -1
}
