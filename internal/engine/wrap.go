package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CleanupWorktrees detaches only clean, shipped, inactive worktrees. Safety is
// checked for every repo before any removal, so one refusal cannot partially
// detach siblings. worktrees maps repo names to paths under one pf.* parent.
// Git commands are injected by callers; only inspection of local credential
// state and of the parent directory uses filesystem I/O here. Repos are
// checked and detached in sorted order. Existing trees use non-forced
// removal; a missing tree uses --force only to prune stale git admin metadata
// during an interrupted-cleanup retry. Neither an unsafe preflight nor a
// failed detach permits removing the shared parent. Paths outside the workspace
// root remain supported when their parent and repo basename are well-formed.
//
// The shared parent is removed ONLY when it is empty after every detach
// succeeded (aihub#708 Batch 4B review): the map names the worktrees this
// attempt owns, but the parent may still hold a repo worktree the map OMITS
// or a scratch file somebody left there. rmParent is caller-supplied and is
// typically a recursive delete, so invoking it on a non-empty parent would
// delete that omitted sibling or scratch with it. The engine therefore reads
// the parent and refuses to hand a non-empty directory to rmParent: a
// retained parent is a safe outcome, not an error, and the engine itself
// never removes anything recursively — not the parent, and never a sibling
// or scratch path outside the worktrees map.
func CleanupWorktrees(git GitRunner, rmParent func(path string) error, workspaceRoot string, worktrees map[string]string) (removed []string, repoErrs map[string]error, parentErr error) {
	repoErrs = map[string]error{}
	if len(worktrees) == 0 {
		return nil, repoErrs, nil
	}

	names := make([]string, 0, len(worktrees))
	for name := range worktrees {
		names = append(names, name)
	}
	sort.Strings(names)

	// Never recursively delete a parent inferred from mismatched or malformed
	// paths, even when git would have refused to detach those paths.
	parent := filepath.Dir(worktrees[names[0]])
	if !strings.HasPrefix(filepath.Base(parent), "pf.") {
		return nil, repoErrs, fmt.Errorf("engine: CleanupWorktrees: refusing shared parent %q: not a pf. worktree parent", parent)
	}
	for _, name := range names {
		if name == "" || filepath.Base(worktrees[name]) != name || filepath.Dir(worktrees[name]) != parent {
			return nil, repoErrs, fmt.Errorf("engine: CleanupWorktrees: invalid worktree path for %q: %q", name, worktrees[name])
		}
	}
	// A credential state file is evidence of an active or resumable attempt.
	// Fail closed if the state directory cannot be inspected; chain sidecars
	// alone are not credentials and do not block post-wrap cleanup.
	stateDir := filepath.Join(workspaceRoot, ".polyforge", "state")
	entries, stateErr := os.ReadDir(stateDir)
	if stateErr != nil && !os.IsNotExist(stateErr) {
		return nil, repoErrs, fmt.Errorf("engine: CleanupWorktrees: inspect active state: %w", stateErr)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".chain.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			return nil, repoErrs, fmt.Errorf("engine: CleanupWorktrees: read active state: %w", err)
		}
		var state struct {
			Worktrees map[string]string `json:"worktrees"`
			Claimed   bool              `json:"claimed"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, repoErrs, fmt.Errorf("engine: CleanupWorktrees: parse active state: %w", err)
		}
		if state.Claimed {
			for _, wt := range state.Worktrees {
				if filepath.Clean(wt) == filepath.Clean(worktrees[names[0]]) || filepath.Dir(wt) == parent {
					return nil, repoErrs, fmt.Errorf("engine: CleanupWorktrees: refusing active worktree under %q", parent)
				}
			}
		}
	}
	// A missing directory may be a retry after an interrupted detach. Existing
	// directories must be clean and have no local commits absent from origin.
	for _, name := range names {
		wt := worktrees[name]
		if _, err := os.Stat(wt); os.IsNotExist(err) {
			continue
		} else if err != nil {
			repoErrs[name] = err
			continue
		}
		status, err := git(wt, "status", "--porcelain", "--untracked-files=all")
		if err != nil {
			repoErrs[name] = fmt.Errorf("inspect status: %w", err)
			continue
		}
		if strings.TrimSpace(status) != "" {
			repoErrs[name] = fmt.Errorf("refusing dirty worktree %q", wt)
			continue
		}
		branch, err := git(wt, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil || strings.TrimSpace(branch) == "" {
			repoErrs[name] = fmt.Errorf("refusing detached or unknown branch %q: %v", wt, err)
			continue
		}
		branch = strings.TrimSpace(branch)
		if _, err := git(wt, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch); err != nil {
			repoErrs[name] = fmt.Errorf("refusing unshipped branch %q: origin ref unavailable: %w", branch, err)
			continue
		}
		ahead, err := git(wt, "rev-list", "--count", "refs/remotes/origin/"+branch+"..HEAD")
		if err != nil || strings.TrimSpace(ahead) != "0" {
			repoErrs[name] = fmt.Errorf("refusing unshipped branch %q (ahead=%q): %v", branch, ahead, err)
		}
	}
	if len(repoErrs) != 0 {
		return nil, repoErrs, nil
	}

	removed = make([]string, 0, len(names))
	for _, name := range names {
		wt := worktrees[name]
		mainClone := filepath.Join(workspaceRoot, ".repo", name)
		args := []string{"worktree", "remove", wt}
		if _, err := os.Stat(wt); os.IsNotExist(err) {
			args = []string{"worktree", "remove", "--force", wt}
		}
		if _, gerr := git(mainClone, args...); gerr != nil {
			repoErrs[name] = gerr
			continue
		}
		removed = append(removed, name)
	}
	if len(repoErrs) != 0 {
		return removed, repoErrs, fmt.Errorf("engine: CleanupWorktrees: shared parent %q retained after detach failure", parent)
	}
	if rmParent != nil {
		// Only an EMPTY parent reaches rmParent (see the doc comment). Reading
		// the directory is the check: any entry left — an omitted sibling
		// worktree, a scratch file — retains the parent and rmParent is never
		// invoked, so a recursive rmParent implementation cannot destroy it.
		// A parent that is already gone needs no removal at all.
		entries, readErr := os.ReadDir(parent)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return removed, repoErrs, nil
			}
			return removed, repoErrs, fmt.Errorf("engine: CleanupWorktrees: inspect shared parent %q: %w", parent, readErr)
		}
		if len(entries) != 0 {
			// Retained, deliberately and silently: the detaches succeeded, so
			// this is not a cleanup failure; the parent simply is not ours to
			// remove anymore. Nothing outside the map was touched.
			return removed, repoErrs, nil
		}
		if perr := rmParent(parent); perr != nil {
			return removed, repoErrs, fmt.Errorf("engine: CleanupWorktrees: removing shared parent %q: %w", parent, perr)
		}
	}
	return removed, repoErrs, nil
}
