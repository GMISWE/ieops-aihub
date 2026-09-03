package domain

// aihub#130: md→HTML rendering moved OFF the write path onto a bounded
// background pool, with the aihub#81/#146 lazy render kept as the backstop.
//
//	AIHUB_TEST_DB='postgres://postgres:…@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/domain/ -run 'TestDeferredRender' -v -count=1
//
// ─────────────────────────────────────────────────────────────────────────────
// 🔴 Read this before adding an assertion to this file.
//
// The lazy render is SELF-HEALING: any read of an artifact whose rendered_html
// is NULL produces the HTML on the fly. So an assertion of the form "after
// saving, rendered_html is non-empty" passes whether or not this work item's
// code exists at all, and passes just as happily if a future change deletes the
// background pool outright. It measures the union of two producers and can
// attribute the result to neither.
//
// These tests therefore separate the producers instead of counting the output:
//
//	TestDeferredRender_WritePathReturnsWithNothingRendered
//	    holds a render OPEN and looks at the row while no render can possibly
//	    have completed. RED before this work item: Remember blocks inside
//	    goldmark and never returns.
//
//	TestDeferredRender_BackgroundWorkerFillsRenderedHTML
//	    same gate, then releases it and waits — BOUNDED, with a deadline that
//	    fails — for the column to change from NULL to real goldmark output.
//	    The NULL-first half is what attributes the value to the worker; without
//	    it the assertion is satisfied by a synchronous save.
//
//	TestDeferredRender_DroppedRenderLeavesNullForLazyFallback
//	    disables the producer (error, then panic) and asserts the save still
//	    succeeds, the column stays NULL — the state the lazy fallback exists to
//	    serve, and the state a dropped job leaves behind — and that the drop is
//	    COUNTED, because "NULL" is also what a job that was never enqueued looks
//	    like. Its panic arm gates runRenderJob's recover(), whose real job is
//	    keeping one malformed document from killing the process.
//
// Two further tests cover the BOUND rather than the three observables. Neither
// needs a database, so both run in the plain `go test ./...` step:
//
//	TestDeferredRender_QueueFullShedsInsteadOfBlocking
//	    fills the queue with every worker parked and asserts the enqueue still
//	    returns promptly and the shed counter moved. A queue that blocks when
//	    full is the synchronous write path by the back door.
//
//	TestDeferredRender_DrainShedsLateArrivalsInsteadOfPanicking
//	    a job offered while DrainRenderQueue is waiting must be shed, not Added:
//	    Adding to a WaitGroup someone is waiting on panics, and echo's Shutdown
//	    can return with handlers still running, so that ordering is reachable.
//
// ⚠️ Those two count DIFFERENT counters (Shed vs DrainShed) and that is not
// tidiness. The drain test first asserted the single shared counter and passed
// with the drain-shed deleted, because the queue filled up behind two parked
// workers and shed for the other reason. A counter that merges two causes cannot
// gate either one.
//
// The other half of the third observable — that a NULL column still SERVES —
// lives in internal/server/routes_artifacts_share_lazy_test.go, because it is a
// property of the read handlers, not of this package.
//
// Latency is deliberately not asserted anywhere here. It is a proxy: it moves
// for unrelated reasons and it cannot tell a skipped render from a fast one.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/render"
)

// swapRenderMarkdown installs fn as the background renderer and returns a
// restore func. Taking the write lock matters: a worker goroutine may be reading
// the variable at the same moment.
func swapRenderMarkdown(fn func(string) (string, error)) func() {
	renderFnMu.Lock()
	prev := renderMarkdownFn
	renderMarkdownFn = fn
	renderFnMu.Unlock()
	return func() {
		renderFnMu.Lock()
		renderMarkdownFn = prev
		renderFnMu.Unlock()
	}
}

// gatedRenderer returns a renderer that blocks until release is closed, then
// delegates to the real goldmark renderer — so anything it eventually stores is
// genuine production output, not a fixture the assertion could be tuned to.
func gatedRenderer(release <-chan struct{}) func(string) (string, error) {
	return func(src string) (string, error) {
		<-release
		return render.Markdown(src)
	}
}

