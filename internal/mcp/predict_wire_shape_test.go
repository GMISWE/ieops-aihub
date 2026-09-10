package mcp_test

// aihub#543 probe wave 1 — the `docs/mcp-cards/pf_predict_conflicts.md` hop 2-3
// sentence about what leaves this process.
//
//	"`internal/mcp/tools_conflicts.go` (`registerConflictTools`) checks only that
//	 `declared_resources` is present and passes the **whole argument map** to
//	 `pkg/client/client.go` (`PredictConflicts`) → `POST /v1/conflicts/predict`,
//	 bound by `internal/server/router.go` (`handlePredictConflicts`)."
//
// 🔴 THREE CLAIMS, AND THE EXISTING GATES SEE NONE OF THEM.
// TestContractEveryPublishedParamLeavesTheProcess drives each PUBLISHED
// parameter and asserts it arrives, which is the presence direction over the
// published set. The card says something stronger and something weaker at once:
// stronger, that the map is forwarded WHOLE — so a key the schema does not
// publish is forwarded too, and a handler that started picking fields would be
// green everywhere while the sentence became false; weaker, that the ONLY
// local check is the presence of `declared_resources` — a second local check
// (a type check, an emptiness check, a project requirement) would turn calls
// the server answers today into calls that never reach it, and no arm would say
// so.
//
// The refusal half is asserted on the WIRE, not on the error text: "the tool
// returned an error" is also what a handler that POSTed and got a 400 back
// looks like, and the card's claim is that the check happens HERE.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestPredict -count=1

import (
	"net/http"
	"testing"
)

// predictWirePath is the route the card names. Asserted rather than assumed:
// the body checks below read the last request to this path, and a tool that
// POSTed somewhere else would leave them reading nothing.
const predictWirePath = "/v1/conflicts/predict"

// predictDeclaredResources is a minimal well-formed payload. The fake aihub
// does not validate it — what is under test is the forwarding, not the rules.
var predictDeclaredResources = []any{
	map[string]any{"type": "path", "uri": "file:internal/domain/conflicts.go", "intent": "read"},
}

// TestPredictForwardsTheWholeArgumentMapAndChecksOnlyForTheDeclaration is the
// hop 2-3 sentence, encoded as the three assertions it makes to a reader.
//
// MUTANTS (run against this tree; the verdict is what happened):
//
//	M18 enforcement: build the body from the four published names instead of
//	    passing `args`                       RED  forwards_an_unpublished_key
//	M19 enforcement: add `if _, ok := args["project"]; !ok { return errResult(…) }`
//	    to the handler                       RED  no_second_local_check/project
//	M20 enforcement: neuter the `declared_resources is required` guard so the call
//	    goes out anyway                      RED  refuses_a_missing_declaration_without_asking_the_server,
//	                                              on BOTH halves — the tool answered
//	                                              ok and the request was recorded
//	M21 enforcement: point client.PredictConflicts at another route
//	                                         RED  the path floor in predictBody
//	M27 publication: drop this arm's citation from the card sentence
//	                                         RED  K12 DEBT_GROWTH. ⚠️ This arm reads
//	                                              the wire, not the card, so the
//	                                              publication side is held by K12's
//	                                              citation binding
func TestPredictForwardsTheWholeArgumentMapAndChecksOnlyForTheDeclaration(t *testing.T) {
	t.Run("forwards_an_unpublished_key", func(t *testing.T) {
		f := newFakeAihub(t)
		args := map[string]any{
			"declared_resources": predictDeclaredResources,
			"work_item_id":       "aihub#543",
			"project":            "aihub",
			"dry_run":            true,
			// Not in the published schema. The card's phrase is "the whole
			// argument map", and this key is the only thing that can tell a
			// wholesale forward from a four-field copy that happens to agree.
			"aihub543_probe_key": "carried-verbatim",
		}
		body := predictBody(t, f, args)

		for key, want := range args {
			got, present := body[key]
			if !present {
				t.Errorf("the predict body carries no %q (it carries %v). The card says this hop "+
					"forwards the WHOLE argument map — a handler that copies named fields answers "+
					"identically for every published parameter and drops everything else, which is "+
					"the drift this sentence exists to deny.", key, sortedBodyKeys(body))
				continue
			}
			if key == "declared_resources" {
				continue // compared structurally below
			}
			if got != want {
				t.Errorf("the predict body carries %q = %#v, and the caller sent %#v. Forwarding is "+
					"wholesale here, so a value that changed on the way out is a value the server "+
					"answers about and the caller never sent.", key, got, want)
			}
		}

		entries, ok := body["declared_resources"].([]any)
		if !ok || len(entries) != len(predictDeclaredResources) {
			t.Fatalf("declared_resources arrived as %#v, want %d entry/entries — the required "+
				"parameter is the one thing this call is about, and a body without it makes every "+
				"assertion above a statement about a request nobody would send",
				body["declared_resources"], len(predictDeclaredResources))
		}
	})

	t.Run("no_second_local_check", func(t *testing.T) {
		// Each published parameter except declared_resources, omitted on its own.
		// The card says the presence check is the ONLY local one, so every one of
		// these must still reach the server.
		for _, omitted := range []string{"work_item_id", "project", "dry_run"} {
			t.Run(omitted, func(t *testing.T) {
				f := newFakeAihub(t)
				args := map[string]any{
					"declared_resources": predictDeclaredResources,
					"work_item_id":       "aihub#543",
					"project":            "aihub",
					"dry_run":            true,
				}
				delete(args, omitted)
				body := predictBody(t, f, args)
				if _, present := body[omitted]; present {
					t.Errorf("the predict body carries %q for a caller who did not send it. The map is "+
						"forwarded whole, so a key that appears from nowhere is a default this process "+
						"invented — and the server cannot tell it from one the caller chose.", omitted)
				}
			})
		}
	})

	t.Run("refuses_a_missing_declaration_without_asking_the_server", func(t *testing.T) {
		f := newFakeAihub(t)
		result, isErr := callTool(t, f, "pf_predict_conflicts", map[string]any{
			"project": "aihub",
			"dry_run": true,
		})
		if !isErr {
			t.Errorf("pf_predict_conflicts answered %v for a call with no declared_resources. The "+
				"parameter is the tool's only required one and the handler checks it before the "+
				"request is built.", result)
		}
		for _, c := range f.recorded() {
			if c.Path == predictWirePath {
				t.Errorf("the handler POSTed to %s anyway (%d request(s) recorded). The card's claim "+
					"is that this check happens HERE — an error that came back from the server looks "+
					"the same to a caller and is a different contract, because it costs a round trip "+
					"and depends on the server keeping a validation this tool promises locally.",
					predictWirePath, len(f.recorded()))
				break
			}
		}
	})
}

// predictBody drives one real pf_predict_conflicts against the fake aihub and
// returns the body the server received.
//
// The floor lives here: every assertion above is about a key in this body, and a
// call that never reached the server produces no body at all — which lastBodyFor
// refuses rather than answering with an empty map.
func predictBody(t *testing.T, f *fakeAihub, args map[string]any) map[string]any {
	t.Helper()
	f.on(predictWirePath, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{
			"severity": "info", "predictions": []any{}, "will_unlock": []any{},
		}
	})
	result, isErr := callTool(t, f, "pf_predict_conflicts", args)
	if isErr {
		t.Fatalf("pf_predict_conflicts failed with args %v: %v — a failed call reaches the server "+
			"with a body nobody should draw conclusions from", sortedBodyKeys(args), result)
	}
	return lastBodyFor(t, f, predictWirePath)
}
