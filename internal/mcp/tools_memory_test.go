package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// memoryPropDescription returns one property's published description, failing if
// the property is not published at all — an absent property must not read as an
// empty description that trivially satisfies a "must not contain" assertion.
func memoryPropDescription(t *testing.T, tool string, raw json.RawMessage, name string) string {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s schema is not valid JSON: %v", tool, err)
	}
	p, ok := schema.Properties[name]
	if !ok {
		t.Fatalf("%s publishes no %q property", tool, name)
	}
	return p.Description
}

// TestPublishedBaseStrengthRangeIsTheEnforcedOne is the bridge between what the
// server enforces on base_strength and what the model is told about it
// (aihub#433 / aihub#411 T1-3, extended by aihub#459).
//
// It is deliberately anchored on domain.MinBaseStrength / domain.MaxBaseStrength
// rather than on the literal "1-5", so it has something to say in both
// directions: reword the description back to a range the column would refuse and
// it fails, move the constants without retyping the descriptions and it fails
// too. A test that only grepped for "1-5" would go green on the day the bounds
// changed, which is the exact failure this work item is about.
//
// ─── The integrality half (aihub#459) ─────────────────────────────────────────
//
// The RANGE half could be anchored on a constant. The INTEGRALITY half cannot —
// there is no number to compare against, only a rule — so the anchor is the
// enforcement itself: this test calls domain.Remember with a NIL POOL, which is
// the same instrument internal/domain's own suite uses. A rejected value comes
// back as an error with the pool never touched; an accepted one can only
// demonstrate that it got past the guard by dying on the nil pool. That makes
// the assertion bidirectional without a database, and, more to the point, it
// binds the published word to the code path a caller actually hits rather than
// to a predicate that might have no callers.
//
// Why domain.Remember and not domain.ValidateIntegralStrength directly: the
// latter would go green on a build where the guard exists and nothing calls it,
// which is precisely the state this repo was in between aihub#433 (the range
// guard, wired) and now (the integrality rule, unwired). One function covers
// both tools here because both reach it — pf_update_memory's body enters
// domain.UpdateMemory, which builds a RememberRequest and calls Remember.
//
// The published TYPE stays "number", because that is what the JSON wire carries
// and narrowing it to "integer" would be a schema change no caller asked for.
// That is exactly why the WORD matters: with the type unchanged, the description
// is the only place a caller learns that 2.5 is a 400.
//
// A description is charged on every request of every session, so these strings
// are short on purpose. The obligation is that they be TRUE, not that they be
// complete: docs/ is where the long version belongs, because docs/ is free.
func TestPublishedBaseStrengthRangeIsTheEnforcedOne(t *testing.T) {
	// "1-5" built from the constants, not typed out.
	want := fmt.Sprintf("%g-%g", float64(domain.MinBaseStrength), float64(domain.MaxBaseStrength))

	for _, tc := range []struct {
		tool string
		raw  json.RawMessage
	}{
		{"pf_remember", rememberSchema()},
		{"pf_update_memory", updateMemorySchema()},
	} {
		desc := memoryPropDescription(t, tc.tool, tc.raw, "base_strength")
		if !strings.Contains(desc, want) {
			t.Errorf("%s publishes base_strength as %q, which never states the enforced "+
				"range %s. A published range disjoint from — or silent about — the "+
				"enforced one is what invited the 500s this gate exists to prevent.",
				tc.tool, desc, want)
		}
		// The specific wrong answer that shipped. Kept as its own assertion
		// because "contains 1-5" would pass on a string that said both.
		if strings.Contains(desc, "0-1") {
			t.Errorf("%s publishes base_strength as %q, which still advertises the (0-1) "+
				"range the column CHECK refuses", tc.tool, desc)
		}
		if !strings.Contains(strings.ToLower(desc), "integer") {
			t.Errorf("%s publishes base_strength as %q, which states the range but not that "+
				"the value must be a whole number — and the server refuses a fractional one "+
				"with a 400 (aihub#459). The published type is still \"number\", so this "+
				"description is the ONLY place a caller can learn that 2.5 is in range and "+
				"still rejected.", tc.tool, desc)
		}
	}

	// The behaviour the word above claims. Both arms are required: without the
	// rejection arm a guard that was deleted would leave the description merely
	// lying, and without the acceptance arm a guard that rejected EVERYTHING
	// would satisfy the rejection arm perfectly.
	for _, v := range []float64{2.5, 1.5, 4.999, 3.0000001} {
		panicked, err := rememberBaseStrengthReachesPool(v)
		if panicked != nil {
			t.Errorf("base_strength=%g reached the (nil) pool: nothing refuses a fractional "+
				"value, so every description asserted above is false. pgx would truncate it "+
				"toward zero and the call would answer 200 having stored a strength the "+
				"caller never named.", v)
			continue
		}
		var ae *domain.AihubError
		if !errors.As(err, &ae) || ae.Code != domain.ErrBadRequest {
			t.Errorf("base_strength=%g was refused with %v; it must be a 400 naming the "+
				"field, not a 500 that sends the reader to a server log (aihub#411 T1-6)",
				v, err)
		}
	}
	for _, v := range []float64{
		float64(domain.MinBaseStrength), 2, float64(domain.DefaultBaseStrength),
		float64(domain.MaxBaseStrength),
	} {
		if panicked, err := rememberBaseStrengthReachesPool(v); panicked == nil {
			t.Errorf("base_strength=%g is a whole number inside [%g,%g] and Remember returned "+
				"%v instead of proceeding to the pool — the integrality guard is refusing "+
				"values the column accepts", v, float64(domain.MinBaseStrength),
				float64(domain.MaxBaseStrength), err)
		}
	}
}

