package coding

import (
	"context"
	"fmt"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// ShipResult reports what a fused commit -> push -> PR call did, including — and
// especially — when it failed.
//
// Ship returns a non-nil *ShipResult even alongside a non-nil error. That is
// deliberate and deliberately un-idiomatic. Fusing pf_commit + pf_push + pf_pr
// into one MCP round-trip is worth ~1% of billed input (aihub#286), but it takes
// something away: with three calls the caller SAW the commit sha come back
// before it ever attempted a push, so "the push failed" still left it knowing a
// commit existed locally. With one call that knowledge only exists if the
// failure path carries it. Every field below exists to hand that back.
type ShipResult struct {
	// Stage is where the chain stopped — StageCommit, StagePush or StagePR on
	// failure, StageDone on success.
	Stage string
	// StagedUncommitted reports a populated index with no commit made from it:
	// `git add` succeeded and `git commit` did not. It is a real side effect —
	// the worktree is left in a state the caller did not ask for — so it is
	// reported rather than inferred.
	StagedUncommitted bool
	// Committed reports that THIS call created a commit. It is false on an
	// idempotent retry whose commit was made by an earlier attempt; read
	// HeadSHA, not CommitSHA, for "what is at the branch tip".
	Committed bool
	// CommitSHA is the commit this call created. Empty when Committed is false.
	CommitSHA string
	// HeadSHA is the worktree HEAD after the commit stage, empty only when it
	// could not be resolved.
	HeadSHA string
	// Branch is the task branch, empty when it could not be resolved.
	Branch string
	// Wrap is the push+PR outcome, nil when the chain stopped before the push
	// stage was entered.
	Wrap *WrapResult
}

// Ship performs the fused commit -> push -> PR sequence for a work item:
//
//  1. Stage (all changes, or only `paths`).
//  2. Commit — but ONLY if something is actually staged. Nothing staged is a
//     fact to branch on, not an error: it is exactly the state a retry after a
//     failed push finds itself in, and treating it as an error there would stop
//     the retry before it reached the stage that actually failed. This is the
//     idempotency guarantee — a retried Ship never duplicates a commit.
//  3. Push + open/reuse the PR, via the same pushAndOpenPR that backs Wrap. Its
//     PR-coverage rules (aihub#203/#207/#226) are exactly what Ship wants: a
//     second Ship on an open PR pushes onto it instead of failing with
//     "a pull request already exists", and a Ship whose PR already covers HEAD
//     does nothing at all.
//
// The push it performs is the same lease-protected force-push as pf_push.
//
// Returns a non-nil *ShipResult in every case; see the type doc.
func Ship(ctx context.Context, sf *config.StateFile, repo, workspaceRoot, message string,
	paths []string, prTitle, prBody, prBase string) (*ShipResult, error) {
	res := &ShipResult{Stage: StageCommit}

	worktreePath, err := WorktreePath(sf.WIID, repo, workspaceRoot)
	if err != nil {
		return res, err
	}

	if err := GitStage(ctx, worktreePath, paths); err != nil {
		return res, err
	}

	staged, err := GitHasStagedChanges(ctx, worktreePath)
	if err != nil {
		// `git add` ran and whether it left anything behind is exactly what
		// could not be determined, so assume the loud answer.
		res.StagedUncommitted = true
		return res, err
	}
	if staged {
		res.StagedUncommitted = true
		sha, err := gitCommitStaged(ctx, worktreePath, message)
		if err != nil {
			return res, err
		}
		res.StagedUncommitted = false
		res.Committed = true
		res.CommitSHA = sha
	}

	// The commit stage is over — either it produced a commit or none was needed.
	// Advance the stage HERE rather than just before the push, so that a failure
	// in the bookkeeping below cannot report StageCommit ("the local commit had
	// not completed") on a response that also reports the commit it made.
	res.Stage = StagePush

	head, err := GitRevParse(ctx, worktreePath, "HEAD")
	if err != nil {
		return res, err
	}
	res.HeadSHA = head

	branch, err := GitCurrentBranch(ctx, worktreePath)
	if err != nil {
		return res, fmt.Errorf("current branch: %w", err)
	}
	res.Branch = branch

	if prTitle == "" {
		prTitle = fmt.Sprintf("feat: %s", sf.WIID)
	}

	wrap, wrapErr := pushAndOpenPR(ctx, worktreePath, branch, prTitle, prBody, prBase)
	res.Wrap = wrap
	if wrap != nil {
		res.Stage = wrap.Stage
	}
	if wrapErr != nil {
		return res, wrapErr
	}
	res.Stage = StageDone
	return res, nil
}
