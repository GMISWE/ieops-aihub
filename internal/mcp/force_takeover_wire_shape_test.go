package mcp_test

// aihub#543 probe wave 1 — `docs/mcp-cards/pf_force_takeover.md`'s hop 2-3 claim
// about what does NOT leave this process:
//
//	"**No `task_branches` are sent**, and since `aihub#416` neither does a claim
//	 — the whole mechanism is gone with the `git_branch` derivation it keyed."
//	    -> TestForceTakeoverBodyCarriesNoTaskBranches   (this tool's half)
//	    -> TestClaimBodyCarriesNoTaskBranches           (the claim's half, aihub#543
//	                                                     slice 1)
//	    -> TestResourceToLock_RepoAndServiceDeriveNoLock (the derivation the
//	                                                     mechanism keyed)
//
// 🔴 An ABSENT wire key is what the universal parameter gate is structurally
// unable to see: it quantifies over what a tool PUBLISHES, and `task_branches`
// was never a published parameter of either tool — it was computed in-process and
// added to the body. So the day somebody restores the aihub#356 machine on this
// route "for symmetry with the claim", every gate in this repo stays green and
// the card's sentence quietly becomes false.
//
// The claim path got its own arm in slice 1 and this one had none, which is the
// asymmetry worth naming: the two handlers are registered in the same file,
// twenty lines apart, and the sentence is about both.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestForceTakeoverBodyCarriesNoTaskBranches -count=1

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	// ftWireWIID is the canonical work_items.id the takeover is addressed by.
	ftWireWIID = "wi_01JFTWIRESHAPE"
	// ftWirePath is the only route a takeover POSTs to, and therefore the only one
	// the body assertions below may read.
	ftWirePath = "/v1/work_items/" + ftWireWIID + "/force_takeover"
)

// driveForceTakeover runs one real pf_force_takeover against a fake aihub in an
// isolated workspace and returns the body the server received.
//
// The FLOOR lives here rather than in the test, for the reason driveClaim states
// for its own: every assertion below is about a key that must be ABSENT, and an
// absent key is indistinguishable from a body that carries nothing at all. So the
// body is first checked to carry the two things the card says this hop builds —
// `reason` and the `session_info` this process mints — and a body missing either
// fails here instead of silently satisfying the absence assertion.
func driveForceTakeover(t *testing.T) map[string]any {
	t.Helper()
	newResolveWorkspace(t)

	f := newFakeAihub(t)
	f.on(ftWirePath, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{
			"ok":              true,
			"id":              ftWireWIID,
			"slug":            "aihub#543",
			"project":         "aihub",
			"new_attempt_id":  "ra_ftwire",
			"new_claim_epoch": float64(3),
		}
	})

	result, isErr := callToolBounded(t, f, "pf_force_takeover", map[string]any{
		"work_item_id": ftWireWIID,
		"reason":       "aihub#543 wire-shape probe",
	}, 20*time.Second)
	if isErr {
		t.Fatalf("pf_force_takeover failed: %v — a failed takeover reaches the server with a "+
			"body nobody should draw conclusions from", result)
	}

	body := lastBodyFor(t, f, ftWirePath)
	if s, _ := body["reason"].(string); strings.TrimSpace(s) == "" {
		t.Fatalf("the takeover body carries no reason (keys: %v) — the walk is broken, and the "+
			"absence assertion below would pass against a body like this", sortedBodyKeys(body))
	}
	si, ok := body["session_info"].(map[string]any)
	if !ok || si["session_secret"] == "" || si["machine_id"] == "" {
		t.Fatalf("the takeover body carries no complete session_info (%v) — the card says this "+
			"hop ADDS session_secret and machine_id, so a body without them is not the body this "+
			"test is about", body["session_info"])
	}
	return body
}

// TestForceTakeoverBodyCarriesNoTaskBranches pins this tool's half of the card's
// "**No `task_branches` are sent**".
//
// The whole serialised body is searched, not only its top level, because on the
// claim route the key used to sit beside `session_info` and a re-introduction
// nested inside it would be just as real and would pass a top-level check.
//
// MUTANTS (run against this tree; the verdict is what happened, not what was
// expected):
//
//	M7  enforcement: add `body["task_branches"] = map[string]any{"aihub": "x"}`
//	    to the pf_force_takeover handler        RED  the top-level assertion
//	M8  enforcement: nest it inside session_info instead
//	                                            RED  the serialised-body assertion
//	                                                 (the top-level one stays GREEN,
//	                                                  which is why both are here)
//	M9  enforcement: stop sending session_info altogether
//	                                            RED  the floor in driveForceTakeover
//	M10 publication: delete the sentence from the card
//	                                            RED  K12 — one candidate and one
//	                                                 citation leave together, which
//	                                                 is the swap the ledger records
//	                                                 Candidates and Cited to catch
func TestForceTakeoverBodyCarriesNoTaskBranches(t *testing.T) {
	body := driveForceTakeover(t)

	if v, present := body["task_branches"]; present {
		t.Errorf("the force_takeover body carries task_branches=%#v. aihub#416 retired the "+
			"git_branch derivation it fed, so the server has no consumer for it: this is a "+
			"field computed at a cost the caller pays and read by nothing.", v)
	}

	blob, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal the takeover body: %v", err)
	}
	if strings.Contains(string(blob), "task_branches") {
		t.Errorf("the serialised force_takeover body mentions task_branches somewhere below the "+
			"top level: %s", blob)
	}
}
