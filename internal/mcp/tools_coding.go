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

// commitLockGate is the client half of the commit-time lock gate (aihub#366):
// it hands the paths a pending commit contains to the server, which compares
// them against the file_scope locks THIS attempt actually holds, acquires the
// difference, and refuses when the difference belongs to somebody else.
//
// It is stateful because the outcome has to reach the tool response. A gate that
// silently widened the attempt's lock set would be a second version of the
// defect this closes — an action whose real effect is not visible where it is
// invoked — so what it took is reported back alongside the commit sha.
type commitLockGate struct {
	s    *Server
	wiID string
	repo string

	// invoked records that run() was reached at all — i.e. something was staged,
	// so the commit had a change set to protect. ran records that the server
	// answered and that change set was actually reconciled. They are two fields
	// rather than one because the gap between them is a whole class of outcome:
	// invoked-but-not-ran is "checked nothing, committed nothing", and report()
	// has to be able to say so. err is why.
	invoked  bool
	ran      bool
	err      error
	acquired []string
	checked  int
}

func (s *Server) newCommitLockGate(wiID, repo string) *commitLockGate {
	return &commitLockGate{s: s, wiID: wiID, repo: repo}
}

// run is the coding.CommitGate implementation.
//
// 🔴 FAIL-CLOSED IN EVERY BRANCH, and that is a deliberate trade rather than an
// oversight. "The gate could not run" and "the gate ran and found nothing" are
// different facts, and collapsing the first into the second is the shape that
// makes a gate look present while being absent. The escape hatch is real but
// expensive — committing by hand with git also loses the commit event, and
// pf_push / pf_ship / pf_wrap still need the state file — so bypassing costs
// more than retrying, which is the property that keeps people inside the gate.
//
// ⚠️ AN EARLIER VERSION OF THIS PASSED WHEN THE STATE FILE WAS MISSING, on the
// stated grounds that it would otherwise break pf_commit in a worktree whose
// work item has been wrapped. That reason was FALSE, and the test that now
// stands in its place is what disproved it: both callers resolve the worktree
// through coding.WorktreePath, which reads the same state file and fails first,
// so pf_commit has never worked without one. The branch was therefore not a
// concession to a real case — it was a way to switch the gate off by deleting a
// file, guarding nothing. Deleting the state file now costs the whole tool,
// which is where a bypass has to cost more than compliance.
func (g *commitLockGate) run(ctx context.Context, paths []string) error {
	// Recorded before anything can fail, so that "the gate was asked about N
	// files" survives every failure below and report() never has to guess.
	g.invoked = true
	g.checked = len(paths)

	sf, err := config.ResolveStateFile(g.wiID)
	if err != nil {
		return g.fail(fmt.Errorf("the commit-time lock check could not read this work item's attempt "+
			"credentials, so nothing was committed: %w", err))
	}

	res, err := g.s.client.ReconcileCommitLocks(ctx, sf.WIID, map[string]any{
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"repo":           g.repo,
		"paths":          paths,
	})
	if err != nil {
		if isAihubCode(err, "CONFLICT_LOCK_TAKEN") {
			return g.fail(fmt.Errorf("commit refused: %w", err))
		}
		return g.fail(fmt.Errorf("the commit-time lock check could not be completed, so nothing was committed "+
			"(fail-closed on purpose: \"could not check\" is not \"checked and clear\"; the files are still "+
			"staged, so retrying costs nothing): %w", err))
	}

	g.ran = true
	g.acquired = jsonStrSlice(res["acquired_paths"])
	return nil
}

// fail records why the gate did not complete and hands the error straight back,
// so the value the caller sees and the value report() classifies are the same
// one. Two independently constructed strings would be free to disagree about
// what happened, which is the defect this whole file is about.
func (g *commitLockGate) fail(err error) error {
	g.err = err
	return err
}