// readRenderedHTML returns the row's rendered_html, distinguishing SQL NULL
// (ok=false) from a stored empty string (ok=true, ""). The two are different
// facts here: NULL means "no producer has written yet", "" would mean one did
// and produced nothing.
func readRenderedHTML(t *testing.T, pool *pgxpool.Pool, memID string) (val string, ok bool) {
	t.Helper()
	var p *string
	err := pool.QueryRow(context.Background(),
		`SELECT rendered_html FROM memories WHERE id=$1`, memID).Scan(&p)
	require.NoError(t, err, "reading rendered_html for %s", memID)
	if p == nil {
		return "", false
	}
	return *p, true
}

// waitForRenderedHTML polls the row until rendered_html is non-NULL or the
// deadline passes, and reports whether it landed. It is bounded on purpose: an
// unbounded wait turns "the background producer never ran" into a hung test,
// and a hang gets attributed to the environment instead of to the code.
func waitForRenderedHTML(t *testing.T, pool *pgxpool.Pool, memID string, within time.Duration) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if v, ok := readRenderedHTML(t, pool, memID); ok {
			return v, true
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// rememberWithin calls Remember off the test goroutine and gives it a deadline.
//
// The deadline is not defensive padding — it is what turns the interesting
// failure into a readable one. Both tests below hold the renderer OPEN while
// they save, so on any build where the write path renders inline, Remember
// cannot return at all. A plain call would wedge until the package timeout and
// be reported as `panic: test timed out`, with the whole package's stacks
// attached and no statement of the cause; measured at 300s of dead suite when
// this was first tried. A hang gets read as an environment problem. This
// reports the diagnosis instead.
func rememberWithin(t *testing.T, pool *pgxpool.Pool, req *RememberRequest, within time.Duration) *Memory {
	t.Helper()
	type outcome struct {
		mem *Memory
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		m, _, err := Remember(context.Background(), pool, req)
		done <- outcome{m, err}
	}()
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.mem)
		return got.mem
	case <-time.After(within):
		t.Fatalf("Remember did not return within %s while the markdown renderer was held open — "+
			"the write path is rendering synchronously (aihub#130 regressed)", within)
		return nil // unreachable; t.Fatalf stops the goroutine
	}
}

// deferredRenderReq builds a render-type memory (methodology.spec is in
// defaultRenderTypes) with markdown that goldmark turns into a recognisable H1.
func deferredRenderReq(project, uid string, marker string) *RememberRequest {
	return &RememberRequest{
		Project:       project,
		Type:          "methodology.spec",
		Content:       "# " + marker + "\n\nbody paragraph for " + marker + ".\n",
		Visibility:    "project",
		DedupMode:     "off",
		CallerUserID:  uid,
		CallerDisplay: uid,
	}
}

// ─── Observable 1: the write path returns having rendered nothing ────────────

// TestDeferredRender_WritePathReturnsWithNothingRendered is the load-bearing
// assertion of aihub#130, and the only one of the three that cannot be satisfied
// by the lazy fallback: it reads the row at a moment when NO render has been
// allowed to finish.
//
// Before this work item, Remember called goldmark inline. With the renderer held
// open, Remember could not return at all — so the failure this test reports on
// the old code is the bounded-wait one below, naming the cause directly rather
// than hanging the suite.
func TestDeferredRender_WritePathReturnsWithNothingRendered(t *testing.T) {
	pool := setupLatestTestDB(t)
	InitRenderTypes(defaultRenderTypes)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	release := make(chan struct{})
	restore := swapRenderMarkdown(gatedRenderer(release))
	// LIFO: the gate opens first, then the real renderer goes back. Closing the
	// gate is what lets a blocked worker (or a blocked pre-aihub#130 Remember)
	// exit instead of leaking for the rest of the run.
	defer restore()
	defer close(release)

	mem := rememberWithin(t, pool, deferredRenderReq(proj, uid, "WritePath"), 20*time.Second)

	// (a) The object handed back to the caller carries no rendered HTML.
	require.Nil(t, mem.RenderedHTML,
		"Remember returned a memory with rendered_html already populated; the write path rendered")

	// (b) Neither does the committed row. This read is race-free rather than
	// lucky: the only producer is the worker, and it is parked inside the gated
	// renderer, so there is no moment at which it could have written.
	_, ok := readRenderedHTML(t, pool, mem.ID)
	require.False(t, ok,
		"memories.rendered_html was non-NULL while every render was still blocked — "+
			"something on the write path produced it")
}

