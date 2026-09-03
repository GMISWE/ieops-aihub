// Command aihub runs the polyforge v1 HTTP API server.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
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

	// Resolved here, next to the other required variable and BEFORE the database
	// pool, so a misconfigured deployment fails in milliseconds without opening a
	// connection. It used to be resolved just before NewRouter, after the pool,
	// the embedding ping and two goroutines had already started.
	cookieSecret, err := loadUICookieSecret()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, uiCookieSecretGuidance)
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
	server.StartIdempotencyCachePurger(ctx)

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

// The /ui session signing key (aihub#344).
//
// POLYFORGE_UI_COOKIE_SECRET is the HMAC key for /ui/* session cookies. The
// property that matters is not that it is secret — it is that it is the SAME
// value in the NEXT process: a cookie minted by one process is verified by its
// successor (internal/server/ui_session.go), so a key that changes on restart
// signs out every /ui user at that moment.
//
// It used to be optional. Unset meant "32 random bytes per process, plus a warn
// line", and production ran that way from the day /ui shipped: every deploy
// signed everybody out, and the only trace was one line of stderr. Nothing in
// the deploy procedure could go red on it — docs/deployment.md step 7 checks
// /v1/version, /v1/health and the authed read path, and an ephemeral cookie key
// leaves all three green. A signal nobody reads is not a signal, so the warn is
// not the fix; requiring the value is.
//
// Three states, and no fourth:
//
//	POLYFORGE_UI_COOKIE_SECRET=<32+ bytes, hex or raw>   sessions survive restarts
//	POLYFORGE_UI_COOKIE_SECRET=ephemeral                 old behaviour, opted into
//	unset                                                refuse to start
//
// Why the opt-out is a sentinel VALUE of the same variable rather than a second
// variable: the state being fixed was invisible — nothing, anywhere, recorded
// that this deployment had thrown its sessions away. Spelling the opt-out inside
// the same variable means `grep POLYFORGE_UI_COOKIE_SECRET /root/aihub.env`
// answers "is this deployment persisting sessions?" with one line either way,
// and there is no second name to drift out of step with the first.
//
// Why refusing to start is the right failure: it trades a loud stop at first
// boot — one line to fix, found in seconds by whoever is doing the install — for
// a silent mass sign-out on every deploy of an already-running system, which was
// measured to survive undetected for months. TestUISessionSurvivesProcessRestart
// gates it.
const (
	uiCookieSecretEnv = "POLYFORGE_UI_COOKIE_SECRET"

	// uiCookieSecretEphemeral is the reserved value that opts a deployment INTO
	// per-process random keys. Not valid hex and far short of 32 bytes, so it
	// cannot be confused with a real key.
	uiCookieSecretEphemeral = "ephemeral"
)

// errUICookieSecretUnset is the startup failure when the variable is absent.
var errUICookieSecretUnset = errors.New("fatal: no /ui session signing key configured")

// uiCookieSecretGuidance is the remedy, printed after the error above.
//
// It names the file rather than only the variable: the variable was documented
// (README and docs/deployment.md both described the consequence accurately) and
// still nobody set it, so the missing half was never the explanation — it was
// the copy-pasteable command.
const uiCookieSecretGuidance = `POLYFORGE_UI_COOKIE_SECRET signs /ui/* session cookies. Without a stable value,
every restart mints a new key and signs out every /ui user, so this is fatal
rather than a warning (aihub#344).

Generate one and add it to the env-file this server is started with — on
production that is /root/aihub.env, mode 600 — then start the server again:

    printf 'POLYFORGE_UI_COOKIE_SECRET=%s\n' "$(openssl rand -hex 32)" >> /root/aihub.env

Treat it like DATABASE_URL from then on: replacing it signs everybody out just
as losing it does.

For local development, or anywhere sessions are genuinely disposable, set

    POLYFORGE_UI_COOKIE_SECRET=ephemeral

which restores the old per-process random key. That is a real choice with a real
consequence, so it has to be written down where the rest of the configuration
lives instead of being the invisible default.
`

// loadUICookieSecret resolves the /ui session signing key from the environment.
func loadUICookieSecret() ([]byte, error) {
	return resolveUICookieSecret(os.Getenv(uiCookieSecretEnv))
}

// resolveUICookieSecret is loadUICookieSecret with the environment read out, so
// the decision can be exercised without mutating process state.
//
// A configured value is accepted as hex (auto-decoded when the whole string
// decodes) or as raw bytes. No minimum length is enforced: dev and test use
// short values, and a length floor here would be a second way to fail startup
// for a deployment whose sessions do survive restarts, which is not this
// function's job.
//
// Surrounding whitespace is trimmed. `FOO=abc ` in a docker --env-file carries
// the trailing space through verbatim, and two edits of the same file that
// differ only in whitespace would otherwise be two different keys — the exact
// silent sign-out this is meant to end.
func resolveUICookieSecret(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	switch {
	case trimmed == "":
		return nil, errUICookieSecretUnset

	// Case-insensitively, because the alternative is worse than a loose match:
	// `=EPHEMERAL` would otherwise fall through and become a nine-byte literal
	// key — restart-stable, so nothing here would complain, and trivially
	// forgeable. Nobody's real 32-byte secret is the word "ephemeral" in any
	// casing.
	case strings.EqualFold(trimmed, uiCookieSecretEphemeral):
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			// No constant fallback. The previous version returned a hard-coded
			// string here, which is a signing key compiled into every copy of
			// the binary: anyone holding it could forge a session for any user.
			// Unreachable in practice is not the same as harmless to ship.
			return nil, fmt.Errorf("fatal: generating an ephemeral /ui session key: %w", err)
		}
		fmt.Fprintf(os.Stderr,
			"warn: %s=%s — /ui sessions are signed with a per-process random key; "+
				"every restart signs out every /ui user\n",
			uiCookieSecretEnv, uiCookieSecretEphemeral)
		return buf, nil

	default:
		secret := []byte(trimmed)
		if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) > 0 {
			secret = decoded
		}
		// A positive line, not just the absence of a warning: a log that has
		// rotated, or a grep against the wrong container, also produces "no
		// warning". This one can be asserted on. It reports the length so a
		// truncated env-file value is visible; the length is not the secret.
		fmt.Printf("aihub: /ui session key from %s (%d bytes) — sessions survive restarts\n",
			uiCookieSecretEnv, len(secret))
		return secret, nil
	}
}
