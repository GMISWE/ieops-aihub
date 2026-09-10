package mcp_test

// aihub#543 probe wave 2, lane L11 — the `docs/mcp-cards/pf_pr.md` sentences
// about WHERE THE WORK HAPPENS and WHAT COMES BACK.
//
//	"The PR is created by the **`gh` CLI**, not by aihub: `internal/coding/gh_ops.go`
//	 (`GHCreatePR`) runs in the worktree."
//	    -> TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent
//	"The only HTTP call to aihub is the best-effort `pr_opened` event through
//	 `internal/mcp/tools_coding.go` (`emitCodingEvent`) -> `POST /v1/events`."
//	    -> TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent
//	"So there is no aihub hop 3 for `title`, `body`, `head` or `base`: they are
//	 `gh` arguments."
//	    -> TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent
//	"`internal/mcp/helpers.go` (`prPayload`) is what turns the `gh` result into
//	 the event payload…"
//	    -> TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent
//	"Creates the PR and returns `gh`'s own JSON, which is why the observed
//	 response keys are GitHub's camelCase names…"
//	    -> TestTheGhObjectReachesTheModelUnprojected
//	"Unlike `pf_ship`, there is no structured side-effect report…"
//	    -> TestAFailedPRIsAPlainErrorWithNoStageOrSideEffects
//	"The event is best-effort, so a PR can exist with no `pr_opened` on the
//	 timeline…"
//	    -> TestThePREventIsBestEffortAndAFailedEmitDoesNotFailTheCall
//	"`pr_opened` … is published by name on `pf_emit_event`…"  (the CORRECTED
//	 sentence — see TestThePREventTypeIsAPublishedVocabularyEntry)
//
// 🔴 WHAT NO EXISTING ARM COVERS. The universal wire gate
// (TestContractEveryPublishedParamLeavesTheProcess) quantifies over published
// parameters and asserts each one LEAVES THE PROCESS as an aihub request. This
// tool publishes seven and sends aihub exactly one request, whose body carries
// none of `title`/`body`/`head`/`base` — so from that gate's point of view four
// of this tool's parameters are violations, recorded in its local-consumption
// table rather than observed. Nothing anywhere drives `gh` and looks at what it
// was handed, which is where four of this card's claims live. The
// internal/coding suite next door (ship_test.go, wrap_test.go) does drive a fake
// `gh`, but it enters at coding.Ship / coding.Wrap and never through the pf_pr
// tool, so the aihub-hop half of the claim is outside its reach entirely.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestThePR|TestTheGhObject|TestAFailedPR' -count=1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const prWIID = "wi_01JPFPRCARD"

// prEventsPath is the one aihub route this tool can reach.
const prEventsPath = "/v1/events"

// prGhResult is what the fake `gh` prints: GitHub's own camelCase vocabulary,
// exactly the five keys the card's response record names, plus one key no
// struct in this repo declares.
//
// The extra key is the projection detector. Five keys that a local type happens
// to list would also survive a projection written over that type, so "the
// object reaches the model" needs a field nothing here could have known about.
const prGhExtraKey = "isDraft"

var prGhResult = map[string]any{
	"baseRefName":  "main",
	"commits":      []any{map[string]any{"oid": "cafebabe"}},
	"number":       9,
	"state":        "OPEN",
	"url":          "https://github.test/o/r/pull/9",
	prGhExtraKey:   false,
	"headRefName":  "polyforge/aihub-583",
	"mergeStateSt": "CLEAN",
}

// prFakeGH puts a fake `gh` on PATH that records its argv and its working
// directory, and returns the path of the log it writes.
//
// A recorded WORKING DIRECTORY rather than only the argv, because "runs in the
// worktree" is half of the card's first sentence and an argv log cannot say
// where the process stood. `gh pr create` resolves the repository from the
// checkout it is invoked in, so a `gh` run anywhere else opens a PR against
// whatever repo that directory belongs to.
func prFakeGH(t *testing.T, exitCode int, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "gh.log")
	script := fmt.Sprintf(`#!/bin/sh
{
  printf 'argv:'
  for a in "$@"; do printf ' <%%s>' "$a"; done
  printf '\n'
  printf 'pwd: %%s\n' "$(pwd)"
} >> %q
cat <<'GHEOF'
%s
GHEOF
exit %d
`, log, stdout, exitCode)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// prGhLog reads the fake gh log, failing when it is absent — which is what a
// tool that never ran `gh` at all leaves behind.
func prGhLog(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the fake gh left no log at %s (%v). pf_pr never ran `gh`, so every assertion "+
			"about what gh was handed would be about nothing", log, err)
	}
	return string(b)
}

