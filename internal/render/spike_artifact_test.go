package render

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The P0 spike artifact (aihub#240 T6) is the thing acceptance criteria 3 and 4 are
// judged on: one real document class carried through the whole three-step architecture,
// carrying a figure at the density Monte's attachment set as the bar.
//
// Scope note, stated so these tests are not mistaken for more than they are: everything
// here is server-side. That the figure is *visually* undistorted, that the sandbox
// actually blocks escape attempts and that CSP fires in a browser are AC4/AC5/AC9 and
// belong to the aihub-test deploy checklist (T7). What is proved here is that nothing in
// our own pipeline destroys the artifact on the way through.

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "render", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

var (
	reH2      = regexp.MustCompile(`(?m)^## (.+)$`)
	reHTMLH2  = regexp.MustCompile(`<h2>([^<]+)</h2>`)
	reD2Fence = regexp.MustCompile("(?s)```d2\\n(.*?)\\n```")
)

// AC3: the artifact exists as a twin pair, which is the premise the whole architecture
// rests on. Both halves are agent-authored, so drift between them is the expected failure
// mode rather than a remote one.
func TestSpikeArtifact_TwinPairIsStructurallyConsistent(t *testing.T) {
	md := fixture(t, "spike_architecture.md")
	h := fixture(t, "spike_architecture.html")

	var mdHeads []string
	for _, m := range reH2.FindAllStringSubmatch(md, -1) {
		mdHeads = append(mdHeads, strings.TrimSpace(m[1]))
	}
	var htmlHeads []string
	for _, m := range reHTMLH2.FindAllStringSubmatch(h, -1) {
		htmlHeads = append(htmlHeads, strings.TrimSpace(m[1]))
	}

	if len(mdHeads) == 0 {
		t.Fatal("markdown twin has no sections")
	}
	if len(mdHeads) != len(htmlHeads) {
		t.Fatalf("section count drifted: md has %d %v, html has %d %v",
			len(mdHeads), mdHeads, len(htmlHeads), htmlHeads)
	}
	for i := range mdHeads {
		if mdHeads[i] != htmlHeads[i] {
			t.Errorf("section %d drifted: md %q vs html %q", i, mdHeads[i], htmlHeads[i])
		}
	}
}

// The complex track must be a DSL in the markdown twin, not hand-placed coordinates: an
// LLM cannot position a 23-node figure reliably, which is the whole reason for the split
// (01-static-html-render-engine-research.md §0.1).
func TestSpikeArtifact_ComplexTrackIsDSLNotCoordinates(t *testing.T) {
	md := fixture(t, "spike_architecture.md")

	m := reD2Fence.FindStringSubmatch(md)
	if m == nil {
		t.Fatal("markdown twin carries no d2 source for the complex track")
	}
	src := m[1]
	if strings.Contains(src, "<svg") || strings.Contains(src, " d=\"M") {
		t.Error("the complex track's source contains raw SVG geometry — it should be a DSL")
	}

	// It must actually freeze through the real T5 pipeline.
	got, err := FreezeDiagram(context.Background(), src, FreezeOptions{})
	if err != nil {
		t.Fatalf("the artifact's own DSL does not freeze: %v", err)
	}
	if !strings.Contains(got.SVG, "<svg") {
		t.Fatal("freeze produced no svg")
	}
	// aihub#234 / mem_0v7S0TTo: without an intrinsic size the figure is upscaled to its
	// container instead of scaled down to fit.
	if !strings.Contains(got.SVG, "width=") || !strings.Contains(got.SVG, "height=") {
		t.Error("frozen figure has no intrinsic size")
	}
}

// AC4. The bar is explicitly not "a three-box flowchart renders": the dense figure must
// survive with its gradients, filters, clip path and curved edges intact.
func TestSpikeArtifact_DenseFigureSurvivesSanitizing(t *testing.T) {
	h := fixture(t, "spike_architecture.html")

	features := map[string]string{
		"linear gradient":  "linearGradient",
		"radial gradient":  "radialGradient",
		"gradient stop":    "stop-color",
		"drop shadow":      "feDropShadow",
		"gaussian blur":    "feGaussianBlur",
		"colour matrix":    "feColorMatrix",
		"filter merge":     "feMerge",
		"clip path def":    "clipPath",
		"clip path use":    "clip-path",
		"filter reference": "filter=",
		"marker":           "marker-end",
		"cubic bezier":     " C",
	}

	before := map[string]int{}
	for name, tok := range features {
		before[name] = strings.Count(h, tok)
		if before[name] == 0 {
			t.Fatalf("fixture does not exercise %s (%q) — the artifact is not at the "+
				"complexity acceptance criterion 4 requires", name, tok)
		}
	}

	out := SanitizeArtifactHTML(h)
	for name, tok := range features {
		if got := strings.Count(out, tok); got != before[name] {
			t.Errorf("sanitizing changed %s (%q): %d -> %d", name, tok, before[name], got)
		}
	}
}

// Density is the property under test, so it is asserted numerically. A future edit that
// swaps the figure for something simpler fails here rather than quietly lowering the bar.
func TestSpikeArtifact_FigureIsActuallyDense(t *testing.T) {
	h := fixture(t, "spike_architecture.html")

	// Geometry is measured on the stored twin, where it belongs: the dense figure is
	// hand-generated inline SVG, so its node and edge counts are a property of the artifact
	// itself and must not depend on a layout engine running.
	const minNodes, minEdges = 20, 20
	if n := strings.Count(h, "<rect"); n < minNodes {
		t.Errorf("figure has %d rects, want >= %d — not a dense architecture diagram", n, minNodes)
	}
	if n := strings.Count(h, "<path"); n < minEdges {
		t.Errorf("figure has %d paths, want >= %d", n, minEdges)
	}

	// Byte volume is measured on the SERVED document, not the stored one.
	//
	// The old floor was 20KB against the stored twin, which held only because a 33KB frozen
	// SVG was pasted into it. Now that the complex track is a fence, the stored twin is
	// ~19KB and the volume appears at compile time instead — so asserting on stored bytes
	// would either fail for the wrong reason or have to be lowered to a number that no
	// longer means anything. What the criterion is actually about is what the reader
	// receives.
	out := RenderDiagramsGated(SanitizeArtifactHTML(h))
	const minServed = 40 * 1024
	if len(out) < minServed {
		t.Errorf("served document is %d bytes, want >= %d — the dense figure and the compiled "+
			"d2 figure together are larger than that, so something was dropped", len(out), minServed)
	}
	if len(out) <= len(h) {
		t.Errorf("serving did not add the compiled figure: stored %d bytes -> served %d bytes",
			len(h), len(out))
	}
}

// Both tracks have to be present. Passing the spike on the simple case alone is exactly
// what wi.content forbids.
//
// The complex track is carried as a d2 fence, not as inlined SVG, so this asserts on the
// output of the real pipeline rather than on the stored bytes. That is the point: the html
// twin ships an uncompiled fence precisely so the sanitizer runs while the figure is inert,
// and the trusted SVG is inserted afterwards.
//
// An earlier version counted "<svg" in the raw fixture and required three. That passed while
// the frozen SVG was pasted into the twin at authoring time — which is how the figure ended
// up going through the sanitizer at all, and how it came to depend on a <style>-filtering
// bypass to keep its paint (see TestSanitizeArtifactHTML_StyleElementIsDroppedWhole).
func TestSpikeArtifact_CarriesBothDiagramTracks(t *testing.T) {
	h := fixture(t, "spike_architecture.html")

	// Stored form: two inline figures plus the complex track as a fence.
	if n := strings.Count(h, "<svg"); n != 2 {
		t.Errorf("stored twin has %d inline <svg>, want exactly 2 (simple hand-written + dense "+
			"complex); the frozen figure must stay an uncompiled fence", n)
	}
	if !strings.Contains(h, `<code class="language-d2">`) {
		t.Error("complex track is not carried as a d2 fence — if it was inlined as SVG, the " +
			"sanitizer will strip its stylesheet and it will render unpainted")
	}
	if !strings.Contains(h, "three step flow") {
		t.Error("simple hand-written track missing")
	}
	if !strings.Contains(h, "aihub render refactor architecture") {
		t.Error("dense complex track missing")
	}

	// Served form: sanitize first, compile second — the production ordering.
	out := RenderDiagramsGated(SanitizeArtifactHTML(h))

	if n := strings.Count(out, "<svg"); n < 3 {
		t.Errorf("served document has %d <svg>, want >= 3 — the fence did not compile", n)
	}
	if strings.Contains(out, `class="language-d2"`) {
		t.Error("a d2 fence survived uncompiled into the served document")
	}
	// The whole reason for the ordering: the compiled figure keeps d2's theming, which the
	// sanitizer would have removed had it run afterwards.
	for _, need := range []string{"<style", ".fill-", ".stroke-"} {
		if !strings.Contains(out, need) {
			t.Errorf("compiled figure is missing %q — d2's theming did not survive, so the "+
				"figure renders with no fills or strokes", need)
		}
	}
}

// End to end: the artifact goes through the layer that will actually serve it.
func TestSpikeArtifact_EmbedsIntoSandboxIntact(t *testing.T) {
	h := fixture(t, "spike_architecture.html")

	out := SafeEmbedDocument(h, EmbedOptions{
		Title:        "aihub#240 spike",
		BridgeScript: AnnotationBridgeFor("https://aihub.example"),
		nonce:        "SPIKE",
	})

	if got := sandboxAttrOf(t, out); got != "allow-scripts" {
		t.Fatalf("sandbox = %q", got)
	}
	doc := innerDoc(t, out)
	for _, want := range []string{"linearGradient", "feDropShadow", "clipPath", "aihub#240 — P0 spike artifact"} {
		if !strings.Contains(doc, want) {
			t.Errorf("embedding lost %q", want)
		}
	}
	if strings.Contains(doc, "<script>") {
		t.Error("an unnonced script reached the frame")
	}
}

// TestSpikeArtifact_CaptionCountsMatchTheMarkup closes the gap that let a wrong number
// ship in the first place.
//
// The dense figure's caption asserted "26 edges" while the SVG carried 25. Nothing
// caught it: the generator that built the figure printed the right count, the caption was
// written by hand, and every structural test only checked that counts exceeded a floor.
// The semantic md<->html consistency check (architecture step 2) found it — which is the
// point of that step, but a deterministic guard is cheaper than an LLM pass for something
// this mechanical.
//
// Edges are counted as paths carrying marker-end: the arrowhead path defined inside
// <marker> is geometry for the marker itself, not an edge, and conflating the two is
// exactly how the off-by-one arose.
func TestSpikeArtifact_CaptionCountsMatchTheMarkup(t *testing.T) {
	h := fixture(t, "spike_architecture.html")

	i := strings.Index(h, `aria-label="aihub render refactor architecture"`)
	if i < 0 {
		t.Fatal("dense figure not found")
	}
	start := strings.LastIndex(h[:i], "<svg")
	end := strings.Index(h[i:], "</svg>") + i + len("</svg>")
	svg := h[start:end]

	claimed := regexp.MustCompile(`(\d+) nodes, (\d+) edges`).FindStringSubmatch(h)
	if claimed == nil {
		t.Fatal("caption makes no node/edge claim")
	}

	nodes := len(regexp.MustCompile(`<rect[^>]*fill="url\(#g\d+\)"`).FindAllString(svg, -1))
	var edges int
	for _, p := range regexp.MustCompile(`<path\b[^>]*>`).FindAllString(svg, -1) {
		if strings.Contains(p, "marker-end") {
			edges++
		}
	}

	if claimed[1] != strconv.Itoa(nodes) {
		t.Errorf("caption claims %s nodes, markup has %d", claimed[1], nodes)
	}
	if claimed[2] != strconv.Itoa(edges) {
		t.Errorf("caption claims %s edges, markup has %d", claimed[2], edges)
	}

	// Both twins must carry the same numbers.
	md := fixture(t, "spike_architecture.md")
	if !strings.Contains(md, claimed[0]) {
		t.Errorf("markdown twin does not carry the same claim %q", claimed[0])
	}
}