// rememberBaseStrengthReachesPool sends one base_strength through the real write
// path with a nil pool and reports which of the two outcomes happened: a
// non-nil panic means the value got past every pre-query guard, a non-nil error
// means it did not.
//
// The recover is not defensive tidiness — a panic escaping a test kills the
// whole package binary, which would report this file's failure as every other
// test in internal/mcp failing too.
func rememberBaseStrengthReachesPool(bs float64) (panicked any, err error) {
	defer func() { panicked = recover() }()
	_, _, err = domain.Remember(context.Background(), nil, &domain.RememberRequest{
		Project: "p", Type: "experience.debug", Content: "c",
		Visibility: "project", BaseStrength: &bs,
	})
	return nil, err
}

// TestPublishedStrengthDeltaSaysWhatReinforceEnforces is the same bridge for
// pf_reinforce_memory (aihub#459).
//
// It is a separate test rather than a third row in the loop above because the
// parameter is a different KIND of thing: strength_delta is not a value the
// column holds, it is an addend, and it is enforced in
// internal/server/routes_memory.go (handleReinforceMemory) rather than in
// domain.Remember. What the two share is the rule — one exported function, so
// "whole number" cannot come to mean two things — and this test anchors on that
// function for the same reason the one above anchors on the constants.
//
// The handler's own obligation, that it actually CALLS this rule and calls it
// before the first query, is asserted where the handler lives
// (internal/server/routes_memory_reinforce_integral_test.go). Splitting them is
// deliberate: this file can see the published string and the rule, and cannot
// see the handler; a test that claimed all three from here would be asserting
// the middle one on trust.
//
// ─── The saturation half (aihub#506) ──────────────────────────────────────────
//
// The owner ruled on 2026-09-09 that the clamp STAYS and is written into the
// contract, so this test grew a second obligation on the same string. It is not
// covered by the range arm alone: the description already said "clamped to 1-5"
// and that reads as a refusal beside a neighbouring sentence about a delta that
// IS refused, while the two outcomes differ in whether a 200 stored a value the
// caller never named. So the word is asserted as well as the numbers.
//
// The numbers are anchored on domain.MinBaseStrength / domain.MaxBaseStrength
// for the same reason as the test above — since aihub#433 those constants ARE
// the handler's clamp bounds, so a bound that moves without the description
// being retyped is red. The behaviour the word claims is asserted where the
// clamp lives, against a real database
// (internal/server/routes_memory_reinforce_returning_db_test.go,
// TestReinforceMemory_IntegralDeltaStillMoves, which drives a delta past the top
// and requires MaxBaseStrength back). Claiming it from here would be the same
// on-trust assertion the paragraph above refuses.
func TestPublishedStrengthDeltaSaysWhatReinforceEnforces(t *testing.T) {
	desc := memoryPropDescription(t, "pf_reinforce_memory", reinforceMemorySchema(), "strength_delta")

	if !strings.Contains(strings.ToLower(desc), "integer") {
		t.Errorf("pf_reinforce_memory publishes strength_delta as %q without saying it must "+
			"be a whole number, which the server has refused non-integrally since "+
			"aihub#459", desc)
	}
	// The withdrawn model, banned by name. Until aihub#459 this string told
	// callers a fractional delta was TRUNCATED — a caller who still believes that
	// will send 0.5 expecting a no-op and get a 400, which is a worse outcome
	// than never having been told anything.
	for _, gone := range []string{"truncated", "usually changes nothing"} {
		if strings.Contains(strings.ToLower(desc), gone) {
			t.Errorf("pf_reinforce_memory publishes strength_delta as %q, which still "+
				"describes the pre-aihub#459 model (%q). Truncation is no longer reachable "+
				"through this parameter: a fractional delta is refused before it is added "+
				"to anything.", desc, gone)
		}
	}

	// aihub#506: the range, built from the constants rather than typed out, and
	// the word that says what happens AT it.
	want := fmt.Sprintf("%g-%g", float64(domain.MinBaseStrength), float64(domain.MaxBaseStrength))
	if !strings.Contains(desc, want) {
		t.Errorf("pf_reinforce_memory publishes strength_delta as %q, which never states the "+
			"%s bounds the handler's clamp actually holds the sum to", desc, want)
	}
	if !strings.Contains(strings.ToLower(desc), "saturat") {
		t.Errorf("pf_reinforce_memory publishes strength_delta as %q, which states the bounds "+
			"but not that a sum outside them is SATURATED rather than refused (owner ruling "+
			"2026-09-09, aihub#506). Beside a sentence about a fractional delta that is "+
			"refused, a bare \"clamped\" leaves a caller free to read the overflow as a 400 "+
			"too — and the difference is whether the call answered 200 having stored a value "+
			"the caller did not name.", desc)
	}

	// The rule the string describes, in both directions.
	if err := domain.ValidateIntegralStrength("strength_delta", 0.5); err == nil {
		t.Error("domain.ValidateIntegralStrength accepts a fractional strength_delta, so the " +
			"published description is false")
	} else if err.Code != domain.ErrBadRequest {
		t.Errorf("a fractional strength_delta must be a 400, got %v", err.Code)
	} else if !strings.HasPrefix(err.Message, "strength_delta ") {
		t.Errorf("the message must OPEN by naming the offending field so a caller learns "+
			"WHICH argument is wrong; got %q", err.Message)
	}
	for _, v := range []float64{-2, -1, 0, 1, 4} {
		if err := domain.ValidateIntegralStrength("strength_delta", v); err != nil {
			t.Errorf("strength_delta=%g is a whole number and must be accepted; got %v",
				v, err.Message)
		}
	}
}