// seedPRState writes a claimed state file whose worktree map points at a real
// directory, so coding.WorktreePath resolves and `gh` has somewhere to stand.
func seedPRState(t *testing.T) string {
	t.Helper()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())
	worktree := t.TempDir()
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          prWIID,
		Project:       "aihub",
		Slug:          "aihub#583",
		AttemptID:     "ra_pr",
		ClaimEpoch:    1,
		SessionSecret: "pr-secret",
		Claimed:       true,
		Worktrees:     map[string]string{"aihub": worktree},
	}); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	return worktree
}

// prArgs is one full pf_pr call, with every optional branch parameter set, so
// the "they are gh arguments" claim is asserted on all four names at once.
var prArgs = map[string]any{
	"work_item_id": prWIID,
	"repo":         "aihub",
	"title":        "PR-TITLE-aihub583",
	"body":         "PR-BODY-aihub583",
	"head":         "polyforge/aihub-583",
	"base":         "main",
}

// ghArgumentNames are the published parameters the card says have no aihub hop.
var ghArgumentNames = []string{"title", "body", "head", "base"}

// TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent is the card's hop 2-3, all
// four sentences of it, in one arm.
//
// They are one arm because they are one claim seen from two sides: the four
// values go to `gh` and NOT to aihub, and the single aihub request is the event.
// Split apart, "gh got the title" is satisfied by a tool that also posts it to
// aihub, and "aihub got no title" is satisfied by a tool that never ran gh — and
// the card's point is precisely that the boundary falls between them.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M1 enforcement: in helpers.go, drop prPayload's `url` copy
//	                                          RED  no_gh_argument_reaches_aihub —
//	                                               the payload is the only aihub-side
//	                                               record, and a PR it cannot name is
//	                                               a timeline entry with no PR on it
//	M2 enforcement: drop `cmd.Dir = worktreePath` from GHCreatePR
//	                                          RED  gh_runs_in_the_worktree
//	M3 enforcement: in GHCreatePR, stop appending --head
//	                                          RED  every_gh_argument_is_handed_to_gh
//	M4 enforcement: emit a SECOND coding event from the pf_pr handler
//	                                          RED  the_event_is_the_only_aihub_hop
//	                                               and no_gh_argument_reaches_aihub
//	                                               (lastBodyFor then reads the wrong
//	                                               request, which is itself the
//	                                               reason the path list is an
//	                                               equality rather than a membership
//	                                               test)
//	M5 publication: delete the citation clause from the card's hop 2-3 sentences
//	                                          RED  K12 DEBT_GROWTH (this arm reads
//	                                               the wire and the argv, so the
//	                                               card's text is K12's half)
func TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent(t *testing.T) {
	worktree := seedPRState(t)
	body, _ := json.Marshal(prGhResult)
	log := prFakeGH(t, 0, string(body))
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_pr", prArgs)
	if isErr {
		t.Fatalf("pf_pr failed: %v", result)
	}
	recorded := prGhLog(t, log)

	t.Run("every_gh_argument_is_handed_to_gh", func(t *testing.T) {
		// FLOOR: the subcommand itself. A fake gh invoked for `pr list` or for
		// nothing at all would satisfy a value-by-value search over a log this
		// test wrote for some other call.
		if !strings.Contains(recorded, "<pr> <create>") {
			t.Fatalf("the fake gh was never asked to create a PR. Log:\n%s", recorded)
		}
		for _, name := range ghArgumentNames {
			flag := "<--" + name + ">"
			value, _ := prArgs[name].(string)
			if !strings.Contains(recorded, flag) {
				t.Errorf("gh was never handed %s. The card's hop 0-1 table promises this parameter "+
					"reaches the PR, and this tool's only route to the PR is the gh argv — a "+
					"parameter dropped here is accepted, acknowledged and inert, which is the "+
					"aihub#259 shape. Log:\n%s", flag, recorded)
			}
			if !strings.Contains(recorded, "<"+value+">") {
				t.Errorf("gh was never handed the VALUE %q for %s; a flag with somebody else's "+
					"value is the same defect one step later. Log:\n%s", value, name, recorded)
			}
		}
	})

	t.Run("gh_runs_in_the_worktree", func(t *testing.T) {
		if !strings.Contains(recorded, "pwd: "+worktree) {
			t.Errorf("gh did not run in the work item's worktree (%s). `gh pr create` reads the "+
				"repository out of the checkout it stands in, so a gh that runs anywhere else "+
				"opens the PR against whatever repo that directory belongs to — and the tool "+
				"would report success. Log:\n%s", worktree, recorded)
		}
	})

	t.Run("the_event_is_the_only_aihub_hop", func(t *testing.T) {
		paths := f.paths()
		if len(paths) != 1 || paths[0] != prEventsPath {
			t.Fatalf("pf_pr made aihub requests %v, want exactly [%s].\nThe card tells a caller "+
				"that this tool's PR is not aihub's doing and that the timeline event is the only "+
				"aihub-side record it produces. A second request would mean aihub holds a record "+
				"the card does not mention, which is what a reader consults this card to rule out.",
				paths, prEventsPath)
		}
	})

	t.Run("no_gh_argument_reaches_aihub", func(t *testing.T) {
		sent := lastBodyFor(t, f, prEventsPath)
		payload, ok := sent["payload"].(map[string]any)
		if !ok {
			t.Fatalf("the event body carries payload=%#v, want an object built by prPayload "+
				"(keys=%v)", sent["payload"], sortedBodyKeys(sent))
		}
		// prPayload's contract: repo, title, and url/number when gh returned them.
		// `title` is in it and is NOT a counter-example to the sentence — the
		// sentence is about a hop-3 PARAMETER binding, and the event payload is a
		// timeline record built after the PR exists. What must not appear is the
		// PR BODY and the two branch names, which have no reader on the aihub side
		// at all.
		for _, name := range []string{"body", "head", "base"} {
			if v, present := payload[name]; present {
				t.Errorf("the pr_opened payload carries %s=%#v. prPayload builds repo/title plus "+
					"url and number; a branch name or a whole PR body stored on the timeline is a "+
					"second copy of something GitHub already owns, and the two would not have to "+
					"agree.", name, v)
			}
		}
		if payload["repo"] != prArgs["repo"] || payload["title"] != prArgs["title"] {
			t.Errorf("the pr_opened payload is %v, want repo=%v title=%v — prPayload's two "+
				"unconditional keys", payload, prArgs["repo"], prArgs["title"])
		}
		if payload["number"] != float64(9) || payload["url"] != prGhResult["url"] {
			t.Errorf("the pr_opened payload is %v; prPayload copies gh's url and number when gh "+
				"returned them, and this is the only aihub-side record that the PR exists — "+
				"without them the timeline says a PR was opened and cannot say which", payload)
		}
		if sent["event_type"] != "pr_opened" {
			t.Errorf("event_type = %#v, want pr_opened", sent["event_type"])
		}
	})
}

