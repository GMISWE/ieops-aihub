package render

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestD2Gate_MarkupEmittingFeaturesAreRefused is the regression suite for a stored XSS on the
// authed artifact viewer.
//
// The compiled figure is inserted AFTER SanitizeArtifactHTML, on the reasoning that d2's output
// is our own. It is not: it is a function of the agent's fence body, and three d2 features pass
// that body through as markup. Each case below was observed reaching the served /ui document.
func TestD2Gate_MarkupEmittingFeaturesAreRefused(t *testing.T) {
	cases := []struct {
		name string
		d2   string
		// Live constructs that must not appear. Deliberately NOT the payload text: a refused
		// fence degrades to its code block, where the source is HTML-escaped, so `alert(1)` and
		// `mem_attacker` legitimately DO appear as inert text. Asserting their absence would
		// demand the content be dropped, which is neither required nor desirable.
		gone []string
	}{
		{
			name: "md block emits a live script through foreignObject",
			d2:   `x: |md **hi** <script>alert(1)</script> |`,
			gone: []string{"<foreignObject", "<script>", "<script "},
		},
		{
			name: "md block emits a full-viewport overlay",
			d2:   `x: |md <div style="position:fixed;top:0;left:0;width:100vw;height:100vh;z-index:99999">PHISH</div> |`,
			gone: []string{"<foreignObject", "<div "},
		},
		{
			name: "md block forges the annotation island the viewer chrome reads",
			d2:   `x: |md <script id="pf-annot-data" type="application/json">{"mem_id":"mem_attacker"}</script> |`,
			gone: []string{"<foreignObject", "<script>", "<script "},
		},
		{
			name: "md block emits a nested browsing context",
			d2:   `x: |md <iframe src="https://evil.example"></iframe> |`,
			gone: []string{"<foreignObject", "<iframe"},
		},
		{
			name: "latex block is the same channel",
			d2:   "x: |latex \\text{hi} |",
			gone: []string{"<foreignObject"},
		},
		{
			name: "link attribute emits a javascript: navigation",
			d2:   "x: hello\nx.link: \"javascript:alert(1)\"",
			gone: []string{"href=\"javascript:", "xlink:href=\"javascript:"},
		},
		{
			name: "icon attribute emits an external image reference",
			d2:   "x: hello\nx.icon: https://evil.example/px.png",
			gone: []string{"href=\"https://evil.example", "xlink:href=\"https://evil.example"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderDiagramsGated(fenceHTML(tc.d2))

			for _, bad := range tc.gone {
				if strings.Contains(out, bad) {
					t.Errorf("gated output still carries %q:\n%.500s", bad, out)
				}
			}
			// Fail closed means: degrade to the original code block, never drop the content and
			// never emit a half-figure. The reader sees the DSL instead of a diagram.
			if !strings.Contains(out, `<code class="language-d2">`) {
				t.Errorf("rejected fence did not degrade to its code block:\n%.500s", out)
			}
			if strings.Contains(out, "<figure class=\"pf-diagram\">") {
				t.Errorf("rejected fence still produced a figure:\n%.300s", out)
			}
		})
	}
}

// The gate must not break legitimate diagrams — that is the failure mode it would be easy to
// ship. A refusal that also refuses everything is not a fix.
func TestD2Gate_OrdinaryDiagramsStillCompile(t *testing.T) {
	for _, src := range []string{
		"a -> b: hello",
		"direction: right\nx: box\ny: {shape: cylinder}\nx -> y: edge",
		"a: A\nb: B\nc: C\na -> b\nb -> c\nc -> a",
		// A style block is not a markup block; `style` must not be caught by the `link`/`icon`
		// patterns or by anything else.
		"x: hi\nx.style.fill: \"#123456\"\nx.style.stroke-width: 2",
		// The word "link" inside a label is not the link attribute.
		"x: this mentions a link: in prose",
	} {
		out := RenderDiagramsGated(fenceHTML(src))
		if !strings.Contains(out, `<figure class="pf-diagram">`) || !strings.Contains(out, "<svg") {
			t.Errorf("legitimate diagram was refused:\n  src: %s\n  out: %.300s", src, out)
		}
		// And it must keep the theming the whole D2/D6 coupling exists to preserve.
		if !strings.Contains(out, "<style") || !strings.Contains(out, ".fill-") {
			t.Errorf("compiled figure lost d2's stylesheet:\n  src: %s", src)
		}
	}
}

