package server

import (
	"strings"
	"testing"
)

// aihub#159: wrapH2SectionsForUI folds each top-level H2 section into <details open>
// for the /ui viewer, leaving pre-H2 content (H1 + intro) untouched. /v1+/share never
// call it, so their byte-identical output is unaffected.
func TestWrapH2SectionsForUI(t *testing.T) {
	in := `<h1 id="t">T</h1><p>intro</p><h2 id="a">A</h2><p>pa</p><ul><li>x</li></ul><h2 id="b">B</h2><p>pb</p>`
	out := wrapH2SectionsForUI(in)

	// Pre-H2 content (H1 + intro) stays before the first <details>.
	if !strings.HasPrefix(out, `<h1 id="t">T</h1><p>intro</p><details open class="pf-sec">`) {
		t.Fatalf("pre-H2 content or first section wrong:\n%s", out)
	}
	// Exactly two folded sections.
	if n := strings.Count(out, `<details open class="pf-sec">`); n != 2 {
		t.Fatalf("want 2 folded sections, got %d:\n%s", n, out)
	}
	// Heading (with its id) lives inside the summary so TOC anchors + heading-anchor
	// commits still resolve.
	if !strings.Contains(out, `<summary class="pf-sec-sum"><h2 id="a">A</h2>`) {
		t.Fatalf("H2 not preserved inside summary:\n%s", out)
	}
	// Section body content is preserved.
	if !strings.Contains(out, `<div class="pf-sec-body"><p>pa</p><ul><li>x</li></ul></div>`) {
		t.Fatalf("section A body wrong:\n%s", out)
	}

	// No H2 → passthrough unchanged (byte-identical).
	flat := `<h1 id="t">T</h1><p>only intro, no sections</p>`
	if got := wrapH2SectionsForUI(flat); got != flat {
		t.Fatalf("no-H2 passthrough mutated input:\n%s", got)
	}

	// Empty input → empty.
	if got := wrapH2SectionsForUI(""); got != "" {
		t.Fatalf("empty input should stay empty, got %q", got)
	}
}
