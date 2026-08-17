package render

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// restoreDiagramTimeout resets the compile budget and clock after a test.
func restoreDiagramTimeout(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		SetDiagramCompileTimeout(DefaultDiagramCompileTimeout)
		diagramNow = time.Now
	})
}

// flushDiagramCache empties the memo so a test starts from a known state.
func flushDiagramCache() {
	diagramCache.mu.Lock()
	diagramCache.m = make(map[string]diagramEntry)
	diagramCache.mu.Unlock()
}

// TestDiagramCompileTimeoutReturnsRatherThanHanging is the wi's headline
// acceptance criterion: a compile that cannot finish inside its budget must
// return an error at the deadline instead of holding the caller.
//
// The budget is set below what a real compile takes (~50ms on this codebase) so
// the deadline is reached deterministically without needing a pathological src.
func TestDiagramCompileTimeoutReturnsRatherThanHanging(t *testing.T) {
	restoreDiagramTimeout(t)
	flushDiagramCache()

	SetDiagramCompileTimeout(1 * time.Millisecond)

	start := time.Now()
	svg, err := renderDiagramUncached("a -> b\nb -> c\nc -> d\nd -> e")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a timeout error, got nil (svg %d bytes)", len(svg))
	}
	if !errors.Is(err, errDiagramTimeout) {
		t.Fatalf("expected errDiagramTimeout, got %v", err)
	}
	if svg != "" {
		t.Errorf("a timed-out compile must not return partial output, got %d bytes", len(svg))
	}
	// The whole point is that the caller is released promptly. A real compile of
	// this src takes ~50ms; returning well inside that proves we did not wait for it.
	if elapsed > 25*time.Millisecond {
		t.Errorf("caller was held for %s — the deadline did not release it", elapsed)
	}
}

// TestDiagramTimeoutIsNotPinnedPermanently is the second acceptance criterion,
// and the one the wi flags as easiest to miss: a timeout must not be written
// into the negative cache as a permanent verdict, or one slow moment turns a
// valid figure into a code block for the life of the cache.
func TestDiagramTimeoutIsNotPinnedPermanently(t *testing.T) {
	restoreDiagramTimeout(t)
	flushDiagramCache()

	const src = "pinned -> not\nnot -> ever"

	// Force a timeout.
	SetDiagramCompileTimeout(1 * time.Millisecond)
	if _, err := RenderDiagram(src); !errors.Is(err, errDiagramTimeout) {
		t.Fatalf("setup: expected a timeout, got %v", err)
	}

	// Give it a real budget again. The src is perfectly valid, so once the
	// suppression window passes it must compile — if the timeout had been pinned
	// the way an ordinary failure is, this would return errDiagramCached forever.
	SetDiagramCompileTimeout(DefaultDiagramCompileTimeout)

	// Inside the window it stays suppressed (this is deliberate — see
	// timeoutNegativeTTL) ...
	if _, err := RenderDiagram(src); !errors.Is(err, errDiagramCached) {
		t.Fatalf("inside the TTL a timed-out src should stay suppressed, got %v", err)
	}

	// ... and past it, it heals.
	diagramNow = func() time.Time { return time.Now().Add(timeoutNegativeTTL + time.Second) }

	svg, err := RenderDiagram(src)
	if err != nil {
		t.Fatalf("after the TTL the src must be retried and succeed, got %v", err)
	}
	if !strings.Contains(svg, "<svg") {
		t.Fatalf("expected a real render after the TTL, got %d bytes", len(svg))
	}
}

// TestDiagramSyntaxErrorStaysPinned guards the other side of the same branch:
// excluding timeouts must not accidentally stop caching ordinary failures, which
// is what keeps a malformed block from recompiling on every request.
func TestDiagramSyntaxErrorStaysPinned(t *testing.T) {
	restoreDiagramTimeout(t)
	flushDiagramCache()

	const bad = ">>> not valid d2 <<<"
	base := diagramCacheMisses.Load()

	if _, err := RenderDiagram(bad); err == nil {
		t.Fatal("expected a syntax error")
	}
	// Age well past the timeout TTL: a syntax failure has no expiry, so this
	// must change nothing.
	diagramNow = func() time.Time { return time.Now().Add(10 * timeoutNegativeTTL) }

	if _, err := RenderDiagram(bad); !errors.Is(err, errDiagramCached) {
		t.Fatalf("a syntax error must stay pinned, got %v", err)
	}
	if got := diagramCacheMisses.Load() - base; got != 1 {
		t.Fatalf("expected the syntax error to be compiled exactly once, got %d misses", got)
	}
}

// TestDiagramTimeoutDegradesToCodeBlock covers the user-visible half of the
// acceptance list: a timeout must leave the original d2 code block on the page,
// not a 500 and not an empty figure.
func TestDiagramTimeoutDegradesToCodeBlock(t *testing.T) {
	restoreDiagramTimeout(t)
	flushDiagramCache()

	md, err := Markdown("before\n\n```d2\ntimeout -> degrades\n```\n\nafter")
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	SetDiagramCompileTimeout(1 * time.Millisecond)
	out := RenderDiagramsGated(md)

	if strings.Contains(out, "<svg") {
		t.Error("a timed-out diagram must not emit an <svg>")
	}
	if !strings.Contains(out, `class="language-d2"`) {
		t.Error("the original d2 code block must be preserved on timeout")
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Error("surrounding prose must survive a timeout")
	}
}

func TestInitDiagramCompileTimeout(t *testing.T) {
	restoreDiagramTimeout(t)

	cases := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"parses a duration", "250ms", 250 * time.Millisecond},
		{"tolerates surrounding space", "  2s  ", 2 * time.Second},
		{"empty keeps the default", "", DefaultDiagramCompileTimeout},
		{"garbage keeps the default", "not-a-duration", DefaultDiagramCompileTimeout},
		{"zero keeps the default", "0s", DefaultDiagramCompileTimeout},
		{"negative keeps the default", "-5s", DefaultDiagramCompileTimeout},
		// A ceiling matters more than it looks: without it this env var is a
		// config-shaped way to restore the unbounded compile the wi removed.
		{"clamps an absurd value", "24h", MaxDiagramCompileTimeout},
		{"clamps just over the max", "31s", MaxDiagramCompileTimeout},
		{"accepts the max exactly", "30s", MaxDiagramCompileTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetDiagramCompileTimeout(DefaultDiagramCompileTimeout)
			got := InitDiagramCompileTimeout(tc.raw)
			if got != tc.want {
				t.Errorf("InitDiagramCompileTimeout(%q) = %s, want %s", tc.raw, got, tc.want)
			}
			if live := DiagramCompileTimeout(); live != tc.want {
				t.Errorf("live budget = %s, want %s", live, tc.want)
			}
		})
	}
}

// TestDiagramCompileIsActuallyBounded pins the finding that motivated the
// goroutine race: d2 v0.7.1 does not observe the context, so a context-only fix
// is inert. If a future d2 starts honouring cancellation this test still passes
// — it asserts the property (bounded) rather than the mechanism.
func TestDiagramCompileIsActuallyBounded(t *testing.T) {
	restoreDiagramTimeout(t)
	flushDiagramCache()

	SetDiagramCompileTimeout(1 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = renderDiagramUncached("bounded -> or -> not")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("renderDiagramUncached did not return within 2s of a 1ms budget — the compile is not bounded")
	}
}