// TestRecallMinStrengthPublishesWhichScaleItIsOn covers aihub#411 T2-19.
//
// min_strength is compared against base_strength after decay, so a threshold
// published without its scale makes every caller's intuition about the value
// wrong in the same direction: while base_strength was published as (0-1), 0.3
// read as a mid-range cutoff. This gate asks the description to say which scale
// the number is on. It does not ask for the default to change.
//
// ⚠️ This comment used to continue "so its default of 0.3 sits below EVERY legal
// base_strength — it filters nothing". aihub#645 measured that and it is false:
// the CHECK bounds the RAW column, the compared quantity is the DECAYED one, and
// the default hides 16 of the 261 rows the measured query reaches. The three
// assertions below were always about the scale rather than about that claim, so
// they are unchanged; what is corrected is the account of why they exist.
// TestRecallMinStrengthDefaultIsPublishedAsFiltering is the arm that holds the
// corrected claim.
func TestRecallMinStrengthPublishesWhichScaleItIsOn(t *testing.T) {
	desc := memoryPropDescription(t, "pf_recall", recallSchema(), "min_strength")
	want := fmt.Sprintf("%g-%g", float64(domain.MinBaseStrength), float64(domain.MaxBaseStrength))

	if !strings.Contains(desc, "base_strength") {
		t.Errorf("pf_recall publishes min_strength as %q without naming the quantity it "+
			"thresholds; a caller cannot place 0.3 on a scale nobody named", desc)
	}
	if !strings.Contains(desc, want) {
		t.Errorf("pf_recall publishes min_strength as %q without stating the %s scale it "+
			"is on", desc, want)
	}
	if !strings.Contains(desc, "0.3") {
		t.Errorf("pf_recall publishes min_strength as %q, which no longer states the "+
			"default; the default is the value whose meaning this gate is about", desc)
	}
}