// TestTheGhObjectReachesTheModelUnprojected is the card's hop 4 and hop 5: the
// response is gh's object, which is why the observed keys are camelCase.
//
// The claim is about ABSENCE OF A PROJECTION, so the arm needs a key that no
// local type declares — see prGhExtraKey. A test built from the five recorded
// keys alone would go green against a projection that happened to list them,
// which is the one shape the card's "unprojected" is warning a reader about.
//
// MUTANTS:
//
//	M6 enforcement: replace jsonResult(result) in the pf_pr handler with a
//	   projection copying url/number/state/baseRefName/commits
//	                                          RED  gh_keys_no_local_type_declares
//	M7 enforcement: `delete(result, "baseRefName")` in the pf_pr handler before
//	   jsonResult — one camelCase key lost on the way out
//	                                          RED  the_camelcase_names_are_ghs
//	M8 publication: delete the citation clause from the hop 4 bullet
//	                                          RED  K12 DEBT_GROWTH
func TestTheGhObjectReachesTheModelUnprojected(t *testing.T) {
	seedPRState(t)
	body, _ := json.Marshal(prGhResult)
	prFakeGH(t, 0, string(body))
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_pr", prArgs)
	if isErr {
		t.Fatalf("pf_pr failed: %v", result)
	}

	t.Run("the_camelcase_names_are_ghs", func(t *testing.T) {
		// The five the card's response record names, read off that record's own
		// list rather than retyped: K7 already holds the card's copy against the
		// corpus file, so this arm inherits the same source of truth.
		for _, key := range []string{"baseRefName", "commits", "number", "state", "url"} {
			if _, present := result[key]; !present {
				t.Errorf("the result does not carry %q (keys=%v). These are GitHub's names, not "+
					"this repo's, and the card explains the camelCase in the response record by "+
					"saying they arrive unprojected — a missing one means something renamed or "+
					"dropped it on the way through.", key, sortedBodyKeys(result))
			}
		}
	})

	t.Run("gh_keys_no_local_type_declares", func(t *testing.T) {
		if _, present := result[prGhExtraKey]; !present {
			t.Errorf("the result does not carry %q, which the fake gh printed and no type in this "+
				"repo declares (keys=%v).\nThis is the projection detector: the five recorded keys "+
				"would survive a projection written over them, and a key gh added that nobody here "+
				"knows about would not. The card's claim is that this tool has no slim function at "+
				"all, so whatever gh prints is what the model gets.",
				prGhExtraKey, sortedBodyKeys(result))
		}
	})
}

