package mcp_test

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_create_work_item.md` sentences
// about what leaves this process, driven end to end against a fake aihub.
//
//	"The handler checks `project` and `goal` locally, applies
//	 `applyForceReasonDefault` …, and passes the **whole argument map** to
//	 `pkg/client/client.go` (`CreateWorkItem`) -> `POST /v1/work_items`…"
//	    -> TestCreateForwardsEveryPublishedPropertyVerbatim
//	    -> TestCreateRefusesAMissingProjectOrGoalWithoutSendingAnything
//	    -> TestCreateSuppliesAForceReasonWheneverForceCreateIsSet
//	"`aihub#463` measured it on go-sdk v1.6.0 …: … polyforge registers through
//	 the untyped `(*mcp.Server).AddTool`, whose `callTool` hands the request
//	 straight to the handler with no schema step."
//	    -> TestCreatePublishedEnumsDoNotRefuseInProcess
//	"Here the SDK **does** enforce `priority` and `source`, which is the
//	 difference."  <- MEASURED FALSE 2026-09-10, corrected on the card by the
//	                  same probe
//	    -> TestCreatePublishedEnumsDoNotRefuseInProcess
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestCreateForwardsEveryPublished|TestCreateRefusesAMissing|TestCreateSuppliesAForceReason|TestCreatePublishedEnums' -count=1 -v

import (
	"net/http"
	"strings"
	"testing"
)

// createWirePath is the single route every create — batched or not — posts to.
const createWirePath = "/v1/work_items"

// driveCreate calls pf_create_work_item through a real client session against a
// fake aihub and hands back the request bodies it saw.
//
// It returns the CALLS rather than the parsed body of the first one, because
// several arms below are about how many requests were made — nought for a local
// refusal, one per item for a batch — and a helper that projected to one body
// could not express that.
func driveCreate(t *testing.T, args map[string]any) ([]recordedCall, map[string]any, bool) {
	t.Helper()
	f := newFakeAihub(t)
	f.on(createWirePath, func(body map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"id": "wi_probe01", "slug": "aihub#1", "goal": body["goal"]}
	})
	result, isErr := callTool(t, f, createToolName, args)
	return f.recorded(), result, isErr
}

// createProbeArgs is one value per published property, each distinguishable from
// every other so a forwarding table that dropped or transposed a field is
// visible in the failure rather than only in a count.
//
// 🔴 Built from the LIVE published set below, not from this literal alone: the
// arm asserts that every key the schema publishes has an entry here, so a
// seventeenth parameter added to the tool makes this map incomplete and the test
// red rather than silently unforwarded. That is the difference between a
// forwarding check and a list of the fields somebody remembered.
var createProbeArgs = map[string]any{
	"project":                "aihub",
	"goal":                   "probe every published property through to the POST body",
	"scenario":               "coding",
	"priority":               "high",
	"wi_type":                "chore",
	"requires_human_session": false,
	"milestone":              "m-probe",
	"labels":                 []any{"probe-label"},
	"declared_resources": []any{map[string]any{
		"type": "path", "uri": "file:internal/mcp/probe.go", "intent": "read",
	}},
	"parent_work_item_id": "wi_parent01",
	"source":              "human",
	"attrs":               map[string]any{"probe": true},
	"blocked_by":          []any{"aihub#1"},
	"content":             "a body the caller sent",
	"force_create":        true,
	"force_reason":        "a reason long enough for the server to accept it",
}

