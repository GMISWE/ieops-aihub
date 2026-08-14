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

// Wrap actions, reported back to the caller so that a successful response can
// never be mistaken for "the local commits were delivered".
const (
	// WrapActionReusedPR means nothing was pushed and no PR was created: the
	// PR already on the branch demonstrably covers local HEAD (or HEAD is
	// already contained in the PR's base branch), so there was nothing to
	// deliver. This is the genuinely idempotent replay.
	WrapActionReusedPR = "reused_existing_pr"
	// WrapActionPushedToPR means local HEAD was not covered by the open PR on
	// the branch, so it was pushed; the existing open PR now carries it and no
	// second PR was created.
	WrapActionPushedToPR = "pushed_to_existing_pr"
	// WrapActionPushedAndCreatedPR means local HEAD was not covered by any PR
	// (there was none, or the only ones are merged/closed), so it was pushed
	// and a new PR was opened for it.
	WrapActionPushedAndCreatedPR = "pushed_and_created_pr"
)

// WrapResult reports what Wrap did, not merely which PR it ended up pointing at.
//
// The distinction matters: a wrap that reuses a MERGED PR delivers nothing, and
// pf_wrap goes on to destroy the attempt credentials and state file, so a caller
// that cannot tell the two apart has no way to notice undelivered work (aihub#226).
type WrapResult struct {
	// PR is the gh JSON for the PR this wi is delivered through.
	PR map[string]any
	// Action is one of the WrapAction* constants.
	Action string
	// Branch is the task branch that was inspected (and pushed, if Pushed).
	Branch string
	// Pushed reports that a push ran and therefore that PushedSHA is now on
	// origin/<Branch>. It does not promise the push moved the ref: when
	// coverage could not be proven but HEAD happened to already be on the
	// remote, git reports "everything up-to-date" and Pushed is still true.
	// The guarantee callers need is the one it makes — HEAD is on the remote —
	// not a count of transferred objects.
	Pushed bool
	// PushedSHA is the pushed commit, empty when Pushed is false.
	PushedSHA string
}

// Wrap executes the wrap sequence for a work item:
//  1. Look up the PR already on the task branch, if any.
//  2. Decide whether that PR already covers local HEAD (see deliveredByPR).
//     If it does, return it untouched — that is the idempotent replay, and it
//     must not push, because after a merge the head branch may already be
//     deleted on the remote and pushing would resurrect it (aihub#203 A7).
//  3. If it does not cover HEAD, there is undelivered local work, so push it.
//     An open PR then carries it; a merged/closed PR cannot, so a new PR is
//     opened for it (aihub#226).
//  4. If there is no PR at all, push + create.
//
// The caller (pf_wrap) then calls pf_complete_attempt(wrapped) and deletes the
// state file. Both are destructive and irreversible from the agent's side, which
// is why "did this actually deliver?" is answered here and surfaced in
// WrapResult rather than left implicit in an ok:true.
func Wrap(ctx context.Context, sf *config.StateFile, repo, workspaceRoot, prTitle, prBody string) (*WrapResult, error) {
	worktreePath, err := WorktreePath(sf.WIID, repo, workspaceRoot)
	if err != nil {
		return nil, err
	}

	branch, err := GitCurrentBranch(ctx, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("current branch: %w", err)
	}

	// Check for an existing PR. A real gh error is not swallowed — every
	// branch below needs to trust that existingPR == nil genuinely means
	// "no PR" rather than "gh failed".
	existingPR, err := GHGetPR(ctx, worktreePath, branch)
	if err != nil {
		return nil, fmt.Errorf("check existing PR: %w", err)
	}

	base := ""
	if existingPR != nil {
		base, _ = existingPR["baseRefName"].(string)

		delivered, err := deliveredByPR(ctx, worktreePath, existingPR)
		if err != nil {
			return nil, fmt.Errorf("check whether PR covers HEAD: %w", err)
		}
		if delivered {
			return &WrapResult{PR: existingPR, Action: WrapActionReusedPR, Branch: branch}, nil
		}

		// HEAD is not covered by this PR. Something local is undelivered, so
		// it gets pushed either way; only whether a second PR is needed
		// depends on the PR's state.
		state, _ := existingPR["state"].(string)
		if strings.EqualFold(state, "OPEN") {
			sha, err := GitPush(ctx, worktreePath)
			if err != nil {
				return nil, fmt.Errorf("push new commits onto open PR #%v: %w", existingPR["number"], err)
			}
			return &WrapResult{
				PR: existingPR, Action: WrapActionPushedToPR, Branch: branch,
				Pushed: true, PushedSHA: sha,
			}, nil
		}
		// MERGED or CLOSED: that PR can never carry these commits, so fall
		// through to push + create a new PR for them. If the merge deleted the
		// head branch, GitPush recreates it (it detects the absence and pushes
		// without a lease) — but only because there are genuinely undelivered
		// commits that need a branch to sit on. The aihub#203 "no push after
		// merge" rule is preserved by the delivered check above, which is what
		// actually covers replay.
		//
		// If GHCreatePR then fails, the push is deliberately NOT rolled back:
		// the commits are safe on the remote and the error is returned, so the
		// human still has both the work and a report. Unwinding the push to
		// keep this operation atomic would trade a loud failure for the exact
		// risk this whole change exists to remove.
	}

	sha, err := GitPush(ctx, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("push: %w", err)
	}
	if prTitle == "" {
		prTitle = fmt.Sprintf("feat: %s", sf.WIID)
	}
	newPR, err := GHCreatePR(ctx, worktreePath, prTitle, prBody, "", base)
	if err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}
	return &WrapResult{
		PR: newPR, Action: WrapActionPushedAndCreatedPR, Branch: branch,
		Pushed: true, PushedSHA: sha,
	}, nil
}

// deliveredByPR reports whether local HEAD needs no further delivery given the
// PR already on the branch. True means "wrap may reuse this PR and push
// nothing"; the bar for that is proof, not the mere existence of a PR.
//
// Two independent proofs are accepted:
//
//  1. HEAD is already contained in the PR's base branch (origin/<baseRefName>).
//     Whatever HEAD holds is in the target branch, so there is nothing to
//     deliver. This covers a branch that was rebased onto the base after its
//     PR merged.
//  2. HEAD is one of the commits the PR covers. This covers the ordinary replay
//     of a wrap whose PR merged (including squash merges, where HEAD is not an
//     ancestor of the base afterwards).
//
// Everything else — including "cannot tell" — returns false. Being wrong in that
// direction costs an extra push and a visible PR; being wrong in the other
// direction silently strands commits nobody will look for, which is the bug this
// function exists to prevent. Two known "cannot tell" cases ride on that bias:
// a stale origin/<base> (no fetch is done here, deliberately — Wrap stays
// offline-tolerant and side-effect-light) and a PR whose commit list `gh`
// truncated at 100 entries.
func deliveredByPR(ctx context.Context, worktreePath string, pr map[string]any) (bool, error) {
	head, err := GitRevParse(ctx, worktreePath, "HEAD")
	if err != nil {
		return false, err
	}

	if base, _ := pr["baseRefName"].(string); base != "" {
		// A missing origin/<base> is "cannot tell", not an error: the ref may
		// simply not be fetched in this worktree.
		inBase, err := GitIsAncestor(ctx, worktreePath, head, "refs/remotes/origin/"+base)
		if err == nil && inBase {
			return true, nil
		}
	}

	return prCommitOIDs(pr)[head], nil
}
