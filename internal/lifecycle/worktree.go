package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/coding"
)

// verifyClaimWorktree answers whether wtPath is a usable git worktree, as
// opposed to a directory that merely exists.
//
// The claim handler used to adopt any existing directory on os.Stat alone,
// record it in the state file, and then take that early return on every later
// claim. So a `git worktree add` that died part-way through its checkout — disk
// full, SIGKILL, the machine rebooting — left a half-populated directory that
// was adopted permanently and never repaired, while pf_diff / pf_commit /
// pf_ship downstream were handed a path that is not a worktree. (aihub#328)
//
// ⚠️ `rev-parse --git-dir` ALONE IS NOT ENOUGH, and this is measured rather than
// reasoned. git searches PARENT directories, and a polyforge workspace root is
// itself commonly a git repository (the live gmi-ws workspace is one), so a
// directory with no .git at all at <wsRoot>/pf.<project>-<seq>/<repo> answers
// exit 0. Measured on git 2.43.0 against exactly that layout: `rev-parse
// --git-dir` printed <wsRoot>/.git and exited 0, i.e. the naive check calls the
// broken directory healthy. `--show-toplevel` printed <wsRoot>, which is NOT
// wtPath — comparing it against wtPath is what separates the two, because a real
// worktree's toplevel is its own path (verified: the control printed wtPath).
//
// The other half-built shape, a .git FILE whose `gitdir:` pointer names a
// missing admin directory, fails both forms with exit 128 ("fatal: not a git
// repository: ...").
//
// ⚠️ WHAT THIS DOES NOT DETECT, and the first entry is the one the work item's
// own motivation names, so read it before trusting this function:
//
//  1. A STRUCTURALLY VALID WORKTREE WHOSE CHECKOUT NEVER FINISHED. git writes
//     the .git file and the $GIT_DIR/worktrees/<id>/ admin directory BEFORE it
//     populates the working tree, so a `worktree add` stopped by SIGKILL or by
//     the machine rebooting mid-checkout leaves exactly that: an intact pointer
//     over a half-populated tree, whose --show-toplevel is its own path. It
//     passes. (The disk-full case usually does NOT reach here: git's own error
//     path removes the admin directory, which produces the dangling pointer of
//     case 1 in the paragraph above and IS caught.) Closing this needs a notion
//     of "the checkout is complete", and git has no such query — `git status`
//     reports the missing files as deletions, which is indistinguishable from a
//     developer who deleted them. That is an API-shape decision, not an
//     oversight to patch here.
//  2. A perfectly good worktree of some OTHER repository, whose toplevel is
//     itself and so passes. The wrong repo rather than a broken one; rejecting
//     it needs a notion of which origin a repo ought to have, which this path
//     does not carry.
func verifyClaimWorktree(wtPath string) error {
	cmd := exec.Command("git", "-C", wtPath, "rev-parse", "--show-toplevel")
	// ⚠️ .Output(), NOT .CombinedOutput(). This is the only place in this file
	// that PARSES git's stdout — everywhere else the combined buffer is error
	// text — and git writes diagnostics to stderr while still exiting 0.
	// Measured: with GIT_TRACE=1 in the environment, CombinedOutput returns
	// "trace: built-in: git rev-parse --show-toplevel\n<path>", TrimSpace keeps
	// the trace line, the comparison below fails, and EVERY healthy worktree is
	// rejected — with a message telling the operator to rm -rf it.
	//
	// GIT_DIR/GIT_WORK_TREE are scrubbed for the same class of reason: `-C`
	// alone does not beat them. Measured — with both set, `git -C <worktree>
	// rev-parse --show-toplevel` printed the OTHER repository's root and exited
	// 0, which would likewise reject every worktree at once.
	cmd.Env = envWithout(os.Environ(), "GIT_DIR", "GIT_WORK_TREE")
	out, err := cmd.Output()
	if err != nil {
		detail := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return fmt.Errorf("git rev-parse --show-toplevel: %w: %s", err, detail)
	}
	top := strings.TrimSpace(string(out))
	if !sameDirPath(top, wtPath) {
		return fmt.Errorf("not the root of a git worktree: git resolves this directory to the repository at %q", top)
	}
	return nil
}

