package server

import (
	"html/template"
	"regexp"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/render"
)

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read embedded static/%s: %v", name, err)
	}
	return string(b)
}

// TestDiagramStyles_LiveInUICSS locks the aihub#234 relocation: the .pf-diagram
// rules must live in ui.css, not viewer.css.
//
// viewer.css is linked only by the artifact viewer; the memory and wi detail pages
// link ui.css alone, and since aihub#231 they compile d2 as well. While the rules
// sat in viewer.css those pages emitted a figure with no rules behind it — no frame,
// and no light/dark map, so the diagram kept d2's baked-in light ramp on a dark
// page. The artifact viewer links ui.css too, so moving them costs it nothing.
func TestDiagramStyles_LiveInUICSS(t *testing.T) {
	ui := readStatic(t, "ui.css")
	viewer := string(render.ViewerCSS())

	// Selector-shaped matches only: the file keeps a comment pointing at the new
	// home, and that comment naturally names the class.
	for _, forbidden := range []string{".pf-diagram {", ".pf-diagram svg", ".pf-diagram [", ".pf-diagram-"} {
		if strings.Contains(viewer, forbidden) {
			t.Errorf("viewer.css must not define %q — diagram rules belong in ui.css "+
				"so the memory/wi detail pages get them too (aihub#234)", forbidden)
		}
	}
	for _, want := range []string{
		".pf-diagram {",
		".pf-diagram svg",
		".pf-diagram svg .fill-N1",   // dark-mode theme map came along
		".pf-diagram svg .stroke-B6", // …including the stroke half
	} {
		if !strings.Contains(ui, want) {
			t.Errorf("ui.css missing %q", want)
		}
	}
}

// TestDiagramThemeMap_OutranksD2sOwnStylesheet guards the aihub#234 specificity fix.
//
// d2 colors every element twice: a presentation attribute (fill="#1c1c20") and a
// class (fill-N1), and it ships its own stylesheet INSIDE the <svg> —
// `.d2-<hash> .fill-N1{fill:#1c1c20}`, specificity (0,2,0), in the body. The
// original map was written `.pf-diagram [fill="#1c1c20"]`, also (0,2,0) but linked
// from <head>, so d2 won every tie on document order and dark pages rendered light
// diagrams. Every selector in the map must therefore carry the `svg` type selector
// that lifts it to (0,2,1). A bare `.pf-diagram .fill-N1` or `.pf-diagram [fill=…]`
// re-introduces the tie, and it fails silently — nothing errors, dark mode is just
// wrong again.
func TestDiagramThemeMap_OutranksD2sOwnStylesheet(t *testing.T) {
	for _, sel := range diagramThemeSelectors(t, readStatic(t, "ui.css")) {
		if !strings.HasPrefix(sel, ".pf-diagram svg ") {
			t.Errorf("theme-map selector %q must start with `.pf-diagram svg ` — without the "+
				"type selector it ties with d2's in-SVG `.d2-<hash> .fill-N1` rule and loses "+
				"on document order (aihub#234)", sel)
		}
	}
}

// diagramThemeSelectors returns every selector from the fill-/stroke- theme map.
func diagramThemeSelectors(t *testing.T, css string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(css, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ".pf-diagram") {
			continue
		}
		if !strings.Contains(line, "fill-") && !strings.Contains(line, "stroke-") &&
			!strings.Contains(line, "[fill=") && !strings.Contains(line, "[stroke=") {
			continue
		}
		if i := strings.Index(line, "{"); i >= 0 {
			line = line[:i]
		}
		for _, sel := range strings.Split(line, ",") {
			if sel = strings.TrimSpace(sel); sel != "" {
				out = append(out, sel)
			}
		}
	}
	if len(out) < 14 {
		t.Fatalf("expected the full d2 theme map in ui.css, found %d selectors", len(out))
	}
	return out
}

// TestDiagramSizing_NoHardPixelCap is the regression guard for the reported bug:
// d2 figures were pinned at max-width:min(100%,600px), a cap unrelated to the
// viewport that shrank wide diagrams to roughly a third of their label size.
func TestDiagramSizing_NoHardPixelCap(t *testing.T) {
	ui := readStatic(t, "ui.css")

	rule := diagramSVGRule(t, ui)
	if strings.Contains(rule, "600px") {
		t.Errorf("the 600px cap is back in the .pf-diagram svg rule: %q", rule)
	}
	for _, want := range []string{"max-width: 100%", "height: auto"} {
		if !strings.Contains(rule, want) {
			t.Errorf(".pf-diagram svg rule missing %q; got %q", want, rule)
		}
	}
}

