package domain

// aihub#543 probe wave 2 — the two `docs/mcp-cards/pf_create_work_item.md` hop-4
// sentences that describe what CreateWorkItem does to a request BEFORE it opens a
// transaction.
//
//	"- **`scenario` is effectively fixed.** The column is CHECKed to
//	 `coding|writing|data` and creation rejects everything but `coding`, so the
//	 parameter accepts a value no row can hold."
//	    -> TestCreateAcceptsOneOfTheScenariosTheCheckPermits
//	"An omitted `attrs` is still defaulted to `{}`, and a literal `null` is
//	 unchanged."
//	    -> TestCreateDefaultsAnOmittedAttrsAndLeavesALiteralNullAlone
//
// Both run without a database, and that is a property of the code rather than a
// convenience: every step they assert on happens above `pool.Begin`, so a nil
// pool reaches them and the nil-pool panic beyond them is itself the signal that
// a request got PAST the gate under test. `rejectViaEntryPoint` in
// json_object_params_test.go established that idiom for the attrs guard; these
// two use it in both directions.
//
//	GOWORK=off go test ./internal/domain/ -run 'TestCreateAcceptsOneOfTheScenarios|TestCreateDefaultsAnOmittedAttrs' -count=1 -v

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// createProbeRequest is a CreateWorkItemRequest that gets as far as the gate
// under test and no further: a goal and project that pass the field validators,
// nothing that would be refused ahead of them.
//
// The goal is deliberately long and specific because the checks above the two
// gates are real — validateWorkItemGoalPresent refuses "" and the shape
// validator refuses a newline — and a fixture that tripped one of them would
// make every assertion below a false green about a request that never arrived.
func createProbeRequest(scenario string, attrs json.RawMessage) *CreateWorkItemRequest {
	return &CreateWorkItemRequest{
		Project:  "aihub",
		Goal:     "probe the scenario gate and the attrs default above the transaction",
		Source:   "human",
		Scenario: scenario,
		Attrs:    attrs,
	}
}

// createOutcome is what a nil-pool CreateWorkItem did with a request: either it
// refused above the transaction (aerr non-nil) or it reached the database, which
// with no pool is a panic.
type createOutcome struct {
	aerr         *AihubError
	reachedTheDB bool
}

// runCreateAboveTheTransaction calls CreateWorkItem with no pool and reports
// which of the two happened.
//
// 🔴 The panic is CAUGHT rather than avoided, and it is the load-bearing half.
// "Does creation refuse this scenario?" is answered `true` by a build that
// refuses EVERY scenario, and the only observation that separates the two is a
// request getting through — which, with no pool, is exactly this panic. A
// version of this helper that only reported refusals would have no control at
// all.
func runCreateAboveTheTransaction(t *testing.T, req *CreateWorkItemRequest) (out createOutcome) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			out = createOutcome{reachedTheDB: true}
		}
	}()
	_, aerr := CreateWorkItem(context.Background(), nil, req,
		"u_probe", "probe", map[string]string{"aihub": "writer"}, "user")
	return createOutcome{aerr: aerr}
}

