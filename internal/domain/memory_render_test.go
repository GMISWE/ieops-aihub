package domain

import "testing"

// TestResolveRenderedHTML_ExplicitOverrides verifies aihub#104: a caller-supplied
// non-empty HTML is stored verbatim regardless of type, and overrides the
// goldmark auto-render even for a configured render type.
func TestResolveRenderedHTML_ExplicitOverrides(t *testing.T) {
	custom := "<!doctype html><html><body>custom report</body></html>"

	// Non-render type (e.g. methodology.review) — explicit HTML stored verbatim.
	got := resolveRenderedHTML(&custom, "methodology.review", "# markdown")
	if got == nil || *got != custom {
		t.Fatalf("explicit html should be stored verbatim for any type; got %v", got)
	}

	// Render type (methodology.spec) — explicit HTML still wins over auto-render.
	got = resolveRenderedHTML(&custom, "methodology.spec", "# markdown")
	if got == nil || *got != custom {
		t.Fatalf("explicit html should override goldmark auto-render; got %v", got)
	}
}

// TestResolveRenderedHTML_Fallback verifies the auto-render / NULL fallback when no
// explicit HTML is supplied, with a deterministic render-type set.
func TestResolveRenderedHTML_Fallback(t *testing.T) {
	InitRenderTypes("methodology.spec,methodology.plan") // deterministic set

	// No explicit + non-render type → NULL.
	if got := resolveRenderedHTML(nil, "methodology.review", "# Title"); got != nil {
		t.Fatalf("non-render type without explicit html should yield nil; got %q", *got)
	}

	// Whitespace-only explicit is treated as absent → falls back to auto-render.
	ws := "   \n\t "
	if got := resolveRenderedHTML(&ws, "methodology.spec", "# Title"); got == nil || *got == "" {
		t.Fatalf("whitespace explicit should fall back to auto-render for a render type; got %v", got)
	}

	// No explicit + render type → goldmark auto-render produces non-empty HTML.
	if got := resolveRenderedHTML(nil, "methodology.plan", "# Title\n\nbody"); got == nil || *got == "" {
		t.Fatalf("render type without explicit html should auto-render; got nil/empty")
	}
}

// TestRenderTypes_DefaultIncludesReview verifies methodology.review auto-renders
// by default (IEBE-1725). Uses the pure parser so it does not touch the global
// renderTypes set shared with other tests.
func TestRenderTypes_DefaultIncludesReview(t *testing.T) {
	set := parseRenderTypes(defaultRenderTypes)
	for _, want := range []string{"methodology.spec", "methodology.plan", "methodology.review"} {
		if !set[want] {
			t.Fatalf("defaultRenderTypes must include %q; got %q", want, defaultRenderTypes)
		}
	}
}
