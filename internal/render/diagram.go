package render

import (
	"context"
	"html"
	"strings"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2layouts/d2dagrelayout"
	"oss.terrastruct.com/d2/d2lib"
	"oss.terrastruct.com/d2/d2renderers/d2svg"
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
	renderOpts := &d2svg.RenderOpts{}
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
