package mcp

import (
	"strings"
	"testing"
)

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