// TestRenderDiagram_HasIntrinsicSize is the other half of the sizing fix. `max-width:
// 100%; height: auto` only means "scale down to fit" if the SVG has an intrinsic
// size; without width/height attributes the browser resolves its width to 100% of
// the container and *upscales* narrow diagrams (that is how a 264-wide flowchart
// became 600px wide and 3400px tall). render/diagram.go passes Scale=1 to make d2
// emit those attributes on the outer <svg>.
func TestRenderDiagram_HasIntrinsicSize(t *testing.T) {
	svg, err := render.RenderDiagram("a -> b: edge label\nb -> c")
	if err != nil {
		t.Fatalf("RenderDiagram: %v", err)
	}
	i := strings.Index(svg, "<svg")
	if i < 0 {
		t.Fatalf("no <svg> in output")
	}
	root := svg[i:]
	if j := strings.Index(root, ">"); j > 0 {
		root = root[:j]
	}
	if !strings.Contains(root, "width=") || !strings.Contains(root, "height=") {
		t.Errorf("outer <svg> must carry width/height (intrinsic size) so CSS scales it "+
			"down instead of stretching it to the container; got %q", root)
	}
	if !strings.Contains(root, "viewBox=") {
		t.Errorf("outer <svg> lost its viewBox; got %q", root)
	}
}

// TestDiagramOverlay_KeepsThemeMapping guards the one way the zoom overlay can
// silently break: the light/dark mapping is a set of `.pf-diagram svg …` descendant
// rules, so the overlay must wrap the moved <svg> in an element that still carries
// .pf-diagram. The CSS side of that contract is the overlay rule; the JS side is the
// class the overlay wrapper is built with.
func TestDiagramOverlay_KeepsThemeMapping(t *testing.T) {
	ui := readStatic(t, "ui.css")
	js := readStatic(t, "diagram.js")

	if !strings.Contains(ui, ".pf-diagram-ovl .pf-diagram") {
		t.Error("ui.css: overlay must style a nested .pf-diagram (that class is what " +
			"keeps the dark-mode theme map matching inside the overlay)")
	}
	// Matched loosely (the literal class name, not one spelling of the assignment)
	// so a className→classList refactor does not fail a test about theming.
	if !strings.Contains(js, `"pf-diagram"`) {
		t.Error("diagram.js: the overlay wrapper must carry the pf-diagram class, " +
			"otherwise the zoomed diagram renders in d2's light ramp on a dark page")
	}
	// The zoom affordance is added by JS only, so a no-JS page never shows a
	// click target that does nothing.
	if strings.Contains(ui, ".pf-diagram { cursor: zoom-in") {
		t.Error("ui.css: zoom cursor must hang off .pf-diagram--zoomable (JS-added), " +
			"not off .pf-diagram itself")
	}
	if !strings.Contains(js, "pf-diagram--zoomable") {
		t.Error("diagram.js must add the .pf-diagram--zoomable marker class")
	}
}

// TestDiagramHint_IsGeneratedContent — annot.js anchors reviewer annotations by
// text-quote over the visible text of #pf-doc-col. A real "click to enlarge" text
// node inside a figure would join that haystack and could break the anchoring of any
// annotation spanning a diagram, and a failed anchor is dropped silently rather than
// reported. The hint must therefore be CSS ::after content, which never appears in
// Range.toString(). aria-hidden and opacity:0 do NOT exclude a node from it.
func TestDiagramHint_IsGeneratedContent(t *testing.T) {
	ui := readStatic(t, "ui.css")
	js := readStatic(t, "diagram.js")

	if !strings.Contains(ui, ".pf-diagram--zoomable::after") ||
		!strings.Contains(ui, `content: "click to enlarge"`) {
		t.Error("ui.css: the zoom hint must be ::after generated content on " +
			".pf-diagram--zoomable")
	}
	// Scoped to what runs against the document: the overlay is appended to <body>,
	// outside #pf-doc-col and built long after annot.js has anchored, so its close
	// button may carry real text. What must stay text-free is the figure itself.
	mark := jsFuncBody(t, js, "markZoomable")
	for _, forbidden := range []string{"textContent", "innerText", "innerHTML", "createTextNode", "appendChild"} {
		if strings.Contains(mark, forbidden) {
			t.Errorf("markZoomable() uses %s — it must not put text (or any node) inside a "+
				"figure, or annot.js's text-quote anchoring can silently break", forbidden)
		}
	}
	if strings.Contains(js, "figure.appendChild") {
		t.Error("diagram.js appends a node to a figure — the zoom hint belongs in CSS")
	}
}

