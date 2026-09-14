package engine

import (
	"fmt"
	"path/filepath"
	"sort"
)

// CleanupWorktrees removes each repo's worktree via `git worktree remove --force`, then removes
// the shared parent directory ONCE, UNCONDITIONALLY, strictly after every per-repo removal has
// been attempted - never inside the loop, and never skipped just because some repo's removal
// failed (lifecycle-details.md §0: "all of a wi's worktrees live under one parent ... remove it
// once, AFTER the loop", with no carve-out there for a per-repo failure).
//
// worktrees is pf_complete_attempt's returned `worktrees` map: repo name -> absolute worktree
// path. Repos are processed in sorted-name order so the returned removed slice (and the call
// order a test's fake GitRunner records) is deterministic rather than following Go's
// randomized map iteration.
//
// git and rmParent are injected: the production caller (internal/cli) supplies an
// os/exec-backed GitRunner and an os.RemoveAll wrapped in an os.Stat-based isDir guard (mirroring
// the pseudocode's `if os.path.isdir(parent): rm -rf <parent>`), so this package never shells out
// or touches a real filesystem itself, and tests inject a fake that records call order instead.
//
// Each per-repo removal runs `git(<workspaceRoot>/.repo/<name>, "worktree", "remove", "--force",
// wt)` - FROM THE MAIN CLONE, exactly as lifecycle-details.md §0 specifies (`git -C
// <workspace_root>/.repo/<repo_name> worktree remove --force <wt>`), never from inside wt itself.
// This is deliberate and load-bearing on a realistic retry path, not a style choice: running it
// with dir=wt instead fails at exec's own chdir, before git even runs, the moment wt has already
// been deleted by an interrupted prior cleanup attempt - exactly the case a retry exists to
// recover from. Running from the main clone's path instead succeeds and prunes the stale
// worktree administration data, because `git worktree remove` resolves its target against that
// admin data, not against a literal directory listing relative to the process's cwd (verified
// against git 2.43.0; both forms agree on the happy path, but only the main-clone form survives
// the wt-already-gone path).
//
// Every per-repo failure is collected into repoErrs (name -> error; always non-nil, possibly
// empty - every attempted repo that failed gets an entry, not just the first) rather than
// aborting at the first one, and the shared parent removal below is attempted UNCONDITIONALLY
// regardless of whether repoErrs is empty: a worktree git could not detach is a problem for that
// repo's own caller to see via repoErrs, but it must not also strand the whole pf.<slug>/ parent
// directory that every OTHER, successfully-detached repo's worktree still lived under.
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

	removed = make([]string, 0, len(names))
	for _, name := range names {
		wt := worktrees[name]
		mainClone := filepath.Join(workspaceRoot, ".repo", name)
		if _, gerr := git(mainClone, "worktree", "remove", "--force", wt); gerr != nil {
			repoErrs[name] = gerr
			continue
		}
		removed = append(removed, name)
	}

	// The shared parent is removed ONCE, after every per-repo removal has been ATTEMPTED -
	// never inside the loop above, and never skipped because repoErrs is non-empty. filepath.Dir
	// on any one worktree path yields it, since all of a wi's worktrees are siblings under the
	// same pf.<slug>/ directory.
	parent := filepath.Dir(worktrees[names[0]])
	if rmParent != nil {
		if perr := rmParent(parent); perr != nil {
			return removed, repoErrs, fmt.Errorf("engine: CleanupWorktrees: removing shared parent %q: %w", parent, perr)
		}
	}
	return removed, repoErrs, nil
}