// minStrengthSelfDefaultRe reads an `if X <= 0 { X = 0.3 }` self-default out of a
// function body. The comparison and the literal are captured separately because
// the published description makes a distinct claim about each: the literal is the
// default a caller is told about, and the comparison is the whole reason a
// literal 0 means "unset" rather than "no floor".
var minStrengthSelfDefaultRe = regexp.MustCompile(
	`if\s+(?:req\.)?[Mm]inStrength\s*(<=|<)\s*0\s*\{\s*(?:req\.)?[Mm]inStrength\s*=\s*([0-9.]+)`)

type minStrengthSelfDefault struct {
	op      string
	literal string
}

// domainFuncBody returns one top-level function's source, doc comment excluded.
// Excluding the doc comment is load-bearing for the arms below: recallLexical's
// comment discusses min_strength at length while its body must not apply it, so a
// scan that included comments would report the opposite of the truth.
func domainFuncBody(t *testing.T, path, fn string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v. It is one side of a comparison, and an unreadable side makes "+
			"the comparison vacuous rather than green.", path, err)
	}
	src := string(raw)
	start := strings.Index(src, "\nfunc "+fn+"(")
	if start < 0 {
		t.Fatalf("%s declares no func %s, so this arm has no subject. If it was renamed, "+
			"rename it here too rather than dropping the arm: the arm is what keeps the "+
			"published description bound to the behaviour.", path, fn)
	}
	body := src[start+1:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end+1]
	}
	return body
}

func readMinStrengthSelfDefault(t *testing.T, path, fn string) minStrengthSelfDefault {
	t.Helper()
	m := minStrengthSelfDefaultRe.FindStringSubmatch(domainFuncBody(t, path, fn))
	if m == nil {
		t.Fatalf("no min_strength self-default found in %s (%s). Every arm below compares the "+
			"published sentence against that statement, so a scan that finds nothing would "+
			"turn this whole gate green rather than red.", fn, path)
	}
	return minStrengthSelfDefault{op: m[1], literal: m[2]}
}

