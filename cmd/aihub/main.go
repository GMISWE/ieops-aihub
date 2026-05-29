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

	// GC background scheduler: runs all sweeps every 60s.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				results := domain.RunAll(context.Background(), pool)
				for _, r := range results {
					if r.Skipped || r.Affected == 0 {
						continue
					}
					fmt.Fprintf(os.Stderr, "gc: %s affected=%d\n", r.SweepType, r.Affected)
				}
			}
		}
	}()

	cookieSecret := loadUICookieSecret()
	e := server.NewRouter(pool, cookieSecret)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

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
