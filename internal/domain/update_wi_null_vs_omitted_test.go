package domain

// aihub#543 probe wave 2, band 1 — the `docs/mcp-cards/pf_update_work_item.md`
// sentences about a value the caller CANNOT send, and about the one value the
// column accepts and Go refuses.
//
//	"`domain.buildWorkItemUpdate` gates it behind a non-nil check, so omitting the
//	 field and sending an explicit `null` are the same no-op — this tool can
//	 correct a classification but not withdraw one."
//	    -> TestAnExplicitNullOnAnUpdateIsIndistinguishableFromAnOmittedField
//	"Omitting `goal` and sending an explicit `null` are still the same no-op — the
//	 check sits inside the `req.Goal != nil` guard…"
//	    -> TestAnExplicitNullOnAnUpdateIsIndistinguishableFromAnOmittedField
//	"`work_items.goal` is `TEXT NOT NULL` and `NOT NULL` admits the empty string,
//	 so unlike the length half below there was no constraint to turn it into even
//	 a bad error."
//	    -> TestTheGoalColumnAdmitsTheEmptyStringThatGoNowRefuses
//
// 🔴 WHY THE EXISTING GATES CANNOT HOLD THEM.
//
// The three-state claim about `requires_human_session` is published-side only:
// TestRequiresHumanSessionPublishesItsThirdState reads the description and
// nothing drives the builder, so a build that published the disclaimer and wrote
// NULL back on an explicit null would be green there. On the goal side,
// TestBothWorkItemWritePathsRefuseAnEmptyGoal pins that the emptiness call sits
// inside the pointer guard — a source property — and says nothing about what the
// two spellings do to the compiled UPDATE. The distinguishability of "absent"
// from "explicitly null" is the substance of both sentences, and it is a property
// of the STATEMENT, which is what this file reads.
//
// The column claim has no arm at all. db_check_policy_test.go's registry mirrors
// CHECK constraints and NOT NULL is not one, so nothing in the tree reads the
// declaration the sentence is about — and the sentence is the reason two Go
// functions exist instead of one (aihub#507).
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run 'TestAnExplicitNullOnAnUpdate|TestTheGoalColumnAdmits' -count=1

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// nullableUpdateFields are the fields whose "explicit null" spelling the card
// says is a no-op, each with a real value that MUST change the statement.
//
// 🔴 The `changed` body is not decoration: "the null body compiles to the same
// UPDATE as the omitted body" is answered `true` by a builder that ignores the
// field in every case, which is a far worse defect than the one being pinned. It
// is the same floor discipline as the withdrawn-parameter arm's live sibling
// (internal/mcp/claim_published_word_test.go).
var nullableUpdateFields = []struct {
	field   string
	null    string
	changed string
	// column is the SET clause fragment the changed body must produce, so the
	// control cannot be satisfied by a statement that differs for some other
	// reason.
	column string
	why    string
}{
	{
		field:   "requires_human_session",
		null:    `{"priority":"high","requires_human_session":null}`,
		changed: `{"priority":"high","requires_human_session":true}`,
		column:  "requires_human_session =",
		why: "the third state: the column is nullable and this tool publishes two of its " +
			"three values, so a null that WROTE null would be a withdrawal the description " +
			"says is impossible",
	},
	{
		field:   "goal",
		null:    `{"priority":"high","goal":null}`,
		changed: `{"priority":"high","goal":"a replacement goal"}`,
		column:  "goal =",
		why: "a null that reached the emptiness check would refuse every update that does " +
			"not mention the goal, which is wider than the aihub#507 bug it fixes",
	},
	{
		field:   "content",
		null:    `{"priority":"high","content":null}`,
		changed: `{"priority":"high","content":"a body"}`,
		column:  "content =",
		why: "the same *string shape, and the field whose null spelling the echo " +
			"suppression reads separately (internal/mcp/wi_echo_slim.go)",
	},
}

// omittedUpdateBody is what every null body above must compile to.
const omittedUpdateBody = `{"priority":"high"}`