// The gate reads the fence body the way the compiler does. If it checked the escaped form, a
// payload that only becomes d2 syntax after unescaping would walk straight through.
func TestD2Gate_ChecksTheUnescapedSourceTheCompilerSees(t *testing.T) {
	// goldmark escapes the fence body; diagram.go unescapes it before compiling. `&#124;` is `|`.
	escaped := `x: &#124;md **hi** &lt;script&gt;alert(1)&lt;/script&gt; &#124;`
	out := RenderDiagramsGated(`<pre><code class="language-d2">` + escaped + `</code></pre>`)
	for _, bad := range []string{"<foreignObject", "<script>", "<script "} {
		if strings.Contains(out, bad) {
			t.Errorf("entity-encoded markup block bypassed the gate (%q):\n%.400s", bad, out)
		}
	}
	if !strings.Contains(out, `<code class="language-d2">`) {
		t.Errorf("did not degrade to a code block:\n%.400s", out)
	}
}

// Belt 2 in isolation: whatever the source looked like, output carrying these constructs is
// refused. This is what catches a d2 feature nobody enumerated.
func TestD2Gate_OutputBeltRejectsMarkupRegardlessOfSource(t *testing.T) {
	cases := map[string]string{
		"foreignObject":     `<svg><foreignObject><div>x</div></foreignObject></svg>`,
		"script":            `<svg><script>alert(1)</script></svg>`,
		"javascript url":    `<svg><a href="javascript:alert(1)">x</a></svg>`,
		"external href":     `<svg><image href="https://evil.example/x.png"/></svg>`,
		"protocol-relative": `<svg><image xlink:href="//evil.example/x.png"/></svg>`,
	}
	for name, svg := range cases {
		if d2OutputRejects(svg) == "" {
			t.Errorf("output belt accepted %s: %s", name, svg)
		}
	}
	// And it must accept what d2 legitimately emits: same-document refs and data: fonts.
	for name, svg := range map[string]string{
		"same-document ref":  `<svg><rect fill="url(#g1)"/><use href="#sym"/></svg>`,
		"data font in style": `<svg><style>@font-face{src:url("data:application/font-woff;base64,AAAA")}</style></svg>`,
		"data image":         `<svg><image xlink:href="data:image/png;base64,iVBORw0KGgo="/></svg>`,
	} {
		if r := d2OutputRejects(svg); r != "" {
			t.Errorf("output belt wrongly refused %s (%s): %s", name, r, svg)
		}
	}
}

// TestNoProductionCallerUsesUngatedDiagramRendering keeps the distinction the gate depends on.
//
// RenderDiagramsForUI stays exported (diagram.go is read-only in this spike, and /v1 + /share
// must keep their frozen bytes), so nothing structural stops a future call site from using it
// and reopening the channel. This is that guard.
func TestNoProductionCallerUsesUngatedDiagramRendering(t *testing.T) {
	// "." is this package. It was missing, and ".." is internal/ — which holds only
	// directories, and the walker skips those — so the guard scanned internal/server only
	// and safeembed.go, a real production call site, was invisible to it. A working-tree
	// accident on 2026-08-12 reverted that exact line to the ungated call and the whole
	// suite stayed green, which is the failure this guard exists to prevent.
	files := goSourceFiles(t, []string{".", "..", "../server"}, func(name string) bool {
		// Tests may call the ungated function deliberately — diagram_test.go tests it directly.
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") &&
			!strings.HasSuffix(name, "/diagram.go")
	})
	re := regexp.MustCompile(`\bRenderDiagramsForUI\s*\(`)
	for name, src := range files {
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // a comment may name it
			}
			if re.MatchString(line) {
				t.Errorf("%s calls RenderDiagramsForUI directly:\n  %s\nProduction code must use "+
					"RenderDiagramsGated — the ungated function inserts compiler output over "+
					"agent-controlled input straight past the sanitizer", name, trimmed)
			}
		}
	}
	if len(files) == 0 {
		t.Fatal("scanned no files; this guard is not looking at anything")
	}
}

func fenceHTML(d2src string) string {
	var esc strings.Builder
	for _, r := range d2src {
		switch r {
		case '<':
			esc.WriteString("&lt;")
		case '>':
			esc.WriteString("&gt;")
		case '&':
			esc.WriteString("&amp;")
		case '"':
			esc.WriteString("&#34;")
		default:
			esc.WriteRune(r)
		}
	}
	return `<pre><code class="language-d2">` + esc.String() + `</code></pre>`
}

