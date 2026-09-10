package mcp_test

// aihub#543 probe wave 1, slice C — the `docs/mcp-cards/pf_acquire_locks.md`
// sentences about WHAT LEAVES THIS PROCESS and about the PARTITION its two
// lists form.
//
//	"`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) resolves the
//	 state file and sends the three credentials — and nothing else — to
//	 `pkg/client/client.go` (`AcquireLocks`) → `POST
//	 /v1/work_items/<id>/acquire_locks`…"
//	    -> TestAcquireLocksSendsTheThreeCredentialsAndNoLockList
//	"There is no lock list on the wire: the server reads the work item's own
//	 `declared_resources`."
//	    -> TestAcquireLocksSendsTheThreeCredentialsAndNoLockList
//	"The description now states the partition: `acquired` is what THIS call
//	 took, `already_held` is every other lock the attempt holds of every type
//	 read from the lock table, the two are disjoint, and together they are the
//	 attempt's full set."
//	    -> TestPublishedLockPartitionNamesTheTwoListsTheResponseDeclares
//	"Read it as a report on the **declarations** rather than as a third lock
//	 list…"
//	    -> TestPublishedLockPartitionNamesTheTwoListsTheResponseDeclares
//
// 🔴 WHY THE EXISTING GATES CANNOT HOLD ANY OF THEM.
// TestContractEveryPublishedParamLeavesTheProcess quantifies over PUBLISHED
// parameters, and this tool publishes exactly one — `work_item_id`, which is
// spent on the URL. A body key nothing published is invisible to it by
// construction, so "and nothing else" and "there is no lock list on the wire"
// are outside its reach: an extra `requested_locks` in the body would leave it
// green. The credential rows in state_resolve_wiring_test.go's credSites() are
// the other candidate and they do not cover this tool at all — the table holds
// pf_reinforce_memory, pf_update_memory, pf_save_artifact, pf_diff, pf_commit,
// pf_push and pf_pr.
//
// The partition arm is the mirror case: TestAcquireLocksReportsEveryHeldLock
// drives the real function against a database and asserts that acquired +
// already_held equals the table, which is the ENFORCED half. Nothing compares
// that against the sentence the tool publishes, and nothing refuses a THIRD
// lock list — a response growing one keeps every DB assertion true while
// "together they are the attempt's full set" becomes false for every caller
// reading two of three.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestAcquireLocksSends|TestPublishedLockPartition' -count=1

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const acquireLocksWIID = "wi_01JACQUIRELOCKSWIRE"

// acquireLocksWirePath is the one route this tool may POST to, and the only one
// the body assertions below may read: an absence assertion made against the
// wrong request is green for a reason that has nothing to do with the claim.
const acquireLocksWirePath = "/v1/work_items/" + acquireLocksWIID + "/acquire_locks"

// acquireLocksCredentials is what the card says the body carries. Named as a
// set rather than checked one at a time, because "and nothing else" is an
// equality and a per-key loop only ever checks the presence half.
var acquireLocksCredentials = []string{"attempt_id", "claim_epoch", "session_secret"}

// lockListKeysCallersMustNotSee are the shapes a reader would reach for if the
// sentence were false: the declaration payload itself, an explicit lock
// request, or either of the response's own list names echoed back. They are
// named so a failure says which promise broke, even though the set equality
// below already refuses every one of them.
var lockListKeysCallersMustNotSee = []string{
	"declared_resources", "requested_locks", "locks", "acquired", "already_held",
}

