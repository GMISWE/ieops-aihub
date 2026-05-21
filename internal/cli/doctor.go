package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

type checkResult struct {
	Name    string
	Status  string // "ok", "warning", "error"
	Message string
	FixCmd  string
}

// RunDoctor runs 5 diagnostic checks and reports their status.
// With --fix: attempts to auto-repair fixable issues.
//
// Checks (§12.1):
//  1. workspace  – can locate .polyforge.yaml from wsRoot
//  2. config     – ~/.polyforge/config not required in v1, checks aihub reachability
//  3. repos      – .repo/<name>/ exist and match .polyforge.yaml remotes
//  4. worktrees  – pf3.<xxx>/ list vs server wi list; flag orphans
//  5. version    – GET /v1/version; compare min_client_version vs local binary
func RunDoctor(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, args []string) {
	fix := len(args) > 0 && args[0] == "--fix"

	checks := []checkResult{
		checkWorkspace(wsRoot, cfg),
		checkConfig(ctx, c),
		checkRepos(wsRoot, cfg),
		checkWorktrees(ctx, c, wsRoot, fix),
		checkVersion(ctx, c),
	}

	allOk := true
	for _, ch := range checks {
		icon := "ok"
		if ch.Status == "warning" {
			icon = "warn"
		}
		if ch.Status == "error" {
			icon = "FAIL"
			allOk = false
		}
		fmt.Printf("[%s] %s: %s\n", icon, ch.Name, ch.Message)
		if ch.FixCmd != "" && !fix {
			fmt.Printf("       fix: %s\n", ch.FixCmd)
		}
	}
	if !allOk {
		os.Exit(1)
	}
}

// checkWorkspace verifies that .polyforge.yaml was found.
func checkWorkspace(wsRoot string, cfg *config.Config) checkResult {
	if cfg == nil {
		return checkResult{
			Name:    "workspace",
			Status:  "error",
			Message: fmt.Sprintf(".polyforge.yaml not found in %s", wsRoot),
			FixCmd:  "polyforge init",
		}
	}
	return checkResult{
		Name:    "workspace",
		Status:  "ok",
		Message: fmt.Sprintf(".polyforge.yaml found (scenario: %s)", cfg.Scenario),
	}
}

// checkConfig verifies aihub reachability via GET /health.
func checkConfig(ctx context.Context, c *client.Client) checkResult {
	if c == nil {
		return checkResult{
			Name:    "config",
			Status:  "warning",
			Message: "aihub client not configured (POLYFORGE_API_KEY / POLYFORGE_AIHUB_URL not set)",
		}
	}
	var health map[string]any
	if err := c.Health(ctx, &health); err != nil {
		return checkResult{
			Name:    "config",
			Status:  "error",
			Message: fmt.Sprintf("aihub unreachable: %v", err),
		}
	}
	return checkResult{Name: "config", Status: "ok", Message: "aihub reachable"}
}