// envWithout returns env with the named variables removed.
func envWithout(env []string, drop ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		keep := true
		for _, d := range drop {
			if strings.HasPrefix(kv, d+"=") {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// sameDirPath compares two directory paths after making both absolute and
// resolving symlinks.
//
// A plain string compare would be wrong in three ways: git prints an ABSOLUTE
// PHYSICAL path, while wtPath is built by joining POLYFORGE_WORKSPACE_ROOT as
// the caller gave it — which can be relative (config.FindWorkspaceRoot returns
// "." when os.Getwd fails) and can run through a symlink. On macOS a
// t.TempDir() under /var is /private/var to git, so a string compare there
// would declare every healthy worktree broken. filepath.EvalSymlinks does not
// absolutize, which is why Abs runs first.
func sameDirPath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := absReal(a)
	rb, errB := absReal(b)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}

func absReal(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// writeWorktreeExcludes adds polyforge scratch-file patterns to a worktree's
// per-worktree git exclude file, so they never get accidentally staged.
// Best-effort and non-fatal: a worktree must still be usable if this fails.
func writeWorktreeExcludes(wtPath string) {
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	excludePath := strings.TrimSpace(string(out))
	if excludePath == "" {
		return
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(wtPath, excludePath)
	}

	patterns := []string{".superpowers/", ".pf_meta.json", ".pf_steps.json"}

	existing := ""
	if b, err := os.ReadFile(excludePath); err == nil {
		existing = string(b)
	}
	existingLines := strings.Split(existing, "\n")
	have := make(map[string]bool, len(existingLines))
	for _, l := range existingLines {
		have[strings.TrimSpace(l)] = true
	}

	var toAdd []string
	for _, p := range patterns {
		if !have[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		_, _ = f.WriteString("\n")
	}
	for _, p := range toAdd {
		_, _ = f.WriteString(p + "\n")
	}
}

// ---------------------------------------------------------------------------
// Retired: keying the git_branch lock on the branch work really happens on
// (aihub#356, retired by aihub#416)
// ---------------------------------------------------------------------------
//
// This is where claimTaskBranches, claimBranchForRepo, declaredRepoNames,
// worktreeCurrentBranch and keyedBranchProblems lived. All five existed to make
// ONE value correct — the "<repo>/<branch>" key of the git_branch lock a repo
// declaration derived — and the de-locking ruling retired that derivation
// outright (domain.resourceToLock). With no key to spell there is nothing left
// for them to get right, so they are deleted rather than left as dead code
// nobody dares touch.
//
// ⚠️ WORKTREE CREATION NEVER USED THEM and is unchanged. It runs
// newClaimBranchNames -> resolveClaimBranch -> addClaimWorktree; the only place
// the two paths ever met was keyedBranchProblems, which compared the predicted
// key against the created worktree. Read that as: nothing about which branch a
// claim checks out has changed, only whether a lock is keyed on the answer.
//
// What replaces the fact these functions were reaching for is
// run_attempts.repo_pins (aihub#416 D2): after the worktrees exist, the claim
// records each one's `git rev-parse HEAD`, which says where the work STARTED
// rather than guessing where it will go. See claimRepoPins below.

// attachWorktree puts an EXISTING branch into wtPath.
//
// Where the branch lives is decided here rather than carried in from the
// resolver, because this is the last moment before the command runs and
// refs/heads is the only authority on the question. A local head is checked out
// directly; otherwise the branch is materialised from origin. The "already
// exists" retry closes the remaining race — a concurrent claim, or a ref the
// resolver could not see — for which the alternative is no worktree at all.
//
// NOT HANDLED, deliberately: `fatal: '<b>' is already used by worktree at '<p>'`,
// which is what git 2.43 says when the branch is checked out in ANOTHER
// worktree. There is no recovery — a branch cannot be in two worktrees — so the
// error is returned and the claim handler logs it and skips that repo, which is
// the correct outcome rather than a gap.
func attachWorktree(srcPath, wtPath, branch string) error {
	if gitRefExists(srcPath, "refs/heads/"+branch) {
		return runGit(srcPath, "worktree", "add", wtPath, branch)
	}
	err := runGit(srcPath, "worktree", "add", "-b", branch, wtPath, "origin/"+branch)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return runGit(srcPath, "worktree", "add", wtPath, branch)
	}
	return err
}

// claimFetchTimeout bounds the ONE network call on this path.
//
// `git fetch origin` reaches a remote and has no timeout of its own: an SSH or
// git-daemon peer that accepts the connection and then never answers leaves it
// blocked on a read forever. This runs inside an MCP request OR inside one
// `polyforge drain` round (aihub#667), so an unbounded hang spends the caller's
// whole budget on a step whose failure is already treated as non-fatal ten lines
// below. Same shape as aihub#316, which bounded an unbounded upstream call for
// the same reason.
//
// A var, not a const, solely so TestAddClaimWorktree_FetchIsBounded can shorten
// it; nothing outside tests assigns to it. ⚠️ That makes it a mutated package
// global, which is safe today only because no test in THIS package calls
// t.Parallel() — verified, not assumed, and re-verified when aihub#667 moved
// these tests here from internal/mcp. Whoever adds the first t.Parallel() here
// owns turning this into an explicit parameter or a per-call option.
//
// 🔴 It is now shared by two processes rather than one. Nothing mutates it
// outside a test, so that is still safe; a future per-caller timeout has to
// become a parameter rather than a second assignment to this global.
var claimFetchTimeout = 90 * time.Second

// claimRepoPins reads the commit each freshly-claimed worktree is sitting on:
// {"<repo>": "<40-char sha>"} (aihub#416 D2).
//
// 🔴 A repo whose HEAD cannot be read is OMITTED, never mapped to "" or to a
// placeholder. The owner ruling (Q-4) is that a claim succeeds anyway and the
// missing pin is reported, and the whole value of the map depends on a reader
// being able to tell "started here" from "no record" at a glance. An empty
// string in a map of shas is the kind of value that gets compared, logged and
// eventually believed.
//
// The same environment scrubbing as verifyClaimWorktree, for the same measured
// reason: `-C` does not beat GIT_DIR / GIT_WORK_TREE, and with those set this
// would report ANOTHER repository's HEAD while exiting 0 — a pin that is
// confidently wrong, which is worse than the absence this function is careful
// about everywhere else.
func claimRepoPins(ctx context.Context, worktrees map[string]string) map[string]string {
	if len(worktrees) == 0 {
		return nil
	}
	pins := make(map[string]string, len(worktrees))
	for repo, wtPath := range worktrees {
		cmd := exec.CommandContext(ctx, "git", "-C", wtPath, "rev-parse", "HEAD")
		cmd.Env = envWithout(os.Environ(), "GIT_DIR", "GIT_WORK_TREE")
		out, err := cmd.Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "polyforge: repo pin for %s (%s): %v\n", repo, wtPath, err)
			continue
		}
		sha := strings.TrimSpace(string(out))
		// Length-checked rather than trusted. `rev-parse HEAD` in a repository
		// with no commits at all exits 128 and is caught above, but an unborn or
		// otherwise odd HEAD can print something short — and a truncated sha in
		// this map would silently fail to match the commit it names.
		if len(sha) != 40 {
			fmt.Fprintf(os.Stderr, "polyforge: repo pin for %s: HEAD is %q, not a 40-char sha\n", repo, sha)
			continue
		}
		pins[repo] = sha
	}
	if len(pins) == 0 {
		return nil
	}
	return pins
}

// addClaimWorktree materialises wtPath as a git worktree of srcPath.
//
// It attaches to whatever branch resolveClaimBranch finds — on a fresh claim
// just as much as on a resume, see that function for why the mode must not gate
// it — and creates a branch off origin/main only when nothing matches.
//
// ctx is the caller's context — an MCP request, or one `polyforge drain` round
// since aihub#667 — and it bounds the fetch, and ONLY the fetch. The local git
// invocations are deliberately left uncancellable.
//
// ⚠️ THE STATED REASON FOR THAT HAS MOVED — recorded rather than quietly left in
// place, because it was the premise the decision was argued from. It used to be
// that the claim handler short-circuited on os.Stat(wtPath) alone, so a
// `worktree add` killed part-way through its checkout left a half-populated
// directory that every later claim adopted and never repaired. aihub#328 made
// that early return validate the directory with verifyClaimWorktree, so a
// half-built one is now refused instead of adopted, and "adopted forever" is no
// longer a consequence of cancelling here.
//
// They stay uncancellable on the weaker reason that survives: a refused
// directory leaves that repo with NO worktree until somebody clears it by hand.
// That is a visible, reported failure rather than a silent one, but it is still
// worse than not making the mess. A local command that outlives a cancelled
// request costs a few seconds of CPU.
func addClaimWorktree(ctx context.Context, srcPath, wtPath string, n claimBranchNames) error {
	if n.Branch == "" {
		return fmt.Errorf("empty branch name")
	}
	if existing := resolveClaimBranch(srcPath, n); existing != "" {
		if err := attachWorktree(srcPath, wtPath, existing); err != nil {
			return err
		}
		return clearBaseUpstream(ctx, srcPath, existing)
	}
	// Nothing to attach to: a genuinely new work item, or one whose branch was
	// deleted. Create it rather than failing and leaving the repo with no
	// worktree at all.

	// Sync the local clone from origin so the new branch starts from the latest
	// remote state, not a stale local HEAD. Non-fatal: a stale base is worse than
	// a fresh one but far better than no worktree, which is why bounding this is
	// safe as well as necessary.
	fetchCtx, cancel := context.WithTimeout(ctx, claimFetchTimeout)
	out, fetchErr := exec.CommandContext(fetchCtx, "git", "-C", srcPath, "fetch", "origin").CombinedOutput()
	cancel()
	if fetchErr != nil {
		fmt.Fprintf(os.Stderr, "polyforge: fetch origin in %s: %v: %s\n", srcPath, fetchErr, string(out))
	}

	// --no-track is what stops the new branch from being configured to push to
	// main; see clearBaseUpstream for the measurement and the consequence.
	// It goes before -b because `worktree add`'s option parser stops at the
	// first non-option argument, and -b takes the branch name as its value.
	err := runGit(srcPath, "worktree", "add", "--no-track", "-b", n.Branch, wtPath, "origin/main")
	if err == nil {
		return clearBaseUpstream(ctx, srcPath, n.Branch)
	}
	// Branch may already exist (a racing retry of the same claim, or a ref
	// resolveClaimBranch could not see) — fall back to attach.
	//
	// ⚠️ "already checked out" is DEAD TEXT and predates aihub#322. Verified
	// against git 2.43.0: `worktree add -b <b>` on an existing branch says
	// `fatal: a branch named '<b>' already exists`, and the checked-out-elsewhere
	// case says `is already used by worktree at '<path>'` — neither contains the
	// string. Left in place rather than removed: it is harmless, it may match an
	// older or newer git, and deleting it is a behaviour change on a path this
	// work item is not about. The live matcher is "already exists".
	if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already checked out") {
		if err := runGit(srcPath, "worktree", "add", wtPath, n.Branch); err != nil {
			return err
		}
		return clearBaseUpstream(ctx, srcPath, n.Branch)
	}
	return err
}

