package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// writeWorkspaceYAML drops a .polyforge.yaml into dir and returns dir.
func writeWorkspaceYAML(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".polyforge.yaml"), []byte(body), 0644); err != nil {
		t.Fatalf("write .polyforge.yaml: %v", err)
	}
	return dir
}

// staleCfg is a stand-in for the config the MCP process loaded at startup: the
// project has a single repo, from before a second one was added.
func staleCfg() *config.Config {
	return &config.Config{
		Version: 1,
		Projects: map[string]config.Project{
			"aihub": {Repos: []config.Repo{
				{Name: "aihub", URL: "git@github.com:GMISWE/ieops-aihub.git"},
			}},
		},
	}
}

// TestResolveWorkspaceConfig_PrefersOnDiskOverStartupSnapshot locks the aihub#228
// regression: claim built worktrees from the config snapshot taken when the MCP
// process started, so a repo added to the project mid-session never got a
// worktree — the claim silently came back short.
func TestResolveWorkspaceConfig_PrefersOnDiskOverStartupSnapshot(t *testing.T) {
	dir := writeWorkspaceYAML(t, t.TempDir(), `version: 1
projects:
    aihub:
        repos:
            - name: aihub
              url: git@github.com:GMISWE/ieops-aihub.git
            - name: ieops-core
              url: git@github.com:GMISWE/ieops-core.git
`)

	got := resolveWorkspaceConfig(dir, staleCfg())
	if got == nil {
		t.Fatal("resolveWorkspaceConfig returned nil, want the on-disk config")
	}
	repos := got.Projects["aihub"].Repos
	if len(repos) != 2 {
		t.Fatalf("got %d repos %v, want 2 — the freshly added repo must be picked up without an MCP restart",
			len(repos), repos)
	}
	if repos[1].Name != "ieops-core" {
		t.Errorf("repos[1].Name = %q, want %q", repos[1].Name, "ieops-core")
	}
}

// TestResolveWorkspaceConfig_FallsBackWhenNoFileOnDisk: with no readable
// .polyforge.yaml there is nothing fresher to use, so the startup snapshot must
// still be honoured rather than dropping to nil and skipping worktree creation.
func TestResolveWorkspaceConfig_FallsBackWhenNoFileOnDisk(t *testing.T) {
	startup := staleCfg()

	got := resolveWorkspaceConfig(t.TempDir(), startup)
	if got != startup {
		t.Fatalf("got %v, want the startup snapshot to be reused when the on-disk read fails", got)
	}
}

// TestResolveWorkspaceConfig_NilStartupCfgWithFileOnDisk covers the background
// session case: the server started without POLYFORGE_WORKSPACE_ROOT so s.cfg is
// nil, but the caller resolved a usable wsRoot before claiming.
func TestResolveWorkspaceConfig_NilStartupCfgWithFileOnDisk(t *testing.T) {
	dir := writeWorkspaceYAML(t, t.TempDir(), `version: 1
projects:
    aihub:
        repos:
            - name: aihub
              url: git@github.com:GMISWE/ieops-aihub.git
`)

	got := resolveWorkspaceConfig(dir, nil)
	if got == nil {
		t.Fatal("resolveWorkspaceConfig returned nil, want the on-disk config despite a nil startup snapshot")
	}
	if len(got.Projects["aihub"].Repos) != 1 {
		t.Errorf("got %d repos, want 1", len(got.Projects["aihub"].Repos))
	}
}

// TestResolveWorkspaceConfig_BothMissing: no file and no snapshot yields nil,
// which the claim handler treats as "skip worktree creation" rather than
// panicking on a nil map lookup.
func TestResolveWorkspaceConfig_BothMissing(t *testing.T) {
	if got := resolveWorkspaceConfig(t.TempDir(), nil); got != nil {
		t.Errorf("resolveWorkspaceConfig = %v, want nil", got)
	}
}