// TestCreateForwardsEveryPublishedPropertyVerbatim is the whole-map arm.
//
// WHY IT HAD NO ARM. The card's own reasoning is that a wholesale forward has no
// table to drift from — "every published property is on the wire by
// construction" — and that is true of the code as written and is exactly the
// claim a refactor invalidates. TestContractEveryPublishedParamLeavesTheProcess
// is the universal gate for this class, and it is a SOURCE-level check: it looks
// for each published parameter's name in the handler's package. A handler that
// switched from `args` to a struct with fifteen of the sixteen fields would
// satisfy it, because the sixteenth name would still appear in
// workItemFieldProps two hundred lines away. This arm observes the request
// instead.
//
// The unrecognised key is the sharper half. A wholesale forward carries a
// property this process has never heard of; a struct-based one cannot, however
// complete its field list is. So the failure of that subtest is the one that
// distinguishes "the map is forwarded" from "every field happens to be listed".
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the handler, the card untouched) ──
//	M23 delete `blocked_by` from the args map before the client call
//	                                          RED  every_published_property/blocked_by
//	M24 forward a rebuilt map of the fields the handler names explicitly
//	    (project, goal, force_reason) instead of `args`
//	                                          RED  twelve properties AND
//	                                               an_unrecognised_key_still_travels
//	M25 point pkg/client.CreateWorkItem at /v1/work_items/batch
//	                                          RED  one_request_to_the_single_route. Note
//	                                               it moves BOTH create tools, because
//	                                               there is one client method behind
//	                                               them — which is the fact the batch
//	                                               card's "same hop-3" claim rests on
//	── publication side (the card, the handler untouched) ──
//	M26 strip this sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift//
//
// ⚠️ The publication-side mutant strips the citation's FILE PATH as well as its
// test symbol, and that is not tidiness. Measured 2026-09-10: de-backticking the
// symbol alone left every one of this wave's seventeen sentences still counted as
// Cited, because cardclaims.CitesAnArm is satisfied by EITHER anchor — so the
// publication side of a citation is only as strong as whichever anchor a later
// edit leaves behind. Reported as an incidental finding of aihub#576.
func TestCreateForwardsEveryPublishedPropertyVerbatim(t *testing.T) {
	published := topLevelSchema(t, createToolName).Properties

	// The fixture must cover the published set, or a field added later is
	// forwarded by nobody and checked by nobody.
	var uncovered []string
	for _, name := range sortedPropKeys(published) {
		if _, ok := createProbeArgs[name]; !ok {
			uncovered = append(uncovered, name)
		}
	}
	if len(uncovered) > 0 {
		t.Fatalf("%s publishes %v, which createProbeArgs has no value for. A published parameter "+
			"this fixture does not send is a parameter this arm says nothing about — add a "+
			"distinguishable value for each in the same change that publishes it.",
			createToolName, uncovered)
	}
	// ...and the other direction: a value for a property that is not published
	// tells a reader this arm covers a parameter callers cannot set.
	for name := range createProbeArgs {
		if _, ok := published[name]; !ok {
			t.Errorf("createProbeArgs sends %q, which %s does not publish. Either the parameter "+
				"was withdrawn and this entry should go with it, or it is being forwarded without "+
				"being offered.", name, createToolName)
		}
	}

	// One unrecognised key, which is what a wholesale forward carries and a
	// rebuilt map cannot.
	args := map[string]any{"a_key_this_process_has_never_heard_of": "and does not need to"}
	for k, v := range createProbeArgs {
		args[k] = v
	}

	calls, _, isErr := driveCreate(t, args)
	if isErr {
		t.Fatalf("the probe call failed, so nothing was observed on the wire: %v", calls)
	}

	t.Run("one_request_to_the_single_route", func(t *testing.T) {
		if len(calls) != 1 {
			t.Fatalf("expected exactly one HTTP call, got %d: %v", len(calls), pathsOf(calls))
		}
		if calls[0].Path != createWirePath {
			t.Errorf("the create posted to %q, want %q — the card names this route as the hop-3 "+
				"binding both create tools share, and pf_batch_create_work_items' card says it is "+
				"the same one N times.", calls[0].Path, createWirePath)
		}
		if calls[0].Method != http.MethodPost {
			t.Errorf("the create used %s, want POST", calls[0].Method)
		}
	})

	body := calls[0].Body
	if len(body) == 0 {
		t.Fatalf("the create posted an empty body, so every absence below is an artefact of that " +
			"and not evidence about forwarding")
	}

	t.Run("every_published_property", func(t *testing.T) {
		for _, name := range sortedPropKeys(published) {
			t.Run(name, func(t *testing.T) {
				got, present := body[name]
				if !present {
					t.Errorf("%q was sent to the tool and is absent from the POST body. The card "+
						"tells callers the whole argument map is forwarded, so a property that is "+
						"published, accepted and dropped is a promise broken at a hop no error "+
						"reaches. Body carried: %v", name, sortedBodyKeys(body))
					return
				}
				if !sameJSONValue(got, createProbeArgs[name]) {
					t.Errorf("%q arrived as %#v, want %#v — forwarded but rewritten, which is worse "+
						"than dropped: the caller's value is gone and the request succeeds.",
						name, got, createProbeArgs[name])
				}
			})
		}
	})

	t.Run("an_unrecognised_key_still_travels", func(t *testing.T) {
		if _, present := body["a_key_this_process_has_never_heard_of"]; !present {
			t.Errorf("a property this process does not publish was dropped before the wire. That "+
				"is the observation that separates \"the whole map is forwarded\" from \"every "+
				"field happens to be in a list\" — and the second shape is the one that loses the "+
				"NEXT field somebody adds to the server without touching this package. Body "+
				"carried: %v", sortedBodyKeys(body))
		}
	})
}

