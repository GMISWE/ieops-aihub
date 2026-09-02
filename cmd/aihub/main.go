// Command aihub runs the polyforge v1 HTTP API server.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/db"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/embedding"
	"github.com/GMISWE/ieops-aihub/internal/render"
	"github.com/GMISWE/ieops-aihub/internal/server"
	"github.com/GMISWE/ieops-aihub/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("aihub %s (%s) built %s\n", version.Version, version.GitCommit, version.BuildTime)

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL required")
		os.Exit(1)
	}

	pool, err := db.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db.New: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// aihub#102: initialise config-driven render types.
	// RENDER_MEMORY_TYPES is comma-separated, e.g. "methodology.spec,methodology.plan".
	// When unset, defaults to "methodology.spec,methodology.plan" (backward-compatible).
	domain.InitRenderTypes(os.Getenv("RENDER_MEMORY_TYPES"))

	// aihub#250: bound a single d2 compile. DIAGRAM_COMPILE_TIMEOUT is a
	// time.Duration string (e.g. "5s", "800ms"); unset or unparseable keeps the
	// 5s default.
	fmt.Printf("d2 compile timeout: %s\n", render.InitDiagramCompileTimeout(os.Getenv("DIAGRAM_COMPILE_TIMEOUT")))

	// aihub#192: initialise embedding provider from env.
	// EMBEDDING_ENABLED=true/1 activates; on error or unreachable backend we
	// degrade to NoopProvider so the server still starts.
	//
	// aihub#316: both fallbacks below are permanent — there is no retry, so a
	// backend that is merely late to start leaves this process with embedding
	// off until someone restarts it. That was already true; what is new is that
	// /v1/health now has an opinion about it, and "configured but we gave up"
	// must not be reported as the same thing as "never configured". Hence
	// NoteEmbeddingUnavailableAtBoot on exactly the paths that give up after
	// embedding WAS asked for.
	{
		p, embErr := embedding.FromEnv()
		switch {
		case embErr != nil:
			fmt.Fprintf(os.Stderr, "warn: embedding.FromEnv: %v — falling back to NoopProvider\n", embErr)
			p = &embedding.NoopProvider{}
			domain.NoteEmbeddingUnavailableAtBoot()
		case p != nil:
			// 10s, and it really is 10s: budgetProvider deliberately does not
			// re-bound Ping, because clamping it to the 5s per-call embed
			// budget would fail a cold backend that needs to load a model on
			// its first embed — and the penalty for failing here is the
			// permanent downgrade below.
			pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
			pingErr := p.Ping(pingCtx)
			pingCancel()
			if pingErr != nil {
				fmt.Fprintf(os.Stderr, "warn: embedding backend unreachable: %v — falling back to NoopProvider\n", pingErr)
				p = &embedding.NoopProvider{}
				domain.NoteEmbeddingUnavailableAtBoot()
			}
		}
		domain.InitEmbeddingProvider(p)
	}

	// GC background scheduler: ticks every 60s and runs the sweeps that are DUE.
	//
	// RunDue, not RunAll. This ticker used to call RunAll, which runs all eight
	// sweeps unconditionally, so the two sweeps documented as "(daily)" ran 1,440
	// times a day — and since both of them EMIT an agent_events row rather than
	// mutating one, every one of those runs left a duplicate. That is aihub#266:
	// ~105,000 rows/day for one project, 111,221 on a single work item.
	//
	// The per-sweep periods live in domain.gcSweepTable so that the six sweeps
	// this tick genuinely drives keep their cadence; the tick interval stays here
	// because it is this loop's property, and the table says "every tick" rather
	// than naming 60 seconds a second time.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				results := domain.RunDue(context.Background(), pool)
				for _, r := range results {
					// aihub#268: a sweep that failed reports Affected == 0, so the
					// Affected==0 filter below was silently discarding every sweep
					// error for the life of the service. Errors are logged first and
					// unconditionally; the filter only ever meant to mute *idle*
					// sweeps.
					if r.Error != "" {
						fmt.Fprintf(os.Stderr, "gc: %s error=%s\n", r.SweepType, r.Error)
					}
					if r.Skipped || r.Affected == 0 {
						continue
					}
					fmt.Fprintf(os.Stderr, "gc: %s affected=%d\n", r.SweepType, r.Affected)
				}
			}
		}
	}()

	// aihub#152: the idempotency cache's own sweep. PurgeExpiredIdempotencyCache
	// existed from the start and had no caller anywhere in cmd/ or internal/, so
	// the only eviction that ever ran was the lazy one on a cache hit after
	// expiry — which by construction never fires for the entries that accumulate.
	// It is a separate ticker rather than another arm of the GC loop above
	// because that loop is the DATABASE sweep scheduler (domain.RunDue against
	// pool) and this is process-local memory with no row behind it.
	server.StartIdempotencyCachePurger(ctx, 10*time.Minute)

	cookieSecret := loadUICookieSecret()
	e := server.NewRouter(pool, cookieSecret)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	applyServerTimeouts(e.Server)

	go func() {
		if err := e.Start(":" + port); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		}
	}()

	fmt.Printf("aihub listening on :%s\n", port)
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown error: %v\n", err)
	}
	fmt.Println("aihub stopped")
}