// TestAcquireLocksSendsTheThreeCredentialsAndNoLockList is the card's hop 2-3
// claim, observed on the request a fake aihub really received.
//
// The FLOOR runs first and is a Fatalf: every interesting assertion here is
// about a key that must be ABSENT, and an absent key is indistinguishable from
// a body that carries nothing at all. A body missing the credentials fails here
// rather than satisfying the absence half by being empty.
//
// MUTANTS (applied to this tree and run; the verdict is what happened, not what
// was expected):
//
//	M1 enforcement: add `body["requested_locks"] = []any{}` to the
//	   pf_acquire_locks handler in internal/mcp/tools_lifecycle.go
//	                                            RED  the key-count equality (4
//	                                                 keys, want 3) and the
//	                                                 lock-list arm, which names
//	                                                 requested_locks
//	M2 enforcement: drop `"session_secret": sf.SessionSecret` from the same body
//	                                            RED  the FLOOR, naming
//	                                                 session_secret — not the
//	                                                 equality, which is the point
//	                                                 of ordering them
//	M3 publication: delete the citation clause from the card's hop 2-3 sentence
//	                                            RED  K12 DEBT_GROWTH — the
//	                                                 sentence stops citing an arm
//	                                                 and falls back into the debt
//	                                                 column. ⚠️ This probe does not
//	                                                 read the card, so its
//	                                                 publication side is held by
//	                                                 K12's citation binding rather
//	                                                 than by the arm itself
func TestAcquireLocksSendsTheThreeCredentialsAndNoLockList(t *testing.T) {
	seedStateFile(t, acquireLocksWIID)
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_acquire_locks", map[string]any{
		"work_item_id": acquireLocksWIID,
	})
	if isErr {
		t.Fatalf("pf_acquire_locks failed: %v — a failed reconcile reaches the server with a "+
			"body nobody should draw conclusions from", result)
	}

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("pf_acquire_locks made %d request(s), want exactly 1: %v. The card describes one "+
			"hop, and a second request is a body these assertions never looked at",
			len(calls), f.paths())
	}
	if calls[0].Path != acquireLocksWirePath {
		t.Fatalf("pf_acquire_locks POSTed to %s, want %s", calls[0].Path, acquireLocksWirePath)
	}
	body := calls[0].Body

	// FLOOR first: see the doc comment.
	for _, k := range acquireLocksCredentials {
		v, present := body[k]
		if !present || v == nil || v == "" {
			t.Fatalf("the acquire_locks body carries no usable %q (keys=%v) — this walk is broken, "+
				"and every absence assertion below would pass against a body like this",
				k, sortedBodyKeys(body))
		}
	}

	got := sortedBodyKeys(body)
	want := append([]string(nil), acquireLocksCredentials...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Errorf("the acquire_locks body carries %d key(s) %v, want exactly %v.\nThe card tells a "+
			"caller this hop sends the three credentials AND NOTHING ELSE, and that there is no "+
			"lock list on the wire — which is what makes the call a reconcile of the work item's "+
			"stored declarations rather than a request the caller composes. A fourth key here is a "+
			"second, undocumented way to ask.", len(got), got, want)
	}
	for _, k := range lockListKeysCallersMustNotSee {
		if _, present := body[k]; present {
			t.Errorf("the acquire_locks body carries %q. The server reads the work item's own "+
				"declared_resources; a lock list on the wire would mean two sources for the same "+
				"answer, and the card promises there is one.", k)
		}
	}
}