// TestRecallMinStrengthDefaultIsPublishedAsFiltering is aihub#645's gate.
//
// From aihub#433 until aihub#645 this schema published "Default 0.3 filters
// nothing". The defect was an inference rather than a typo: base_strength IS
// CHECK-constrained to 1-5 and 0.3 IS below 1, but the quantity compared is
// `base_strength * exp(-days/stability_days)`, which the CHECK does not bound and
// which crosses 0.3 on any memory old enough relative to its stability.
//
// Measured through the MCP tool on 2026-09-13, project=aihub, one query held
// fixed and only this parameter varied: no min_strength gave total 245, `0` gave
// 245, an explicit `0.3` gave 245, and `0.001` gave 261. So the default hides 16
// of the 261 rows that query can reach, `0` is read as "unset" rather than as
// "no floor", and
// lexical.total stayed 10 across all four, which is the section split visible
// from outside.
//
// A gate that only grepped the description for words would drift the moment the
// behaviour moved instead of the string, so each arm is anchored on the source
// that makes the published sentence true or false:
//
//	A1  the old "filters nothing" sentence returns              RED
//	A2  recallRouted's self-default literal moves to 0.5 while
//	    the description still says 0.3                          RED
//	A3  the self-default condition weakens to `< 0`, which makes
//	    a literal 0 a real "no floor" and the published sentence
//	    about UNSET false                                       RED
//	A4  recallLexical starts applying MinStrength, falsifying
//	    the published section split                             RED
//	A5  recallText or RecallWithVector stops applying it,
//	    falsifying "gates the ranked halves"                    RED (control)
//	A6  the handler widens min_strength below 0, so a negative
//	    stops being a 400 and the published sentence about it
//	    goes false                                             RED
//
// 🔴 It does NOT assert what the default should be. Whether 0.3 belongs there is
// the owner's call and an input to aihub#364; this gate only requires that
// whatever the code does is what the caller is told.
func TestRecallMinStrengthDefaultIsPublishedAsFiltering(t *testing.T) {
	desc := memoryPropDescription(t, "pf_recall", recallSchema(), "min_strength")
	lower := strings.ToLower(desc)

	// A1. The exact claim aihub#645 falsified, and the inference that produced it.
	for _, banned := range []string{"filters nothing", "below every legal value"} {
		if strings.Contains(lower, banned) {
			t.Errorf("pf_recall publishes min_strength as %q, which is back to claiming %q. "+
				"Measured 2026-09-13 on project aihub: 245 results at the default against 261 "+
				"at min_strength=0.001, so the default hides 16 of the 261 rows that query can "+
				"reach.", desc, banned)
		}
	}

	// A2/A3. The self-default is applied in two places and they must agree with
	// each other as well as with the description: a divergence means the same row
	// is filtered on one path and kept on the other.
	routed := readMinStrengthSelfDefault(t, filepath.Join(domainPkgDir, "memory.go"), "recallRouted")
	vector := readMinStrengthSelfDefault(t, filepath.Join(domainPkgDir, "memory_vector.go"), "RecallWithVector")
	if routed.literal != vector.literal {
		t.Errorf("recallRouted defaults min_strength to %s and RecallWithVector to %s. One "+
			"description cannot be true of both, and a row near the threshold would be "+
			"visible on one path and gone on the other.", routed.literal, vector.literal)
	}
	for fn, d := range map[string]minStrengthSelfDefault{"recallRouted": routed, "RecallWithVector": vector} {
		if !strings.Contains(desc, d.literal) {
			t.Errorf("%s defaults min_strength to %s, and the published description %q does "+
				"not state that number. A caller who is told the wrong default cannot "+
				"predict which memories a plain pf_recall can reach.", fn, d.literal, desc)
		}
		if d.op != "<=" {
			t.Errorf("%s self-defaults on `%s 0` rather than `<= 0`, so a literal 0 is no "+
				"longer rewritten to the default. The published description says 0 means "+
				"UNSET, and that sentence is now false: update it in the same change.",
				fn, d.op)
		}
	}
	if !strings.Contains(lower, "unset") {
		t.Errorf("pf_recall publishes min_strength as %q without saying that 0 means unset. "+
			"Both recall paths read a literal 0 as the default, and a negative is refused at "+
			"the handler, so a caller sending 0 to remove the floor gets the floor and no "+
			"warning.", desc)
	}

	// A4. The lexical section is outside the gate, by design (aihub#360).
	lexicalBody := domainFuncBody(t, filepath.Join(domainPkgDir, "memory_lexical.go"), "recallLexical")
	if strings.Contains(lexicalBody, "MinStrength") {
		t.Error("recallLexical now applies MinStrength, so pf_recall's published claim that " +
			"the lexical section ignores this parameter is false. That is a behaviour change " +
			"rather than a typo: decide it deliberately, then republish the description.")
	}
	if !strings.Contains(lower, "lexical") {
		t.Errorf("pf_recall publishes min_strength as %q without disclosing that the lexical "+
			"section is outside this gate. One response then carries rows that its own "+
			"threshold excluded, with nothing saying which half is which.", desc)
	}

	// A5. The control: the two ranked halves DO apply it. Without this arm, A4
	// would stay green on a tree where nothing applied min_strength at all.
	for _, c := range []struct{ file, fn string }{
		{"memory.go", "recallText"},
		{"memory_vector.go", "RecallWithVector"},
	} {
		body := domainFuncBody(t, filepath.Join(domainPkgDir, c.file), c.fn)
		if !strings.Contains(body, "MinStrength") && !strings.Contains(body, "minStrength") {
			t.Errorf("%s does not apply min_strength at all, so the published claim that this "+
				"parameter gates the ranked halves of a recall is false.", c.fn)
		}
	}

	// A6. The description tells a caller that a negative is a 400, which is the
	// natural next guess once they learn 0 does not lift the floor. That sentence
	// is true only while the handler's lower bound is 0: widen it and a negative
	// stops being refused, at which point the published text is wrong in the
	// direction that costs a caller a request. The bound is read rather than
	// assumed, so this arm fails on the change rather than on the wording.
	handler := readFileForArmLocal(t, filepath.Join("..", "server", "routes_memory.go"))
	m := minStrengthRangeRe.FindStringSubmatch(handler)
	if m == nil {
		t.Fatal("internal/server/routes_memory.go no longer binds min_strength through " +
			"queryFloatInRange, so the bound this arm compares the description against " +
			"cannot be read. A scan that finds nothing must fail rather than pass.")
	}
	if m[1] != "0" {
		t.Errorf("the handler bounds min_strength at [%s, +Inf) rather than [0, +Inf), so a "+
			"negative is no longer refused and the published sentence \"a negative is a 400\" "+
			"is false. Republish the description in the same change.", m[1])
	}
	if !strings.Contains(desc, "400") {
		t.Errorf("pf_recall publishes min_strength as %q without saying that a negative is a "+
			"400. A caller who has just read that 0 means unset will try -1 next, and the "+
			"only thing standing between them and a wasted round-trip is this sentence.", desc)
	}
}

