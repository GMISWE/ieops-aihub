package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// artifactFetcher is the subset of *client.Client used by RunArtifactView.
// Defined as an interface so tests can inject a stub without spinning up an
// HTTP server.
type artifactFetcher interface {
	GetArtifactHTML(ctx context.Context, memoryID string) (string, error)
}

// openerFn is the system-call wrapper used to launch the user's default
// browser. Overridable from tests.
var openerFn = func(opener, path string) error {
	return exec.Command(opener, path).Start()
}

// lookPathFn wraps exec.LookPath so tests can pretend the opener is/isn't
// installed without modifying PATH.
var lookPathFn = exec.LookPath

// RunArtifactView fetches the cached HTML for a spec/plan artifact, writes it
// to $TMPDIR/polyforge/artifact-<id>.html, and tries to open it in the user's
// default browser via xdg-open (Linux) or open (macOS). If no opener is
// available, the path is printed and the function returns nil so the user can
// open it manually.
//
// Usage: polyforge artifact view <memory_id>
func RunArtifactView(ctx context.Context, c artifactFetcher, memID string) error {
	if memID == "" {
		return fmt.Errorf("artifact memory_id is required")
	}
	if c == nil {
		return fmt.Errorf("aihub client not configured (run polyforge init or set api_key/aihub_url)")
	}

	html, err := c.GetArtifactHTML(ctx, memID)
	if err != nil {
		return fmt.Errorf("fetch artifact %s: %w", memID, err)
	}

	dir := filepath.Join(os.TempDir(), "polyforge")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return fmt.Errorf("create temp dir: %w", mkErr)
	}
	path := filepath.Join(dir, "artifact-"+memID+".html")
	if wErr := os.WriteFile(path, []byte(html), 0o644); wErr != nil {
		return fmt.Errorf("write %s: %w", path, wErr)
	}

	opener := ""
	switch runtime.GOOS {
	case "darwin":
		opener = "open"
	case "linux":
		opener = "xdg-open"
	}
	if opener == "" {
		fmt.Printf("HTML written to %s — open it manually (unsupported OS %s)\n", path, runtime.GOOS)
		return nil
	}
	if _, perr := lookPathFn(opener); perr != nil {
		fmt.Printf("HTML written to %s — %s not found, open manually\n", path, opener)
		return nil
	}
	if oerr := openerFn(opener, path); oerr != nil {
		fmt.Printf("HTML written to %s — failed to launch %s: %v\n", path, opener, oerr)
		return nil
	}
	fmt.Printf("opened %s in browser\n", path)
	return nil
}

// RunArtifact dispatches `polyforge artifact <subcmd>`. Currently only `view`
// is wired; future subcommands (e.g. `list`, `export`) can extend the switch.
func RunArtifact(ctx context.Context, c *client.Client, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: polyforge artifact view <memory_id>")
		os.Exit(1)
	}
	switch args[0] {
	case "view":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: polyforge artifact view <memory_id>")
			os.Exit(1)
		}
		if err := RunArtifactView(ctx, c, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "artifact view: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown artifact subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: polyforge artifact view <memory_id>")
		os.Exit(1)
	}
}
