package mcp

import (
	"context"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/coding"
	"github.com/GMISWE/ieops-aihub/internal/config"
)

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
		Description: "Commit staged changes in the work item's worktree. Credentials injected from state file.",
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

		var paths []string
		if v, ok := args["paths"]; ok {
			if pathSlice, ok := v.([]any); ok {
				for _, p := range pathSlice {
					if ps, ok := p.(string); ok {
						paths = append(paths, ps)
					}
				}
			}
		}

		sha, err := coding.GitCommit(ctx, worktreePath, message, paths)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(map[string]any{
			"sha":   sha,
			"repo":  repo,
			"files": paths,
		})
	})

	// pf_push
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_push",
		Description: "Push the current branch to origin with --force-with-lease. Refuses to push to main/master.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root":  prop("string", "Workspace root path"),
			"work_item_id":    prop("string", "Work item ID"),
			"repo":            prop("string", "Repository name"),
			"skip_base_check": prop("boolean", "Skip base branch protection check"),
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

		baseSHA, err := coding.GitPush(ctx, worktreePath, boolArg(args, "skip_base_check"))
		if err != nil {
			if strings.Contains(err.Error(), "base_moved") {
				return jsonResult(map[string]any{
					"error":  "base_moved",
					"advice": "Rebase on the latest base branch and retry pf_push",
				})
			}
			return errResult(err)
		}

		return jsonResult(map[string]any{
			"ok":              true,
			"branch":          branch,
			"base_sha_at_push": baseSHA,
		})
	})

	// pf_pr
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_pr",
		Description: "Create a GitHub PR for the work item's task branch",
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
		return jsonResult(result)
	})

	// pf_wrap
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_wrap",
		Description: "Wrap a work item: push + PR (idempotent if PR exists) + complete_attempt(wrapped) + delete state file",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"work_item_id":   prop("string", "Work item ID"),
			"repo":           prop("string", "Repository name"),
			"pr_title":       prop("string", "PR title (if PR doesn't exist yet)"),
			"pr_body":        prop("string", "PR body (if PR doesn't exist yet)"),
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

		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		// Execute wrap: push + PR (idempotent)
		prResult, err := coding.Wrap(ctx, sf, repo,
			strArg(args, "workspace_root"),
			strArg(args, "pr_title"),
			strArg(args, "pr_body"))
		if err != nil {
			return errResult(fmt.Errorf("wrap sequence (push+PR): %w", err))
		}

		// Complete attempt
		body := map[string]any{
			"status":         "wrapped",
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		completeResult, err := s.client.CompleteAttempt(ctx, sf.AttemptID, body)
		if err != nil {
			return errResult(fmt.Errorf("complete_attempt: %w", err))
		}

		// Delete state file (terminal status)
		_ = config.DeleteStateFile(wiID)

		return jsonResult(map[string]any{
			"ok":             true,
			"pr":             prResult,
			"complete_result": completeResult,
		})
	})
}