// Server timeouts (aihub#250). Before this the server ran with every http.Server
// timeout at its zero value, i.e. none: a request that wedged in a handler held
// its connection until the client gave up, and a slow or idle peer could hold one
// indefinitely for free.
//
// What these do and do not buy, stated exactly — WriteTimeout is the one that
// gets over-trusted: net/http enforces these on the CONNECTION, not on the
// handler goroutine. A passed WriteTimeout fails the write and frees the socket
// and the client; it does not unwind a goroutine stuck inside d2's goja runtime.
// Only the compile deadline in internal/render reclaims the request, and even
// that abandons rather than kills the wedged goroutine (aihub#244 is the root
// cause). These are the other half: they stop a wedge from also consuming
// connections indefinitely and from looking like a silent hang to the client.
const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 60 * time.Second
	serverIdleTimeout       = 120 * time.Second
)

// applyServerTimeouts installs the bounds above.
//
// Split out of main so it can be tested: the invariant that matters is not that
// the fields are set but that WriteTimeout stays comfortably clear of the d2
// compile budget. A document compiles each of its figures in turn and
// diagram_gate's narrowerLayout can compile a wide one twice, so a WriteTimeout
// at or below the budget would cut legitimate renders — the exact failure this
// change is supposed to prevent, arriving through the fix instead.
func applyServerTimeouts(s *http.Server) {
	s.ReadHeaderTimeout = serverReadHeaderTimeout
	s.ReadTimeout = serverReadTimeout
	s.WriteTimeout = serverWriteTimeout
	s.IdleTimeout = serverIdleTimeout
}

// loadUICookieSecret resolves the secret used to sign /ui/* session cookies.
//
// Source order:
//  1. POLYFORGE_UI_COOKIE_SECRET — preferred. Accepted as hex (auto-decoded
//     if the string is even-length and all hex chars) or raw bytes.
//  2. Random 32 bytes from crypto/rand — emits a stderr warning so operators
//     know sessions will be invalidated on every restart.
//
// We do not enforce a minimum length on env-supplied secrets so dev/test can
// use short values; for prod, supply 32+ bytes of high-entropy data.
func loadUICookieSecret() []byte {
	if raw := os.Getenv("POLYFORGE_UI_COOKIE_SECRET"); raw != "" {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) > 0 {
			return decoded
		}
		return []byte(raw)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintf(os.Stderr, "warn: failed to generate ephemeral UI cookie secret: %v\n", err)
		// Fall back to a process-lifetime fixed value rather than crash —
		// the UI is still usable, just brittle across restarts.
		return []byte("aihub-ephemeral-fallback-secret")
	}
	fmt.Fprintln(os.Stderr,
		"warn: POLYFORGE_UI_COOKIE_SECRET not set — using an ephemeral random secret. "+
			"Existing UI sessions will be invalidated on the next restart.")
	return buf
}