// ─── Observable 2: the background worker eventually fills it ─────────────────

// TestDeferredRender_BackgroundWorkerFillsRenderedHTML attributes the stored
// value to the background producer by showing the column CHANGE: NULL while the
// gate is shut, real goldmark output after it opens.
//
// The NULL-first half is not decoration. Drop it and the remaining assertion —
// "rendered_html eventually contains <h1>" — is satisfied by a synchronous save,
// by a lazy render, and by this work item alike.
func TestDeferredRender_BackgroundWorkerFillsRenderedHTML(t *testing.T) {
	pool := setupLatestTestDB(t)
	InitRenderTypes(defaultRenderTypes)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	release := make(chan struct{})
	// The gate MUST be opened on every exit path, including the t.Fatal ones. A
	// worker parked in a renderer that is never released holds a slot in a pool
	// of two for the rest of the binary, so one failure here would turn every
	// later test in this file into a timeout — the classic "first failure is
	// real, the rest are noise" cascade. sync.OnceFunc so the happy path can
	// open it early and the defer stays a no-op.
	openGate := sync.OnceFunc(func() { close(release) })
	defer openGate()
	restore := swapRenderMarkdown(gatedRenderer(release))
	defer restore()

	before := renderQueueStats()
	mem := rememberWithin(t, pool, deferredRenderReq(proj, uid, "BackgroundFill"), 20*time.Second)

	if _, ok := readRenderedHTML(t, pool, mem.ID); ok {
		t.Fatal("rendered_html was already stored before the renderer was allowed to run; " +
			"the value cannot be attributed to the background worker")
	}

	openGate()

	stored, ok := waitForRenderedHTML(t, pool, mem.ID, 20*time.Second)
	require.True(t, ok,
		"rendered_html was still NULL 20s after the renderer was released — the background producer never stored it")

	// Assert the CONTENT, not merely non-emptiness: a producer that writes a
	// constant, or an empty string, must not pass.
	require.Contains(t, stored, "<h1", "stored HTML is not goldmark output: %q", excerpt(stored))
	require.Contains(t, stored, "BackgroundFill",
		"stored HTML does not derive from this memory's content: %q", excerpt(stored))

	// The caller-visible object is still the un-rendered one it was handed. The
	// worker writes to the database, not back into the response.
	require.Nil(t, mem.RenderedHTML)

	// A second, independent attribution of the same value: the producer counted
	// a store, and shed/failed nothing. The row read above says "the column is
	// populated"; this says "the background pool is what populated it".
	after := renderQueueStats()
	require.Equal(t, before.Stored+1, after.Stored, "the background pool did not record a store")
	require.Equal(t, before.Failed, after.Failed)
	require.Equal(t, before.Shed, after.Shed)
}

// ─── Observable 3a: a dropped render leaves the lazy-fallback state ──────────

