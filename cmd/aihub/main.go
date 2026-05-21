// Command aihub runs the polyforge v1 HTTP API server.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/GMISWE/ieops-aihub/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("aihub %s (%s) built %s\n", version.Version, version.GitCommit, version.BuildTime)
	// TODO(wi-2a/wi-2b): wire up echo router, pgx pool, and start server
	<-ctx.Done()
}
