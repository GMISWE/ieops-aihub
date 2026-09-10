package mcp_test

// aihub#543 probe wave 1 — the four `docs/mcp-cards/pf_claim_work_item.md`
// sentences about WHAT LEAVES THIS PROCESS, each encoded as the assertion the
// sentence already makes to a reader.
//
// The card's hop 2-3 section makes four claims no arm held before this file, and
// three of them are claims about ABSENCE or about ORDER, which is the shape the
// existing contract gates are structurally unable to see:
//
//	"`requested_locks` is forwarded verbatim when present; `force_takeover` only
//	 when true; `scenario_ref` only when non-empty."
//	    -> TestClaimForwardsAnOptionalParamOnlyWhenItCarriesAValue
//	"**`task_branches` is NO LONGER SENT** (`aihub#416`)."
//	    -> TestClaimBodyCarriesNoTaskBranches
//	"…so `claimTaskBranches` and its whole chain are gone along with the extra
//	 `GetWorkItem` round-trip they cost."
//	    -> TestClaimMakesNoWorkItemReadBeforeItClaims
//	"**`session_info.session_secret`.** Chosen BEFORE the request (protocol C6-2)
//	 and written to a partial state file first…"
//	    -> TestClaimPersistsTheSessionSecretBeforeTheServerSeesIt
//
// 🔴 WHY THE EXISTING WIRE GATE DOES NOT COVER ANY OF THEM.
// TestClaimEveryPublishedParamReachesTheWire drives each published parameter
// with a probe VALUE it chooses (`true` for a boolean, a non-empty string) and
// asserts the key ARRIVES. That is the presence direction, and aihub#452
// measured what it costs: G1 forces one branch with its own probe values, so a
// correctly-closed omission path was reported as PARAM_NOT_FORWARDED. The
// omission direction — `force_takeover: false` must put NO key in the body — is
// only observable by sending the other value, and nothing sent it. An absent key
// and a key carrying a false value are different requests: the server's
// isTakeover branch reads presence.
//
// Everything here is observed on the request the fake aihub really received. A
// source scan for the parameter's name would answer for the code rather than for
// the wire, and the one claim below that IS about the code (claimTaskBranches no
// longer existing) says so and is asserted separately.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestClaim -count=1

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

const claimWireWIID = "wi_01JCLAIMWIRESHAPE"

// claimWirePath is the route a claim POSTs to, which is also the only route the
// body assertions below may read: /repo_pins is a SECOND request with a body of
// its own, and asserting "task_branches is absent" against the wrong one of the
// two would be green for a reason that has nothing to do with the claim.
const claimWirePath = "/v1/work_items/" + claimWireWIID + "/claim"

// driveClaim runs one real pf_claim_work_item against a fake aihub in a real
// workspace and returns the claim body the server received plus every request
// the tool made, in order.
//
// The FLOOR is here rather than in each test: every assertion below is about a
// key that must be ABSENT, and an absent key is indistinguishable from a body
// that carries nothing at all. So the body is first checked to carry the two
// things the card says every claim carries — the `idempotency_key` and the
// `session_info` this process mints — and a body missing those fails here
// instead of silently satisfying every absence assertion in the file.
func driveClaim(t *testing.T, extra map[string]any) (map[string]any, []recordedCall) {
	t.Helper()
	newClaimWorkspace(t)

	f := newFakeAihub(t)
	f.on(claimWirePath, func(map[string]any) (int, any) {
		return http.StatusOK, claimResponse(claimWireWIID, "aihub#543", "aihub", "probe the claim wire")
	})

	args := map[string]any{
		"work_item_id":    claimWireWIID,
		"idempotency_key": "idem-wire-shape",
	}
	for k, v := range extra {
		args[k] = v
	}
	result, isErr := callTool(t, f, "pf_claim_work_item", args)
	if isErr {
		t.Fatalf("pf_claim_work_item failed with args %v: %v — a failed claim reaches the "+
			"server with a body nobody should draw conclusions from", args, result)
	}

	body := lastBodyFor(t, f, claimWirePath)
	if body["idempotency_key"] == nil {
		t.Fatalf("the claim body carries no idempotency_key (%v) — the walk is broken, and every "+
			"absence assertion in this file would pass against a body like this",
			sortedBodyKeys(body))
	}
	si, ok := body["session_info"].(map[string]any)
	if !ok || si["session_secret"] == "" || si["machine_id"] == "" {
		t.Fatalf("the claim body carries no complete session_info (%v) — the card says this hop "+
			"ADDS session_secret and machine_id, so a body without them is not the body these "+
			"tests are about", body["session_info"])
	}
	return body, f.recorded()
}

