// Package cli implements the polyforge binary CLI subcommands.
package cli

import (
	"fmt"

	"github.com/GMISWE/ieops-aihub/internal/version"
)

// RunVersion prints the binary version string.
func RunVersion() {
	fmt.Printf("polyforge %s (%s) built %s\n", version.Version, version.GitCommit, version.BuildTime)
}
