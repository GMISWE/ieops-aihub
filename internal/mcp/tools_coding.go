package mcp

import (
	"context"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/coding"
	"github.com/GMISWE/ieops-aihub/internal/config"
)

// emitCodingEvent resolves the state file and emits an event, best-effort.
// A failure here (bad wi_id, no state file, network error) must never fail
// the calling tool — the git/gh operation already succeeded by the time this
// runs, so we just skip the emit silently.
func (s *Server) emitCodingEvent(ctx context.Context, wiID, eventType string, payload map[string]any) {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return
	}
	body := map[string]any{
		"work_item_id":   wiID,
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"event_type":     eventType,
		"payload":        payload,
	}
	_, _ = s.client.EmitEvent(ctx, body)
}

func (s *Server) registerCodingTools() {
	// pf_diff
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_diff",
		Description: "Show git diff for the work item's worktree",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"work_item_id":   prop("string", "Work item ID"),
			"repo":           prop("string", "Repository name"),
			"vs_base":        prop("boolean", "Diff vs base branch instead of HEAD"),
		}, []string{"work_item_id", "repo"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		repo := strArg(args, "repo")
		if repo == "" {
			return errResult(fmt.Errorf("repo is required"))
		}

		worktreePath, err := coding.WorktreePath(wiID, repo, strArg(args, "workspace_root"))
		if err != nil {
			return errResult(err)
		}

		diff, err := coding.GitDiff(ctx, worktreePath, boolArg(args, "vs_base"))
		if err != nil {
			return errResult(err)
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: diff}},
		}, nil
	})

	// pf_commit
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_commit",
		Description: "Commit staged changes in the work item's worktree and emit a commit event on the wi timeline.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"work_item_id":   prop("string", "Work item ID"),
			"repo":           prop("string", "Repository name"),
			"message":        prop("string", "Commit message"),
			"paths":          prop("array", "Specific paths to stage (default: all)"),
		}, []string{"work_item_id", "repo", "message"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		repo := strArg(args, "repo")
		if repo == "" {
			return errResult(fmt.Errorf("repo is required"))
		}
		message := strArg(args, "message")
		if message == "" {
			return errResult(fmt.Errorf("message is required"))
		}

		worktreePath, err := coding.WorktreePath(wiID, repo, strArg(args, "workspace_root"))
		if err != nil {
			return errResult(err)
		}

		paths := strSliceArg(args, "paths")

		sha, err := coding.GitCommit(ctx, worktreePath, message, paths)
		if err != nil {
			return errResult(err)
		}
		s.emitCodingEvent(ctx, wiID, "commit", map[string]any{
			"repo":    repo,
			"sha":     sha,
			"message": message,
			"files":   paths,
		})
		return jsonResult(map[string]any{
			"sha":   sha,
			"repo":  repo,
			"files": paths,
		})
	})

	// pf_push
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_push",
		Description: "Push the current branch to origin with --force-with-lease and emit a push event on the wi timeline. Refuses to push to main/master/dev/tot.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"work_item_id":   prop("string", "Work item ID"),
			"repo":           prop("string", "Repository name"),
		}, []string{"work_item_id", "repo"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		repo := strArg(args, "repo")
		if repo == "" {
			return errResult(fmt.Errorf("repo is required"))
		}

		worktreePath, err := coding.WorktreePath(wiID, repo, strArg(args, "workspace_root"))
		if err != nil {
			return errResult(err)
		}

		branch, err := coding.GitCurrentBranch(ctx, worktreePath)
		if err != nil {
			return errResult(err)
		}

		baseSHA, err := coding.GitPush(ctx, worktreePath)
		if err != nil {
			if isBaseMoved(err) {
				return jsonResult(map[string]any{
					"error":  coding.BaseMovedMarker,
					"advice": "Rebase on the latest base branch and retry pf_push",
				})
			}
			return errResult(err)
		}

		s.emitCodingEvent(ctx, wiID, "push", map[string]any{
			"repo":   repo,
			"branch": branch,
		})
		return jsonResult(map[string]any{
			"ok":               true,
			"branch":           branch,
			"base_sha_at_push": baseSHA,
		})
	})

	// pf_pr
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_pr",
		Description: "Create a GitHub PR for the work item's task branch and emit a pr_opened event on the wi timeline.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"work_item_id":   prop("string", "Work item ID"),
			"repo":           prop("string", "Repository name"),
			"title":          prop("string", "PR title"),
			"body":           prop("string", "PR body"),
			"head":           prop("string", "Head branch (default: current)"),
			"base":           prop("string", "Base branch (default: default branch)"),
		}, []string{"work_item_id", "repo", "title", "body"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		repo := strArg(args, "repo")
		if repo == "" {
			return errResult(fmt.Errorf("repo is required"))
		}
		title := strArg(args, "title")
		if title == "" {
			return errResult(fmt.Errorf("title is required"))
		}
		body := strArg(args, "body")
		if body == "" {
			return errResult(fmt.Errorf("body is required"))
		}

		worktreePath, err := coding.WorktreePath(wiID, repo, strArg(args, "workspace_root"))
		if err != nil {
			return errResult(err)
		}

		result, err := coding.GHCreatePR(ctx, worktreePath, title, body,
			strArg(args, "head"), strArg(args, "base"))
		if err != nil {
			return errResult(err)
		}
		s.emitCodingEvent(ctx, wiID, "pr_opened", prPayload(repo, title, result))
		return jsonResult(result)
	})

	// pf_ship — pf_commit + pf_push + pf_pr in one round-trip (aihub#286).
	//
	// Why a new tool rather than pf_commit(push=true, open_pr=true): pr_title
	// and pr_body would then be required only when open_pr is set, and
	// objectSchema() renders a flat `required` array that cannot say so — the
	// published schema would understate its own contract, which is aihub#238 and
	// aihub#241 for the third time. A tool named "commit" that force-pushes to
	// origin also hides exactly the property that most needs to stay visible.
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_ship",
		Description: "FUSED AND IT PUSHES TO ORIGIN: commit + push + open PR in one call. " +
			"The push is the same lease-protected FORCE-PUSH as pf_push (--force-with-lease) " +
			"and it refuses main/master/dev/tot. Use this instead of pf_commit + pf_push + pf_pr, " +
			"which cost three round-trips for three confirmations no decision depends on. " +
			"Idempotent: a retry commits only if something is staged and skips the push when a PR " +
			"already covers HEAD, so retrying after a failure never duplicates a commit; if an open " +
			"PR already exists on the branch it is pushed to and reused rather than duplicated. " +
			"On failure the response is a JSON object, not an error string: \"stage\" says which of " +
			"commit/push/pr failed and \"side_effects\" lists what already happened (typically a " +
			"local commit that was never pushed). Reach for pf_commit / pf_push / pf_pr separately " +
			"only when you need to inspect state between the steps.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"work_item_id":   prop("string", "Work item ID"),
			"repo":           prop("string", "Repository name"),
			"message":        prop("string", "Commit message"),
			"paths":          prop("array", "Specific paths to stage (default: all)"),
			"pr_title":       prop("string", "PR title"),
			"pr_body":        prop("string", "PR body"),
			"pr_base":        prop("string", "Base branch to use if a NEW PR has to be opened. Default: the base of a previous PR on this branch, else the repo default branch. Unused when an open PR on the branch already carries the commits (that PR keeps its own base)."),
		}, []string{"work_item_id", "repo", "message", "pr_title", "pr_body"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		repo := strArg(args, "repo")
		if repo == "" {
			return errResult(fmt.Errorf("repo is required"))
		}
		message := strArg(args, "message")
		if message == "" {
			return errResult(fmt.Errorf("message is required"))
		}
		prTitle := strArg(args, "pr_title")
		if prTitle == "" {
			return errResult(fmt.Errorf("pr_title is required"))
		}
		prBody := strArg(args, "pr_body")
		if prBody == "" {
			return errResult(fmt.Errorf("pr_body is required"))
		}

		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		paths := strSliceArg(args, "paths")
		res, shipErr := coding.Ship(ctx, sf, repo, strArg(args, "workspace_root"),
			message, paths, prTitle, prBody, strArg(args, "pr_base"))

		// Emit the events the individual tools would have emitted for the stages
		// that actually ran, so shipping in one call leaves the same wi timeline
		// as shipping in three. (A superset, strictly: this push event also
		// carries `sha`, which pf_push's does not.) A partial ship still records
		// the commit it made — the timeline is the only durable record that it
		// happened.
		if res.Committed {
			s.emitCodingEvent(ctx, wiID, "commit", map[string]any{
				"repo":    repo,
				"sha":     res.CommitSHA,
				"message": message,
				"files":   paths,
			})
		}
		if res.Wrap != nil && res.Wrap.Pushed {
			s.emitCodingEvent(ctx, wiID, "push", map[string]any{
				"repo":   repo,
				"branch": res.Wrap.Branch,
				"sha":    res.Wrap.PushedSHA,
			})
		}
		if res.Wrap != nil && res.Wrap.Action == coding.WrapActionPushedAndCreatedPR {
			s.emitCodingEvent(ctx, wiID, "pr_opened", prPayload(repo, prTitle, res.Wrap.PR))
		}

		// jsonResult, not errResult, even on failure: errResult carries a bare
		// string, and the structured side-effect report IS the deliverable of
		// this tool's failure path.
		return jsonResult(shipPayload(repo, res, shipErr))
	})

	// pf_wrap
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_wrap",
		Description: "Wrap a work item: push + PR + complete_attempt(wrapped) + delete state file. Idempotent only when a PR on the branch already covers local HEAD; local commits no PR covers are pushed, and a new PR is opened if the existing one is merged/closed. The response's pr_action says which happened. " +
			"Pass `note` to record the closing note in the same call rather than emitting it with a separate pf_emit_event beforehand.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"work_item_id":   prop("string", "Work item ID"),
			"repo":           prop("string", "Repository name"),
			"pr_title":       prop("string", "PR title (if PR doesn't exist yet)"),
			"pr_body":        prop("string", "PR body (if PR doesn't exist yet)"),
			"note":           prop("string", "Closing note recorded as a `note` event before the attempt is completed (e.g. \"wrapped: <one sentence>\"). Replaces a separate pf_emit_event call."),
		}, []string{"work_item_id", "repo"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		repo := strArg(args, "repo")
		if repo == "" {
			return errResult(fmt.Errorf("repo is required"))
		}

		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		// Execute wrap: push + PR (idempotent only when the existing PR
		// already covers HEAD).
		wrap, err := coding.Wrap(ctx, sf, repo,
			strArg(args, "workspace_root"),
			strArg(args, "pr_title"),
			strArg(args, "pr_body"))
		if err != nil {
			return errResult(fmt.Errorf("wrap sequence (push+PR): %w", err))
		}

		// Emit the same timeline events pf_push / pf_pr emit, so a wrap that
		// actually delivered something is auditable afterwards — by then the
		// state file and credentials are gone, and the timeline is all that is
		// left to tell delivery apart from a no-op replay (aihub#226).
		if wrap.Pushed {
			s.emitCodingEvent(ctx, wiID, "push", map[string]any{
				"repo":   repo,
				"branch": wrap.Branch,
				"sha":    wrap.PushedSHA,
			})
		}
		if wrap.Action == coding.WrapActionPushedAndCreatedPR {
			s.emitCodingEvent(ctx, wiID, "pr_opened",
				prPayload(repo, strArg(args, "pr_title"), wrap.PR))
		}

		// Closing note (aihub#290), emitted HERE — after the push/PR half has
		// actually succeeded, before the completion that deletes the credentials.
		// Emitting it earlier would leave a "wrapped: ..." note on the timeline of
		// a wrap that then failed at the push; emitting it later is impossible,
		// because by then the attempt is closed and the state file is gone.
		//
		// This is NOT exactly-once. A wrap retried after a failed PUSH records the
		// note once, because the push precedes it. A wrap retried after a failed
		// COMPLETE_ATTEMPT records it twice — and that is the failure that
		// actually happens, since this call never sets force_terminate_step, so
		// wrapping with a step still in_progress always fails there. The note is
		// short and duplicate notes are noise rather than damage, which is why
		// this is documented rather than solved; the alternative (an idempotency
		// key on agent_events) is a bigger change than this work item.
		note := strArg(args, "note")
		var noteErr error
		if note != "" {
			noteErr = s.emitNote(ctx, wiID, sf, note)
		}

		// Complete attempt — server expects wi_id in URL path; attempt_id in body for credential check.
		body := map[string]any{
			"status":         "wrapped",
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		completeResult, err := s.client.CompleteAttempt(ctx, sf.WIID, body)
		if err != nil {
			return errResult(fmt.Errorf("complete_attempt: %w%s", err, noteOutcomeSuffix(note != "", noteErr)))
		}

		// Delete state file (terminal status). Delete by the resolved canonical key
		// (sf.WIID), and best-effort the passed key too, so a slug-addressed wrap
		// cleans any stale slug-keyed stub instead of orphaning the canonical file.
		// Mirrors pf_complete_attempt's cleanup. (aihub#141 / #149)
		_ = config.DeleteStateFile(sf.WIID)
		if wiID != sf.WIID {
			_ = config.DeleteStateFile(wiID)
		}

		wrapResult := map[string]any{
			"ok":              true,
			"pr":              wrap.PR,
			"pr_action":       wrap.Action,
			"pushed":          wrap.Pushed,
			"complete_result": completeResult,
		}
		if wrap.PushedSHA != "" {
			wrapResult["pushed_sha"] = wrap.PushedSHA
		}
		applyNoteResult(wrapResult, note != "", noteErr)
		addWorktrees(wrapResult, sf.Worktrees)
		return jsonResult(wrapResult)
	})
}

