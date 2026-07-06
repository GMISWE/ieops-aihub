package mcp

import "testing"

// TestAddWorktrees covers the worktrees pass-through used by pf_wrap,
// pf_claim_work_item, and pf_complete_attempt (aihub#207 Change 2): the
// response of each of these must carry the state file's worktree map so the
// caller doesn't need to have read the state file itself first.
func TestAddWorktrees(t *testing.T) {
	t.Run("non-empty worktrees are added", func(t *testing.T) {
		result := map[string]any{"ok": true}
		addWorktrees(result, map[string]string{"aihub": "/ws/.repo/aihub"})
		got, ok := result["worktrees"].(map[string]string)
		if !ok {
			t.Fatalf("result[worktrees] = %#v, want map[string]string", result["worktrees"])
		}
		if got["aihub"] != "/ws/.repo/aihub" {
			t.Errorf("worktrees[aihub] = %q, want /ws/.repo/aihub", got["aihub"])
		}
	})

	t.Run("nil worktrees leave result untouched", func(t *testing.T) {
		result := map[string]any{"ok": true}
		addWorktrees(result, nil)
		if _, ok := result["worktrees"]; ok {
			t.Errorf("result[worktrees] should be absent for nil input, got %#v", result["worktrees"])
		}
	})

	t.Run("empty worktrees leave result untouched", func(t *testing.T) {
		result := map[string]any{"ok": true}
		addWorktrees(result, map[string]string{})
		if _, ok := result["worktrees"]; ok {
			t.Errorf("result[worktrees] should be absent for empty input, got %#v", result["worktrees"])
		}
	})
}
