package mcp_test

// aihub#543 probe wave 2 — the pf_get_step claims that live entirely on this
// side of the wire: what the description promises, what leaves the process, and
// what comes back unprojected.
//
//	"the description makes six promises: …"
//	    -> TestPublishedGetStepPromisesAreTheSixTheCardNames
//	"`registerStepTools` validates the argument is non-empty and calls `GetStep`,
//	 which issues `GET /v1/work_items/<id>/step`"
//	    -> TestGetStepValidatesTheIdBeforeIssuingOneGet
//	"`completed_steps: []` and the key being absent are different answers"
//	    -> TestGetStepDoesNotNormaliseAnAbsentHistory
//	"passed through by `jsonResult` with no projection … every key the server
//	 sends reaches the model"
//	    -> TestGetStepForwardsEveryKeyTheServerSends
//	"`current_step_attempt` and `scenario_ref` … are carried, not promised"
//	    -> TestGetStepCarriesTwoKeysTheDescriptionDoesNotPromise
//
// ⚠️ The card's slug sentence is NOT held here. Its corrected halves are held by
// internal/server/get_step_history_source_test.go (the echo and the events
// filter) and internal/domain/card_claims_wave2_test.go (the recall filter),
// beside the two DB-gated arms those name. The pf_get_step DESCRIPTION still
// carries the stale "return nothing for a slug" advice; that is product text
// outside this card's scope and is reported for a follow-up rather than pinned
// here — a probe on today's wording would arrive red on the day it is fixed.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestGetStep|TestPublishedGetStep' -count=1

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/server"
)

// getStepPath is the only route pf_get_step reads.
func getStepPath(wiID string) string { return "/v1/work_items/" + wiID + "/step" }

// driveGetStep runs the registered pf_get_step against a fake aihub that answers
// with `payload`, and returns the decoded tool result plus the fake's request log.
func driveGetStep(t *testing.T, wiID string, payload map[string]any) (map[string]any, []string) {
	t.Helper()
	f := newFakeAihub(t)
	f.on(getStepPath(wiID), func(map[string]any) (int, any) {
		return http.StatusOK, payload
	})
	got, isErr := callTool(t, f, "pf_get_step", map[string]any{"work_item_id": wiID})
	if isErr {
		t.Fatalf("pf_get_step returned an error result: %v — this is a read with no credentials, so "+
			"a refusal here is not the case under test", got)
	}
	return got, f.paths()
}

// getStepSixPromises is the card's own enumeration, each paired with the words
// of the description that carry it.
//
// The pairing is explicit rather than a bag of substrings: the card says the
// description makes SIX promises and names them, so an arm that only checked
// "some of these words are present" could stay green while one promise had been
// dropped and another reworded.
var getStepSixPromises = []struct {
	Promise string
	Words   []string
}{
	{"authoritative and unique", []string{"AUTHORITATIVE", "the only one"}},
	{"completed_steps is the history, oldest first, retries included",
		[]string{"completed_steps", "oldest", "retries included"}},
	{"a resuming agent should call it FIRST", []string{"call this FIRST"}},
	{"only an entry whose status is completed means that step is done",
		[]string{"count only entries whose status", "completed"}},
	{"a slug is accepted and the canonical id is echoed",
		[]string{"slug or a canonical id", "echoes the canonical one"}},
	{"there is no step graph here", []string{"No step graph here"}},
}

// TestPublishedGetStepPromisesAreTheSixTheCardNames holds the card's hop 0-1
// sentence against the schema a real session publishes.
//
// internal/mcp/tools_step_contract_test.go already holds this description in
// two other directions: every response FIELD it names is a bound key on
// server.StepState, and four phrases that name no field at all are required by
// getStepRequiredPhrases. Neither covers the card's count-and-list claim: two of
// the six promises (authoritative-and-unique, and the slug echo) are named by
// nothing there, and a promise dropped from the description is invisible to a
// guard built on the promises that remain.
//
// ⚠️ What this cannot see is a SEVENTH promise arriving. The card's "six" is a
// statement about the description's whole content, and an arm keyed on a list
// cannot detect an addition; that is the review half's job
// (polyforge-scenario#20) and K11's stated limit.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M49 enforcement: delete "and the only one" from the description
//	                                        RED  (authoritative and unique)
//	M50 enforcement: replace the slug clause with "Pass a canonical id;"
//	                                        RED  (the slug-and-echo promise)
//	P4  publication: make every citation in the card unresolvable  RED  K12
func TestPublishedGetStepPromisesAreTheSixTheCardNames(t *testing.T) {
	desc := publishedTool(t, "pf_get_step").Description
	if len(desc) < 200 {
		t.Fatalf("pf_get_step's published description is %d character(s): %q. Six promises do not "+
			"fit in that, so the assertions below would be reporting an empty schema rather than a "+
			"dropped promise", len(desc), desc)
	}
	if len(getStepSixPromises) != 6 {
		t.Fatalf("this arm enumerates %d promise(s) and the card says six; the two have to move "+
			"together or the count in the card is unheld", len(getStepSixPromises))
	}
	for _, p := range getStepSixPromises {
		for _, w := range p.Words {
			if !strings.Contains(desc, w) {
				t.Errorf("the pf_get_step description no longer carries %q, which is how it makes the "+
					"promise the card lists as %q.\n  got: %s\nEvery promise here is one a resuming "+
					"agent acts on; dropping one changes what an agent does, not merely what it reads.",
					w, p.Promise, desc)
			}
		}
	}
}

