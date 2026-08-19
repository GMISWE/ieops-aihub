package render

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

// restoreDiagramCompileSeam resets the compile seam and the abandonment cap.
//
// abandonedCompiles is deliberately NOT reset here: forcing it to zero would hide
// exactly the leak these tests exist to catch. A test that fills it must release
// its workers and let it drain on its own — see waitAbandoned.
func restoreDiagramCompileSeam(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		compileDiagramFn = compileDiagram
		maxAbandonedCompiles.Store(defaultMaxAbandonedCompiles)
	})
}

// waitAbandoned blocks until the in-flight abandoned count reaches want, or fails.
func waitAbandoned(t *testing.T, want int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := abandonedCompiles.Load()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned compiles in flight = %d, want %d after %s", got, want, within)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAbandonedCompilesAreCappedNotUnbounded is aihub#244's headline acceptance
// criterion: timing out N times must not put N uninterruptible goja evaluations
// in flight.
//
// aihub#250 bounded how OFTEN a wedging src is retried (timeoutNegativeTTL) but
// not how many of its abandoned workers can coexist, so on the read path the
// count still grew one per TTL window, forever, for as long as the src stayed
// wedged. Here the cap is lowered and the stub blocks instead of returning, which
// is the wedged-goja case without needing a source that genuinely wedges.
func TestAbandonedCompilesAreCappedNotUnbounded(t *testing.T) {
	restoreDiagramTimeout(t)
	restoreDiagramCompileSeam(t)
	flushDiagramCache()

	const limit = 3

	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	// Every stub worker must be released and drained, or the counter stays dirty
	// for every test that runs after this one.
	t.Cleanup(func() {
		releaseOnce()
		waitAbandoned(t, 0, 2*time.Second)
	})

	var started atomic.Int64
	compileDiagramFn = func(ctx context.Context, src string) (string, error) {
		started.Add(1)
		<-release
		return "<svg>late</svg>", nil
	}
	maxAbandonedCompiles.Store(limit)
	SetDiagramCompileTimeout(1 * time.Millisecond)

	// Precondition, not decoration. Four tests in this file time out a REAL compile
	// and return while the real goja worker is still running, so the counter stays
	// non-zero for ~50ms afterwards. This test passes today only because Go runs
	// tests in source order and those four sit below it; under -shuffle it fails.
	// Waiting for the drain — rather than resetting the counter, which would defeat
	// the point of tracking it at all — makes the test independent of order.
	waitAbandoned(t, 0, 2*time.Second)

	for i := 1; i <= limit; i++ {
		if _, err := renderDiagramUncached("wedged"); !errors.Is(err, errDiagramTimeout) {
			t.Fatalf("call %d: want errDiagramTimeout, got %v", i, err)
		}
	}
	// Deterministic, not racy: the workers are parked on <-release, so none of
	// them can have decremented yet.
	if got := abandonedCompiles.Load(); got != limit {
		t.Fatalf("after %d timeouts, abandoned = %d, want %d", limit, got, limit)
	}

	// The cap is reached. Further calls must be refused WITHOUT starting another
	// evaluation — refusing but still spawning would keep the leak and merely
	// change the error text.
	beforeRefusals := started.Load()
	for i := 1; i <= 5; i++ {
		if _, err := renderDiagramUncached("wedged"); !errors.Is(err, errDiagramOverloaded) {
			t.Fatalf("over-cap call %d: want errDiagramOverloaded, got %v", i, err)
		}
	}
	if extra := started.Load() - beforeRefusals; extra != 0 {
		t.Errorf("refused calls still started %d compile(s)", extra)
	}
	if got := abandonedCompiles.Load(); got != limit {
		t.Errorf("in-flight count grew past the cap: %d > %d", got, limit)
	}

	// A cap that never releases would be its own outage. Once the workers finish,
	// the count must fall back to zero and compiles must be admitted again.
	releaseOnce()
	waitAbandoned(t, 0, 2*time.Second)

	compileDiagramFn = func(ctx context.Context, src string) (string, error) {
		return "<svg>healthy</svg>", nil
	}
	SetDiagramCompileTimeout(DefaultDiagramCompileTimeout)
	if _, err := renderDiagramUncached("healthy again"); err != nil {
		t.Fatalf("after the pile drained, compiles must be admitted again: %v", err)
	}
}

// TestAbandonedCapDoesNotBoundASimultaneousBurst pins what the cap does NOT do.
//
// It exists because the first version of this change shipped a comment claiming
// the cap put "a hard ceiling" under a concurrent burst, which is false: admission
// tests a count that only rises AFTER a caller has timed out, so callers that
// arrive together all read the same pre-timeout value and are all let through. The
// cap bounds accumulation across windows, not the width of one burst.
//
// That distinction lives in a comment, and a comment in this package has now been
// wrong three times. So it is asserted here instead: if someone later makes
// admission genuinely bound concurrency, this test fails and tells them the
// comments describing the weaker guarantee are now the stale ones.
//
// Deterministic despite being about concurrency: the budget is long enough that
// every worker is inside the stub before any caller reaches its deadline, and the
// stub parks them there, so "all 8 entered" is a fact by the time it is read.
func TestAbandonedCapDoesNotBoundASimultaneousBurst(t *testing.T) {
	restoreDiagramTimeout(t)
	restoreDiagramCompileSeam(t)
	flushDiagramCache()

	const (
		workers = 8
		limit   = 2
	)

	entered := make(chan struct{}, workers)
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		releaseOnce()
		waitAbandoned(t, 0, 2*time.Second)
	})

	compileDiagramFn = func(ctx context.Context, src string) (string, error) {
		entered <- struct{}{}
		<-release
		return "<svg>late</svg>", nil
	}
	maxAbandonedCompiles.Store(limit)
	SetDiagramCompileTimeout(200 * time.Millisecond)

	waitAbandoned(t, 0, 2*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = renderDiagramUncached("one wedging figure, many readers")
		}()
	}

	// Every worker got in, at a cap of 2. If admission ever starts bounding
	// concurrency this receive stalls at i == limit and the test says so.
	for i := 0; i < workers; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d workers were admitted at cap %d — admission now bounds "+
				"a burst, so the comments saying it does not are stale", i, workers, limit)
		}
	}

	wg.Wait() // every caller has now hit its deadline and counted its worker
	if got := abandonedCompiles.Load(); got != workers {
		t.Fatalf("in flight = %d, want %d: the burst is bounded by arrival concurrency, "+
			"not by the cap", got, workers)
	}
}