// TestDeferredRender_DroppedRenderLeavesNullForLazyFallback covers what happens
// when the background producer does NOT deliver — the case that has to stay
// survivable for the whole design to be acceptable.
//
// Both arms assert the same two things: the save still succeeds (a render is
// never allowed to fail a write), and rendered_html stays NULL (the exact state
// the lazy fallback serves from).
//
// What the panic arm actually gates, stated exactly, because the obvious reading
// is wrong: an unrecovered panic in a goroutine terminates the PROCESS, it does
// not merely kill that goroutine. So deleting runRenderJob's recover() does not
// leave a quietly-degraded pool — it makes this test binary (and, in
// production, the server) die outright. That is what makes the arm a gate, and
// it is a very strong one; measured by removing the recover, which turns this
// run into `panic: deliberate render panic` and a non-zero exit.
//
// The follow-up "AfterPanic" save is therefore NOT the thing that catches a
// missing recover — with asyncRenderWorkers > 1 and one injected panic, another
// worker would serve it anyway even if a worker really could die alone. It is
// here for the weaker property it can actually observe: after a panic is
// swallowed, the pool still renders. Do not upgrade that comment into a claim
// about worker survival.
//
// This one cannot be RED before aihub#130 and it is not meant to be: leaving
// NULL on a failed render is pre-existing behaviour (the old resolveRenderedHTML
// logged and returned nil), and the lazy fallback it depends on shipped in
// aihub#81/#146. Its job is to keep the backstop from being removed later, when
// the async path has made it load-bearing rather than merely defensive.
func TestDeferredRender_DroppedRenderLeavesNullForLazyFallback(t *testing.T) {
	pool := setupLatestTestDB(t)
	InitRenderTypes(defaultRenderTypes)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	drain := func(t *testing.T) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		require.NoError(t, DrainRenderQueue(ctx), "background renders did not settle within 20s")
	}

	t.Run("render error", func(t *testing.T) {
		restore := swapRenderMarkdown(func(string) (string, error) {
			return "", errRenderProbe
		})
		defer restore()

		before := renderQueueStats()
		mem, _, err := Remember(context.Background(), pool, deferredRenderReq(proj, uid, "RenderError"))
		require.NoError(t, err, "a failing render must not fail the save")
		drain(t)

		_, ok := readRenderedHTML(t, pool, mem.ID)
		require.False(t, ok, "a failed render stored something; it must leave NULL for the lazy fallback")

		// The drop has to be COUNTED, not merely absent. "rendered_html is NULL"
		// is also what a job that was never enqueued looks like, and what a
		// deleted background pool looks like; the counter is what says a producer
		// ran and gave up, which is the case this arm is about.
		after := renderQueueStats()
		require.Equal(t, before.Failed+1, after.Failed,
			"the failed render was not recorded; a drop that leaves no trace is indistinguishable "+
				"from a job that was never enqueued")
		require.Equal(t, before.Stored, after.Stored, "a failed render must not count as stored")
	})

	t.Run("render panic does not take the worker pool down", func(t *testing.T) {
		// OnceFunc + defer, not a bare call after drain(): if drain fatals, a
		// plain `restore()` further down never runs and the PANICKING renderer
		// stays installed package-wide, so every later test in this file dies of
		// a cause that has nothing to do with what it asserts.
		restore := sync.OnceFunc(swapRenderMarkdown(func(string) (string, error) {
			panic("deliberate render panic")
		}))
		defer restore()

		mem, _, err := Remember(context.Background(), pool, deferredRenderReq(proj, uid, "RenderPanic"))
		require.NoError(t, err, "a panicking render must not fail the save")
		drain(t)
		restore() // real renderer back, so the probe save below can actually render

		_, ok := readRenderedHTML(t, pool, mem.ID)
		require.False(t, ok, "a panicking render stored something")

		// The pool still renders after swallowing a panic. See the note on this
		// function: this is NOT what catches a deleted recover() — that kills the
		// process — it is the weaker "the queue and workers still work" property.
		next, _, err := Remember(context.Background(), pool, deferredRenderReq(proj, uid, "AfterPanic"))
		require.NoError(t, err)
		stored, ok := waitForRenderedHTML(t, pool, next.ID, 20*time.Second)
		require.True(t, ok,
			"a save after a panicking render was never rendered — the panic killed a worker")
		require.Contains(t, stored, "AfterPanic")
	})
}

// ─── The bound itself: what happens at capacity ──────────────────────────────

// TestDeferredRender_QueueFullShedsInsteadOfBlocking checks the one property
// that makes "move it to a background pool" different from "spawn a goroutine
// per save": there is a ceiling, and reaching it must cost a dropped render
// rather than a blocked writer.
//
// A queue that blocks when full puts the render back on the write path by the
// back door — the caller waits for render capacity instead of for goldmark, and
// every latency claim in this work item quietly stops being true under exactly
// the load that motivated it. So the assertion is on BOTH halves: the enqueue
// loop finishes within a deadline, AND the shed counter moved.
//
// No database: queueRenderJob does not touch one, and the renderer installed
// here returns empty output, which runRenderJob discards before it would reach
// job.pool. That keeps this in the plain `go test ./...` step alongside the rest
// of the DB-free suite.
func TestDeferredRender_QueueFullShedsInsteadOfBlocking(t *testing.T) {
	release := make(chan struct{})
	openGate := sync.OnceFunc(func() { close(release) })
	defer openGate()
	restore := swapRenderMarkdown(func(string) (string, error) {
		<-release
		return "", nil // discarded before the (nil) pool is touched
	})
	defer restore()

	before := renderQueueStats()

	// Every worker parks in the gate, the queue fills, and the remainder must be
	// shed. The margin over depth+workers is what makes the shed unambiguous
	// rather than a boundary coincidence.
	const margin = 8
	total := asyncRenderQueueDepth + asyncRenderWorkers + margin

	enqueued := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			queueRenderJob(renderJob{
				memID:   fmt.Sprintf("mem_shed%03d", i),
				memType: "methodology.spec",
				// Empty content on purpose: these jobs exist to occupy queue slots,
				// and an empty render is the one outcome that neither logs nor
				// counts, so 266 of them leave the CI log readable.
			})
		}
		close(enqueued)
	}()

	select {
	case <-enqueued:
	case <-time.After(20 * time.Second):
		openGate()
		t.Fatalf("queueRenderJob blocked while %d workers were busy and the %d-deep queue was full — "+
			"a save then waits for render capacity, which is the synchronous write path by the back door",
			asyncRenderWorkers, asyncRenderQueueDepth)
	}

	shed := renderQueueStats().Shed - before.Shed
	require.GreaterOrEqual(t, shed, int64(margin),
		"enqueued %d jobs against a %d-deep queue with every worker blocked, but only %d were shed; "+
			"the backlog is not actually bounded", total, asyncRenderQueueDepth, shed)

	openGate()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, DrainRenderQueue(ctx), "the shed burst did not settle")
}