// goSourceFiles reads Go sources from the given directories (relative to this package) that pass
// the filter. Reading from disk rather than parsing an import graph keeps the guard simple and
// keeps it honest about what it scanned — it fails if it scanned nothing.
func goSourceFiles(t *testing.T, dirs []string, keep func(string) bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			full := dir + "/" + e.Name()
			if !keep(full) {
				continue
			}
			b, err := os.ReadFile(full)
			if err != nil {
				t.Fatalf("read %s: %v", full, err)
			}
			out[full] = string(b)
		}
	}
	return out
}

// TestNarrowerLayout_RelayoutsAnOverWideGraph runs the real fixture figure through the gate and
// checks the layout was actually re-run, not merely marked.
//
// The numbers matter more than the pass/fail here: `direction: right` on this graph produces
// 4295px against an ~800px column, and the whole reason this code exists is that scaling that to
// fit turns 24px labels into 4.5px.
func TestNarrowerLayout_RelayoutsAnOverWideGraph(t *testing.T) {
	src, err := os.ReadFile("../../test/render/fixtures/spike_architecture.md")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	fence := regexp.MustCompile("(?s)```d2\\n(.*?)\\n```").FindStringSubmatch(string(src))
	if fence == nil {
		t.Fatal("fixture has no d2 fence; this guard is looking at nothing")
	}
	d2src := fence[1]
	if !strings.Contains(d2src, "direction: right") {
		t.Skip("fixture no longer lays out horizontally; nothing to re-layout")
	}

	wide, err := RenderDiagram(d2src)
	if err != nil {
		t.Fatalf("compile as authored: %v", err)
	}
	wideW := svgIntrinsicWidth(wide)
	if wideW <= wideFigurePx {
		t.Skipf("authored layout is already %vpx, under the %dpx threshold", wideW, wideFigurePx)
	}

	got := narrowerLayout(d2src, wide)
	gotW := svgIntrinsicWidth(got)
	t.Logf("authored %vpx → re-laid-out %vpx", wideW, gotW)

	if got == wide {
		t.Fatalf("figure was %vpx and was not re-laid-out", wideW)
	}
	if gotW >= wideW {
		t.Errorf("re-layout must be narrower: %v >= %v", gotW, wideW)
	}
	// Still a real figure, and still gated.
	if !strings.Contains(got, "<svg") {
		t.Error("re-layout produced no svg")
	}
	if why := d2OutputRejects(got); why != "" {
		t.Errorf("re-layout output must still clear belt 2, got %q", why)
	}
	// The label size the whole exercise is about: at ~800px the scale must keep 24px legible.
	const column = 800.0
	if scale := column / gotW; scale < 0.5 {
		t.Errorf("re-layout still scales to %.2f (labels ~%.1fpx); vertical layout did not help enough",
			scale, 24*scale)
	}
}

// TestNarrowerLayout_LeavesNarrowFiguresAlone is the other half: a figure that already fits must
// come back byte-identical, or every ordinary diagram pays for a second compile.
func TestNarrowerLayout_LeavesNarrowFiguresAlone(t *testing.T) {
	svg, err := RenderDiagram("a -> b")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if w := svgIntrinsicWidth(svg); w > wideFigurePx {
		t.Skipf("two-node graph is %vpx, unexpectedly over threshold", w)
	}
	if got := narrowerLayout("a -> b", svg); got != svg {
		t.Error("a figure that already fits must be returned unchanged")
	}
}

// TestNarrowerLayout_OnlyRewritesTopLevelDirection pins the scope of the rewrite: a container's
// own direction is a shape the author chose and must survive.
func TestNarrowerLayout_OnlyRewritesTopLevelDirection(t *testing.T) {
	src := "direction: right\ngrp: g {\n  direction: right\n  a -> b\n}\n"
	out := reTopLevelDirection.ReplaceAllString(src, "")
	if strings.Contains(out, "\ndirection: right") || strings.HasPrefix(out, "direction: right") {
		t.Error("top-level direction should have been removed")
	}
	if !strings.Contains(out, "  direction: right") {
		t.Error("indented (container) direction must be left alone")
	}
}