// TestPublishedLockPartitionNamesTheTwoListsTheResponseDeclares binds the
// sentence the description makes to the response type that has to keep it.
//
// The two list names are read off AcquireLocksResponse by reflection rather
// than written here, so "the constant moved but the description did not" is red
// as readily as the reverse — the base-strength precedent. The COUNT is the
// other half: two lock lists partition a set, three do not, and no arm anywhere
// refused a third one.
//
// MUTANTS:
//
//	M4 enforcement: add `Retained []ResourceLock \`json:"retained"\`` to
//	   domain.AcquireLocksResponse                RED  a_third_lock_list, naming
//	                                                   retained; the description
//	                                                   arms stay green, which is
//	                                                   what says the halves are
//	                                                   separable
//	M5 enforcement: rename the AlreadyHeld json tag to `held`
//	                                              RED  the_description_names_both
//	   ⚠️ GREEN on the first version of this arm, which searched the description
//	   for the bare key: "held" occurs inside "already_held" in that same
//	   description, so the rename was invisible. The arm now looks for the
//	   BACKTICKED token, which is also the form the description writes.
//	M6 publication: delete " The two are disjoint and together are the attempt's
//	   full lock set. " from the tool description RED  the_description_states_the
//	                                                   _partition, naming disjoint
//	M7 publication: delete both citations from the card's hop 0-1 partition
//	   sentence                                   RED  K12 DEBT_GROWTH
//	G1 control:     reword that card sentence, leaving the description and the
//	   struct alone                             GREEN  this arm reads the live
//	                                                   description and the struct;
//	                                                   the card's own text is K12's
//	                                                   half
func TestPublishedLockPartitionNamesTheTwoListsTheResponseDeclares(t *testing.T) {
	typ := reflect.TypeOf(domain.AcquireLocksResponse{})
	lockList := reflect.TypeOf([]domain.ResourceLock{})

	var lockListKeys, otherKeys []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		if field.Type == lockList {
			lockListKeys = append(lockListKeys, tag)
			continue
		}
		otherKeys = append(otherKeys, tag)
	}
	sort.Strings(lockListKeys)

	// FLOOR: a walk that found fewer than two lists is not measuring a partition,
	// and every assertion below would be about whatever it did find.
	if len(lockListKeys) < 2 {
		t.Fatalf("%s declares %d list(s) of ResourceLock %v — this walk is broken, and a "+
			"partition of fewer than two things is not one", typ.Name(), len(lockListKeys), lockListKeys)
	}

	t.Run("a_third_lock_list", func(t *testing.T) {
		if len(lockListKeys) != 2 {
			t.Errorf("%s declares %d lists of ResourceLock %v, want exactly 2.\nThe description "+
				"promises the two are DISJOINT and TOGETHER the attempt's full lock set. A third "+
				"list keeps both of those literally true of the pair while making the answer a "+
				"caller assembles from them incomplete, and every existing assertion — including "+
				"TestAcquireLocksReportsEveryHeldLock's acquired+already_held equality — stays "+
				"green. Publish it in the description, or do not add it.",
				typ.Name(), len(lockListKeys), lockListKeys)
		}
		// The card's own gloss on the third KEY: it is a report on the
		// declarations, not a lock list. Checked as a property of the type,
		// because "not a third lock list" is only worth saying while the key
		// exists and is something else.
		field, _, ok := jsonFieldOf(typ, "unrecognized_resources")
		if !ok {
			t.Errorf("%s declares no unrecognized_resources field. The card's hop 5 describes it as "+
				"the third key and says in the same breath that it is not a third lock list; with "+
				"the key gone that sentence is about nothing.", typ.Name())
		} else if field.Type == lockList {
			t.Errorf("%s.unrecognized_resources is %s — the card tells a caller to read it as a "+
				"report on the DECLARATIONS rather than as a third lock list, and a []ResourceLock "+
				"makes that instruction false", typ.Name(), field.Type)
		}
	})

	desc := publishedTool(t, "pf_acquire_locks").Description

	t.Run("the_description_names_both", func(t *testing.T) {
		for _, key := range lockListKeys {
			// The BACKTICKED form, and that is measured rather than tidy. A plain
			// substring search passes `already_held` -> `held` unnoticed, because
			// "held" occurs inside "already_held" in the same description: the
			// mutant renaming the tag left this arm green. The description writes
			// every response key in backticks, so the token form is both what a
			// reader sees and what a rename cannot satisfy by accident.
			if !strings.Contains(desc, "`"+key+"`") {
				t.Errorf("the pf_acquire_locks description never names %q, which is one of the two "+
					"lock lists its response declares (%v).\naihub#345 exists because a caller read "+
					"an unmentioned population as an empty one; a list the description does not name "+
					"is that failure with the names swapped. Description:\n%s", key, lockListKeys, desc)
			}
		}
		// The reverse direction, and the reason the loop above is not enough: a
		// name in the prose that the struct no longer carries sends a caller
		// looking for a key nothing emits.
		for _, key := range otherKeys {
			if key == "unrecognized_resources" {
				// Deliberately unpublished on this tool: hop 5 of the card records
				// that it is `omitempty`, that no observed or live response has ever
				// carried it, and that it is therefore in neither key file.
				continue
			}
			if !strings.Contains(desc, key) {
				t.Logf("note: %s carries %q, which the description does not name — not a failure "+
					"here, since only the two lock lists are what the partition sentence is about",
					typ.Name(), key)
			}
		}
	})

	t.Run("the_description_states_the_partition", func(t *testing.T) {
		// These two are the sentence, not decoration: "disjoint" is what stops a
		// caller double-counting a lock reported twice, and "full lock set" is
		// what makes the union answerable at all. aihub#345's whole finding was a
		// caller acting on one list as though it were the set.
		for _, phrase := range []string{"disjoint", "full lock set"} {
			if !strings.Contains(desc, phrase) {
				t.Errorf("the pf_acquire_locks description does not say %q.\nThe card's hop 0-1 "+
					"calls this description a FIX rather than decoration: without the partition "+
					"stated, `already_held: []` reads as \"this attempt holds no locks\" while the "+
					"server goes on enforcing locks it never mentioned. Description:\n%s", phrase, desc)
			}
		}
	})
}

// aihub509KeyCarriers are the two responses that GAINED the
// `unrecognized_resources` key in aihub#509, which is what makes the card's
// corpus-window argument apply to them.
//
// 🔴 pf_claim_work_item is excluded, and the exclusion is MEASURED rather than
// assumed: its card has listed the key in `response_keys_observed` since the
// card set was created (`c9a2760`), because on that path the key predates
// aihub#412's extraction window. So "the window closed before the key existed"
// is a per-card fact, not a repo-wide one — and an arm that quantified over all
// three carriers would report the claim card as a violation of a sentence that
// was never about it. The exclusion is asserted below in the other direction,
// so it cannot quietly widen into "whichever card is inconvenient".
var aihub509KeyCarriers = []string{"pf_acquire_locks", "pf_force_takeover"}