// clearBaseUpstream drops an upstream pointing at a protected branch from the
// task branch this claim just materialised (aihub#257).
//
// It runs on all three exits of addClaimWorktree, and each one needs it for a
// different reason:
//
//   - on the CREATE path it is DEFENCE IN DEPTH and nothing more. --no-track
//     wins over branch.autoSetupMerge=always — measured on git 2.43.0, the
//     branch comes out with no upstream — so on any git that accepts the flag
//     this call has no reachable behaviour. It is kept because the invariant
//     worth holding is "the branch is not configured to push to main" rather
//     than "the flag was passed", and a git too old for --no-track would break
//     the flag and not the check. ⚠️ An earlier draft of this comment claimed
//     autoSetupMerge=always was a second live reason. It is not; it was written
//     from reasoning and disproved by measuring.
//   - the two ATTACH paths take a branch that already exists, and every task
//     branch created before this change carries upstream=origin/main. This is
//     the exit that repairs, and it is deliberately NOT a workspace-wide sweep
//     — see `polyforge doctor`'s branch-upstream check, which reports the rest
//     and repairs nothing.
//
// ⚠️ THE ATTACH EXITS ARE NOT WHERE MOST OF THE DAMAGE IS. An existing task
// worktree has a directory on disk, so the claim handler's os.Stat reuse fires
// and addClaimWorktree is never called at all. repairReusedWorktreeUpstream
// covers that path; this function covers only the case where the branch
// survived but its directory did not.
//
// ctx has its cancellation stripped: the surrounding claim path deliberately
// runs local git uncancellably (see addClaimWorktree), and a repair that failed
// with "context canceled" after `worktree add` had already succeeded would fail
// the whole claim for that repo and drop a healthy worktree from the state file.
//
// A genuine failure IS returned rather than swallowed: it means `git branch
// --unset-upstream` failed on a branch that reported having an upstream, which
// is a broken repo, not a routine outcome. Nothing here fails merely because
// the branch has no upstream — GitClearProtectedUpstream reports that as
// "nothing to clear".
func clearBaseUpstream(ctx context.Context, srcPath, branch string) error {
	cleared, err := coding.GitClearProtectedUpstream(context.WithoutCancel(ctx), srcPath, branch)
	if err != nil {
		return err
	}
	if cleared != "" {
		fmt.Fprintf(os.Stderr, "polyforge: %s tracked %s and would have pushed there; upstream cleared\n", branch, cleared)
	}
	return nil
}