// pathsOf lists the request paths, so a wrong-count failure says where the
// requests went.
func pathsOf(calls []recordedCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

// sameJSONValue compares a value that has been through JSON with the Go literal
// it was built from.
//
// Written out rather than reflect.DeepEqual because the round trip is lossy in
// exactly one direction that matters here: []any and map[string]any survive, but
// every number arrives as float64 and a []any of strings is not deep-equal to
// its source unless each element is compared. A comparison that got this wrong
// would report every array and object as rewritten, which reads as a much bigger
// finding than "the test cannot compare its own fixtures".
func sameJSONValue(got, want any) bool {
	switch w := want.(type) {
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !sameJSONValue(g[i], w[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k := range w {
			if !sameJSONValue(g[k], w[k]) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}

// TestCreateRefusesAMissingProjectOrGoalWithoutSendingAnything is the
// local-check arm.
//
// WHY IT HAD NO ARM. TestCreateStillRefusesAnEmptyGoalInTheSameWords asserts the
// DOMAIN refusal, which happens on the server after a round trip.
// TestPublishedGoalCapIsTheEnforcedOne asserts the published text. The card's
// claim is about neither: it says the MCP handler refuses these two BEFORE the
// request leaves the process, which is why an empty goal costs no round trip and
// why the same value is refused identically whether or not a server is reachable.
// The only observation of that is the absence of an HTTP call.
//
// MUTANTS.
//
//	── enforcement side ──
//	M27 neutralise the `strArg(args, "goal") == ""` guard (`if false`)
//	                                          RED  empty_goal and missing_goal (a call
//	                                               was recorded for each)
//	M28 neutralise the `strArg(args, "project") == ""` guard (`if false`)
//	                                          RED  missing_project
//	M29 make the project guard refuse unconditionally (`if true`)
//	                                          RED  TestCreateForwardsEveryPublishedPropertyVerbatim,
//	                                               which needs a create to succeed —
//	                                               named here because an arm asserting
//	                                               only refusals is satisfied by a
//	                                               handler that refuses everything
//	── publication side ──
//	M30 strip the citation on the goal-refusal sentence, path and symbol both
//	                                          RED  K12 ledger drift
func TestCreateRefusesAMissingProjectOrGoalWithoutSendingAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "missing_project",
			args: map[string]any{"goal": "a goal with no project to file it under"},
			want: "project is required",
		},
		{
			name: "empty_goal",
			args: map[string]any{"project": "aihub", "goal": ""},
			want: "goal is required",
		},
		{
			name: "missing_goal",
			args: map[string]any{"project": "aihub"},
			want: "goal is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, result, isErr := driveCreate(t, tc.args)
			if !isErr {
				t.Fatalf("the call succeeded; %s must be refused: %v", tc.name, result)
			}
			if len(calls) != 0 {
				t.Errorf("%d HTTP call(s) were made (%v). The card says these two are checked "+
					"LOCALLY, and the point of a local check is that a doomed request does not "+
					"spend a round trip — nor reach a server that would answer the same 400 with "+
					"the same words, so the failure is indistinguishable to a caller and different "+
					"in cost.", len(calls), pathsOf(calls))
			}
			if text := createRefusalText(result); !strings.Contains(text, tc.want) {
				t.Errorf("the refusal reads %q, want it to contain %q — a caller who sent several "+
					"fields cannot tell which one was refused otherwise", text, tc.want)
			}
		})
	}
}

// createRefusalText renders whatever a refused callTool handed back.
//
// An error result is a bare string rather than JSON, so callTool wraps it under
// `_raw`; every other key is rendered too, because a refusal that came back as a
// JSON object would otherwise read as an empty message and the assertion would
// report the wrong thing about it.
func createRefusalText(result map[string]any) string {
	if result == nil {
		return ""
	}
	if raw, ok := result["_raw"].(string); ok {
		return raw
	}
	var b strings.Builder
	for _, k := range sortedBodyKeys(result) {
		b.WriteString(k + "=")
		if s, ok := result[k].(string); ok {
			b.WriteString(s)
		}
		b.WriteString(" ")
	}
	return b.String()
}

// serverForceReasonMinimum is the length the SERVER demands of a force_reason,
// as CreateWorkItem enforces it (`len(req.ForceReason) < 10`).
//
// ⚠️ It is typed here because domain exports no accessor for it, unlike
// MaxWorkItemGoalRunes() — which aihub#434 introduced for exactly this reason,
// after three hand-typed copies of the goal cap. So this arm is ONE-SIDED and
// says so: shortening the MCP default makes it red, but RAISING the server's
// minimum leaves it green. Reported as an incidental finding of aihub#576;
// exporting a MinForceReasonRunes() would make it two-sided the way the goal cap
// is, and is a production change this card-clearing change deliberately did not
// make.
const serverForceReasonMinimum = 10

// TestCreateSuppliesAForceReasonWheneverForceCreateIsSet is the
// applyForceReasonDefault arm.
//
// WHY IT HAD NO ARM. applyForceReasonDefault is shared by the single and batch
// create paths precisely so a batch item does not fail a validation the
// single-item path quietly satisfies, and nothing observed either path doing it.
// The server-side refusal lives inside the create transaction, past pool.Begin,
// so it is unreachable without a database — which is why the claim that matters
// on this side is that the request never gets there without a reason.
//
// MUTANTS.
//
//	── enforcement side ──
//	M31 delete the applyForceReasonDefault call from the create handler
//	                                          RED  force_create_alone/default_supplied
//	M32 default the reason to "forced"        RED  force_create_alone/long_enough_for_the_server
//	M33 apply the default unconditionally (drop the force_create half of its
//	    condition)                            RED  no_force_create/nothing_invented
//	── publication side ──
//	M34 strip this sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestCreateSuppliesAForceReasonWheneverForceCreateIsSet(t *testing.T) {
	t.Run("force_create_alone", func(t *testing.T) {
		calls, result, isErr := driveCreate(t, map[string]any{
			"project": "aihub", "goal": "force a create past the duplicate check",
			"force_create": true,
		})
		if isErr || len(calls) != 1 {
			t.Fatalf("expected one forwarded create, got %d call(s) (err=%v): %v",
				len(calls), isErr, result)
		}
		reason, _ := calls[0].Body["force_reason"].(string)

		t.Run("default_supplied", func(t *testing.T) {
			if reason == "" {
				t.Errorf("force_create=true left the wire with no force_reason. The server refuses "+
					"that combination inside the create transaction, so the round trip is spent and "+
					"the caller is handed a 400 for a field they were never told they had to set — "+
					"and the batch path, which shares this defaulting, would fail per item. Body: %v",
					sortedBodyKeys(calls[0].Body))
			}
		})

		t.Run("long_enough_for_the_server", func(t *testing.T) {
			if n := len(reason); n < serverForceReasonMinimum {
				t.Errorf("the supplied force_reason is %d bytes (%q) and CreateWorkItem refuses "+
					"anything under %d. A default that does not satisfy the check it exists to "+
					"satisfy is worse than none: it makes the refusal look like the caller's fault.",
					n, reason, serverForceReasonMinimum)
			}
		})
	})

	// The control. Without it, "a reason is supplied when force_create is set" is
	// satisfied by supplying one always — which would put an admin-bypass reason
	// on every create that never asked for one.
	t.Run("no_force_create", func(t *testing.T) {
		calls, result, isErr := driveCreate(t, map[string]any{
			"project": "aihub", "goal": "an ordinary create that runs the duplicate check",
		})
		if isErr || len(calls) != 1 {
			t.Fatalf("expected one forwarded create, got %d call(s) (err=%v): %v",
				len(calls), isErr, result)
		}
		t.Run("nothing_invented", func(t *testing.T) {
			if got, present := calls[0].Body["force_reason"]; present {
				t.Errorf("an ordinary create carried force_reason=%#v. The default is conditional on "+
					"force_create for a reason: a reason on a request that is not forcing anything "+
					"reads, on the timeline and to the next reviewer, as a bypass somebody chose.", got)
			}
		})
	})
}

