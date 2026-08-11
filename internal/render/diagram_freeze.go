package render

// Write-time diagram freezing (aihub#240 T5).
//
// The architecture removes the DSL engine from the *read* path: a complex figure is laid
// out once, when the artifact is written, and stored as static SVG. Viewing then compiles
// nothing (aihub-render-refactor-proposal.md §1, and 01-static-html-render-engine-
// research.md §0.1 on why an LLM should emit a DSL and let a deterministic engine do
// layout rather than hand-writing coordinates).
//
// This is NOT the rejected "freeze D2" proposal. That one kept the same badly-rendered
// output and merely cached it. Here the engine is demoted from a request-path dependency
// to a generation-time tool, which is the same direction as removing it.
//
// The five root causes of the old runtime path (aihub-d2-rendering-research.md §3) are
// each answered explicitly rather than assumed gone, because a structural change is
// "should not recur", not "verified not to recur" (02-test-verification-plan.md §0):
//
//	R1 transient failure cached forever  -> nothing is cached; a failure returns an error
//	                                        and the caller must not persist a half-product
//	R2 a new Ruler per render            -> NOT fixed here; see the note at the foot of
//	                                        this file. Freezing runs once per write, so the
//	                                        per-request cost that made R2 bite does not apply
//	R3 dagre-in-goja is concurrency-frail-> compiles are serialized
//	R4 no retry                          -> one retry, transient failures only
//	backlog: no timeout, no size ceiling -> context deadline + explicit input caps
//
// This file deliberately does not touch diagram.go or RenderDiagramsForUI: retiring the
// runtime path is P2 and out of scope for the P0 spike.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FreezeOptions bounds one freeze. The zero value is usable; defaults are applied.
type FreezeOptions struct {
	// Timeout caps a single attempt. The old path had none, so one pathological graph
	// could occupy a request indefinitely.
	Timeout time.Duration
	// MaxSourceBytes and MaxSourceLines reject oversized input before layout runs,
	// which is the expensive part.
	MaxSourceBytes int
	MaxSourceLines int
	// MaxAttempts is the total number of tries, not the number of retries. Zero means
	// "use the default".
	//
	// This replaces a Retries field whose zero value silently meant "never retry": the
	// default was applied only when Retries < 0, so every caller using FreezeOptions{}
	// — which is every caller — got no retry at all, and R4 was inactive on the only
	// path anyone used. The tests missed it because they all set Retries explicitly.
	// Counting attempts rather than retries makes the zero value mean the safe thing.
	MaxAttempts int
}

const (
	defaultFreezeTimeout    = 15 * time.Second
	defaultMaxSourceBytes   = 64 * 1024
	defaultMaxSourceLines   = 2000
	defaultMaxAttempts      = 2 // one initial try plus one retry
	defaultMaxRenderedBytes = 8 * 1024 * 1024
)

func (o FreezeOptions) withDefaults() FreezeOptions {
	if o.Timeout <= 0 {
		o.Timeout = defaultFreezeTimeout
	}
	if o.MaxSourceBytes <= 0 {
		o.MaxSourceBytes = defaultMaxSourceBytes
	}
	if o.MaxSourceLines <= 0 {
		o.MaxSourceLines = defaultMaxSourceLines
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = defaultMaxAttempts
	}
	return o
}

// FrozenDiagram is the result of a successful freeze.
type FrozenDiagram struct {
	// SVG is d2's own output, verbatim. It is trusted and is NOT sanitized — see the
	// note on FreezeDiagram for why, and for what the caller must therefore not do
	// with it.
	SVG string
	// Attempts is how many tries it took (1 = first time).
	Attempts int
	// Elapsed is wall time across all attempts.
	Elapsed time.Duration
}

// FreezeError carries enough detail for the caller to decide between surfacing the
// failure to the author and retrying later. It never carries a partial diagram: the
// old path's defining bug was treating a failed render as a cacheable result, and the
// write path must not repeat that by storing something half-made.
type FreezeError struct {
	Stage     string // validate | compile | render | panic | timeout | canceled
	Transient bool
	Attempts  int
	Err       error
}

func (e *FreezeError) Error() string {
	return fmt.Sprintf("freeze diagram: %s failed after %d attempt(s) (transient=%v): %v",
		e.Stage, e.Attempts, e.Transient, e.Err)
}

func (e *FreezeError) Unwrap() error { return e.Err }

// ErrDiagramTooLarge is returned when the source exceeds the configured caps.
var ErrDiagramTooLarge = errors.New("diagram source exceeds configured limits")

