package render

import (
	"context"
	"html"
	"strings"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2layouts/d2dagrelayout"
	"oss.terrastruct.com/d2/d2lib"
	"oss.terrastruct.com/d2/d2renderers/d2svg"
	"oss.terrastruct.com/d2/d2target"
	"oss.terrastruct.com/d2/d2themes/d2themescatalog"
	"oss.terrastruct.com/d2/lib/textmeasure"
)

// RenderDiagram compiles a d2 source string into an inline <svg> (aihub#160).
// Pure Go (D2 lays out via goja). Used /ui-only — see RenderDiagramsForUI; the
// /v1 + /share paths keep the raw code block so their byte output is unchanged.
func RenderDiagram(src string) (string, error) {
	ruler, err := textmeasure.NewRuler()
	if err != nil {
		return "", err
	}
	compileOpts := &d2lib.CompileOptions{
		Ruler: ruler,
		LayoutResolver: func(engine string) (d2graph.LayoutGraph, error) {
			return d2dagrelayout.DefaultLayout, nil
		},
	}
	// Base on NeutralGrey, then override every color slot with the #129 warm-grey
	// ramp (text→surface) so the diagram is truly monochrome — NeutralGrey itself
	// is blue-tinted. Pad trims D2's large default margin so the figure stays compact.
	themeID := d2themescatalog.NeutralGrey.ID
	pad := int64(20)
	sp := func(s string) *string { return &s }
	renderOpts := &d2svg.RenderOpts{
		ThemeID: &themeID,
		Pad:     &pad,
		ThemeOverrides: &d2target.ThemeOverrides{
			N1: sp("#1c1c20"), N2: sp("#646469"), N3: sp("#94949b"), N4: sp("#d6d5d0"), N5: sp("#e6e5e1"), N6: sp("#f6f6f4"), N7: sp("transparent"),
			B1: sp("#646469"), B2: sp("#646469"), B3: sp("#94949b"), B4: sp("#d6d5d0"), B5: sp("#e6e5e1"), B6: sp("#ffffff"),
			AA2: sp("#646469"), AA4: sp("#d6d5d0"), AA5: sp("#e6e5e1"),
			AB4: sp("#d6d5d0"), AB5: sp("#e6e5e1"),
		},
	}
	// Bump the default node font so labels stay legible after the figure is scaled
	// down for display. Author d2 can still override per-shape.
	src = "**.style.font-size: 24\n" + src
	diagram, _, err := d2lib.Compile(context.Background(), src, compileOpts, renderOpts)
	if err != nil {
		return "", err
	}
	svg, err := d2svg.Render(diagram, renderOpts)
	if err != nil {
		return "", err
	}
	return string(svg), nil
}

// RenderDiagramsForUI rewrites goldmark's `d2` fenced code blocks into inline SVG
// figures (aihub#160). It is a /ui-only post-process over the rendered HTML body:
// the /v1 + /share output is never passed through it, so their bytes stay frozen.
// A diagram that fails to compile is left as its original code block (graceful
// degradation — never drops content, never panics).
func RenderDiagramsForUI(h string) string {
	// goldmark renders ```d2 as <pre><code class="language-d2">…escaped src…</code></pre>.
	const open = `<pre><code class="language-d2">`
	const closeTag = `</code></pre>`
	if !strings.Contains(h, open) {
		return h
	}
	var b strings.Builder
	b.Grow(len(h) + 1024)
	rest := h
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+len(open):]
		j := strings.Index(rest, closeTag)
		if j < 0 { // malformed; emit verbatim and stop
			b.WriteString(open)
			b.WriteString(rest)
			break
		}
		srcEscaped := rest[:j]
		rest = rest[j+len(closeTag):]
		src := html.UnescapeString(srcEscaped)
		if svg, err := RenderDiagram(src); err == nil && strings.Contains(svg, "<svg") {
			b.WriteString(`<figure class="pf-diagram">`)
			b.WriteString(svg)
			b.WriteString(`</figure>`)
		} else {
			// keep the original code block on failure
			b.WriteString(open)
			b.WriteString(srcEscaped)
			b.WriteString(closeTag)
		}
	}
	return b.String()
}
