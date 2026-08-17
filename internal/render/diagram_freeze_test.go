package render

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Each test here maps to a named root cause of the old runtime path
// (aihub-d2-rendering-research.md §3) or to a backlog item. The point of
// 02-test-verification-plan.md §0 is that the new architecture makes those failures
// structurally unlikely, which is not the same as verified — so they are exercised
// rather than assumed.

func withFreezeRenderFn(fn func(string) (string, error)) func() {
	prev := freezeRenderFn
	freezeRenderFn = fn
	return func() { freezeRenderFn = prev }
}

// Real end-to-end freeze through d2. Also pins the aihub#234 lesson: Scale=1 makes d2
// emit width/height alongside viewBox, without which CSS resolves the figure to 100% of
// its container and *upscales* it (mem_0v7S0TTo).
func TestFreezeDiagram_RealD2(t *testing.T) {
	got, err := FreezeDiagram(context.Background(), "a -> b: hello", FreezeOptions{})
	if err != nil {
		t.Fatalf("freeze failed: %v", err)
	}
	if !strings.Contains(got.SVG, "<svg") {
		t.Fatalf("no svg in output: %.200s", got.SVG)
	}
	if !strings.Contains(got.SVG, "width=") || !strings.Contains(got.SVG, "height=") {
		t.Error("frozen svg has no intrinsic size — it will be upscaled to its container")
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
}

// TestFreezeDiagram_OutputIsNotSanitized pins the inverse of what this test used to assert.
//
// It previously required the frozen SVG to come back sanitized, on the reasoning that the
// write path should not persist something the read path must keep re-cleaning. That was
// wrong in a way only a dense figure exposes: SanitizeArtifactHTML drops <style> and its
// body, and d2 ships all of its theming there — every .fill-*/.stroke-* class plus its
// base64 webfont. Sanitizing here returned an SVG whose shapes had lost every fill and
// stroke while still containing <svg>, <rect> and all the attribute-level markers any
// structural assertion would look for. The old test passed against exactly that output.
//
// FreezeDiagram's input is a d2 DSL fed to an in-process layout engine, not agent markup,
// so there is nothing here to sanitize. The guarantee moved rather than disappeared: the
// SVG is inserted only after the surrounding document has been sanitized, and it is served
// inside the sandboxed iframe whose CSP allows inline style and data: fonts but no script.
func TestFreezeDiagram_OutputIsNotSanitized(t *testing.T) {
	const stylesheet = `<style><![CDATA[.fill-N1{fill:#1c1c20}` +
		`@font-face{font-family:d2;src:url("data:application/font-woff;base64,AAAA")}]]></style>`
	defer withFreezeRenderFn(func(string) (string, error) {
		return `<svg viewBox="0 0 10 10">` + stylesheet +
			`<rect class="fill-N1" width="10" height="10"/></svg>`, nil
	})()

	got, err := FreezeDiagram(context.Background(), "a -> b", FreezeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The theming must arrive intact. These are the exact substrings the sanitizer removes,
	// so a regression that reintroduces the call fails here rather than in a browser.
	for _, need := range []string{
		"<style", "<![CDATA[", ".fill-N1{fill:#1c1c20}",
		"@font-face", "data:application/font-woff;base64,", `class="fill-N1"`,
	} {
		if !strings.Contains(got.SVG, need) {
			t.Errorf("frozen svg lost %q — d2's theming does not survive sanitization, so a "+
				"sanitized figure renders with no fills or strokes\ngot: %s", need, got.SVG)
		}
	}

	// And it must be verbatim: no rewriting at all, not merely no stripping.
	if want := `<svg viewBox="0 0 10 10">` + stylesheet +
		`<rect class="fill-N1" width="10" height="10"/></svg>`; got.SVG != want {
		t.Errorf("frozen svg was rewritten\n got: %s\nwant: %s", got.SVG, want)
	}
}

// TestFreezeDiagram_SanitizingItsOutputWouldBreakIt is the evidence for the decision above,
// asserted rather than asserted-in-a-comment: it shows what the removed call actually did to
// a d2 figure. If SanitizeArtifactHTML is ever changed to preserve <style>, this test fails
// and the tradeoff documented on FreezeDiagram needs revisiting.
func TestFreezeDiagram_SanitizingItsOutputWouldBreakIt(t *testing.T) {
	const svg = `<svg viewBox="0 0 10 10"><style><![CDATA[.fill-N1{fill:#1c1c20}]]></style>` +
		`<rect class="fill-N1" width="10" height="10"/></svg>`

	sanitized := SanitizeArtifactHTML(svg)

	if strings.Contains(sanitized, "fill:#1c1c20") {
		t.Fatal("SanitizeArtifactHTML now preserves <style> bodies — FreezeDiagram's " +
			"no-sanitize decision was justified by the opposite, so re-derive it")
	}
	// The failure mode is specifically silent: structure survives, appearance does not.
	for _, stillThere := range []string{"<svg", "<rect", `class="fill-N1"`} {
		if !strings.Contains(sanitized, stillThere) {
			t.Errorf("expected %q to survive — the point is that structural checks stay "+
				"green while the figure loses its paint", stillThere)
		}
	}
}

// backlog: "D2 has no timeout and no size ceiling; 512 entries flushes the whole table".
// Layout is the expensive step, so oversized input is rejected before it runs.
func TestFreezeDiagram_RejectsOversizedSourceBeforeLayout(t *testing.T) {
	called := false
	defer withFreezeRenderFn(func(string) (string, error) {
		called = true
		return "<svg></svg>", nil
	})()

	cases := []struct {
		name string
		src  string
		opt  FreezeOptions
	}{
		{"bytes", strings.Repeat("a -> b\n", 500), FreezeOptions{MaxSourceBytes: 100}},
		{"lines", strings.Repeat("a -> b\n", 500), FreezeOptions{MaxSourceLines: 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FreezeDiagram(context.Background(), tc.src, tc.opt)
			if err == nil {
				t.Fatal("oversized source was accepted")
			}
			if !errors.Is(err, ErrDiagramTooLarge) {
				t.Errorf("error is not ErrDiagramTooLarge: %v", err)
			}
			var fe *FreezeError
			if !errors.As(err, &fe) || fe.Stage != "validate" {
				t.Errorf("want validate-stage FreezeError, got %v", err)
			}
		})
	}
	if called {
		t.Error("layout ran despite the source exceeding its cap — the cap saved nothing")
	}
}

// R1: a transient failure must never be persisted as if it were a result. The contract
// is that a failed freeze yields an error and NO svg, so a caller cannot accidentally
// store a half-made document.
func TestFreezeDiagram_FailureCarriesNoPartialOutput(t *testing.T) {
	defer withFreezeRenderFn(func(string) (string, error) {
		return "<svg>partial", errors.New("boom")
	})()

	got, err := FreezeDiagram(context.Background(), "a -> b", FreezeOptions{MaxAttempts: 1})
	if err == nil {
		t.Fatal("expected failure")
	}
	if got.SVG != "" {
		t.Errorf("failed freeze returned svg %q — a caller could persist a half-product", got.SVG)
	}
}

// R4: transient failures get one more try.
func TestFreezeDiagram_RetriesTransientOnce(t *testing.T) {
	var calls int32
	var mu sync.Mutex
	defer withFreezeRenderFn(func(string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			panic("simulated goja panic")
		}
		return `<svg viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`, nil
	})()

	got, err := FreezeDiagram(context.Background(), "a -> b", FreezeOptions{MaxAttempts: 2})
	if err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
}

// A panic inside layout must not escape. On the write path an unrecovered panic is a
// dead request at best and a dead process at worst — the backlog records exactly this
// ("panic -> 500, no recover") against the old path.
func TestFreezeDiagram_PanicIsContained(t *testing.T) {
	defer withFreezeRenderFn(func(string) (string, error) {
		panic("always")
	})()

	_, err := FreezeDiagram(context.Background(), "a -> b", FreezeOptions{MaxAttempts: 2})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("panic was not reported as such: %v", err)
	}
}

