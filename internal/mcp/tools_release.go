package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

func (s *Server) registerReleaseTools() {
	// pf_cut_alpha
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_cut_alpha",
		Description: "Cut the next alpha release for a project. Admin/release-manager only.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root": prop("string", "Workspace root path"),
			"project":        prop("string", "Project name"),
			"repos":          prop("array", "Repos to tag"),
			"base_tag":       prop("string", "Base tag for changelog comparison"),
			"work_item_id":   prop("string", "Work item ID (for credential injection)"),
		}, []string{"project", "repos", "work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		if strArg(args, "project") == "" {
			return errResult(fmt.Errorf("project is required"))
		}

		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"workspace_root": strArg(args, "workspace_root"),
			"project":        strArg(args, "project"),
			"repos":          args["repos"],
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		if v := strArg(args, "base_tag"); v != "" {
			body["base_tag"] = v
		}

		result, err := s.client.CutAlpha(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_promote
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_promote",
		Description: "Promote an alpha release to stable. Admin/release-manager only.",
		InputSchema: objectSchema(map[string]any{
			"workspace_root":   prop("string", "Workspace root path"),
			"source_alpha_tag": prop("string", "Alpha tag to promote"),
			"new_stable_tag":   prop("string", "New stable tag name"),
			"project":          prop("string", "Project name"),
			"work_item_id":     prop("string", "Work item ID (for credential injection)"),
		}, []string{"source_alpha_tag", "new_stable_tag", "project", "work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}

		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"workspace_root":   strArg(args, "workspace_root"),
			"source_alpha_tag": strArg(args, "source_alpha_tag"),
			"new_stable_tag":   strArg(args, "new_stable_tag"),
			"project":          strArg(args, "project"),
			"attempt_id":       sf.AttemptID,
			"claim_epoch":      sf.ClaimEpoch,
			"session_secret":   sf.SessionSecret,
		}

		result, err := s.client.Promote(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
