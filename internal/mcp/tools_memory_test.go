package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// min_strength is compared against base_strength after decay, so its default of
// 0.3 sits below EVERY legal base_strength — it filters nothing. That is a
// defensible default and this gate does not ask for it to change; it asks the
// description to say which scale the number is on, because a threshold published
// without its scale makes every caller's intuition about the value wrong in the
// same direction. While base_strength was published as (0-1), 0.3 read as a
// mid-range cutoff.
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