// TestAFailedPRIsAPlainErrorWithNoStageOrSideEffects is the card's contrast with
// pf_ship: a single-stage tool has nothing partial to report, so its failure is
// an error result rather than a structured object.
//
// Both halves matter and they fail differently. "It is an error result" is what
// a caller's own error handling turns on; "it carries no stage/side_effects" is
// what stops a caller from writing recovery logic against keys that will never
// be there. pf_ship's card documents those keys as the way to find out what
// already happened, and a reader comparing the two tools is asking exactly
// whether this one answers the same way.
//
// MUTANTS:
//
//	M9  enforcement: in the pf_pr handler, replace errResult(err) with
//	    jsonResult(map[string]any{"stage": "pr", "side_effects": []any{}})
//	                                          RED  a_failure_is_an_error_result
//	                                               and no_structured_report
//	M10 enforcement: emit the pr_opened event before checking GHCreatePR's error
//	                                          RED  a_failed_pr_leaves_no_event
//	M11 publication: delete the citation clause from the hop 4 bullet
//	                                          RED  K12 DEBT_GROWTH
func TestAFailedPRIsAPlainErrorWithNoStageOrSideEffects(t *testing.T) {
	seedPRState(t)
	prFakeGH(t, 1, "gh: GraphQL: Resource not accessible by integration")
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_pr", prArgs)

	t.Run("a_failure_is_an_error_result", func(t *testing.T) {
		if !isErr {
			t.Fatalf("pf_pr reported success on a gh that exited 1: %v.\nThe card says a failure "+
				"here is a plain error result; a success carrying gh's stderr is a PR a caller "+
				"believes exists.", result)
		}
		text, _ := result["_raw"].(string)
		if !strings.Contains(text, "gh pr create") {
			t.Errorf("the refusal is %q and does not name the command that failed. GHCreatePR "+
				"wraps gh's combined output, which is the only diagnosis a caller has — the PR "+
				"was never aihub's to explain.", text)
		}
	})

	t.Run("no_structured_report", func(t *testing.T) {
		for _, key := range []string{"stage", "side_effects", "lock_gate"} {
			if _, present := result[key]; present {
				t.Errorf("the failed pf_pr result carries %q. Those are pf_ship's keys, and the "+
					"card draws the distinction deliberately: a fused call can fail with a commit "+
					"already made, and a single-stage call cannot. A stage report here would "+
					"invite recovery logic for a partial state this tool cannot produce.", key)
			}
		}
	})

	t.Run("a_failed_pr_leaves_no_event", func(t *testing.T) {
		if paths := f.paths(); len(paths) != 0 {
			t.Errorf("a failed pf_pr made aihub requests %v, want none.\nThe event is the only "+
				"aihub-side record that the PR exists, so emitting it for a PR that was never "+
				"created puts a pr_opened on the timeline with no PR behind it — and after a wrap "+
				"the timeline is the only durable record left.", paths)
		}
	})
}

