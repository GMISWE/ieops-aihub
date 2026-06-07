package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html"
	"strings"
	"sync"
	"sync/atomic"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2layouts/d2dagrelayout"
	"oss.terrastruct.com/d2/d2lib"
	"oss.terrastruct.com/d2/d2renderers/d2svg"
	"oss.terrastruct.com/d2/d2target"
	"oss.terrastruct.com/d2/d2themes/d2themescatalog"
	"oss.terrastruct.com/d2/lib/textmeasure"
)

// diagramEntry caches one RenderDiagram result. ok=false means the src failed to
// compile/render — that outcome is cached too, so a malformed block isn't retried
// on every request.
type diagramEntry struct {
	svg string
	ok  bool
}

// diagramCache memoizes RenderDiagram by src. Rendering is pure (theme/font/pad
// are compile-time constants), so a given src always yields byte-identical SVG.
var diagramCache = struct {
	mu sync.RWMutex
	m  map[string]diagramEntry
}{m: make(map[string]diagramEntry)}

const diagramCacheCap = 512

// diagramCacheMisses is incremented on every cache miss; only read by tests
// (to assert a second render of the same src is served from cache).
var diagramCacheMisses atomic.Int64

// errDiagramCached is returned on a hit of a previously-failed src, so callers
// keep their err != nil fallback path without re-running the compiler.
var errDiagramCached = errors.New("d2 diagram failed to render")

// RenderDiagram compiles a d2 source string into an inline <svg> (aihub#160).
// Pure Go (D2 lays out via goja). Used /ui-only — see RenderDiagramsForUI; the
// /v1 + /share paths keep the raw code block so their byte output is unchanged.
// Results are memoized in diagramCache keyed by src (success and failure both).
//
// Callers must treat a render as successful only when err == nil AND the result
// contains an <svg> element: on a cache hit of a previously-failed src this
// returns ("", errDiagramCached), not the first call's raw (svg, err).
// RenderDiagramsForUI already gates on both conditions.
func RenderDiagram(src string) (string, error) {
	sum := sha256.Sum256([]byte(src))
	key := hex.EncodeToString(sum[:])

	diagramCache.mu.RLock()
	e, hit := diagramCache.m[key]
	diagramCache.mu.RUnlock()
	if hit {
		if e.ok {
			return e.svg, nil
		}
		return "", errDiagramCached
	}

	diagramCacheMisses.Add(1)
	svg, err := renderDiagramUncached(src)

	if err == nil && strings.Contains(svg, "<svg") {
		diagramCachePut(key, diagramEntry{svg: svg, ok: true})
	} else {
		// Cache failures too, so a malformed d2 block isn't recompiled on every
		// request. Trade-off: a rare transient/env error (e.g. textmeasure ruler
		// init) is also pinned to this src until the cache flushes — acceptable
		// since d2 render failures are overwhelmingly deterministic syntax errors.
		diagramCachePut(key, diagramEntry{ok: false})
	}

	return svg, err
}

// diagramCachePut stores one entry, flushing the whole cache first if it has
// reached diagramCacheCap. Flush is the simplest bounded policy — rendering is
// idempotent, so a cold cache only costs recompute, never correctness.
func diagramCachePut(key string, e diagramEntry) {
	diagramCache.mu.Lock()
	defer diagramCache.mu.Unlock()
	if len(diagramCache.m) >= diagramCacheCap {
		diagramCache.m = make(map[string]diagramEntry)
	}
	diagramCache.m[key] = e
}

// renderDiagramUncached holds the original (uncached) compile+render pipeline.
func renderDiagramUncached(src string) (string, error) {
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