// compileGate serializes layout. dagre runs inside goja, whose concurrency safety is not
// guaranteed and which the research doc identifies as a live source of first-render
// failures (R3). Freezing is a write-path operation, so trading throughput for
// determinism is the right way round — the read path no longer compiles at all.
var compileGate = make(chan struct{}, 1)

// freezeRenderFn is the seam the tests replace to exercise timeout, retry and failure
// handling without depending on d2 producing a particular error.
var freezeRenderFn = renderDiagramUncached

// FreezeDiagram lays out a d2 source once and returns static SVG.
//
// On success the SVG is d2's output verbatim. On failure it returns a *FreezeError and no
// SVG at all.
//
// The output is deliberately NOT run through SanitizeArtifactHTML. Two reasons, and the
// second is the load-bearing one:
//
//  1. It is our own output. d2 is an in-process layout engine fed a DSL, not agent-authored
//     markup, so laundering it through a policy built for untrusted input buys nothing.
//  2. That policy would destroy it. d2 ships its theming — every .fill-*/.stroke-* class
//     and its base64 webfont — in a <style> inside the <svg>, and SanitizeArtifactHTML
//     drops <style> and its body outright. Sanitizing here returned an SVG whose shapes
//     had lost every fill and stroke: still well-formed, still passing every element- and
//     attribute-level assertion, and visually wrong.
//
// The caller's obligation follows from this: a FrozenDiagram must be inserted into a
// document AFTER that document has been sanitized, never before. Insert it first and the
// document sanitizer will eat the stylesheet on the way past.
//
// There are no production callers of FreezeDiagram yet — the write-time freeze path is built
// and tested but not wired, so the obligation above applies to whoever adds the first one.
// Stated explicitly because an earlier version of this comment claimed "both production call
// sites satisfy this", which would have a maintainer believe this path is live. The two sites
// that comment meant belong to RenderDiagramsForUI (routes_artifacts.go and ui_embed.go's
// {{md}} helper); they do satisfy exactly this ordering, which is why it is the pattern to
// copy.
//
// The SVG is not a hole in the sanitizer's guarantee: it is inserted into a document that
// is itself served inside the sandboxed iframe (safeembed.go), whose inner CSP allows
// 'unsafe-inline' style and data: fonts precisely so this stylesheet works, while keeping
// script-src at 'none'.
func FreezeDiagram(ctx context.Context, src string, opt FreezeOptions) (FrozenDiagram, error) {
	opt = opt.withDefaults()
	started := time.Now()

	if err := validateSource(src, opt); err != nil {
		return FrozenDiagram{}, &FreezeError{Stage: "validate", Transient: false, Attempts: 0, Err: err}
	}

	var last error
	var lastStage string
	transient := false
	used := 0

	for attempt := 1; attempt <= opt.MaxAttempts; attempt++ {
		used = attempt
		svg, stage, isTransient, err := freezeOnce(ctx, src, opt)
		if err == nil {
			return FrozenDiagram{
				SVG:      svg,
				Attempts: attempt,
				Elapsed:  time.Since(started),
			}, nil
		}
		last, lastStage, transient = err, stage, isTransient

		// A deterministic failure is almost always a syntax error in the source. Retrying
		// it burns the timeout budget again and cannot succeed, so stop immediately —
		// the author needs to see it, not wait for it.
		if !isTransient {
			break
		}
		// Do not start another attempt if the caller's context is already finished.
		if ctx.Err() != nil {
			break
		}
	}

	return FrozenDiagram{}, &FreezeError{
		Stage:     lastStage,
		Transient: transient,
		Attempts:  used,
		Err:       last,
	}
}

func validateSource(src string, opt FreezeOptions) error {
	if strings.TrimSpace(src) == "" {
		return errors.New("empty diagram source")
	}
	if len(src) > opt.MaxSourceBytes {
		return fmt.Errorf("%w: %d bytes > %d", ErrDiagramTooLarge, len(src), opt.MaxSourceBytes)
	}
	if n := strings.Count(src, "\n") + 1; n > opt.MaxSourceLines {
		return fmt.Errorf("%w: %d lines > %d", ErrDiagramTooLarge, n, opt.MaxSourceLines)
	}
	return nil
}

