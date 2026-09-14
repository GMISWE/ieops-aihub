package engine

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
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
//
// "Unconditionally" has exactly one condition, added by aihub#679: the parent's BASENAME must
// begin with "pf.". rmParent is a recursive delete and the parent is derived from caller-supplied
// paths, so the one thing that is checked is that the directory about to be removed is a
// polyforge worktree parent at all. A parent that fails it is returned as parentErr and nothing
// is deleted. See the guard's own comment below for why the check is on the basename rather than
// on a workspaceRoot prefix.
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
		// aihub#679: the sentence directly above is a PREMISE, and until now nothing
		// checked it. rmParent is an unconditional recursive delete (os.RemoveAll behind
		// an is-a-directory guard, in both production closures), and the only thing
		// standing between it and an arbitrary path was the caller's word that
		// `worktrees` describes real pf.<slug>/ worktrees.
		//
		// Verified rather than assumed, because the premise holds for the writers and
		// not for the inputs. Every in-tree writer of the state file's worktree map
		// builds `pf.<project>-<seq>/<repo>` from the workspace root
		// (internal/lifecycle/claim.go, the only one that synthesises a path;
		// internal/mcp/tools_lifecycle.go only copies an existing map), so no writer
		// CAN produce a different shape. But both paths into this function accept a map
		// nobody validated: `engine cleanup-worktrees --worktrees <JSON>` takes it
		// straight off argv, and the drain path reads it from a state file on disk that
		// any editor or a partial write can corrupt. A map whose single entry is
		// `{"aihub": "/root/aihub"}` — a plausible typo, a stale hand-edit, a truncated
		// file — makes filepath.Dir yield $HOME and deletes it.
		//
		// This is local self-harm, not a remote hole: reaching it takes write access to
		// the caller's own state file or its own command line. It is guarded anyway
		// because the cost of the guard is one string comparison and the cost of the
		// premise being wrong once is unbounded and silent — cleanup is best-effort, so
		// the deletion would be reported as a warning at most.
		//
		// The guard is on the BASENAME, not on a workspaceRoot prefix. Prefix-checking
		// would be the tighter rule and is the wrong one here: `engine
		// cleanup-worktrees` is documented to take absolute worktree paths that need not
		// sit under --workspace-root (internal/cli/engine.go passes them through
		// untouched, and internal/cli/drain_test.go pins /elsewhere/pf.aihub-667/aihub as
		// a legitimate input), so a prefix rule would refuse real cleanups. "The
		// directory I am about to delete recursively is named pf.something" is the
		// property every legitimate caller has and every corruption described above
		// lacks.
		//
		// Refusing is reported, not swallowed: parentErr is already the channel for a
		// parent that could not be removed, and both callers surface it. A cleanup that
		// declined to delete $HOME must be loud - the per-repo `git worktree remove`
		// calls above have already run, so silence here would leave the operator with a
		// half-finished cleanup and no reason given.
		if base := filepath.Base(parent); !strings.HasPrefix(base, "pf.") {
			return removed, repoErrs, fmt.Errorf(
				"engine: CleanupWorktrees: refusing to recursively remove shared parent %q: "+
					"its name %q does not start with \"pf.\", so it is not a polyforge worktree "+
					"parent. This is computed as filepath.Dir of worktree %q; that worktree path "+
					"came from a state file or from --worktrees, and one of them is wrong",
				parent, base, worktrees[names[0]])
		}
		if perr := rmParent(parent); perr != nil {
			return removed, repoErrs, fmt.Errorf("engine: CleanupWorktrees: removing shared parent %q: %w", parent, perr)
		}
	}
	return removed, repoErrs, nil
}
