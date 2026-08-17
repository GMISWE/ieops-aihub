package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
//
// until is the expiry for entries that must NOT be permanent. Zero means "no
// expiry", which is every entry except a timeout (see RenderDiagram): a deadline
// is a fact about the moment, so it earns a short suppression window rather than
// a permanent verdict on the source.
type diagramEntry struct {
	svg   string
	ok    bool
	until time.Time
}

// expired reports whether a bounded entry has aged out.
func (e diagramEntry) expired(now time.Time) bool {
	return !e.until.IsZero() && now.After(e.until)
}

// timeoutNegativeTTL is how long a timed-out src is suppressed before it is
// retried. It is a balance between the two ways of being wrong:
//
//   - Never caching a timeout means every request re-enters a compile that is,
//     by hypothesis, wedged — and because a wedged goja layout cannot be
//     interrupted (see renderDiagramUncached), each of those abandons a
//     goroutine. That turns one bad figure into an unbounded leak under load.
//   - Caching it permanently is the bug this wi exists to prevent: one slow
//     moment demotes a valid figure to a code block for the life of the cache.
//
// A short window gives up neither: the figure self-heals within seconds, and the
// abandoned-goroutine count stops scaling with request count.
//
// # What this does NOT bound — read before relying on it
//
// There is no single-flight here. RenderDiagram releases the cache lock before
// compiling, so every request that arrives for the same uncached src BEFORE the
// negative entry is written starts its own compile. The bound is therefore:
//
//	arrival concurrency during one budget  ×  one budget per TTL window  ×  per src
//
// not "one goroutine per window". Measured on this code with 50 concurrent
// requests for one wedging src at a 1ms budget: two independent runs produced 11
// and 35 concurrent compiles. The spread is the point — the count is whatever the
// scheduler admits before the negative entry lands, so there is no fixed number
// to rely on, only a burst bound. It is still far better than one-per-request,
// which is the comparison that justifies the TTL.
//
// Closing the gap needs single-flight (concurrent callers for one key waiting on
// one compile). That is deliberately not done here: it changes the concurrency
// behaviour of every render, not just timing-out ones, and this wi is the
// backstop, not the cure. It belongs with aihub#244, which owns making a wedged
// compile genuinely reclaimable.
const timeoutNegativeTTL = 30 * time.Second

// diagramNow is time.Now, indirected so tests can age the cache without sleeping.
var diagramNow = time.Now

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

// errDiagramTimeout wraps any compile that ended because its deadline expired.
// It exists to keep such a failure OUT of the negative cache (see RenderDiagram):
// a deadline describes the moment, not the source, and pinning it would turn one
// slow render into a permanently degraded figure.
var errDiagramTimeout = errors.New("d2 compile exceeded deadline")

// DefaultDiagramCompileTimeout bounds a single d2 compile. Normal compiles finish
// in milliseconds to low hundreds of milliseconds, so 5s is loose enough never to
// cut a legitimate figure while still reclaiming a wedged one promptly.
const DefaultDiagramCompileTimeout = 5 * time.Second

// MaxDiagramCompileTimeout caps what configuration may ask for.
//
// Without a ceiling, DIAGRAM_COMPILE_TIMEOUT=24h is accepted and silently
// restores exactly the unbounded behaviour this change exists to remove — a
// config-shaped way to reintroduce the defect, with no error to notice.
//
// 30s is not arbitrary: cmd/aihub's WriteTimeout is 60s and diagram_gate's
// narrowerLayout can compile one wide figure twice, so anything above 30s could
// let a single figure outlive the response it belongs to.
// cmd/aihub's TestWriteTimeoutClearsDiagramBudget pins that relationship from
// the other side.
const MaxDiagramCompileTimeout = 30 * time.Second

// diagramCompileTimeout is the live per-compile budget, in nanoseconds. Stored
// atomically because it is read on every render (concurrent request goroutines)
// and written by InitDiagramCompileTimeout at startup and by tests.
var diagramCompileTimeout atomic.Int64

func init() { diagramCompileTimeout.Store(int64(DefaultDiagramCompileTimeout)) }

// DiagramCompileTimeout reports the current per-compile budget.
func DiagramCompileTimeout() time.Duration {
	return time.Duration(diagramCompileTimeout.Load())
}

// SetDiagramCompileTimeout sets the per-compile budget. A non-positive value
// restores the default; anything above MaxDiagramCompileTimeout is clamped to it.
// Returns the value now in effect.
func SetDiagramCompileTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		d = DefaultDiagramCompileTimeout
	}
	if d > MaxDiagramCompileTimeout {
		d = MaxDiagramCompileTimeout
	}
	diagramCompileTimeout.Store(int64(d))
	return d
}