// TestThePREventIsBestEffortAndAFailedEmitDoesNotFailTheCall is the card's
// best-effort sentence, and the direction that matters is the one a caller
// cannot see: the PR is already open by the time the event is attempted, so a
// failing emit must not turn a successful call into an error.
//
// The control is the other direction. "A failed emit does not fail the call" is
// satisfied by a tool that never emits at all, which is why this runs the same
// call against a healthy events route and requires the request to happen.
//
// MUTANTS:
//
//	M12 enforcement: replace the best-effort emitCodingEvent call in the pf_pr
//	    handler with a strict inline s.client.EmitEvent whose error is returned
//	                                          RED  a_rejected_event_does_not_fail
//	                                               _the_call
//	M13 enforcement: delete the emitCodingEvent call from the pf_pr handler
//	                                          RED  CONTROL/the_event_is_attempted,
//	                                               and a_rejected_event… too, on its
//	                                               "the emit was ATTEMPTED" floor —
//	                                               which is the floor doing its job
//	M14 publication: delete the citation clause from the hop 4 bullet
//	                                          RED  K12 DEBT_GROWTH
func TestThePREventIsBestEffortAndAFailedEmitDoesNotFailTheCall(t *testing.T) {
	drive := func(t *testing.T, eventStatus int) (map[string]any, bool, []string) {
		t.Helper()
		seedPRState(t)
		body, _ := json.Marshal(prGhResult)
		prFakeGH(t, 0, string(body))
		f := newFakeAihub(t)
		f.on(prEventsPath, func(map[string]any) (int, any) {
			if eventStatus == http.StatusOK {
				return http.StatusOK, map[string]any{"ok": true}
			}
			return eventStatus, map[string]any{
				"error": map[string]any{"code": "FORBIDDEN", "message": "not your work item"}}
		})
		result, isErr := callTool(t, f, "pf_pr", prArgs)
		return result, isErr, f.paths()
	}

	t.Run("a_rejected_event_does_not_fail_the_call", func(t *testing.T) {
		result, isErr, paths := drive(t, http.StatusForbidden)
		if len(paths) != 1 {
			t.Fatalf("the events route was called %v; this arm needs the emit to have been "+
				"ATTEMPTED and refused, or \"a refusal does not fail the call\" is about nothing",
				paths)
		}
		if isErr {
			t.Fatalf("pf_pr failed because the timeline event was refused: %v.\nThe PR is already "+
				"open at that point. Reporting failure would send a caller to retry a call whose "+
				"gh stage is not idempotent from this tool's side — GHCreatePR recovers an "+
				"existing PR rather than duplicating one, but the caller has no way to know that "+
				"from an error string.", result)
		}
		if result["url"] != prGhResult["url"] {
			t.Errorf("the result lost gh's url when the event was refused: %v", result)
		}
	})

	t.Run("CONTROL/the_event_is_attempted", func(t *testing.T) {
		_, isErr, paths := drive(t, http.StatusOK)
		if isErr {
			t.Fatalf("pf_pr failed on the healthy path")
		}
		if len(paths) != 1 || paths[0] != prEventsPath {
			t.Errorf("pf_pr made aihub requests %v, want exactly [%s].\nWithout this control the "+
				"arm above is satisfied by a tool that emits nothing: \"a failed emit is "+
				"harmless\" is trivially true when there is no emit.", paths, prEventsPath)
		}
	})
}

