// Package coding provides helpers for the coding scenario (git, gh, wrap).
package coding

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// WorktreePath returns the absolute worktree path for a given work item and repo.
// It reads the state file and looks up the worktrees map (primary path).
//
// Fallback (state files written before worktree creation ran): reconstructs the
// path from state file fields Project + Slug using the canonical directory format
// pf.<project>-<seq>/<repo>/ (e.g. pf.aihub-80/aihub).
//
// Returns a clear error if the path cannot be determined; no silent fallback to
// an incorrect path.
func WorktreePath(wiID, repo, workspaceRoot string) (string, error) {
	sf, err := config.ReadStateFile(wiID)
	if err != nil {
		return "", fmt.Errorf("read state file for wi %s: %w", wiID, err)
	}

	// Primary: state file has explicit worktrees map (set by pf_claim_work_item).
	if sf.Worktrees != nil {
		if path, ok := sf.Worktrees[repo]; ok {
			return path, nil
		}
	}

	// Fallback: reconstruct from Project + Slug using the canonical format
	// pf.<project>-<seq>/<repo>/ (mirrors tools_lifecycle.go worktree creation).
	if workspaceRoot == "" {
		return "", fmt.Errorf("worktree path for repo %q not found in state file (wi %s) and workspace_root not provided", repo, wiID)
	}
	if sf.Project == "" || sf.Slug == "" {
		return "", fmt.Errorf("worktree path for repo %q not found in state file (wi %s): worktrees map is absent and state file has no project/slug fields to reconstruct the path", repo, wiID)
	}
	idx := strings.LastIndex(sf.Slug, "#")
	if idx < 0 || idx == len(sf.Slug)-1 {
		return "", fmt.Errorf("worktree path for repo %q not found in state file (wi %s): cannot parse seq from slug %q", repo, wiID, sf.Slug)
	}
	seq := sf.Slug[idx+1:]
	return filepath.Join(workspaceRoot, fmt.Sprintf("pf.%s-%s", sf.Project, seq), repo), nil
}

// Wrap executes the wrap sequence for a work item:
//  1. Check if PR already exists (idempotent)
//  2. If it's already MERGED, or still open, return it as-is (never push again —
//     pushing after merge would resurrect a deleted head branch)
//  3. Otherwise push + create PR
//  4. Call pf_complete_attempt(wrapped)
//  5. Cleanup state file
func Wrap(ctx context.Context, sf *config.StateFile, repo, workspaceRoot, prTitle, prBody string) (map[string]any, error) {
	worktreePath, err := WorktreePath(sf.WIID, repo, workspaceRoot)
	if err != nil {
		return nil, err
	}

	branch, err := GitCurrentBranch(ctx, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("current branch: %w", err)
	}

	// Check for existing PR (idempotent: W24). A real gh error is not
	// swallowed — the merged/open/absent cases below all need to trust that
	// existingPR == nil genuinely means "no PR".
	existingPR, err := GHGetPR(ctx, worktreePath, branch)
	if err != nil {
		return nil, fmt.Errorf("check existing PR: %w", err)
	}
	if existingPR != nil {
		if state, _ := existingPR["state"].(string); strings.EqualFold(state, "MERGED") {
			// Already merged: never push (the head branch may already be
			// deleted on the remote) and never create a duplicate PR.
			return existingPR, nil
		}
		// Open (or any other non-merged) PR already exists: idempotent no-op.
		return existingPR, nil
	}

	// No existing PR: push then create.
	if _, err := GitPush(ctx, worktreePath); err != nil {
		return nil, fmt.Errorf("push: %w", err)
	}
	if prTitle == "" {
		prTitle = fmt.Sprintf("feat: %s", sf.WIID)
	}
	newPR, err := GHCreatePR(ctx, worktreePath, prTitle, prBody, "", "")
	if err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}
	return newPR, nil
}