// TestAnExplicitNullOnAnUpdateIsIndistinguishableFromAnOmittedField drives the
// real builder with the two spellings and compares the statement it compiles.
//
// The bodies are decoded the way echo's c.Bind decodes them, so the *pointer*
// distinction under test is produced by the same json.Unmarshal the server runs
// rather than by a struct literal that could only ever express one of the two.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M10 enforcement: give the `req.RequiresHumanSession != nil` guard an else
//	    branch that writes NULL — the withdrawal the description says is impossible
//	                                             RED  the requires_human_session
//	                                                  CONTROL, and that is the
//	                                                  measurement worth keeping: the
//	                                                  mutant makes the null body,
//	                                                  the omitted body AND a real
//	                                                  value all emit the same SET
//	                                                  clause, so the equality half
//	                                                  stays green and the control is
//	                                                  what fires
//	M11 enforcement: make buildWorkItemUpdate skip the goal column entirely
//	                                             RED  the goal CONTROL, which is
//	                                                  what stops "identical" being
//	                                                  satisfied by a builder that
//	                                                  ignores the field
//	M12 enforcement: bind Goal as a plain string instead of *string
//	                                             DOES NOT COMPILE (4 errors in
//	                                                  work_items.go). Recorded
//	                                                  because it is the only
//	                                                  mutation that can make the two
//	                                                  spellings distinguishable at
//	                                                  all — Go's json.Unmarshal
//	                                                  cannot tell an absent field
//	                                                  from a null one for a POINTER,
//	                                                  so the equality half is a
//	                                                  property of the type and the
//	                                                  compiler is its gate. ⚠️ Read
//	                                                  with M10: the controls carry
//	                                                  this arm's discriminating
//	                                                  power, the equality assertions
//	                                                  carry the claim
//	M13 publication: drop the citation clause from either card sentence
//	                                             RED  K12 (DEBT_GROWTH for the goal
//	                                                  sentence, POPULATION_MOVED for
//	                                                  the requires_human_session one,
//	                                                  whose clause carries a sentence
//	                                                  boundary with it). ⚠️ This arm
//	                                                  does not read the card, so its
//	                                                  publication side is held by
//	                                                  K12's citation binding
func TestAnExplicitNullOnAnUpdateIsIndistinguishableFromAnOmittedField(t *testing.T) {
	omitted := buildFromJSON(t, omittedUpdateBody)

	for _, tc := range nullableUpdateFields {
		t.Run(tc.field, func(t *testing.T) {
			var req UpdateWorkItemRequest
			if err := json.Unmarshal([]byte(tc.null), &req); err != nil {
				t.Fatalf("fixture is not valid JSON: %v", err)
			}
			// The pointer half, stated separately: the two spellings must not merely
			// compile to the same statement, they must arrive as the same value. A
			// builder could paper over a non-nil pointer holding the zero value.
			if pointerIsSet(t, req, tc.field) {
				t.Errorf("an explicit `%s: null` bound to a NON-nil value. Nothing downstream "+
					"can then tell it from a real one, and %s", tc.field, tc.why)
			}

			// The same work-item id buildFromJSON binds, so the comparison below is
			// about the fields under test and not about the WHERE clause.
			withNull := buildWorkItemUpdate(&req, "wi_test")
			if withNull.Query != omitted.Query {
				t.Errorf("an explicit `%s: null` compiles to a different statement than omitting "+
					"the field:\n  null:    %s\n  omitted: %s\nThe card says the two are the same "+
					"no-op, and %s", tc.field, withNull.Query, omitted.Query, tc.why)
			}
			if fmt.Sprintf("%#v", withNull.Args) != fmt.Sprintf("%#v", omitted.Args) {
				t.Errorf("an explicit `%s: null` binds different args than omitting the field:\n"+
					"  null:    %#v\n  omitted: %#v", tc.field, withNull.Args, omitted.Args)
			}
			if withNull.CAS != omitted.CAS {
				t.Errorf("an explicit `%s: null` changed the CAS flag from %v to %v",
					tc.field, omitted.CAS, withNull.CAS)
			}

			// The control. A real value MUST reach the column, or every assertion
			// above is about a field this builder never writes.
			changed := buildFromJSON(t, tc.changed)
			if changed.Query == omitted.Query {
				t.Fatalf("a real %s value compiles to the same statement as omitting the field "+
					"(%s), so this builder writes no %s column at all and the null assertions "+
					"above are vacuous", tc.field, changed.Query, tc.field)
			}
			if !strings.Contains(changed.Query, tc.column) {
				t.Errorf("a real %s value produced %q, which contains no %q clause — the "+
					"control passed for the wrong reason", tc.field, changed.Query, tc.column)
			}
		})
	}
}

// pointerIsSet reports whether the named wire field arrived as a non-nil value.
// Written per field rather than by reflection over the json tag so a field
// renamed on the struct fails to compile here instead of silently reporting
// false — the direction that would read as compliance.
func pointerIsSet(t *testing.T, req UpdateWorkItemRequest, field string) bool {
	t.Helper()
	switch field {
	case "requires_human_session":
		return req.RequiresHumanSession != nil
	case "goal":
		return req.Goal != nil
	case "content":
		return req.Content != nil
	}
	t.Fatalf("no pointer reader for %q — add one rather than letting this arm report false", field)
	return false
}