// isBaseMoved reports the push rejection that GitPush tags "base_moved".
//
// Shared by pf_push and pf_ship so the two cannot drift on the fragile half of
// this — recognising the condition. They deliberately do NOT share a response
// shape: pf_ship has to add which stage it reached and what it already did.
func isBaseMoved(err error) bool {
	return err != nil && strings.Contains(err.Error(), coding.BaseMovedMarker)
}

// shipPayload renders a ShipResult, and the error that ended it, into the
// pf_ship response.
//
// It is a plain function rather than inline handler code because this mapping is
// the whole load-bearing contract of the fused tool's failure path: it is what
// stands between "push failed" and a caller that cannot tell whether a commit is
// sitting unpushed in its worktree. A pure function can be tested for that
// without a git repo, and the git-level half is tested separately — two hops in
// the contract, two assertions.
func shipPayload(repo string, res *coding.ShipResult, err error) map[string]any {
	out := map[string]any{
		"ok":        err == nil,
		"repo":      repo,
		"stage":     res.Stage,
		"committed": res.Committed,
	}
	if res.CommitSHA != "" {
		out["commit_sha"] = res.CommitSHA
	}
	if res.HeadSHA != "" {
		out["head_sha"] = res.HeadSHA
	}
	if res.Branch != "" {
		out["branch"] = res.Branch
	}
	if w := res.Wrap; w != nil {
		out["pushed"] = w.Pushed
		if w.PushedSHA != "" {
			out["pushed_sha"] = w.PushedSHA
		}
		if w.PR != nil {
			out["pr"] = w.PR
		}
		if w.Action != "" {
			out["pr_action"] = w.Action
		}
	}
	if err == nil {
		return out
	}

	if isBaseMoved(err) {
		// Pass the marker through unchanged: callers keying on error ==
		// "base_moved" (the pf_push contract) must keep working through the
		// fused tool. The full git output is kept alongside rather than dropped.
		out["error"] = coding.BaseMovedMarker
		out["error_detail"] = err.Error()
	} else {
		out["error"] = err.Error()
	}
	out["side_effects"] = shipSideEffects(res)
	out["advice"] = shipAdvice(res, err)
	return out
}