// TestSuccessfulCompileIsNotCountedAbandoned guards the counter from the other
// side. A count that crept up on healthy traffic would eventually refuse every
// compile in a process that never leaked anything — the failure mode of a leak
// detector is a false positive, and this one is load-bearing.
func TestSuccessfulCompileIsNotCountedAbandoned(t *testing.T) {
	restoreDiagramTimeout(t)
	restoreDiagramCompileSeam(t)
	flushDiagramCache()

	compileDiagramFn = func(ctx context.Context, src string) (string, error) {
		return "<svg>fast</svg>", nil
	}
	SetDiagramCompileTimeout(5 * time.Second)

	// See the note in TestAbandonedCompilesAreCappedNotUnbounded: another test's
	// real goja worker may still be draining, and this test reads the same global.
	waitAbandoned(t, 0, 2*time.Second)

	for i := 0; i < 50; i++ {
		if _, err := renderDiagramUncached("healthy"); err != nil {
			t.Fatalf("compile %d: %v", i, err)
		}
	}
	if got := abandonedCompiles.Load(); got != 0 {
		t.Fatalf("healthy compiles counted as abandoned: %d", got)
	}
}

// TestCompilePanicIsContainedNotFatal covers a regression aihub#250 introduced
// without noticing: it moved the compile onto its own goroutine, and a panic on a
// detached goroutine is caught by nobody — not net/http's per-request recover on
// the read path, and not FreezeDiagram's recover on the write path, which guards
// the goroutine that CALLS this function rather than the one that runs goja. A
// malformed source that made goja panic would have taken the process down.
//
// Reaching any assertion below is itself the proof: an uncontained panic fails
// this test by killing the test binary.
func TestCompilePanicIsContainedNotFatal(t *testing.T) {
	restoreDiagramTimeout(t)
	restoreDiagramCompileSeam(t)
	flushDiagramCache()

	compileDiagramFn = func(ctx context.Context, src string) (string, error) {
		panic("goja blew up during layout")
	}

	// See the note in TestAbandonedCompilesAreCappedNotUnbounded: the assertion
	// below reads the shared in-flight counter, so another test's real goja worker
	// must have drained first or it is attributed to this panic.
	waitAbandoned(t, 0, 2*time.Second)

	svg, err := renderDiagramUncached("panics")
	if err == nil {
		t.Fatalf("a panicking compile must surface as an error, got nil (svg %d bytes)", len(svg))
	}
	if !errors.Is(err, errDiagramPanic) {
		t.Fatalf("want errDiagramPanic, got %v", err)
	}
	if svg != "" {
		t.Errorf("a panicking compile must not return partial output, got %d bytes", len(svg))
	}
	if got := abandonedCompiles.Load(); got != 0 {
		t.Errorf("a panicking worker exited, so it must not be counted in flight: %d", got)
	}
}

// TestOverloadRefusalIsNotCached: a refusal is a fact about the process, not
// about the src being asked for — the src never reached the compiler. Caching it
// would let one wedging figure demote every OTHER figure requested while the pile
// is saturated, which is the aihub#250 "one slow moment pins a valid figure"
// mistake reintroduced through a different door.
func TestOverloadRefusalIsNotCached(t *testing.T) {
	restoreDiagramTimeout(t)
	restoreDiagramCompileSeam(t)
	flushDiagramCache()

	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		releaseOnce()
		waitAbandoned(t, 0, 2*time.Second)
	})

	compileDiagramFn = func(ctx context.Context, src string) (string, error) {
		<-release
		return "<svg>late</svg>", nil
	}
	maxAbandonedCompiles.Store(1)
	SetDiagramCompileTimeout(1 * time.Millisecond)

	// See the note in TestAbandonedCompilesAreCappedNotUnbounded: with a cap of 1,
	// one leftover worker from another test is enough to refuse the setup call.
	waitAbandoned(t, 0, 2*time.Second)

	// Saturate the cap with an abandoned worker belonging to a DIFFERENT figure.
	if _, err := renderDiagramUncached("the wedging figure"); !errors.Is(err, errDiagramTimeout) {
		t.Fatalf("setup: want errDiagramTimeout, got %v", err)
	}

	const victim = "innocent -> figure"
	if _, err := RenderDiagram(victim); !errors.Is(err, errDiagramOverloaded) {
		t.Fatalf("want errDiagramOverloaded while saturated, got %v", err)
	}

	releaseOnce()
	waitAbandoned(t, 0, 2*time.Second)

	compileDiagramFn = func(ctx context.Context, src string) (string, error) {
		return "<svg>innocent</svg>", nil
	}
	SetDiagramCompileTimeout(DefaultDiagramCompileTimeout)

	svg, err := RenderDiagram(victim)
	if err != nil {
		t.Fatalf("the refusal was cached: this src must be compiled once the pile drains, got %v", err)
	}
	if !strings.Contains(svg, "<svg") {
		t.Errorf("want a compiled figure, got %q", svg)
	}
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