// TestThePREventTypeIsAPublishedVocabularyEntry holds the CORRECTED §6.2 T2-5
// sentence.
//
// 🔴 THE CARD SAID THE OPPOSITE AND WAS WRONG. Its Policy bullet read
// "`pr_opened` is another free-text event type with no published vocabulary; the
// ruling is to publish the vocabulary on `pf_emit_event`" — written while T2-5
// was open. aihub#444 landed it: domain.EventVocabulary carries `pr_opened`, and
// emitEventTypePropDescription() derives the published event_type description
// from that list, so the vocabulary IS published and this tool's own type is in
// it. The "free-text" half survives intact and is the other half of this arm:
// the set is published and NOT closed.
//
// Why this arm and not the two existing ones alone.
// TestEmitEventTypeDescriptionPublishesTheVocabulary quantifies over
// EventVocabulary, so it goes green the moment `pr_opened` is REMOVED from that
// list — the list is the thing it reads. Nothing tied the type this tool
// actually emits to the vocabulary the other tool publishes, and that binding is
// what the card's sentence is now claiming.
//
// MUTANTS:
//
//	M15 enforcement: delete "pr_opened" from domain.EventVocabulary
//	                                          RED  the_emitted_type_is_published
//	                                               (and NOT the two existing arms,
//	                                               which quantify over the list)
//	M16 enforcement: change the pf_pr handler's event type to "pull_request"
//	                                          RED  the_emitted_type_is_published
//	M17 publication: reword the card's T2-5 bullet back to "no published
//	    vocabulary"                           RED  K12 DEBT_GROWTH — the citation
//	                                               goes with the wording
func TestThePREventTypeIsAPublishedVocabularyEntry(t *testing.T) {
	seedPRState(t)
	body, _ := json.Marshal(prGhResult)
	prFakeGH(t, 0, string(body))
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_pr", prArgs)
	if isErr {
		t.Fatalf("pf_pr failed: %v", result)
	}
	emitted, _ := lastBodyFor(t, f, prEventsPath)["event_type"].(string)
	if emitted == "" {
		t.Fatalf("pf_pr emitted no event_type; the whole arm is about which name it uses")
	}

	t.Run("the_emitted_type_is_published", func(t *testing.T) {
		// FLOOR: the published list has to be non-trivial, or membership in it
		// means nothing.
		if len(domain.EventVocabulary) < 20 {
			t.Fatalf("domain.EventVocabulary holds %d entries; the walk is broken",
				len(domain.EventVocabulary))
		}
		if !prVocabularyHas(emitted) {
			t.Errorf("pf_pr emits event_type=%q, which domain.EventVocabulary does not carry — so "+
				"pf_emit_event's published event_type description does not name it either "+
				"(TestEmitEventTypeDescriptionPublishesTheVocabulary derives that description from "+
				"this list).\nA type only this tool writes and no schema names is a type a reader "+
				"of the timeline can only learn by accident, which is what §6.2 T2-5 was filed "+
				"about.", emitted)
		}
		desc := publishedParamDescriptionFor(t, "pf_emit_event", "event_type")
		if !strings.Contains(desc, emitted) {
			t.Errorf("pf_emit_event's published event_type description does not name %q.\nThe "+
				"vocabulary is published on that parameter, and this card's Policy bullet now says "+
				"so; the description is the surface a caller reads, and membership in a Go slice "+
				"is not a publication.", emitted)
		}
	})

	t.Run("the_set_is_published_but_not_closed", func(t *testing.T) {
		// The "free-text" half of the card's sentence, which aihub#444 did NOT
		// change and deliberately: an MCP enum is advisory, and agent_events has
		// no CHECK, so a closed enum here would state a contract neither end keeps.
		schema := publishedInputSchema(t, "pf_emit_event")
		var decoded struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatalf("pf_emit_event InputSchema is not JSON: %v", err)
		}
		if raw, present := decoded.Properties["event_type"]["enum"]; present {
			t.Errorf("pf_emit_event.event_type publishes enum %v.\nThe card calls pr_opened a "+
				"FREE-TEXT type, which is the accurate half of its original sentence: the column "+
				"has no CHECK and EmitEvent accepts any string, so an enum states a closed set "+
				"nothing keeps.", raw)
		}
	})
}

func prVocabularyHas(typ string) bool {
	for _, v := range domain.EventVocabulary {
		if v == typ {
			return true
		}
	}
	return false
}

// publishedInputSchema returns one published tool's InputSchema as JSON, read
// off a real session.
func publishedInputSchema(t *testing.T, tool string) json.RawMessage {
	t.Helper()
	schema, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("marshal %s InputSchema: %v", tool, err)
	}
	return schema
}

// publishedParamDescriptionFor is publishedParamDescription with the schema
// lookup folded in, and a failure rather than a false when the property is
// absent: an arm asserting on a description that does not exist would otherwise
// report the wrong finding.
func publishedParamDescriptionFor(t *testing.T, tool, param string) string {
	t.Helper()
	desc, ok := publishedParamDescription(t, publishedInputSchema(t, tool), param)
	if !ok {
		t.Fatalf("%s publishes no %q property", tool, param)
	}
	return desc
}

// prSortedKeys is here only so the helper set above reads the same way as the
// neighbouring files'; sortedBodyKeys already does the work.
var _ = sort.Strings