// TestGetStepValidatesTheIdBeforeIssuingOneGet holds the card's hop 2-3
// sentence: the argument is validated here, and the value travels as a path
// segment on one GET.
//
// The refusal arm is the one with no substitute. An empty work_item_id would
// otherwise reach `GET /v1/work_items//step`, which is a different route (or a
// 404) and reads to a caller as "no such work item" rather than as their own
// missing argument. The absence of a request is asserted, not the error text:
// the text is the same either way.
//
// MUTANTS:
//
//	M51 enforcement: delete the `if wiID == ""` guard   RED  no_request_without
//	                                                         _an_id
//	M52 enforcement: append the id as a query parameter rather than a path
//	    segment (in pkg/client)                         RED  one_get_to_the_step
//	                                                         _path
//	M53 enforcement: issue POST instead of GET          RED  one_get_to_the_step
//	                                                         _path
//	P4  publication: make every citation in the card unresolvable  RED  K12
func TestGetStepValidatesTheIdBeforeIssuingOneGet(t *testing.T) {
	t.Run("no_request_without_an_id", func(t *testing.T) {
		f := newFakeAihub(t)
		got, isErr := callTool(t, f, "pf_get_step", map[string]any{"work_item_id": ""})
		if !isErr {
			t.Errorf("pf_get_step accepted an empty work_item_id and answered %v", got)
		}
		if paths := f.paths(); len(paths) != 0 {
			t.Errorf("pf_get_step sent %v for an empty work_item_id. The card says this process "+
				"validates the argument; a request to /v1/work_items//step is answered by the "+
				"router, and its 404 tells the caller the work item does not exist rather than "+
				"that they sent nothing.", paths)
		}
	})

	t.Run("one_get_to_the_step_path", func(t *testing.T) {
		const wiID = "wi_getstep_path"
		f := newFakeAihub(t)
		f.on(getStepPath(wiID), func(map[string]any) (int, any) {
			return http.StatusOK, map[string]any{"work_item_id": wiID, "current_step_status": "idle"}
		})
		if _, isErr := callTool(t, f, "pf_get_step", map[string]any{"work_item_id": wiID}); isErr {
			t.Fatal("pf_get_step failed against a fake that answers the step path")
		}
		calls := f.recorded()
		if len(calls) != 1 {
			t.Fatalf("pf_get_step made %d request(s) %v, want exactly one — the card describes a "+
				"single GET, and a second call is a round trip nobody published", len(calls),
				f.paths())
		}
		if calls[0].Method != http.MethodGet || calls[0].Path != getStepPath(wiID) {
			t.Errorf("pf_get_step sent %s %s, want GET %s. The id travels as a PATH SEGMENT, which "+
				"is why the card says there is no forwarding table for it to be dropped from — as "+
				"a query parameter or a body field it could be, silently.",
				calls[0].Method, calls[0].Path, getStepPath(wiID))
		}
	})
}

// TestGetStepDoesNotNormaliseAnAbsentHistory holds "`completed_steps: []` and
// the key being absent are different answers" on the side a caller sees them.
//
// The server half — that an empty history is `[]` and never null, so `[]`
// really does mean "nothing has completed" — is held with a database by
// internal/server/routes_step_dbgated_test.go
// (TestHandleGetStep_EmptyHistoryIsAnEmptyArrayNeverNull). This arm holds that
// this process keeps the two apart: a client that filled the key in, or dropped
// an empty one, would collapse the distinction the description insists on and
// the collapse would be invisible from either end alone.
//
// ⚠️ The absent case is only reachable from a peer older than aihub#265. That
// is what the fake stands in for, and it is the one shape a live server cannot
// produce.
//
// MUTANTS:
//
//	M54 enforcement: default the key in the tool handler when the server omits
//	    it (`if _, ok := result["completed_steps"]; !ok { … = []any{} }`)
//	                                        RED  an_absent_key_stays_absent
//	M55 enforcement: delete an empty completed_steps before answering
//	                                        RED  an_empty_array_stays_empty
//	P4  publication: make every citation in the card unresolvable  RED  K12
func TestGetStepDoesNotNormaliseAnAbsentHistory(t *testing.T) {
	t.Run("an_empty_array_stays_empty", func(t *testing.T) {
		got, _ := driveGetStep(t, "wi_hist_empty", map[string]any{
			"work_item_id": "wi_hist_empty", "current_step_status": "idle",
			"completed_steps": []any{},
		})
		entries, present := got["completed_steps"]
		if !present {
			t.Fatalf("the server sent completed_steps: [] and the tool answered without the key "+
				"(%v). Empty means nothing has completed; absent means the peer predates "+
				"aihub#265, and a client that turns the first into the second invents an old "+
				"server", sortedKeysOfAny(got))
		}
		if list, ok := entries.([]any); !ok || len(list) != 0 {
			t.Errorf("completed_steps came back as %#v, want an empty array", entries)
		}
	})

	t.Run("an_absent_key_stays_absent", func(t *testing.T) {
		got, _ := driveGetStep(t, "wi_hist_absent", map[string]any{
			"work_item_id": "wi_hist_absent", "current_step_status": "idle",
		})
		if _, present := got["completed_steps"]; present {
			t.Errorf("the server omitted completed_steps and the tool answered with it (%#v). The "+
				"description tells callers those are NOT the same answer: filling the key in "+
				"reports \"nothing has completed\" for a server that cannot answer the question at "+
				"all, which is the aihub#265 defect from the other direction.",
				got["completed_steps"])
		}
		if got["current_step_status"] != "idle" {
			t.Fatalf("the tool did not forward the rest of the payload either (%v) — the absence "+
				"above would then be a lost response rather than a preserved distinction",
				sortedKeysOfAny(got))
		}
	})
}