// jsFuncBody returns the source of `function <name>(…) {…}` up to the next
// top-level-ish declaration. Crude, but enough to scope an assertion to one function.
func jsFuncBody(t *testing.T, js, name string) string {
	t.Helper()
	i := strings.Index(js, "function "+name+"(")
	if i < 0 {
		t.Fatalf("diagram.js has no function %s", name)
	}
	rest := js[i:]
	if j := strings.Index(rest[1:], "\n  function "); j > 0 {
		rest = rest[:j+1]
	}
	return rest
}

// TestDiagramThemeMap_CoversAllFourFamilies — d2 emits fill-, stroke-, color- and
// background-color- rules per theme slot. Mapping only fill/stroke is worse than
// mapping nothing: a markdown label renders as <div class="md color-N1"> over a
// rect.fill-B6, so remapping the rect alone leaves near-black text on a dark
// surface. Asserted against d2's real output, not a hardcoded list.
func TestDiagramThemeMap_CoversAllFourFamilies(t *testing.T) {
	svg, err := render.RenderDiagram("a: |md **bold** label |\nb: plain\na -> b: edge")
	if err != nil {
		t.Fatalf("RenderDiagram: %v", err)
	}
	ui := readStatic(t, "ui.css")

	// Every theme-slot class d2 actually put on an element in this diagram.
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`class="([^"]*)"`).FindAllStringSubmatch(svg, -1) {
		for _, c := range strings.Fields(m[1]) {
			if regexp.MustCompile(`^(fill|stroke|color|background-color)-[A-Z]+\d$`).MatchString(c) {
				used[c] = true
			}
		}
	}
	if len(used) < 4 {
		t.Fatalf("expected d2 to emit several theme-slot classes, saw %v", used)
	}
	for c := range used {
		// N7 is `transparent` in ThemeOverrides — deliberately unmapped.
		if strings.HasSuffix(c, "N7") {
			continue
		}
		if !strings.Contains(ui, ".pf-diagram svg ."+c+" ") &&
			!strings.Contains(ui, ".pf-diagram svg ."+c+",") {
			t.Errorf("d2 emits class %q but ui.css does not map it — that slot keeps its "+
				"baked-in light-ramp color on a dark page", c)
		}
	}
}

// TestDiagramJS_LoadedOnBothSurfaces — both surfaces compile d2: the artifact viewer,
// and the memory/wi detail pages via uiFuncMap's md helper since aihub#231. The zoom
// script has to be wired into both. The viewer half is asserted end-to-end by
// TestArtifactViewer_UIvsV1Share_BytePurity; this covers the app-shell half, plus the
// CSS/JS pairing that makes a detail-page figure zoomable at all.
func TestDiagramJS_LoadedOnBothSurfaces(t *testing.T) {
	if _, err := staticFS.ReadFile("static/diagram.js"); err != nil {
		t.Fatalf("static/diagram.js must be embedded: %v", err)
	}
	layout, err := templateFS.ReadFile("templates/layout.html.tmpl")
	if err != nil {
		t.Fatalf("read layout template: %v", err)
	}
	for _, asset := range []string{"/ui/static/diagram.js", "/ui/static/ui.css"} {
		if !strings.Contains(string(layout), asset) {
			t.Errorf("layout.html.tmpl must load %s — it is what makes a d2 figure on the "+
				"memory/wi detail pages styled and zoomable", asset)
		}
	}

	// And the app-shell surface really does emit such a figure: uiFuncMap's md
	// helper runs RenderDiagramsForUI (aihub#231), which is the whole reason the
	// diagram CSS had to leave viewer.css.
	md, ok := uiFuncMap()["md"].(func(string) template.HTML)
	if !ok {
		t.Fatal("uiFuncMap has no md(string) helper")
	}
	if out := string(md("intro\n\n```d2\na -> b\n```\n")); !strings.Contains(out, `<figure class="pf-diagram"`) {
		t.Errorf("the detail-page md helper does not emit a pf-diagram figure; got %.200s", out)
	}
}

// diagramSVGRule returns the declaration block of the `.pf-diagram svg` rule.
func diagramSVGRule(t *testing.T, css string) string {
	t.Helper()
	const sel = ".pf-diagram svg {"
	i := strings.Index(css, sel)
	if i < 0 {
		t.Fatalf("ui.css has no %q rule", sel)
	}
	rest := css[i+len(sel):]
	j := strings.Index(rest, "}")
	if j < 0 {
		t.Fatalf("unterminated %q rule", sel)
	}
	return rest[:j]
}