// freezeOnce runs a single attempt under the timeout.
func freezeOnce(ctx context.Context, src string, opt FreezeOptions) (svg, stage string, transient bool, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	// Acquire the serialization gate. It is released by the WORKER, not by this
	// function.
	//
	// Releasing it here via defer looked equivalent and was not: on timeout this
	// function returns while the goja evaluation is still running, so the gate opened
	// and the next attempt started layout concurrently with the abandoned one. That
	// defeated R3 in exactly the situation it exists for — a slow or wedged layout is
	// precisely when a second one must not join it. Handing the release to the worker
	// keeps at most one evaluation in flight no matter how the caller unwinds.
	select {
	case compileGate <- struct{}{}:
	case <-attemptCtx.Done():
		// Same distinction the post-layout branch below makes: a caller that cancelled did
		// not hit our timeout budget, and reporting it as "timeout" sends someone hunting a
		// performance problem that never happened. Reaching this branch at all usually means
		// the gate is held by an earlier wedged layout — see the note there.
		if ctx.Err() != nil {
			return "", "canceled", true, ctx.Err()
		}
		return "", "timeout", true, attemptCtx.Err()
	}

	type result struct {
		svg string
		err error
		// panicked distinguishes a recovered panic from a compile error. Both arrive as
		// a non-nil err, but they mean opposite things for retry: a syntax error will
		// fail identically forever, whereas a goja panic is exactly the intermittent
		// class R4 says must be retried.
		panicked bool
	}
	done := make(chan result, 1) // buffered: the worker must never block if we time out

	// Resolve the render function in THIS goroutine, not inside the worker.
	//
	// On timeout the worker is deliberately left to finish (see below), so a worker can
	// still be running after FreezeDiagram has returned. If it read this package-level
	// var itself, that read would race any concurrent write to it. Production never
	// reassigns it, so this is not a shipping bug — but the tests substitute it, and a
	// seam whose only safe use is "never touch it while anything is in flight" is a bad
	// seam. Capturing it here makes the hazard structurally impossible instead.
	render := freezeRenderFn

	go func() {
		// Release the gate only when layout has genuinely finished, including on panic.
		defer func() { <-compileGate }()
		// goja can panic on malformed input. A panic on the write path would take down
		// the process; recovering it here turns it into a transient failure the retry
		// can absorb.
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("panic during layout: %v", r), panicked: true}
			}
		}()
		out, e := render(src)
		done <- result{svg: out, err: e}
	}()

	select {
	case <-attemptCtx.Done():
		// The worker is left to finish and its result discarded: a goja evaluation cannot be
		// killed mid-flight in Go, and it still holds compileGate, so at most one is ever in
		// flight.
		//
		// KNOWN LIMITATION (aihub#244), stated accurately because an earlier version of this
		// comment understated it as "leaking one bounded goroutine beats blocking the write
		// path indefinitely" — which describes a per-request cost. The real blast radius is
		// process-wide and permanent: if the layout never returns (the wedged-goja case this
		// gate exists for), compileGate is never released, and every subsequent FreezeDiagram
		// in this process fails after MaxAttempts × Timeout. One poisoned diagram source
		// disables freezing for everyone until restart.
		//
		// Not fixed here deliberately: the honest fix is either a generation counter so a
		// timed-out worker's slot can be reclaimed, or an interruptible layout, and the latter
		// is the real root cause (goja cannot be force-interrupted). Tracked in aihub#244.
		// This is an availability limit, not a security one — no untrusted output escapes.
		//
		// Distinguish the two ways we get here. A caller that cancelled did not hit our
		// budget, and reporting that as "timeout" would send someone hunting a
		// performance problem that never happened.
		if ctx.Err() != nil {
			return "", "canceled", true, ctx.Err()
		}
		return "", "timeout", true, attemptCtx.Err()
	case r := <-done:
		if r.err != nil {
			if r.panicked {
				return "", "panic", true, r.err
			}
			// d2 compile failures are overwhelmingly deterministic syntax errors.
			return "", "compile", false, r.err
		}
		if r.svg == "" {
			return "", "render", true, errors.New("layout produced no output")
		}
		if len(r.svg) > defaultMaxRenderedBytes {
			return "", "render", false, fmt.Errorf("%w: rendered %d bytes", ErrDiagramTooLarge, len(r.svg))
		}
		return r.svg, "", false, nil
	}
}

// On R2 (a fresh textmeasure.Ruler per render, both costly and a named source of
// first-render failures): the freeze path inherits whatever renderDiagramUncached does,
// which today builds its own. Hoisting it to a shared instance means editing diagram.go,
// and diagram.go is read-only for this wi — the runtime path is P2. Recorded here as a
// known, deliberate non-fix rather than silently counted as handled: freezing runs once
// per artifact write, so the per-render cost that made R2 matter on the request path
// does not apply at this call site.
