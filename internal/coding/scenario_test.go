package coding

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// writeStateFile is a helper that creates a minimal state file in a temp dir.
func writeStateFile(t *testing.T, wsRoot string, sf *config.StateFile) {
	t.Helper()
	dir := filepath.Join(wsRoot, ".polyforge", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", wsRoot)
	if err := config.WriteStateFile(sf); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
}

// TestWorktreePathFromStateFile verifies that when the state file has a
// worktrees map, WorktreePath returns the stored path (primary code path).
func TestWorktreePathFromStateFile(t *testing.T) {
	tmp := t.TempDir()
	expected := filepath.Join(tmp, "pf.aihub-42", "aihub")
	sf := &config.StateFile{
		WIID:      "wi_TestAAA",
		AttemptID: "att_1",
		Worktrees: map[string]string{"aihub": expected},
	}
	writeStateFile(t, tmp, sf)

	got, err := WorktreePath("wi_TestAAA", "aihub", tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

// TestWorktreePathFallbackPrefix verifies that the fallback path uses the
// "pf." prefix (not the stale "pf3." prefix), matching the directory format
// used by pf_claim_work_item when creating worktrees.
func TestWorktreePathFallbackPrefix(t *testing.T) {
	tmp := t.TempDir()
	// State file with NO worktrees field — simulates old state file or
	// state written before the worktree creation logic ran.
	sf := &config.StateFile{
		WIID:      "wi_TestBBB",
		AttemptID: "att_2",
		Worktrees: nil,
	}
	writeStateFile(t, tmp, sf)

	got, err := WorktreePath("wi_TestBBB", "aihub", tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fallback derives shortid from wi_id by stripping "wi_" prefix.
	// shortID("wi_TestBBB") == "TestBBB"
	want := filepath.Join(tmp, "pf.TestBBB", "aihub")
	if got != want {
		t.Errorf("got %q, want %q (check for stale 'pf3.' prefix)", got, want)
	}
}

// TestWorktreePathFallbackNeedsWorkspaceRoot verifies that when the state
// file has no worktrees and no workspace_root is given, an error is returned.
func TestWorktreePathFallbackNeedsWorkspaceRoot(t *testing.T) {
	tmp := t.TempDir()
	sf := &config.StateFile{
		WIID:      "wi_TestCCC",
		AttemptID: "att_3",
		Worktrees: nil,
	}
	writeStateFile(t, tmp, sf)

	_, err := WorktreePath("wi_TestCCC", "aihub", "")
	if err == nil {
		t.Error("expected error when no worktrees and no workspace_root, got nil")
	}
}