// InitDiagramCompileTimeout configures the budget from a raw env value (a
// time.Duration string such as "5s" or "800ms"), following the same
// os.Getenv-into-an-Init-function shape as domain.InitRenderTypes. An empty or
// unparseable value keeps the default, so a typo degrades to "still bounded"
// rather than "unbounded again". Returns the value now in effect.
func InitDiagramCompileTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DiagramCompileTimeout()
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DiagramCompileTimeout()
	}
	return SetDiagramCompileTimeout(d)
}

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
	if hit && !e.expired(diagramNow()) {
		if e.ok {
			return e.svg, nil
		}
		return "", errDiagramCached
	}

	diagramCacheMisses.Add(1)
	svg, err := renderDiagramUncached(src)

	switch {
	case err == nil && strings.Contains(svg, "<svg"):
		diagramCachePut(key, diagramEntry{svg: svg, ok: true})
	case errors.Is(err, errDiagramTimeout):
		// A timeout is NOT a permanent verdict on this src. The comment below is
		// right that d2 failures are overwhelmingly deterministic syntax errors —
		// a deadline is the one failure that is explicitly not a property of the
		// source. Pinning it permanently would let a single slow moment demote a
		// valid figure to a raw code block for the whole life of the cache.
		//
		// It is still cached, briefly: not caching at all would re-enter a wedged
		// compile on every request and abandon a goroutine each time. See
		// timeoutNegativeTTL for that trade in full.
		diagramCachePut(key, diagramEntry{ok: false, until: diagramNow().Add(timeoutNegativeTTL)})
	default:
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

// renderDiagramUncached runs the compile+render pipeline under a deadline
// (aihub#250). Before this the pipeline ran on context.Background(): a layout
// that wedged inside d2's goja runtime held its request goroutine forever, with
// nothing in the process able to reclaim it.
//
// # Why this is a race and not just a context
//
// Handing d2lib.Compile a deadline-bearing context is necessary but, on its own,
// inert: d2 v0.7.1 does not check the context during layout. Measured, not
// assumed — with the budget set to 1ns the compile still ran to completion in
// ~50ms and returned a valid SVG. A change that only swapped the context would
// have read as a fix, passed review, and reclaimed nothing.
//
// So the compile is run on its own goroutine and the caller races it against the
// deadline. What that does and does not buy, stated exactly:
//
//   - It reclaims the REQUEST: the caller returns at the deadline, the figure
//     degrades to its original code block, and the connection is freed.
//   - It does NOT reclaim the WEDGED GOROUTINE. goja layout cannot be interrupted
//     mid-execution; the abandoned goroutine runs until it finishes or the process
//     exits. That is the root cause tracked in aihub#244 and deliberately out of
//     scope here — this wi is the backstop, not the cure.
//
// The abandoned goroutine writes to a buffered channel, so it never blocks on a
// receiver that has already gone away. timeoutNegativeTTL bounds how often a
// wedging src can spawn a fresh one.
func renderDiagramUncached(src string) (string, error) {
	budget := DiagramCompileTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	type outcome struct {
		svg string
		err error
	}
	// Buffered: the abandoned goroutine must be able to finish its send.
	done := make(chan outcome, 1)
	go func() {
		svg, err := compileDiagram(ctx, src)
		done <- outcome{svg: svg, err: err}
	}()

	select {
	case o := <-done:
		return o.svg, o.err
	case <-ctx.Done():
		return "", fmt.Errorf("%w after %s", errDiagramTimeout, budget)
	}
}

// compileDiagram is the original (unbounded) compile+render pipeline. It takes a
// context so d2 gets one, and so it will start honouring cancellation for free if
// a future d2 version begins checking it — but see renderDiagramUncached: as of
// v0.7.1 nothing here observes ctx, which is why the deadline is enforced by the
// caller racing this function rather than by ctx alone.
func compileDiagram(ctx context.Context, src string) (string, error) {
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
	// aihub#234: Scale=1 makes d2 emit width/height on the outer <svg> alongside the
	// viewBox. Without them the SVG has no intrinsic size, so CSS resolves its width
	// to 100% of whatever box it lands in — the figure was *upscaled* to fill the
	// column (a 264-wide flowchart stretched to 600px and 3400px of vertical scroll)
	// while wide diagrams were squeezed by the same rule. With an intrinsic size it
	// behaves like an image: `max-width:100%; height:auto` scales it down to fit the
	// column and never blows it up past 1:1.
	scale := 1.0
	sp := func(s string) *string { return &s }
	renderOpts := &d2svg.RenderOpts{
		ThemeID: &themeID,
		Pad:     &pad,
		Scale:   &scale,
		ThemeOverrides: &d2target.ThemeOverrides{
			N1: sp("#1c1c20"), N2: sp("#646469"), N3: sp("#94949b"), N4: sp("#d6d5d0"), N5: sp("#e6e5e1"), N6: sp("#f6f6f4"), N7: sp("transparent"),
			B1: sp("#646469"), B2: sp("#646469"), B3: sp("#94949b"), B4: sp("#d6d5d0"), B5: sp("#e6e5e1"), B6: sp("#ffffff"),
			AA2: sp("#646469"), AA4: sp("#d6d5d0"), AA5: sp("#e6e5e1"),
			AB4: sp("#d6d5d0"), AB5: sp("#e6e5e1"),
		},
	}
	// Bump the default node font so labels stay legible when a wide figure still has
	// to be scaled down to the column width. Author d2 can still override per-shape.
	src = "**.style.font-size: 24\n" + src
	diagram, _, err := d2lib.Compile(ctx, src, compileOpts, renderOpts)
	if err != nil {
		// Classify by asking the context, not by unwrapping d2's error: d2 does not
		// promise to wrap context.DeadlineExceeded, so errors.Is on its return value
		// alone would misfile a cancelled compile as a syntax error and cache it
		// permanently. This branch is dormant against v0.7.1 (which never returns
		// early) and becomes live the day d2 starts honouring the context.
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: %w", errDiagramTimeout, err)
		}
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