// minStrengthRangeRe reads the lower bound the recall handler enforces on
// min_strength. Captured rather than matched literally, so the arm can report
// which bound it found instead of only that the expected one was absent.
var minStrengthRangeRe = regexp.MustCompile(
	`queryFloatInRange\(c,\s*"min_strength",\s*([^,]+),`)

// readFileForArmLocal is package mcp's copy of the mcp_test helper of nearly the
// same name: one side of a comparison, with an unreadable side treated as a
// failure rather than as nothing to compare.
func readFileForArmLocal(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v. An unreadable side makes the comparison vacuous rather "+
			"than green.", path, err)
	}
	return string(raw)
}

// TestValidatePfRememberArgs covers the aihub#210 client-side guard: pf_remember
// requires the four core fields and rejects methodology.* (those go through
// pf_save_artifact).
func TestValidatePfRememberArgs(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"project": "aihub", "type": "experience.debug", "content": "c", "visibility": "project"}
	}

	t.Run("valid non-methodology passes", func(t *testing.T) {
		if err := validatePfRememberArgs(base()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing required field", func(t *testing.T) {
		a := base()
		delete(a, "content")
		if err := validatePfRememberArgs(a); err == nil {
			t.Fatal("expected error for missing content")
		}
	})

	for _, mt := range []string{"methodology.spec", "methodology.plan", "methodology.wrap_summary"} {
		t.Run("rejects "+mt, func(t *testing.T) {
			a := base()
			a["type"] = mt
			err := validatePfRememberArgs(a)
			if err == nil {
				t.Fatalf("expected rejection for %s", mt)
			}
			if !strings.Contains(err.Error(), "pf_save_artifact") {
				t.Errorf("rejection should point to pf_save_artifact, got %q", err.Error())
			}
		})
	}
}