// TestClaimForwardsAnOptionalParamOnlyWhenItCarriesAValue is the omission
// direction of the card's hop 2-3 forwarding rule.
//
// Each parameter is driven BOTH ways, because one way is not a contract: a
// handler that always forwards satisfies the present half, a handler that never
// forwards satisfies the absent half, and only the pair says "only when".
//
// MUTANTS (run against this tree; the verdict is what happened, not what was
// expected):
//
//	M1 enforcement: `body["force_takeover"] = boolArg(args, "force_takeover")`
//	   — forward the parameter unconditionally, i.e. send `false`
//	                                              RED  force_takeover/omitted_when_false
//	M2 enforcement: `body["scenario_ref"] = strArg(args, "scenario_ref")`
//	                                              RED  scenario_ref/omitted_when_empty
//	M3 enforcement: drop the `if v, ok := args["requested_locks"]` forward
//	                                              RED  requested_locks/forwarded_verbatim
//	M4 publication: reword the card sentence to "`scenario_ref` is always
//	   forwarded", dropping its citation          RED  K12 DEBT_GROWTH — the
//	                                                   sentence stops citing an arm
//	                                                   and falls back into the debt
//	                                                   column. ⚠️ This probe does not
//	                                                   read the card, so the
//	                                                   publication side is held by
//	                                                   K12's citation binding rather
//	                                                   than by the arm itself
func TestClaimForwardsAnOptionalParamOnlyWhenItCarriesAValue(t *testing.T) {
	t.Run("force_takeover/omitted_when_false", func(t *testing.T) {
		body, _ := driveClaim(t, map[string]any{"force_takeover": false})
		if v, present := body["force_takeover"]; present {
			t.Errorf("the claim body carries force_takeover=%#v for a caller who sent false.\n"+
				"The server branches on the KEY being present, so forwarding an explicit false "+
				"turns every ordinary claim into a takeover request — and the card's \"only when "+
				"true\" would be describing a wire this process does not produce.", v)
		}
	})

	t.Run("force_takeover/present_when_true", func(t *testing.T) {
		body, _ := driveClaim(t, map[string]any{"force_takeover": true})
		if body["force_takeover"] != true {
			t.Errorf("the claim body carries force_takeover=%#v for a caller who sent true; the "+
				"takeover the caller asked for never leaves this process", body["force_takeover"])
		}
	})

	t.Run("scenario_ref/omitted_when_empty", func(t *testing.T) {
		body, _ := driveClaim(t, map[string]any{"scenario_ref": ""})
		if v, present := body["scenario_ref"]; present {
			t.Errorf("the claim body carries scenario_ref=%#v for a caller who sent the empty "+
				"string. An empty pin is not a pin: it would be recorded as the scenario SHA this "+
				"attempt ran against, which is provenance that names nothing.", v)
		}
	})

	t.Run("scenario_ref/present_when_set", func(t *testing.T) {
		const sha = "0f1e2d3c4b5a69788796a5b4c3d2e1f001234567"
		body, _ := driveClaim(t, map[string]any{"scenario_ref": sha})
		if body["scenario_ref"] != sha {
			t.Errorf("the claim body carries scenario_ref=%#v, want %q", body["scenario_ref"], sha)
		}
	})

	t.Run("requested_locks/forwarded_verbatim", func(t *testing.T) {
		sent := []any{
			map[string]any{"resource_type": "file_scope", "resource_key": "aihub:aihub:internal/mcp/tools_lifecycle.go"},
			map[string]any{"resource_type": "file_scope", "resource_key": "aihub:aihub:docs/mcp-cards/pf_claim_work_item.md"},
		}
		body, _ := driveClaim(t, map[string]any{"requested_locks": sent})
		got, ok := body["requested_locks"].([]any)
		if !ok {
			t.Fatalf("the claim body carries requested_locks=%#v, want the array the caller sent",
				body["requested_locks"])
		}
		// Verbatim, not merely present: a handler that re-derived the locks, or
		// dropped the second entry, or renamed a field would satisfy "the key is
		// there" while changing which rows the claim takes.
		if !reflect.DeepEqual(got, sent) {
			t.Errorf("requested_locks arrived as %#v, want %#v — \"forwarded verbatim\" is what "+
				"lets a caller who supplies locks explicitly predict which rows the claim takes",
				got, sent)
		}
	})

	t.Run("requested_locks/omitted_when_the_caller_sends_none", func(t *testing.T) {
		body, _ := driveClaim(t, nil)
		if v, present := body["requested_locks"]; present {
			t.Errorf("the claim body carries requested_locks=%#v for a caller who sent none. The "+
				"card tells callers to \"usually omit and let the server derive\", and an empty "+
				"array is not the same request as no array at all.", v)
		}
	})
}