// report writes the gate's outcome into a tool response.
//
// 🔴 THE FAILURE SIDE HAS THREE OUTCOMES, NOT ONE. An earlier version keyed the
// whole switch on !g.ran, which is true for all three — the gate was never
// invoked, the gate could not run, and the gate ran and refused — and printed
// "no staged changes, so no files needed locking" for each. On a pf_ship whose
// commit was refused over a contested file that produced a self-contradicting
// object: an `error` naming CONFLICT_LOCK_TAKEN and a `side_effects` entry
// saying files were staged, sitting next to a `lock_gate_detail` denying there
// was anything staged at all. That is precisely the conflation run()'s own doc
// comment above forbids — "the gate could not run" is not "the gate ran and
// found nothing" — reproduced one layer up, in the reporting rather than in the
// gate. pf_commit hides it (it answers a refusal with errResult and never calls
// report), so pf_ship's structured failure object is where it surfaces, and
// that object is the entire deliverable of pf_ship's failure path.
func (g *commitLockGate) report(out map[string]any) map[string]any {
	switch {
	case !g.invoked:
		// runCommitGate never reached it: nothing was staged, so no commit was
		// created and there was no change set to protect.
		out["lock_gate"] = "not_run"
		out["lock_gate_detail"] = "no staged changes, so no files needed locking"
	case isAihubCode(g.err, "CONFLICT_LOCK_TAKEN"):
		out["lock_gate"] = "refused"
		out["lock_gate_detail"] = fmt.Sprintf(
			"%d changed file(s) were checked and at least one is held by another live attempt, so "+
				"nothing was committed; `error` names every blocked path, its holder and what to do",
			g.checked)
	case !g.ran:
		out["lock_gate"] = "could_not_run"
		out["lock_gate_detail"] = fmt.Sprintf(
			"the lock check over %d changed file(s) did not complete, so nothing was committed "+
				"(fail-closed: \"could not check\" is not \"checked and clear\"); the files are still "+
				"staged, so retrying costs nothing", g.checked)
	case len(g.acquired) > 0:
		out["lock_gate"] = "acquired"
		out["locks_acquired_for"] = g.acquired
		out["lock_gate_detail"] = fmt.Sprintf(
			"%d of %d changed file(s) were outside this attempt's lock set and are now locked by it until the attempt ends",
			len(g.acquired), g.checked)
	default:
		out["lock_gate"] = "covered"
		out["lock_gate_detail"] = fmt.Sprintf(
			"all %d changed file(s) were already covered; no lock was taken", g.checked)
	}
	return out
}

// jsonStrSlice pulls a []string out of a decoded JSON field.
func jsonStrSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
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
		Name: "pf_commit",
		Description: "Commit staged changes in the work item's worktree and emit a commit event on the wi timeline. " +
			"⚠️ THIS CALL CAN ACQUIRE LOCKS — a heavier semantic than \"commit\" normally carries, so read this before using it. " +
			"Before committing it lists the files the commit would contain and compares them against the file_scope locks THIS ATTEMPT ACTUALLY HOLDS " +
			"(the live lock set, NOT declared_resources — the two routinely disagree). " +
			"Any changed file no held lock covers is locked for this attempt automatically and stays locked until the attempt ends, so committing WIDENS your lock set. " +
			"If another live attempt already holds one of those files the commit is REFUSED with CONFLICT_LOCK_TAKEN: nothing is committed, the files stay staged, and the error names every blocked path plus its holder — actor, work item and attempt. " +
			"When every changed file is already covered, no lock is taken and nothing is written. " +
			"On SUCCESS the response says which happened in `lock_gate`: covered | acquired (with locks_acquired_for) | not_run (nothing was staged, so no commit was made). A refusal or a failed check comes back as a plain error string with no `lock_gate` field at all. " +
			"There is no pass-through: a lock check that cannot reach the server fails the commit rather than allowing it.",
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

		gate := s.newCommitLockGate(wiID, repo)
		sha, err := coding.GitCommitGated(ctx, worktreePath, message, paths, gate.run)
		if err != nil {
			return errResult(err)
		}
		s.emitCodingEvent(ctx, wiID, "commit", map[string]any{
			"repo":    repo,
			"sha":     sha,
			"message": message,
			"files":   paths,
		})
		return jsonResult(gate.report(map[string]any{
			"sha":   sha,
			"repo":  repo,
			"files": paths,
		}))
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
			"only when you need to inspect state between the steps. " +
			"⚠️ AND ITS COMMIT STAGE ACQUIRES LOCKS, exactly as pf_commit's does: every file the commit " +
			"contains that this attempt does not already hold a file_scope lock for is locked for it " +
			"automatically and stays locked until the attempt ends. A file held by another live attempt " +
			"stops the whole call at stage=\"commit\" with CONFLICT_LOCK_TAKEN — nothing committed, nothing " +
			"pushed, no PR — and the error names every blocked path and its holder. `lock_gate` in the " +
			"response reports which of five things happened — covered | acquired | not_run (nothing was " +
			"staged) | refused (another live attempt holds one of the files) | could_not_run (the check " +
			"itself failed) — and there is no pass-through: a check that cannot reach the server fails " +
			"the ship rather than allowing it.",
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

		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		paths := strSliceArg(args, "paths")
		gate := s.newCommitLockGate(wiID, repo)
		res, shipErr := coding.Ship(ctx, sf, repo, strArg(args, "workspace_root"),
			message, paths, prTitle, prBody, strArg(args, "pr_base"), gate.run)

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
		return jsonResult(gate.report(shipPayload(repo, res, shipErr)))
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