// TestTheUnrecognizedResourcesKeyIsInNeitherPublishedKeyList is the card's own
// account of why a real response key appears in neither of the two files that
// declare response keys.
//
// The claim is a negative about two generated artefacts, so the FLOOR carries
// it: the key has to be really declared by the response types, or "absent from
// both lists" is only a statement about a key that does not exist.
//
// ⚠️ What this arm does NOT hold is the last clause: "an invented entry there
// fails that arm as readily as a missing one". That is a property of K10's
// golden-file EQUALITY, which lives in
// internal/mcp/card_response_keys_live_e2e_db_test.go
// (TestE2ELiveResponseKeysAreDeclaredOnTheCards) and needs a database and a
// live walk to run. The card cites both arms for that reason.
//
// MUTANTS:
//
//	M8  enforcement: add "unrecognized_resources" to pf_acquire_locks.md's
//	    response_keys_observed                  RED  absent_from_response_keys
//	                                                 _observed/pf_acquire_locks
//	M9  enforcement: add an entry for the key to live-response-keys.json under
//	    pf_claim_work_item                      RED  absent_from_live_response
//	                                                 _keys
//	M10 enforcement: remove the key from pf_claim_work_item.md's
//	    response_keys_observed                  RED  the_claim_card_carries_it —
//	                                                 the exclusion above is checked
//	                                                 in both directions, so
//	                                                 emptying it out is not a way
//	                                                 to widen the scope quietly
//	M11 enforcement: rename AcquireLocksResponse's json tag to
//	    `unmapped_resources`                    RED  the FLOOR — the absence arms
//	                                                 do NOT go green, which is the
//	                                                 point of ordering them
//	M12 publication: delete the citation clause from the card's hop 5 sentence
//	                                            RED  K12 DEBT_GROWTH
func TestTheUnrecognizedResourcesKeyIsInNeitherPublishedKeyList(t *testing.T) {
	const key = "unrecognized_resources"

	// FLOOR: see the doc comment.
	if len(unrecognizedResourceCarriers) < 2 {
		t.Fatal("fewer than two carriers of the report — this walk is broken")
	}
	for tool, typ := range unrecognizedResourceCarriers {
		if _, _, ok := jsonFieldOf(typ, key); !ok {
			t.Fatalf("%s's response type %s declares no %q field, so every absence assertion "+
				"below is about a key nothing emits", tool, typ.Name(), key)
		}
	}

	cards := readCards(t)
	declaredOn := func(t *testing.T, tool string) (string, map[string]bool) {
		t.Helper()
		c, ok := cards[tool]
		if !ok {
			t.Fatalf("no card for %s", tool)
		}
		out := map[string]bool{}
		for _, k := range c.block.ResponseKeysObserved {
			out[k] = true
		}
		if len(out) == 0 {
			t.Fatalf("%s declares no response_keys_observed at all, so this arm cannot tell a "+
				"deliberate absence from a card with no machine block", c.path)
		}
		return c.path, out
	}

	t.Run("absent_from_response_keys_observed", func(t *testing.T) {
		for _, tool := range aihub509KeyCarriers {
			t.Run(tool, func(t *testing.T) {
				path, declared := declaredOn(t, tool)
				if declared[key] {
					t.Errorf("%s lists %q in response_keys_observed.\nThat block is generated from "+
						"aihub#412's corpus, whose window closed before this response gained the key, "+
						"so an entry here was either hand-written into a machine block that "+
						"PF_CARDS_REGEN=1 overwrites, or the corpus has been re-extracted — in which "+
						"case the card's prose explaining the absence is now false and has to go "+
						"with it.", path, key)
				}
			})
		}
	})

	t.Run("the_claim_card_carries_it", func(t *testing.T) {
		path, declared := declaredOn(t, "pf_claim_work_item")
		if !declared[key] {
			t.Errorf("%s does NOT list %q in response_keys_observed.\nThe scope above excludes this "+
				"card because on the claim path the key predates aihub#412's window and the corpus "+
				"therefore recorded it. With the entry gone, that exclusion is exempting a card "+
				"for a reason that no longer holds — which is how a scoped arm ends up measuring "+
				"less than its name claims.", path, key)
		}
	})

	t.Run("absent_from_live_response_keys", func(t *testing.T) {
		golden := readLiveKeysFile(t)
		// FLOOR on the file too: an empty golden file makes every absence below
		// vacuous, and the file is not empty on a healthy tree.
		if len(golden.Keys) == 0 {
			t.Fatalf("%s declares no keys at all — this arm cannot tell a deliberate absence from "+
				"an empty file", liveKeysFileRel)
		}
		for tool, keys := range golden.Keys {
			if reason, present := keys[key]; present {
				t.Errorf("%s declares %s.%s (%q).\nThe card's argument for the absence is that the "+
					"key is `omitempty` and every work item the K10 live walk drives declares no "+
					"resources, so no live response can carry it — and K10's golden file is an "+
					"equality against what that walk just saw, so an entry the walk cannot produce "+
					"fails there too.", liveKeysFileRel, tool, key, reason)
			}
		}
	})
}