// A deterministic failure must NOT be retried: it cannot succeed, and retrying spends
// the timeout budget twice before the author sees the syntax error.
func TestFreezeDiagram_DoesNotRetryDeterministicFailures(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	defer withFreezeRenderFn(func(string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return "", errors.New("syntax error near line 1")
	})()

	_, err := FreezeDiagram(context.Background(), "!!!", FreezeOptions{MaxAttempts: 4})
	if err == nil {
		t.Fatal("expected failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("deterministic failure was retried %d times", calls-1)
	}
	var fe *FreezeError
	if errors.As(err, &fe) && fe.Transient {
		t.Error("a syntax error was classified transient")
	}
}

func TestFreezeDiagram_TimeoutIsEnforced(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	defer withFreezeRenderFn(func(string) (string, error) {
		<-release // never returns within the timeout
		return "<svg></svg>", nil
	})()

	start := time.Now()
	_, err := FreezeDiagram(context.Background(), "a -> b",
		FreezeOptions{Timeout: 120 * time.Millisecond, MaxAttempts: 1})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout")
	}
	var fe *FreezeError
	if !errors.As(err, &fe) || fe.Stage != "timeout" {
		t.Errorf("want timeout-stage FreezeError, got %v", err)
	}
	if !fe.Transient {
		t.Error("a timeout should be classified transient")
	}
	if elapsed > 2*time.Second {
		t.Errorf("freeze ran %v past its 120ms budget", elapsed)
	}
}

