// Command gen renders Claude Code's step-<role>.md agent files from the
// role/tier/capability data model in internal/roles (aihub#642). It is
// invoked two ways:
//
//   - via internal/roles/roles.go's `//go:generate go run ./gen` directive
//     (cwd = internal/roles when `go generate` runs it);
//   - directly, e.g. `go run ./internal/roles/gen -out plugins/polyforge/agents`
//     (cwd = wherever the caller happens to be).
//
// The default -out value is computed from this file's own compile-time path
// (runtime.Caller), not from the working directory, so it resolves to the
// same place either way.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/GMISWE/ieops-aihub/internal/roles"
)

// defaultOutDir resolves to <repo root>/plugins/polyforge/agents regardless of
// the process's current working directory at invocation time.
func defaultOutDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		// Extremely unlikely (runtime.Caller only fails on a corrupted build);
		// fall back to a cwd-relative guess and let -out override it.
		return "plugins/polyforge/agents"
	}
	// thisFile is .../internal/roles/gen/main.go; three levels up is the repo root.
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
	return filepath.Join(repoRoot, "plugins", "polyforge", "agents")
}

func main() {
	outDir := flag.String("out", defaultOutDir(), "directory to write step-<role>.md files into")
	flag.Parse()

	roleList, err := roles.LoadRoles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "roles gen: %v\n", err)
		os.Exit(1)
	}
	aliases, err := roles.LoadCCAliases()
	if err != nil {
		fmt.Fprintf(os.Stderr, "roles gen: %v\n", err)
		os.Exit(1)
	}
	rendered, err := roles.RenderCCAgentFiles(roleList, aliases)
	if err != nil {
		fmt.Fprintf(os.Stderr, "roles gen: %v\n", err)
		os.Exit(1)
	}

	names := make([]string, 0, len(rendered))
	for name := range rendered {
		names = append(names, name)
	}
	sort.Strings(names)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "roles gen: mkdir %s: %v\n", *outDir, err)
		os.Exit(1)
	}
	for _, name := range names {
		path := filepath.Join(*outDir, name)
		if err := os.WriteFile(path, []byte(rendered[name]), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "roles gen: write %s: %v\n", path, err)
			os.Exit(1)
		}
		_, _ = fmt.Fprintf(os.Stdout, "roles gen: wrote %s\n", path)
	}
}
