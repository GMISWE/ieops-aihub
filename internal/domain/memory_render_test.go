package domain

import "testing"

// TestResolveRenderedHTML_ExplicitOverrides verifies aihub#104: a caller-supplied
// non-empty HTML is stored verbatim regardless of type, and overrides the
// auto-render even for a configured render type.
//
// aihub#130 added the second return value. Precedence #1 must report
// deferred=false: it is a passthrough of a string the caller already has, so
// there is no render to move off the write path, and enqueuing a job for it
// would give a background worker a chance to overwrite the caller's own HTML.
func TestResolveRenderedHTML_ExplicitOverrides(t *testing.T) {
	custom := "<!doctype html><html><body>custom report</body></html>"

	// Non-render type (e.g. methodology.review) — explicit HTML stored verbatim.
	got, deferred := resolveRenderedHTML(&custom, "methodology.review", "# markdown")
	if got == nil || *got != custom {
		t.Fatalf("explicit html should be stored verbatim for any type; got %v", got)
	}
	if deferred {
		t.Fatal("explicit html must not enqueue a deferred render; a worker could overwrite the caller's own HTML")
	}

	// Render type (methodology.spec) — explicit HTML still wins over auto-render.
	got, deferred = resolveRenderedHTML(&custom, "methodology.spec", "# markdown")
	if got == nil || *got != custom {
		t.Fatalf("explicit html should override auto-render; got %v", got)
	}
	if deferred {
		t.Fatal("explicit html on a render type must not enqueue a deferred render")
	}
}

// TestResolveRenderedHTML_Fallback verifies the deferred-render / NULL decision
// when no explicit HTML is supplied, with a deterministic render-type set.
//
// Before aihub#130 the two "render type" cases below asserted that this function
// RETURNED rendered HTML. That is precisely the behaviour the work item removed:
// the value is now decided by a background worker, and the write path's job is
// only to say whether one is owed.
func TestResolveRenderedHTML_Fallback(t *testing.T) {
	InitRenderTypes("methodology.spec,methodology.plan") // deterministic set
	t.Cleanup(func() { InitRenderTypes(defaultRenderTypes) })

	// No explicit + non-render type → NULL, and nothing owed.
	if got, deferred := resolveRenderedHTML(nil, "methodology.review", "# Title"); got != nil || deferred {
		t.Fatalf("non-render type without explicit html should yield (nil,false); got (%v,%v)", got, deferred)
	}

	// Whitespace-only explicit is treated as absent → falls back to the deferred render.
	ws := "   \n\t "
	if got, deferred := resolveRenderedHTML(&ws, "methodology.spec", "# Title"); got != nil || !deferred {
		t.Fatalf("whitespace explicit should fall back to a deferred render; got (%v,%v)", got, deferred)
	}

	// No explicit + render type → the INSERT stores NULL and a render is owed.
	if got, deferred := resolveRenderedHTML(nil, "methodology.plan", "# Title\n\nbody"); got != nil || !deferred {
		t.Fatalf("render type without explicit html should defer; got (%v,%v)", got, deferred)
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

// TestRenderTypes_DefaultIncludesExecuteRetroWrapSummary verifies aihub#81: the
// three new types also auto-render at save time using the default render-type set.
// Uses parseRenderTypes directly (no global mutation).
func TestRenderTypes_DefaultIncludesExecuteRetroWrapSummary(t *testing.T) {
	set := parseRenderTypes(defaultRenderTypes)
	for _, want := range []string{"methodology.execute", "methodology.retro", "methodology.wrap_summary"} {
		if !set[want] {
			t.Fatalf("defaultRenderTypes must include %q; got %q", want, defaultRenderTypes)
		}
	}
}

// TestResolveRenderedHTML_NewTypes asserts the three types added in aihub#81 are
// still carried through to a render under the default render-type set — since
// aihub#130 that means "a render is owed" rather than "HTML is returned here".
// The HTML those types end up with is asserted end-to-end against a real
// database in memory_render_async_test.go; what this covers is type coverage.
func TestResolveRenderedHTML_NewTypes(t *testing.T) {
	// Reset to default so the test is independent of ordering with
	// TestResolveRenderedHTML_Fallback, which overrides with a narrower set.
	InitRenderTypes(defaultRenderTypes)

	content := "# Heading\n\nbody paragraph"
	for _, memType := range []string{"methodology.execute", "methodology.retro", "methodology.wrap_summary"} {
		got, deferred := resolveRenderedHTML(nil, memType, content)
		if got != nil {
			t.Fatalf("resolveRenderedHTML(nil, %q, content) stored %q inline; the write path must not render", memType, *got)
		}
		if !deferred {
			t.Fatalf("resolveRenderedHTML(nil, %q, content) owes no render; that type would never get rendered_html", memType)
		}
	}
}