// TestGetStepForwardsEveryKeyTheServerSends holds "this tool has no slim
// function, so every key the server sends reaches the model".
//
// The instrument is a key nothing in this process knows about, for the reason
// aihub#419's G3 arm gives: `items`-style known keys arrive under a keep-list
// too, so only an unknown one distinguishes a pass-through from a projection
// that happens to be complete today.
//
// MUTANTS:
//
//	M56 enforcement: project the result to a four-key keep-list  RED
//	P7  publication: rename this arm        RED  K12 ARM_CITATION_UNRESOLVED
func TestGetStepForwardsEveryKeyTheServerSends(t *testing.T) {
	got, _ := driveGetStep(t, "wi_passthrough", map[string]any{
		"work_item_id":        "wi_passthrough",
		"current_step_status": "in_progress",
		"repo_pins":           map[string]any{"aihub": strings.Repeat("a", 40)},
		// Neither the struct nor any card lists this one.
		"a_key_no_slim_function_lists": "survives-only-without-a-projection",
	})
	if got["a_key_no_slim_function_lists"] != "survives-only-without-a-projection" {
		t.Errorf("pf_get_step dropped a key the server sent (answer keys: %v). The card says every "+
			"key reaches the model, and that is what makes the corpus record above it a record of "+
			"the SERVER's answer rather than of this process's allowlist.", sortedKeysOfAny(got))
	}
	if got["repo_pins"] == nil {
		t.Error("repo_pins did not arrive either; it is a real StepState field (aihub#416), and its " +
			"loss would make the assertion above about an unknown key alone")
	}
}

// TestGetStepCarriesTwoKeysTheDescriptionDoesNotPromise holds "`current_step_attempt`
// and `scenario_ref` appear there and are not named in the description; they are
// carried, not promised".
//
// Both halves, in opposite directions: the struct must bind them (carried) and
// the description must not name them (not promised). The corpus half — that they
// really do appear in real answers — is K10's, the live-response-keys arm in
// card_response_keys_live_e2e_db_test.go.
//
// This is the arm that goes red if somebody "documents" one of the two, which is
// the likely direction: naming a field in the description turns it into a
// promise, and tools_step_contract_test.go's getStepAdvertised table is then the
// place that has to change.
//
// MUTANTS:
//
//	M57 enforcement: append "Also returns scenario_ref." to the description
//	                                                     RED  (not promised)
//	M58 enforcement: change CurrentStepAttempt's tag to `json:"-"`
//	                                                     RED  (carried)
//	P4  publication: make every citation in the card unresolvable  RED  K12
func TestGetStepCarriesTwoKeysTheDescriptionDoesNotPromise(t *testing.T) {
	desc := publishedTool(t, "pf_get_step").Description
	bound := boundJSONKeys(t, server.StepState{})
	if len(bound) < 5 {
		t.Fatalf("server.StepState binds %d json key(s); the reflection walk is broken and the "+
			"carried half below would fail for the wrong reason", len(bound))
	}
	for _, key := range []string{"current_step_attempt", "scenario_ref"} {
		if !bound[key] {
			t.Errorf("server.StepState binds no %q, so the card's \"carried\" is false — a key that "+
				"is neither promised nor carried is simply gone, and the corpus record above the "+
				"card would be describing a response that no longer exists", key)
		}
		if strings.Contains(desc, key) {
			t.Errorf("pf_get_step's description names %q, so it is now PROMISED rather than carried. "+
				"That is a contract change: tools_step_contract_test.go's getStepAdvertised is "+
				"where a promise is recorded, and this card's sentence has to move with it.", key)
		}
	}
}
