package mcp

import (
	"encoding/json"
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

// TestPublishedBaseStrengthRangeIsTheEnforcedOne is the bridge between the range
// the server enforces and the range the model is told about (aihub#433 /
// aihub#411 T1-3).
//
// It is deliberately anchored on domain.MinBaseStrength / domain.MaxBaseStrength
// rather than on the literal "1-5", so it has something to say in both
// directions: reword the description back to a range the column would refuse and
// it fails, move the constants without retyping the descriptions and it fails
// too. A test that only grepped for "1-5" would go green on the day the bounds
// changed, which is the exact failure this work item is about.
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