// TestDeferredRender_DrainShedsLateArrivalsInsteadOfPanicking gates the one
// thing standing between shutdown and a `sync: WaitGroup misuse: Add called
// concurrently with Wait` panic on the way out.
//
// It is not hypothetical. echo's Shutdown returns its context's error when the
// grace period expires WITH HANDLERS STILL RUNNING, so a Remember can reach
// queueRenderJob after main.go has already called DrainRenderQueue. Lifting the
// WaitGroup counter off zero with a waiter parked is exactly what panics.
//
// The mechanism under test is that a job offered during a drain is SHED rather
// than Added. Asserted by counter, not by "it did not panic": a build that
// simply never enqueued anything would also not panic.
func TestDeferredRender_DrainShedsLateArrivalsInsteadOfPanicking(t *testing.T) {
	release := make(chan struct{})
	openGate := sync.OnceFunc(func() { close(release) })
	defer openGate()
	restore := swapRenderMarkdown(func(string) (string, error) {
		<-release
		return "", nil
	})
	defer restore()

	// One job parked inside the gated renderer, so the drain below cannot finish
	// and is guaranteed to still be draining while this test offers more work.
	queueRenderJob(renderJob{memID: "mem_drainheld", memType: "methodology.spec"})

	drainErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		drainErr <- DrainRenderQueue(ctx)
	}()

	// Offer jobs until one is shed. Bounded, with a real break condition: the
	// drain sets its flag under a mutex before it starts waiting, so once the
	// goroutine above is inside DrainRenderQueue every offer sheds; the loop only
	// exists to cover the microseconds before it gets there.
	before := renderQueueStats().DrainShed
	deadline := time.Now().Add(20 * time.Second)
	shedSeen := false
	for !shedSeen {
		queueRenderJob(renderJob{memID: "mem_drainlate", memType: "methodology.spec"})
		if renderQueueStats().DrainShed > before {
			shedSeen = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	openGate()
	require.NoError(t, <-drainErr, "the drain did not settle once the held render was released")
	require.True(t, shedSeen,
		"no job offered during an in-progress drain was shed; a late enqueue then Adds to the "+
			"WaitGroup DrainRenderQueue is waiting on, which panics the process at shutdown")

	// And the queue is usable again afterwards — the drain flag must not be a
	// one-way latch, or the first drain in a test binary would break every test
	// after it.
	after := renderQueueStats()
	queueRenderJob(renderJob{memID: "mem_afterdrain", memType: "methodology.spec"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, DrainRenderQueue(ctx))
	require.Equal(t, after.DrainShed, renderQueueStats().DrainShed,
		"a job enqueued after the drain returned was shed; the drain flag latched on")
}

// errRenderProbe is the failure a test renderer returns. A package-level value
// so the assertion above is about the drop, not about the message.
var errRenderProbe = renderProbeError("probe: renderer disabled for this test")

type renderProbeError string

func (e renderProbeError) Error() string { return string(e) }

// excerpt trims a long HTML blob for a failure message.
func excerpt(s string) string {
	const max = 300
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
