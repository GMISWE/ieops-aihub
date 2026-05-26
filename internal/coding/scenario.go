// Package coding provides helpers for the coding scenario (git, gh, wrap).
package coding

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// WorktreePath returns the absolute worktree path for a given work item and repo.
// It reads the state file and looks up the worktrees map.
func WorktreePath(wiID, repo, workspaceRoot string) (string, error) {
	sf, err := config.ReadStateFile(wiID)
	if err != nil {
		return "", fmt.Errorf("read state file for wi %s: %w", wiID, err)
	}

	if sf.Worktrees != nil {
		if path, ok := sf.Worktrees[repo]; ok {
			return path, nil
		}
	}

	// Fallback: derive from workspace root
	if workspaceRoot == "" {
		return "", fmt.Errorf("worktree path for repo %q not found in state file and workspace_root not provided", repo)
	}

	// Fallback for old state files that predate the worktrees field.
	// Uses <workspace_root>/pf.<shortid>/<repo>/ where shortid is the segment
	// after the last underscore in wi_id (e.g. "wi_KmBoOqUE" → "KmBoOqUE").
	// Note: shortid != <project>-<seq>; this path is only used for legacy compat.
	shortID := shortID(wiID)
	return filepath.Join(workspaceRoot, "pf."+shortID, repo), nil
}

// shortID extracts the 8-char base62 shortid from a full wi ID (e.g. "wi_01ks510z").
func shortID(wiID string) string {
	// wi_XXXXXXXX format: take everything after the last underscore
	for i := len(wiID) - 1; i >= 0; i-- {
		if wiID[i] == '_' {
			return wiID[i+1:]
		}
	}
	return wiID
}

// Wrap executes the wrap sequence for a work item:
// 1. Check if PR already exists (idempotent)
// 2. If not, push + create PR
// 3. Call pf_complete_attempt(wrapped)
// 4. Cleanup state file
func Wrap(ctx context.Context, sf *config.StateFile, repo, workspaceRoot, prTitle, prBody string) (map[string]any, error) {
	worktreePath, err := WorktreePath(sf.WIID, repo, workspaceRoot)
	if err != nil {
		return nil, err
	}

	// Check for existing PR (idempotent: W24)
	existingPR, _ := GHGetPR(ctx, worktreePath)
	if existingPR == nil {
		// Push then create PR
		if _, err := GitPush(ctx, worktreePath, false); err != nil {
			return nil, fmt.Errorf("push: %w", err)
		}
		if prTitle == "" {
			prTitle = fmt.Sprintf("feat: %s", sf.WIID)
		}
		existingPR, err = GHCreatePR(ctx, worktreePath, prTitle, prBody, "", "")
		if err != nil {
			return nil, fmt.Errorf("create PR: %w", err)
		}
	}

	return existingPR, nil
}