// TestClaimBodyCarriesNoTaskBranches pins the card's "`task_branches` is NO
// LONGER SENT (`aihub#416`)".
//
// 🔴 An ABSENT wire key is exactly what the universal parameter gate cannot see:
// it quantifies over what the tool PUBLISHES, and `task_branches` was never a
// published parameter — it was computed here and added to the body. So the day
// somebody restores the aihub#356 machine "to be safe", every gate in this repo
// stays green and the card's sentence quietly becomes false.
//
// The whole serialised body is searched, not just its top level, because the key
// used to sit beside `session_info` and a re-introduction nested inside it would
// be just as real and would pass a top-level check.
//
// MUTANTS:
//
//	M5 enforcement: add `body["task_branches"] = map[string]any{"aihub": "x"}`
//	                                              RED  the top-level assertion
//	M6 enforcement: nest it under session_info instead
//	                                              RED  the serialised-body assertion
//	                                                   (the top-level one stays GREEN,
//	                                                    which is why both are here)
//	M7 publication: delete the sentence from the card entirely
//	                                              RED  K12 POPULATION_MOVED — one
//	                                                   candidate and one citation
//	                                                   leave together, which is the
//	                                                   swap the ledger records
//	                                                   Candidates and Cited to catch
func TestClaimBodyCarriesNoTaskBranches(t *testing.T) {
	body, _ := driveClaim(t, nil)

	if v, present := body["task_branches"]; present {
		t.Errorf("the claim body carries task_branches=%#v. aihub#416 retired the git_branch "+
			"derivation it fed, so the server has no consumer for it: this is a field computed "+
			"at a cost of one extra round-trip and read by nothing.", v)
	}

	blob, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal the claim body: %v", err)
	}
	if strings.Contains(string(blob), "task_branches") {
		t.Errorf("the serialised claim body mentions task_branches somewhere below the top "+
			"level: %s", blob)
	}
}

// TestClaimMakesNoWorkItemReadBeforeItClaims pins the second half of the same
// bullet — "`claimTaskBranches` and its whole chain are gone along with the
// extra `GetWorkItem` round-trip they cost".
//
// Two assertions because the sentence makes two claims, and the cheap one does
// not imply the expensive one: deleting the FUNCTION while some other call site
// kept reading the work item leaves the round-trip in place, and keeping the
// function while nothing calls it leaves the round-trip gone. The request census
// is the load-bearing half; the symbol census is what keeps the chain from being
// resurrected as dead code somebody later wires back up.
//
// MUTANTS:
//
//	M8  enforcement: call s.client.GetWorkItem(ctx, wiID) before the claim
//	                                              RED  the request census
//	M9  enforcement: re-declare `func claimTaskBranches(...)` in package mcp
//	                                              RED  the symbol census
//	M10 control: declare `claimTaskBranchesHelper` — a neighbouring name
//	                                              GREEN the census is bound to the
//	                                                    exact name the card states
//	                                                    rather than to a prefix
//	M32 publication: drop the citation from the card sentence, leaving the sentence
//	                                              RED  K12 DEBT_GROWTH
func TestClaimMakesNoWorkItemReadBeforeItClaims(t *testing.T) {
	_, calls := driveClaim(t, nil)

	// The claim's own route, and the repo-pin route the card describes as a
	// SECOND request, are the only two this tool is supposed to make.
	wiRead := "/v1/work_items/" + claimWireWIID
	for _, c := range calls {
		if c.Path == wiRead {
			t.Errorf("the claim made a %s %s before claiming. That is the GetWorkItem round-trip "+
				"aihub#416 removed: it existed to spell a git_branch lock key nothing derives any "+
				"more, so every claim would again pay a request for a value with no consumer.",
				c.Method, c.Path)
		}
	}
	if len(calls) == 0 {
		t.Fatal("the claim made no requests at all, so the census above checked nothing")
	}

	// The symbol half. Parsed rather than grepped: a mention inside a comment (this
	// file has several) is not a declaration, and a census that counted one would
	// be red on its own documentation.
	if decls := declaredInPackageMCP(t, "claimTaskBranches"); len(decls) > 0 {
		t.Errorf("package mcp declares claimTaskBranches again (%s). The card says the chain is "+
			"gone; a re-declared one is either dead code the next reader wires up, or a live "+
			"round-trip this test's request census would have to be told about.",
			strings.Join(decls, ", "))
	}
}

// declaredInPackageMCP returns the files of package mcp that declare a top-level
// name, so "the chain is gone" can be checked without a grep that a comment
// satisfies.
func declaredInPackageMCP(t *testing.T, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var found []string
	parsed := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", n), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		parsed++
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == name {
				found = append(found, n)
			}
		}
	}
	if parsed < 10 {
		t.Fatalf("only parsed %d source files of package mcp — the walk is broken, and a broken "+
			"walk declares every symbol absent", parsed)
	}
	return found
}