// shipSideEffects spells out in plain sentences what a failed pf_ship already
// did to the worktree and the remote. This is precisely the information three
// separate calls gave the caller for free.
func shipSideEffects(res *coding.ShipResult) []string {
	var effects []string
	pushed := res.Wrap != nil && res.Wrap.Pushed

	switch {
	case res.Committed:
		effects = append(effects,
			fmt.Sprintf("a local commit %s was created in the worktree", res.CommitSHA))
	case res.StagedUncommitted:
		effects = append(effects,
			"changes were staged into the index but no commit was created from them")
	case res.HeadSHA != "" && !pushed:
		// The retry case, and the one most likely to be hit: the first pf_ship
		// made the commit and failed at the push, so this call had nothing left
		// to commit. Committed is false and CommitSHA is empty — correctly, this
		// call created nothing — but there IS undelivered local work, and
		// reporting "none" here would be a flat denial of it at the exact moment
		// the caller is deciding whether to redo the work.
		effects = append(effects, fmt.Sprintf(
			"no new commit was needed, but worktree HEAD %s on branch %s is NOT on origin "+
				"(an earlier call already committed it)", res.HeadSHA, res.Branch))
	}

	if pushed {
		effects = append(effects,
			fmt.Sprintf("commit %s was force-pushed to origin/%s", res.Wrap.PushedSHA, res.Wrap.Branch))
	}
	if len(effects) == 0 {
		effects = append(effects, "none: nothing was staged, committed or pushed")
	}
	return effects
}

// shipAdvice says what to do next, per stage. Retrying is always safe — Ship
// re-derives every stage from the repository rather than from a stored cursor —
// so the advice differs only in what the caller should fix first and in how much
// of the work is already safe on the remote.
func shipAdvice(res *coding.ShipResult, err error) string {
	if isBaseMoved(err) {
		return "The base branch moved. Fetch and rebase onto the latest base, then retry pf_ship " +
			"with the same arguments — any commit already made will not be duplicated."
	}
	switch res.Stage {
	case coding.StageCommit:
		return "Nothing was pushed. Fix the commit failure and retry pf_ship with the same arguments."
	case coding.StagePush:
		// Name head_sha rather than "the commit above": on a retry this call
		// created no commit, so there is no commit "above" to point at, and the
		// undelivered work is identified only by HEAD.
		return "Nothing reached origin. Worktree HEAD (head_sha in this response) is local only. " +
			"Fix the push failure and retry pf_ship with the same arguments — no already-made " +
			"commit will be duplicated."
	case coding.StagePR:
		return "The commits are already on origin and are safe; only opening the PR failed. " +
			"Retry pf_ship with the same arguments, or open the PR by hand with pf_pr."
	}
	return "Retry pf_ship with the same arguments — no completed stage is repeated."
}
