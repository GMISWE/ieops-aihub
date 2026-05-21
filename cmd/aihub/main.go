// Command aihub runs the polyforge v1 HTTP API server.
package main

import (
	"context"
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

	e := server.NewRouter(pool)

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