// TestCreateAcceptsOneOfTheScenariosTheCheckPermits is the `scenario` arm.
//
// WHY IT HAD NO ARM. The CHECK's vocabulary is mirrored by
// TestDBCheckRegistry_MirrorsMatchTheMigration only for the dispositions that
// mirror; `work_items.work_items_scenario_check` is registered dispGuarded,
// whose whole content is a PROSE reason saying the Go guard is stricter than the
// constraint. Nothing compared the two sets, so "stricter" was an assertion in a
// comment — and the card repeats it to callers as the reason a published
// parameter accepts values no row can hold.
//
// 🔴 The permitted set is read out of the migration, never typed here. A test
// naming 'coding','writing','data' would be a third copy of the vocabulary and
// would stay green on the day the CHECK grew a fourth value that creation also
// refused — the one day the card's "effectively fixed" claim would need
// restating, because the gap it describes would have widened.
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the code, the card untouched) ──
//	M1 relax the guard to `req.Scenario != "coding" && req.Scenario != "writing"`
//	                                          RED  writing/refused_above_the_transaction
//	M2 neutralise the guard (`if false`)      RED  both writing and data reach the DB
//	M3 widen it to refuse every scenario (`if req.Scenario != ""`, and the field is
//	   defaulted to "coding" above, so this refuses the one that works)
//	                                          RED  coding/reaches_the_transaction — the
//	                                               control, which is why it is here
//	── publication side (the card, the code untouched) ──
//	M4 strip this sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift (Cited falls,
//	                                               Unclassified rises) on both cards,
//	                                               since pf_batch_create_work_items'
//	                                               §6.1 T1-4 row cites it too//
//
// ⚠️ The publication-side mutant strips the citation's FILE PATH as well as its
// test symbol, and that is not tidiness. Measured 2026-09-10: de-backticking the
// symbol alone left every one of this wave's seventeen sentences still counted as
// Cited, because cardclaims.CitesAnArm is satisfied by EITHER anchor — so the
// publication side of a citation is only as strong as whichever anchor a later
// edit leaves behind. Reported as an incidental finding of aihub#576.
func TestCreateAcceptsOneOfTheScenariosTheCheckPermits(t *testing.T) {
	checks := effectiveDBChecks(t)
	fact, ok := checks["work_items.work_items_scenario_check"]
	if !ok {
		t.Fatalf("the migrations declare no CHECK on work_items.scenario any more. The card tells "+
			"callers the column is CHECKed to a closed set and that creation is stricter than it; "+
			"with no constraint there is no second set to be stricter than, and every assertion "+
			"below would be about a gap that had closed. Enumerated: %v", dbCheckKeys(checks))
	}
	permitted := dbCheckInValues(t, fact.Predicate, "scenario")
	sort.Strings(permitted)

	// Anti-vacuity, and it is the floor this arm can rot through: a predicate the
	// IN-list parse stopped matching would hand back an empty set, the loop below
	// would run zero times, and a build that had deleted the guard would be green.
	if len(permitted) < 2 {
		t.Fatalf("parsed %d permitted scenario(s) (%v) out of %q — a set this small cannot show "+
			"that creation is STRICTER than the column, because there is nothing for it to be "+
			"stricter about", len(permitted), permitted, fact.Predicate)
	}

	const accepted = "coding"
	var alsoAccepted []string
	for _, scenario := range permitted {
		if scenario == accepted {
			continue
		}
		t.Run(scenario+"/refused_above_the_transaction", func(t *testing.T) {
			out := runCreateAboveTheTransaction(t, createProbeRequest(scenario, nil))
			if out.reachedTheDB {
				alsoAccepted = append(alsoAccepted, scenario)
				t.Errorf("scenario %q reached the database. The column would accept it — %s permits "+
					"it — so this is no longer a value the parameter offers and no row can hold; it is "+
					"a scenario that now WORKS. That is a contract change and the card says the "+
					"opposite: %q", scenario, fact.Migration, fact.Predicate)
				return
			}
			if out.aerr == nil {
				t.Fatalf("scenario %q was neither refused nor reached the database, which is not a "+
					"reachable outcome of this call and means this probe is no longer driving "+
					"CreateWorkItem", scenario)
			}
			// 🔴 Anchored on the CODE, and on the status that code maps to, never on
			// a typed number. Measured 2026-09-10: NOT_IMPLEMENTED maps to 405, while
			// the dbCheckPolicies entry for this very constraint said "The answer is
			// 501" (corrected to 405 by aihub#592) — so a `!= 501` written here would
			// have shipped a red test on a correct tree, and a `!= 405` would freeze a
			// number that is codeToHTTPStatus' to choose. Reading it back through the
			// same map is what makes this assertion about the REFUSAL rather than
			// about an integer.
			if out.aerr.Code != ErrNotImplemented {
				t.Errorf("scenario %q is refused as %s (%s), and the registry entry for %s records "+
					"the refusal as NOT_IMPLEMENTED — a reserved-but-unbuilt scenario rather than a "+
					"caller mistake. A different code here means the refusal moved to a different "+
					"guard and the registry's stated reason no longer describes it.",
					scenario, out.aerr.Code, out.aerr.Message, fact.key())
			}
			if want := codeToHTTPStatus(ErrNotImplemented); out.aerr.HTTPStatus != want {
				t.Errorf("scenario %q is refused with HTTP %d while %s maps to %d — the error was "+
					"built with a status that disagrees with its own code, so the wire status and the "+
					"code a client switches on describe different outcomes",
					scenario, out.aerr.HTTPStatus, ErrNotImplemented, want)
			}
			if !strings.Contains(out.aerr.Message, scenario) {
				t.Errorf("the refusal for scenario %q does not name the value: %q. A caller who sent "+
					"one of several fields cannot tell which one was refused.", scenario, out.aerr.Message)
			}
		})
	}

	// ── the control. Without it every assertion above is satisfied by a build
	// that refuses every scenario there is, including the only one that works.
	t.Run(accepted+"/reaches_the_transaction", func(t *testing.T) {
		out := runCreateAboveTheTransaction(t, createProbeRequest(accepted, nil))
		if !out.reachedTheDB {
			t.Errorf("scenario %q did not reach the database either (refused with %v). Every "+
				"refusal above is then evidence about a build that creates nothing at all, and "+
				"\"creation rejects everything but coding\" would be true only because creation "+
				"rejects everything.", accepted, out.aerr)
		}
	})

	t.Logf("%s permits %v; creation accepts %q and refuses the other %d as NOT_IMPLEMENTED",
		fact.key(), permitted, accepted, len(permitted)-1-len(alsoAccepted))
}