// goalColumnDecl matches the goal column's own declaration in the migration that
// creates work_items: the type and the null constraint, in that order.
var goalColumnDecl = regexp.MustCompile(`(?i)\bgoal\s+TEXT\s+NOT\s+NULL\b`)

// emptinessPredicates are the shapes a CHECK would take if the DATABASE refused
// an empty goal. None may appear in the effective predicate: the sentence's whole
// point is that the column accepts what Go now refuses, which is why the refusal
// needed a second Go function rather than a tightened mirror.
var emptinessPredicates = []string{"<> ''", "!= ''", "length(goal) > 0", "btrim", "char_length(goal) > 0"}

// TestTheGoalColumnAdmitsTheEmptyStringThatGoNowRefuses reads the DDL and the Go
// pair together, because the sentence is about the gap between them.
//
// MUTANTS (applied to this tree and run):
//
//	M14 enforcement: add `AND goal <> ''` to work_items_goal_check in
//	    0002_work_items.sql                      RED  names the predicate — the
//	                                                  database would then refuse
//	                                                  what the card says it admits,
//	                                                  and validateWorkItemGoalPresent
//	                                                  would no longer be the only
//	                                                  refusal
//	M15 enforcement: make validateWorkItemGoalShape refuse ""
//	                                             RED  the mirror arm — and also RED
//	                                                  on TestGoalRequirednessIsNotFoldedIntoTheCheckMirror,
//	                                                  which is the overlap that
//	                                                  shows this half is not new
//	M16 enforcement: change the column to `goal TEXT` in the migration
//	                                             RED  the declaration arm
//	M17 publication: drop the citation clause from the card sentence
//	                                             RED  K12 DEBT_GROWTH
func TestTheGoalColumnAdmitsTheEmptyStringThatGoNowRefuses(t *testing.T) {
	// ── the declaration ────────────────────────────────────────────────────
	const decl = "0002_work_items.sql"
	raw, err := os.ReadFile(filepath.Join(migrationsDir, decl))
	if err != nil {
		t.Fatalf("read %s: %v — the DDL is this arm's subject, so an unreadable migration is a "+
			"failure rather than an empty pass", decl, err)
	}
	if !goalColumnDecl.MatchString(string(raw)) {
		t.Errorf("%s no longer declares the goal column as `TEXT NOT NULL`. The card's account "+
			"of the aihub#507 defect rests on that pair: NOT NULL is what made the column look "+
			"guarded, and TEXT is what let the empty string through it. If the column is now "+
			"nullable, the story a caller is told about `goal: \"\"` needs rewriting too.", decl)
	}

	// ── the effective CHECK, replayed across every migration ───────────────
	//
	// effectiveDBChecks folds the whole migrations directory in order, so this
	// covers "unaltered by any later migration" rather than only what 0002 said.
	checks := effectiveDBChecks(t)
	fact, ok := checks["work_items.work_items_goal_check"]
	if !ok {
		t.Fatalf("the migrations no longer define work_items.work_items_goal_check (found %d "+
			"check(s)). Every assertion below would then be about a constraint that does not "+
			"exist, and the Go mirror db_check_policy_test.go registers would be mirroring "+
			"nothing", len(checks))
	}
	for _, pred := range emptinessPredicates {
		if strings.Contains(strings.ToLower(fact.Predicate), strings.ToLower(pred)) {
			t.Errorf("work_items_goal_check now contains %q:\n    %s\nThe column would then "+
				"refuse the empty string itself, and the card's reason for keeping "+
				"validateWorkItemGoalPresent separate from the CHECK's Go mirror — that the "+
				"mirror must not enforce what the database does not — is no longer the "+
				"situation. Update both.", pred, fact.Predicate)
		}
	}
	if !strings.Contains(fact.Predicate, "length(goal)") {
		t.Errorf("work_items_goal_check no longer bounds length(goal):\n    %s\nThe absence "+
			"assertions above are then about a predicate that stopped being the one the card "+
			"describes", fact.Predicate)
	}

	// ── the two Go functions, on the one value that tells them apart ───────
	if err := validateWorkItemGoalShape(""); err != nil {
		t.Errorf("validateWorkItemGoalShape(\"\") = %v. It is the declared Go mirror of a CHECK "+
			"that admits the empty string, so refusing it here makes the correspondence "+
			"db_check_policy_test.go asserts false while that test keeps passing", err)
	}
	if err := validateWorkItemGoalPresent(""); err == nil {
		t.Error("validateWorkItemGoalPresent(\"\") = nil, so nothing refuses an empty goal. The " +
			"column admits it, NOT NULL does not, and no CHECK does — which is exactly why the " +
			"refusal has to live in Go, and why aihub#507 was a silent 200 rather than a bad " +
			"error")
	}
}