// TestCreatePublishedEnumsDoNotRefuseInProcess is the enum arm, and it is GREEN
// on both sides of the change that produced it by design: it is not evidence
// that the enums are worth publishing, it is what stops them from being mistaken
// for the guard.
//
// 🔴 IT ALSO CORRECTS A FALSE CARD SENTENCE, measured 2026-09-10. The card's
// Policy section read "§6.2 T2-6 — … Here the SDK **does** enforce `priority` and
// `source`, which is the difference." That contradicted the card's OWN hop 0-1
// paragraph four screens above, which records aihub#496's correction of
// aihub#396: applySchema -> resolved.Validate is wired only into the generic
// AddTool[In, Out], polyforge registers through the untyped
// (*mcp.Server).AddTool, and Server.callTool hands that path straight to the
// handler with no schema step. Measured here: an out-of-vocabulary `priority`
// and an out-of-vocabulary `source` both arrive in the POST body unchanged. The
// sentence was corrected in the same change as this arm.
//
// The values are the mistakes a caller actually makes rather than nonsense
// strings: `critical` is the priority word every other tracker uses, and `jira`
// is what aihub#396 recorded a caller sending in place of `sync_jira` — and that
// one was a 500 before the Go validator, which is the cost this pair of layers
// removes.
//
// MUTANTS.
//
//	── enforcement side ──
//	M35 register pf_create_work_item through the generic AddTool[In, Out]
//	    (not applied: it is a rewrite of the whole registration, and the arm's
//	    verdict on it is the assertion itself — if a future SDK upgrade or
//	    registration change starts validating untyped tools, this test goes RED
//	    and every comment claiming otherwise gets corrected with it, which is the
//	    only way a note about somebody else's code stays true)
//	M36 make the handler refuse an out-of-vocabulary priority itself
//	                                          RED  priority (no call recorded)
//	M37 make the handler normalise `jira` to `sync_jira`
//	                                          RED  source (value rewritten)
//	── publication side ──
//	M38 restore the false "the SDK **does** enforce" sentence on the card
//	                                          RED  nothing here — this arm cannot read
//	                                               the card. The corrected sentence
//	                                               carries the citation, so K12 owns
//	                                               that side, and the arm's job is to
//	                                               make the true statement checkable
//	M39 strip the corrected sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestCreatePublishedEnumsDoNotRefuseInProcess(t *testing.T) {
	for _, tc := range []struct{ param, illegal, legalNeighbour string }{
		{param: "priority", illegal: "critical", legalNeighbour: "urgent"},
		{param: "source", illegal: "jira", legalNeighbour: "sync_jira"},
	} {
		t.Run(tc.param, func(t *testing.T) {
			// The published enum must really offer a closed set, or "the enum does not
			// refuse this" is a statement about a parameter that has no enum.
			offered := enumOfProp(t, createToolName, tc.param)
			if len(offered) < 2 {
				t.Fatalf("%s publishes %q with %d enum value(s) (%v) — with no closed set there is "+
					"nothing for an out-of-vocabulary value to be outside of",
					createToolName, tc.param, len(offered), offered)
			}
			var offersTheIllegal, offersTheNeighbour bool
			for _, v := range offered {
				if v == tc.illegal {
					offersTheIllegal = true
				}
				if v == tc.legalNeighbour {
					offersTheNeighbour = true
				}
			}
			if offersTheIllegal {
				t.Fatalf("%q is now a legal %s (%v), so sending it is no longer out of vocabulary "+
					"and this arm is measuring nothing. Pick a value the enum does not offer.",
					tc.illegal, tc.param, offered)
			}
			if !offersTheNeighbour {
				t.Errorf("%q is not in the published %s enum (%v) — the fixture's premise is that "+
					"the illegal value is the mistake a caller makes INSTEAD of this one, and a "+
					"vocabulary that no longer contains it has moved out from under this arm",
					tc.legalNeighbour, tc.param, offered)
			}

			calls, result, _ := driveCreate(t, map[string]any{
				"project": "aihub",
				"goal":    "send an out-of-vocabulary " + tc.param + " through every in-process hop",
				tc.param:  tc.illegal,
			})
			if len(calls) != 1 {
				t.Fatalf("expected exactly one HTTP call, got %d (%v). If this is zero, something "+
					"in THIS process refused the value and the published enum is a guard after all "+
					"— which would make the corrected paragraph in %s's hop 0-1 section, and the "+
					"aihub#496 comment on workItemFieldProps, both false. Result: %v",
					len(calls), pathsOf(calls), createToolName, result)
			}
			if got := calls[0].Body[tc.param]; got != tc.illegal {
				t.Errorf("%s reached the POST body as %#v, want %q unchanged — the published enum "+
					"neither refuses nor rewrites it in this process. The refusal that IS "+
					"load-bearing is the Go validator in internal/domain, which answers 400 naming "+
					"the field; both halves are required and neither is the SDK.",
					tc.param, got, tc.illegal)
			}
		})
	}
}