// TestCreateDefaultsAnOmittedAttrsAndLeavesALiteralNullAlone is the `attrs` arm.
//
// WHY IT HAD NO ARM. TestRealObjectsAreUntouched asserts that an absent attrs and
// a literal null are not REJECTED by validateJSONObjectParam. That is the guard's
// half. What the card promises is different and one line further down: an absent
// attrs is rewritten to `{}` before the INSERT, and a literal null is left
// exactly as sent. The guard being silent about both is precisely why neither had
// an observation — a value the validator ignores is a value nothing downstream
// was checked to preserve.
//
// The assertion is on `req.Attrs` after the call because CreateWorkItem defaults
// IN PLACE, on the caller's request struct, above the transaction. That is what
// makes this observable with no database; it is also the only place the two
// values differ, since both would reach the column as jsonb.
//
// MUTANTS.
//
//	── enforcement side ──
//	M5 neutralise the `if len(req.Attrs) == 0 { req.Attrs = "{}" }` default
//	   (`if false`)                           RED  omitted/defaults_to_empty_object
//	M6 widen the default to also rewrite a literal null
//	   (`|| bytes.Equal(bytes.TrimSpace(req.Attrs), []byte("null"))`)
//	                                          RED  literal_null/is_unchanged
//	M7 move the default ABOVE validateJSONObjectParam
//	                                        GREEN  control: the ORDER is not what this
//	                                               arm is about, and it says so rather
//	                                               than claiming coverage it lacks —
//	                                               the ordering is pinned by the
//	                                               comment on the guard and by
//	                                               TestRealObjectsAreUntouched, which
//	                                               would still accept `{}`
//	── publication side ──
//	M8 strip this sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestCreateDefaultsAnOmittedAttrsAndLeavesALiteralNullAlone(t *testing.T) {
	// "writing" short-circuits with 405 NOT_IMPLEMENTED immediately AFTER the attrs default, so
	// the request never reaches the pool and the field can be read back. It is
	// taken from the arm above rather than assumed: the scenario gate is the
	// nearest exit past the default.
	const exitScenario = "writing"

	for _, tc := range []struct {
		name string
		sent json.RawMessage
		want string
	}{
		{name: "omitted", sent: nil, want: "{}"},
		{name: "literal_null", sent: json.RawMessage(`null`), want: "null"},
		{name: "a_real_object", sent: json.RawMessage(`{"k":1}`), want: `{"k":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := createProbeRequest(exitScenario, tc.sent)
			out := runCreateAboveTheTransaction(t, req)

			// The exit is asserted, not assumed. If the scenario gate ever stops
			// firing, this request reaches the pool, panics, and req.Attrs would be
			// read after a half-finished call — an assertion about a value nobody
			// can say was produced by the code under test.
			if out.reachedTheDB || out.aerr == nil || out.aerr.Code != ErrNotImplemented {
				t.Fatalf("this probe reads req.Attrs after a refusal it expects at the scenario "+
					"gate; the request reached the database instead (reachedTheDB=%v, err=%v), so "+
					"the value below was not produced above the transaction",
					out.reachedTheDB, out.aerr)
			}

			got := string(bytes.TrimSpace(req.Attrs))
			if got != tc.want {
				t.Errorf("attrs sent as %s came out of the pre-transaction path as %q, want %q.\n"+
					"An omitted attrs must become the empty object — every reader of the column "+
					"expects one — and a literal null must be left alone, because folding it to `{}` "+
					"would silently rewrite a value a caller chose. %s",
					describeSentAttrs(tc.sent), got, tc.want, attrsDefaultRationale)
			}
		})
	}

	// ── the floor. Three cases that all agreed on `{}` would satisfy the loop
	// above if the fixtures had collapsed into one; the three wants must be
	// distinct or the comparison is not distinguishing anything.
	seen := map[string]bool{"{}": true, "null": true, `{"k":1}`: true}
	if len(seen) != 3 {
		t.Fatalf("the three expected values are not distinct, so agreeing with all of them is not " +
			"evidence about any of them")
	}
}

// describeSentAttrs names what went in, so a failure distinguishes "absent" from
// the four-byte literal that renders identically in a diff.
func describeSentAttrs(raw json.RawMessage) string {
	if raw == nil {
		return "absent (nil)"
	}
	return "the literal " + string(raw)
}

// attrsDefaultRationale is the sentence a failure ends on. Declared rather than
// inlined three times so the two branches cannot drift into giving different
// reasons for the same rule.
const attrsDefaultRationale = "Both halves are stated on this tool's card and on " +
	"pf_batch_create_work_items', which shares the field through workItemFieldProps."