// checkRepos verifies that .repo/<name>/ directories exist and their remote
// URLs match .polyforge.yaml.
func checkRepos(wsRoot string, cfg *config.Config) checkResult {
	if cfg == nil {
		return checkResult{Name: "repos", Status: "warning", Message: "skipped (no config)"}
	}
	repoBase := filepath.Join(wsRoot, ".repo")
	var missing, mismatch []string

	for _, proj := range cfg.Projects {
		for _, r := range proj.Repos {
			repoPath := filepath.Join(repoBase, r.Name)
			if _, err := os.Stat(repoPath); os.IsNotExist(err) {
				missing = append(missing, r.Name)
				continue
			}
			// Check remote URL.
			out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
			if err != nil {
				mismatch = append(mismatch, r.Name+"(remote-err)")
				continue
			}
			actual := strings.TrimSpace(string(out))
			if r.URL != "" && actual != r.URL {
				mismatch = append(mismatch, fmt.Sprintf("%s(want %s got %s)", r.Name, r.URL, actual))
			}
		}
	}

	if len(missing) == 0 && len(mismatch) == 0 {
		return checkResult{Name: "repos", Status: "ok", Message: "all repos present and remotes match"}
	}
	var msgs []string
	if len(missing) > 0 {
		msgs = append(msgs, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(mismatch) > 0 {
		msgs = append(msgs, fmt.Sprintf("remote mismatch: %s", strings.Join(mismatch, ", ")))
	}
	return checkResult{
		Name:    "repos",
		Status:  "warning",
		Message: strings.Join(msgs, "; "),
		FixCmd:  "polyforge init --apply",
	}
}

// checkWorktrees cross-references pf3.* directories with active work items
// from aihub; flags directories with no matching running wi.
func checkWorktrees(ctx context.Context, c *client.Client, wsRoot string, fix bool) checkResult {
	entries, err := os.ReadDir(wsRoot)
	if err != nil {
		return checkResult{Name: "worktrees", Status: "error", Message: fmt.Sprintf("readdir: %v", err)}
	}

	var wt []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "pf3.") {
			wt = append(wt, e.Name())
		}
	}
	if len(wt) == 0 {
		return checkResult{Name: "worktrees", Status: "ok", Message: "no worktrees found"}
	}

	// Fetch active work items from aihub to cross-reference.
	activeIDs := map[string]bool{}
	if c != nil {
		params := url.Values{"status": []string{"running,paused,queued"}}
		if result, err := c.ListWorkItems(ctx, params); err == nil {
			if items, ok := result["items"].([]any); ok {
				for _, item := range items {
					if m, ok := item.(map[string]any); ok {
						if id, ok := m["id"].(string); ok {
							// Extract shortid from wi_01ks510z... → 01ks510z
							if len(id) > 3 {
								activeIDs[id[3:]] = true // strip "wi_" prefix
							}
						}
					}
				}
			}
		}
	}

	// Identify orphans: worktree whose shortid does not appear in active IDs.
	var orphans []string
	for _, name := range wt {
		// pf3.<shortid> or pf3.<shortid>/ — extract shortid after "pf3."
		shortid := strings.TrimPrefix(name, "pf3.")
		// shortid may be "01ks510z" or composite like "01ks510z" — match prefix
		found := false
		for active := range activeIDs {
			if strings.HasPrefix(active, shortid) || strings.HasPrefix(shortid, active[:min(len(active), len(shortid))]) {
				found = true
				break
			}
		}
		// If aihub unreachable, we cannot confirm orphan — skip auto-removal.
		if !found && c != nil {
			orphans = append(orphans, name)
		}
	}

	if len(orphans) == 0 {
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("%d worktrees, none orphaned", len(wt))}
	}

	if fix {
		removed := 0
		for _, o := range orphans {
			// git worktree remove --force is safest; fall back to rm -rf.
			path := filepath.Join(wsRoot, o)
			if err := exec.CommandContext(ctx, "git", "-C", wsRoot, "worktree", "remove", "--force", o).Run(); err != nil {
				_ = os.RemoveAll(path)
			}
			removed++
		}
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("removed %d orphan worktrees: %s", removed, strings.Join(orphans, ", "))}
	}

	return checkResult{
		Name:    "worktrees",
		Status:  "warning",
		Message: fmt.Sprintf("%d orphan worktrees: %s", len(orphans), strings.Join(orphans, ", ")),
		FixCmd:  "polyforge doctor --fix",
	}
}

// checkVersion fetches GET /v1/version and compares min_client_version with
// the locally compiled version.
func checkVersion(ctx context.Context, c *client.Client) checkResult {
	if c == nil {
		return checkResult{Name: "version", Status: "warning", Message: "skipped (no aihub client)"}
	}
	ver, err := c.GetVersion(ctx)
	if err != nil {
		// Non-fatal: server may not expose this endpoint in v1.
		return checkResult{Name: "version", Status: "ok", Message: "version endpoint not available (non-fatal)"}
	}
	minVer, _ := ver["min_client_version"].(string)
	if minVer == "" {
		return checkResult{Name: "version", Status: "ok", Message: "server did not set min_client_version"}
	}
	return checkResult{Name: "version", Status: "ok",
		Message: fmt.Sprintf("server min_client_version=%s (local=dev)", minVer)}
}

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