// repairReusedWorktreeUpstream clears a protected upstream from the branch an
// ALREADY-EXISTING worktree has checked out (aihub#257).
//
// It reads the branch from the WORKTREE rather than using the name this claim
// computed. A reused directory is routinely on a name today's derivation does
// not produce — a pre-aihub#322 polyforge/<ulid8>, or a name from before the
// work item's goal was edited — and unsetting the upstream of the name we
// happen to have computed would either do nothing or touch an unrelated branch.
// The worktree's own HEAD is the only authority on what is checked out in it.
//
// Failure is logged, never propagated. The caller has already decided this
// worktree is healthy and is about to record it in the state file; a claim must
// not be downgraded to "no worktree for this repo" because a repair of a
// pre-existing condition did not work out.
func repairReusedWorktreeUpstream(ctx context.Context, srcPath, wtPath string) {
	out, err := exec.Command("git", "-C", wtPath, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		// Detached HEAD (exit 1) or an unreadable worktree. Neither has a branch
		// whose upstream could send anything anywhere.
		return
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return
	}
	if err := clearBaseUpstream(ctx, srcPath, branch); err != nil {
		fmt.Fprintf(os.Stderr, "polyforge: could not clear %s's upstream in %s: %v\n", branch, srcPath, err)
	}
}

// runGit runs a local git command in srcPath, folding its combined output into
// the error so callers can both report it and match on it. Every invocation is
// local — `worktree add` resolves origin/<x> from a remote-tracking ref and does
// not reach the network — so there is nothing here for a timeout to protect; see
// addClaimWorktree for why these are deliberately not cancellable.
func runGit(srcPath string, args ...string) error {
	full := append([]string{"-C", srcPath}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