// A caller-cancelled context must abort promptly rather than run to the full timeout.
func TestFreezeDiagram_RespectsCallerCancellation(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	defer withFreezeRenderFn(func(string) (string, error) {
		<-release
		return "<svg></svg>", nil
	})()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := FreezeDiagram(ctx, "a -> b", FreezeOptions{Timeout: 30 * time.Second, MaxAttempts: 2})
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %v", elapsed)
	}
}

// R3: dagre runs inside goja, whose concurrency safety is not guaranteed. Freezing
// serializes layout; this runs under -race to pin that the surrounding machinery is
// clean too.
func TestFreezeDiagram_ConcurrentIsSerializedAndRaceFree(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	defer withFreezeRenderFn(func(string) (string, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()

		time.Sleep(5 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
		return `<svg viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`, nil
	})()

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := FreezeDiagram(context.Background(), "a -> b", FreezeOptions{}); err != nil {
				t.Errorf("concurrent freeze failed: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 1 {
		t.Errorf("layout ran %d-way concurrent; goja is not known to be safe under that (R3)", maxInFlight)
	}
}

func TestFreezeDiagram_RejectsEmptySource(t *testing.T) {
	for _, src := range []string{"", "   ", "\n\t\n"} {
		if _, err := FreezeDiagram(context.Background(), src, FreezeOptions{}); err == nil {
			t.Errorf("empty source %q was accepted", src)
		}
	}
}

// TestFreezeDiagram_ZeroValueOptionsStillRetry — the zero value must mean "the safe
// default", not "never retry".
//
// The previous field was Retries, defaulted only when < 0, so FreezeOptions{} produced
// zero retries and R4 was inactive on the path every caller uses. Every existing test
// passed because they all set the field explicitly.
func TestFreezeDiagram_ZeroValueOptionsStillRetry(t *testing.T) {
	if n := (FreezeOptions{}).withDefaults().MaxAttempts; n < 2 {
		t.Fatalf("zero-value MaxAttempts resolved to %d; R4 would be inactive by default", n)
	}

	calls := 0
	var mu sync.Mutex
	defer withFreezeRenderFn(func(string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			panic("transient")
		}
		return `<svg viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`, nil
	})()

	got, err := FreezeDiagram(context.Background(), "a -> b", FreezeOptions{})
	if err != nil {
		t.Fatalf("default options did not retry a transient failure: %v", err)
	}
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
}

// TestFreezeDiagram_GateHeldAcrossTimeout — serialization must survive a timeout.
//
// The gate used to be released by freezeOnce's defer, which fires when it returns. On
// timeout that is while the goja evaluation is still running, so the next attempt started
// layout concurrently with the abandoned one — defeating R3 in exactly the case it exists
// for, since a wedged layout is precisely when a second must not join it.
func TestFreezeDiagram_GateHeldAcrossTimeout(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	defer withFreezeRenderFn(func(string) (string, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return "<svg></svg>", nil
	})()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = FreezeDiagram(context.Background(), "a -> b",
				FreezeOptions{Timeout: 60 * time.Millisecond, MaxAttempts: 1})
		}()
	}
	wg.Wait() // all four have timed out and returned

	mu.Lock()
	got := maxInFlight
	mu.Unlock()
	close(release)

	if got > 1 {
		t.Errorf("%d layouts ran concurrently after timeouts; R3 serialization is defeated", got)
	}
}
