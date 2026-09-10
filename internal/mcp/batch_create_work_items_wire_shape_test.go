package mcp_test

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_batch_create_work_items.md`
// sentences about what the batch handler does per item, driven end to end
// against a fake aihub.
//
//	"There is **no batch endpoint**. The handler loops and makes one
//	 `POST /v1/work_items` per item … the same hop-3 as `pf_create_work_item`,
//	 N times."  /  "§6.1 T1-4 — the same CHECK-versus-schema gap … applies to
//	 every item here, since hop 3 is the same handler."
//	    -> TestBatchPostsEveryItemToTheSingleCreateRoute
//	"A batch item that omits it reaches the same `unclassified[]` segment as a
//	 single create that omits it; there is no batch-specific default."
//	    -> TestBatchAppliesNoRequiresHumanSessionDefaultOfItsOwn
//	"- **An item with no `goal` fails locally** and is reported with its index
//	 rather than sent."
//	    -> TestBatchReportsALocalFailureAgainstItsOwnIndex
//	"The guard is … `validateJSONObjectParam` …, which runs on the server inside
//	 `CreateWorkItem` — so unlike the missing-`goal` check above it costs a round
//	 trip, and unlike a whole-batch refusal it takes exactly one item down…"
//	    -> TestBatchPerItemRejectionCostsARoundTripAndTakesOneItemDown
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestBatchPostsEveryItem|TestBatchAppliesNo|TestBatchReportsALocal|TestBatchPerItemRejection' -count=1 -v

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// driveBatch calls pf_batch_create_work_items against a fake aihub whose reply
// per item is decided by reply, and hands back the requests it saw.
//
// reply is given the decoded request body rather than an index so a fixture can
// key its answer on the ITEM — an index-keyed fake would agree with the handler
// about the order it sent things, which is one of the properties under test.
func driveBatch(t *testing.T, args map[string]any,
	reply func(body map[string]any) (int, any)) ([]recordedCall, map[string]any, bool) {
	t.Helper()
	f := newFakeAihub(t)
	f.on(createWirePath, func(body map[string]any) (int, any) {
		if reply != nil {
			return reply(body)
		}
		return http.StatusOK, map[string]any{
			"id": "wi_" + fmt.Sprint(len(f.recorded())), "slug": "aihub#1", "goal": body["goal"],
		}
	})
	result, isErr := callTool(t, f, batchToolName, args)
	return f.recorded(), result, isErr
}

// batchFailures decodes the `failed` array into index/error pairs, failing the
// test rather than returning an empty slice when the shape is wrong — an empty
// `failed` and an unparseable one are different findings and only one of them is
// about the handler.
func batchFailures(t *testing.T, result map[string]any) []struct {
	Index   int
	Message string
	Goal    string
} {
	t.Helper()
	raw, ok := result["failed"].([]any)
	if !ok {
		t.Fatalf("the batch response carries no `failed` array (got %#v). The card tells callers "+
			"to read `created_count` and `failed` rather than `ok`, so a response without it "+
			"leaves a partially successful batch unrecoverable.", result["failed"])
	}
	out := make([]struct {
		Index   int
		Message string
		Goal    string
	}, 0, len(raw))
	for i, entry := range raw {
		m, isMap := entry.(map[string]any)
		if !isMap {
			t.Fatalf("failed[%d] is %#v, not an object — the per-item index and error cannot be "+
				"read out of it", i, entry)
		}
		idx, hasIndex := m["index"].(float64)
		if !hasIndex {
			t.Errorf("failed[%d] carries no numeric `index` (%#v). Without it a retry cannot tell "+
				"which item to resend, which is the whole reason per-item failures are reported "+
				"instead of aborting the batch.", i, m)
		}
		msg, _ := m["error"].(string)
		goal, _ := m["goal"].(string)
		out = append(out, struct {
			Index   int
			Message string
			Goal    string
		}{Index: int(idx), Message: msg, Goal: goal})
	}
	return out
}

// TestBatchPostsEveryItemToTheSingleCreateRoute is the no-batch-endpoint arm.
//
// WHY IT HAD NO ARM. TestBatchCreateSendsOneCallPerItemAndInheritsProject counts
// the calls and reads their bodies, and it registers its fake handler ON
// /v1/work_items — so a handler that posted somewhere else would produce two
// calls the fake answered with its default {"ok":true} and the existing arm would
// pass, having asserted the project default against a route nobody named. The
// PATH is what makes "the same hop-3 as pf_create_work_item, N times" a
// measurement, and it was the one thing not asserted.
//
// It matters beyond tidiness because both cards hang a second claim off it: the
// scenario CHECK-versus-schema gap applies to every batch item BECAUSE hop 3 is
// the same handler. If the batch ever grew its own endpoint, that inherited
// claim would silently stop following.
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the handler, the card untouched) ──
//	M40 post each item to /v1/work_items/batch
//	                                          RED  every_item_posts_to_the_create_route
//	M41 coalesce the loop: issue the request for item 0 only and report the rest as
//	    created                               RED  one_request_per_item
//	M42 have the create tool post somewhere else while the batch does not
//	                                    NOT APPLIED, and the reason is the finding:
//	                                          the two tools cannot diverge, because
//	                                          both go through one pkg/client method
//	                                          (M40 moves both at once). So the
//	                                          same-route subtest is a control against
//	                                          a change that ADDS a second client
//	                                          method — which is exactly the change
//	                                          that would break the batch card's
//	                                          inheritance and is stated here rather
//	                                          than left as a green nobody can move
//	── publication side (the cards, the handler untouched) ──
//	M43 strip the citing sentence's citation, path and symbol both, on either card
//	                                          RED  K12 ledger drift on that card//
//
// ⚠️ The publication-side mutant strips the citation's FILE PATH as well as its
// test symbol, and that is not tidiness. Measured 2026-09-10: de-backticking the
// symbol alone left every one of this wave's seventeen sentences still counted as
// Cited, because cardclaims.CitesAnArm is satisfied by EITHER anchor — so the
// publication side of a citation is only as strong as whichever anchor a later
// edit leaves behind. Reported as an incidental finding of aihub#576.
func TestBatchPostsEveryItemToTheSingleCreateRoute(t *testing.T) {
	const items = 3
	entries := make([]any, 0, items)
	for i := 0; i < items; i++ {
		entries = append(entries, map[string]any{
			"goal": fmt.Sprintf("batch probe item %d, distinct enough to skip dedup", i),
		})
	}

	calls, result, isErr := driveBatch(t, map[string]any{
		"project": "aihub", "items": entries,
	}, nil)
	if isErr {
		t.Fatalf("the batch call failed, so nothing was observed on the wire: %v", result)
	}

	t.Run("one_request_per_item", func(t *testing.T) {
		if len(calls) != items {
			t.Errorf("%d item(s) produced %d request(s): %v. The card says the handler LOOPS — one "+
				"POST per item — so a different count means either a batch endpoint appeared or "+
				"items are being coalesced, and in both cases the per-item `index` in `failed` "+
				"stops meaning what it says.", items, len(calls), pathsOf(calls))
		}
	})

	t.Run("every_item_posts_to_the_create_route", func(t *testing.T) {
		for i, c := range calls {
			if c.Path != createWirePath || c.Method != http.MethodPost {
				t.Errorf("item %d went to %s %s, want POST %s. There is no batch endpoint; both "+
					"cards say hop 3 is the single create handler, and the scenario "+
					"CHECK-versus-schema gap the batch card inherits is inherited THROUGH that "+
					"fact.", i, c.Method, c.Path, createWirePath)
			}
		}
	})

	// The control that ties the two cards together: the route the batch uses is
	// the route the single create uses. Asserted by driving both rather than by
	// comparing each against the same constant, which would agree with itself.
	t.Run("the_same_route_the_single_create_uses", func(t *testing.T) {
		single, _, singleErr := driveCreate(t, map[string]any{
			"project": "aihub", "goal": "a single create posted for comparison with the batch",
		})
		if singleErr || len(single) != 1 {
			t.Fatalf("the comparison create failed (%d call(s), err=%v), so there is nothing to "+
				"compare the batch's route against", len(single), singleErr)
		}
		if single[0].Path != calls[0].Path {
			t.Errorf("%s posts to %q and %s posts to %q. The two cards state ONE hop 3; two "+
				"routes means every claim either card inherits from the other — the scenario "+
				"CHECK, the dedup behaviour, the attrs guard — has to be re-established for each.",
				createToolName, single[0].Path, batchToolName, calls[0].Path)
		}
	})
}

// TestBatchAppliesNoRequiresHumanSessionDefaultOfItsOwn is the third-state arm
// on the batch path.
//
// WHY IT HAD NO ARM. TestRequiresHumanSessionPublishesItsThirdState covers what
// both tools SAY, including the batch tool's nested copy. What the batch card
// adds is a behavioural claim: an item that omits the field reaches the same
// NULL, because the handler invents nothing. The handler defaults three things
// per item (`project`, `force_reason`, and nothing else), and a fourth default
// added here would be invisible to every existing arm while quietly moving every
// unclassified batch item into `items[]` — the segment that means "takeable now
// by an agent".
//
// 🔴 Absence is asserted against a POSITIVE control in the same batch: item 1
// sends `false` explicitly and must arrive carrying it. Without that, "the key is
// absent" is also true of a handler that dropped the field altogether, which
// would be the same wrong answer with the opposite cause.
//
// MUTANTS.
//
//	── enforcement side ──
//	M44 default an omitted requires_human_session to false in the batch loop
//	                                          RED  omitted_item_carries_no_key
//	M45 default it to true                    RED  omitted_item_carries_no_key
//	M46 strip requires_human_session from every item
//	                                          RED  explicit_false_still_travels — the
//	                                               control, which is why it is here
//	── publication side ──
//	M47 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestBatchAppliesNoRequiresHumanSessionDefaultOfItsOwn(t *testing.T) {
	const field = "requires_human_session"

	calls, result, isErr := driveBatch(t, map[string]any{
		"project": "aihub",
		"items": []any{
			map[string]any{"goal": "an item that says nothing about who has to be present"},
			map[string]any{"goal": "an item that says an agent may take it unattended", field: false},
		},
	}, nil)
	if isErr || len(calls) != 2 {
		t.Fatalf("expected two forwarded creates, got %d (err=%v): %v", len(calls), isErr, result)
	}

	t.Run("omitted_item_carries_no_key", func(t *testing.T) {
		if got, present := calls[0].Body[field]; present {
			t.Errorf("an item that omitted %s reached the wire carrying %#v. Omission is the third "+
				"state: it stores NULL and puts the wi in the ready queue's unclassified[] segment, "+
				"which items[] and pf_list_work_items' ready_only both exclude. A batch-specific "+
				"default would hand unclassified work straight to an agent — and the batch is where "+
				"a caller files the most items with the least attention per item.", field, got)
		}
	})

	t.Run("explicit_false_still_travels", func(t *testing.T) {
		got, present := calls[1].Body[field]
		if !present || got != false {
			t.Errorf("an item that sent %s=false arrived with %#v (present=%v). The absence "+
				"asserted above is only evidence about DEFAULTING if an explicit value gets "+
				"through; otherwise both subtests pass on a handler that drops the field.",
				field, got, present)
		}
	})

	// The comparison the card actually makes: the batch reaches the same state as
	// a single create that omits it. Driven rather than assumed.
	t.Run("the_same_omission_a_single_create_makes", func(t *testing.T) {
		single, _, singleErr := driveCreate(t, map[string]any{
			"project": "aihub", "goal": "a single create that says nothing about human presence",
		})
		if singleErr || len(single) != 1 {
			t.Fatalf("the comparison create failed (%d call(s), err=%v)", len(single), singleErr)
		}
		_, singlePresent := single[0].Body[field]
		_, batchPresent := calls[0].Body[field]
		if singlePresent != batchPresent {
			t.Errorf("%s sends %s (present=%v) and %s sends it (present=%v) for the same omission. "+
				"The card says there is no batch-specific default; two different requests for the "+
				"same input means one of the two paths is deciding something the caller did not.",
				createToolName, field, singlePresent, batchToolName, batchPresent)
		}
	})
}

// TestBatchReportsALocalFailureAgainstItsOwnIndex is the missing-goal arm.
//
// WHY IT HAD NO ARM. TestBatchCreateRejectsMalformedItemsWithoutSendingThem
// asserts the COUNTS — two failures, one call, one create — for a batch holding a
// goal-less item and a non-object. It never reads an index, so it cannot tell a
// failure reported against the right item from two failures reported against
// index 0, or against the loop counter of the surviving item. And
// TestBatchCreateContinuesPastAFailedItem does assert an index, but only for a
// failure the SERVER produced; the local path builds its own entry in a different
// branch, with different keys, and that branch had no observation of its index at
// all.
//
// The middle position in the fixture is deliberate. An index reported as 0 is
// indistinguishable from a correct answer when the failing item is first, and
// indistinguishable from `len(items)-1` when it is last.
//
// MUTANTS.
//
//	── enforcement side ──
//	M48 report the local failure with no index at all
//	                                          RED  batchFailures (no numeric index)
//	M49 report it against `len(raw)-1`        RED  the_failing_items_own_index
//	M50 drop the `continue`, so the goal-less item is reported AND sent
//	                                          RED  it_costs_no_round_trip
//	M51 return errResult on a local failure   RED  its_siblings_are_still_created
//	── publication side ──
//	M52 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestBatchReportsALocalFailureAgainstItsOwnIndex(t *testing.T) {
	// The goal-less item is index 1 of 3: not first, not last.
	const failing = 1
	calls, result, isErr := driveBatch(t, map[string]any{
		"project": "aihub",
		"items": []any{
			map[string]any{"goal": "the first item, which is fine and should be created"},
			map[string]any{"wi_type": "chore"},
			map[string]any{"goal": "the third item, which is also fine"},
		},
	}, nil)
	if isErr {
		t.Fatalf("a per-item failure must not fail the whole tool call: %v", result)
	}

	failures := batchFailures(t, result)

	t.Run("the_failing_items_own_index", func(t *testing.T) {
		if len(failures) != 1 {
			t.Fatalf("expected exactly one failure, got %d: %#v", len(failures), failures)
		}
		if failures[0].Index != failing {
			t.Errorf("the goal-less item is at index %d and the failure is reported against %d. A "+
				"retry resends by index, so a wrong one resends an item that already landed — "+
				"which then trips dedup — and leaves the broken one unfiled.", failing, failures[0].Index)
		}
		if !strings.Contains(failures[0].Message, "goal is required") {
			t.Errorf("the failure reads %q, want it to say what was wrong with the item. An index "+
				"with no reason tells a caller which item to look at and nothing about what to "+
				"change.", failures[0].Message)
		}
	})

	t.Run("it_costs_no_round_trip", func(t *testing.T) {
		if len(calls) != 2 {
			t.Errorf("%d request(s) were made for a batch with one goal-less item out of three: "+
				"%v. The card contrasts this check with the per-item `attrs` guard precisely on "+
				"cost — this one is local and spends nothing, that one is on the server and spends "+
				"a round trip.", len(calls), pathsOf(calls))
		}
		for i, c := range calls {
			if goal, _ := c.Body["goal"].(string); goal == "" {
				t.Errorf("request %d carried an empty goal, so the goal-less item was sent after "+
					"all: %#v", i, c.Body)
			}
		}
	})

	t.Run("its_siblings_are_still_created", func(t *testing.T) {
		if got := result["created_count"]; got != float64(2) {
			t.Errorf("created_count = %v, want 2. Items are created INDEPENDENTLY; a caller mistake "+
				"in one entry must not withdraw the two beside it, because re-sending the whole "+
				"batch would then trip dedup on whatever did land.", got)
		}
		if got := result["ok"]; got != false {
			t.Errorf("ok = %v, want false — `ok` is len(failed) == 0, so a partially successful "+
				"batch answers false while having created real work items. That is why the card "+
				"tells callers to read created_count.", got)
		}
	})
}

// TestBatchPerItemRejectionCostsARoundTripAndTakesOneItemDown is the per-item
// server-rejection arm.
//
// WHY IT HAD NO ARM. TestStringifiedObjectParamIsRejected drives the attrs guard
// through CreateWorkItem itself, which is the right place for the rejection's
// SHAPE — the message, the byte count, details.string_decodes_to. The batch
// card's claim is about what that rejection does to a BATCH, and it is stated as
// two contrasts: unlike the local missing-goal check it costs a round trip, and
// unlike a whole-batch refusal it takes exactly one item down. Neither contrast
// is observable from inside domain, and no arm in this package drove a
// server-side per-item 400 at all — TestBatchCreateContinuesPastAFailedItem uses
// a 409 DUPLICATE, which is the outcome the card calls normal rather than the one
// it calls a caller mistake.
//
// 🔴 The round-trip half is asserted as a REQUEST, not inferred from a status.
// "The guard runs on the server" and "the handler refused it locally" produce the
// same `failed` entry to a reader of the response; only the recorded call
// separates them, which is the whole reason the card draws the contrast.
//
// MUTANTS.
//
//	── enforcement side ──
//	M53 pre-screen a non-object attrs in the batch loop and skip the send
//	                                          RED  the_rejected_item_was_sent
//	M54 abort the batch when an item is refused with 400
//	                                          RED  every_well_formed_sibling_is_created
//	M55 report the 400 without the item's index
//	                                          RED  batchFailures (no numeric index)
//	M56 drop the server's message from the failed entry
//	                                          RED  the_servers_own_words_reach_the_caller
//	── publication side ──
//	M57 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestBatchPerItemRejectionCostsARoundTripAndTakesOneItemDown(t *testing.T) {
	const failing = 1
	const serverMessage = "attrs must be a JSON object; got a JSON string of 21 bytes"

	// The fake stands in for the server-side guard: it refuses the one item whose
	// attrs is a string and accepts the rest. Standing in for it is correct here
	// — the rejection's own shape is domain's to assert and is asserted there;
	// what this arm is about is what the BATCH does with a 400 that arrives.
	calls, result, isErr := driveBatch(t, map[string]any{
		"project": "aihub",
		"items": []any{
			map[string]any{"goal": "the first item, well formed, must be created"},
			map[string]any{"goal": "the second item, whose attrs is a stringified object",
				"attrs": `{"escaped":"badly"}`},
			map[string]any{"goal": "the third item, well formed, must also be created"},
		},
	}, func(body map[string]any) (int, any) {
		if _, isString := body["attrs"].(string); isString {
			return http.StatusBadRequest, map[string]any{
				"code":    "BAD_REQUEST",
				"message": serverMessage,
				"details": map[string]any{"field": "attrs", "string_decodes_to": "a JSON object"},
			}
		}
		return http.StatusOK, map[string]any{"id": "wi_ok", "slug": "aihub#2", "goal": body["goal"]}
	})
	if isErr {
		t.Fatalf("a per-item 400 must not fail the whole tool call: %v", result)
	}

	failures := batchFailures(t, result)

	t.Run("the_rejected_item_was_sent", func(t *testing.T) {
		if len(calls) != 3 {
			t.Fatalf("%d request(s) were made for three items: %v. The attrs guard is on the "+
				"SERVER, so every item — including the one that will be refused — costs a round "+
				"trip. That contrast with the local missing-goal check is what the card states, "+
				"and a handler that pre-screened attrs here would make it false.",
				len(calls), pathsOf(calls))
		}
		var sentTheBadOne bool
		for _, c := range calls {
			if _, isString := c.Body["attrs"].(string); isString {
				sentTheBadOne = true
			}
		}
		if !sentTheBadOne {
			t.Errorf("no request carried the stringified attrs, so the rejection did not come from " +
				"the server. The card says this guard costs a round trip precisely because it is " +
				"not in this process; a local pre-screen would be a different contract with the " +
				"same response shape.")
		}
	})

	t.Run("exactly_one_item_is_taken_down", func(t *testing.T) {
		if len(failures) != 1 {
			t.Fatalf("expected exactly one failure, got %d: %#v", len(failures), failures)
		}
		if failures[0].Index != failing {
			t.Errorf("the malformed item is at index %d and the failure names %d", failing, failures[0].Index)
		}
	})

	t.Run("the_servers_own_words_reach_the_caller", func(t *testing.T) {
		if !strings.Contains(failures[0].Message, serverMessage) {
			t.Errorf("the failed entry reads %q and the server said %q. The rejection names the "+
				"type and the byte length and reports through details.string_decodes_to whether "+
				"the quoted text was itself valid JSON — all of which is guidance a caller can act "+
				"on, and none of which survives being replaced by a batch-level summary.",
				failures[0].Message, serverMessage)
		}
	})

	t.Run("every_well_formed_sibling_is_created", func(t *testing.T) {
		if got := result["created_count"]; got != float64(2) {
			t.Errorf("created_count = %v, want 2. A batch is where a model hand-writes N escaped "+
				"payloads in one message, so N items can carry the same serialisation mistake — "+
				"the response reports it once per index rather than as one failed call, and the "+
				"well-formed siblings still land.", got)
		}
		if got := result["failed_count"]; got != float64(1) {
			t.Errorf("failed_count = %v, want 1", got)
		}
	})
}