// TestClaimPersistsTheSessionSecretBeforeTheServerSeesIt pins the card's
// "Chosen BEFORE the request (protocol C6-2) and written to a partial state file
// first, because the server binds the attempt to the secret THIS request
// carries."
//
// 🔴 THE ORDER IS THE CLAIM, so the observation is made from INSIDE the server's
// handler: at the moment the request is being served, the state file must
// already hold the secret the request carries. Reading the file after callTool
// returns would be green for a process that wrote it afterwards — which is
// precisely the failure the C6-2 ordering exists to prevent, since a crash
// between the server's commit and a later local write leaves an attempt bound to
// a secret this machine never recorded.
//
// The read happens on the httptest handler's goroutine and is consumed on the
// test goroutine only after callTool has returned; the MCP call is synchronous,
// so the round trip has completed by then (the same pattern, and the same
// reasoning, as claim_committed_side_effect_test.go).
//
// MUTANTS:
//
//	M11 enforcement: move the config.WriteStateFile(partial) call to after
//	    s.client.ClaimWorkItem returns    RED  "no state file existed"
//	M12 enforcement: keep the pre-write but mint a second secret for the body
//	                                     RED  "the file holds a secret the server
//	                                           was never sent"
//	M13 publication: reword the card to "written to the state file once the claim
//	    returns", dropping the citation  RED  K12 DEBT_GROWTH
//	M14 control: stop reusing the recorded secret (`recordedClaimSecret` ->
//	    always mint fresh)               GREEN the replay behaviour is a different
//	                                           claim, held by
//	                                           claim_idempotent_replay_e2e_db_test.go;
//	                                           this probe is about the ORDER
func TestClaimPersistsTheSessionSecretBeforeTheServerSeesIt(t *testing.T) {
	newClaimWorkspace(t)

	var (
		served       bool
		secretOnDisk string
		claimedFlag  bool
		readErr      error
		bodySecret   string
	)

	f := newFakeAihub(t)
	f.on(claimWirePath, func(body map[string]any) (int, any) {
		served = true
		if si, ok := body["session_info"].(map[string]any); ok {
			bodySecret, _ = si["session_secret"].(string)
		}
		sf, err := config.ReadStateFile(claimWireWIID)
		if err != nil {
			readErr = err
		} else {
			secretOnDisk, claimedFlag = sf.SessionSecret, sf.Claimed
		}
		return http.StatusOK, claimResponse(claimWireWIID, "aihub#543", "aihub", "secret before the request")
	})

	result, isErr := callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id":    claimWireWIID,
		"idempotency_key": "idem-secret-order",
	})
	if isErr {
		t.Fatalf("pf_claim_work_item failed: %v", result)
	}
	if !served {
		t.Fatal("the fake never served the claim, so nothing was observed mid-request")
	}
	if readErr != nil {
		t.Fatalf("no state file existed while the server was answering the claim: %v.\n"+
			"C6-2 requires the secret to be persisted BEFORE the request, because the server "+
			"binds the attempt to the secret the request carries — written afterwards, any "+
			"failure in between leaves a live attempt whose credential this machine does not hold.",
			readErr)
	}
	if bodySecret == "" {
		t.Fatal("the claim request carried no session_info.session_secret, so there is nothing " +
			"for the state file to have matched")
	}
	if secretOnDisk != bodySecret {
		t.Errorf("mid-request the state file held session_secret %q while the request carried "+
			"%q — the file records a credential the server was never sent, which is aihub#392's "+
			"shape: every later credential-checked call answers \"invalid session_secret\".",
			redactSecret(secretOnDisk), redactSecret(bodySecret))
	}
	// The card says PARTIAL: the pre-write is the secret and the key, not a
	// record of a claim that has not happened yet. A file already marked claimed
	// would make a crashed claim look completed to the next session.
	if claimedFlag {
		t.Error("the pre-claim state file is already marked claimed while the server has not " +
			"answered yet, so a claim that fails leaves this machine believing it holds an attempt")
	}

	// The after side, without which "before" is not a measurement: the same file
	// must end up claimed, so the ordering above is a step in a claim that
	// completed rather than one that died.
	sf, err := config.ReadStateFile(claimWireWIID)
	if err != nil {
		t.Fatalf("read the state file after the claim: %v", err)
	}
	if !sf.Claimed || sf.SessionSecret != bodySecret {
		t.Errorf("after the claim the state file is claimed=%v with secret %q, want claimed=true "+
			"with the secret the request carried", sf.Claimed, redactSecret(sf.SessionSecret))
	}
}

// redactSecret keeps a failure message useful without printing a credential in
// full, which is the rule claim_response_projection_test.go applies to the same
// value.
func redactSecret(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:8] + "…"
}
