package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

func (s *Server) registerDependencyTools() {
	// pf_create_dependency
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_create_dependency",
		Description: "Create a dependency between two work items. Credentials injected from state file.",
		InputSchema: objectSchema(map[string]any{
			"blocked_wi_id":  prop("string", "Work item that is blocked"),
			"blocking_wi_id": prop("string", "Work item that is blocking"),
			"kind":           prop("string", "blocks|supersedes|related"),
			"work_item_id":   prop("string", "Work item ID for credential injection (must be claimed)"),
			"note":           prop("string", "Optional note"),
		}, []string{"blocked_wi_id", "blocking_wi_id", "kind", "work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		for _, f := range []string{"blocked_wi_id", "blocking_wi_id", "kind", "work_item_id"} {
			if strArg(args, f) == "" {
				return errResult(fmt.Errorf("%s is required", f))
			}
		}

		wiID := strArg(args, "work_item_id")
		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"blocked_wi_id":  strArg(args, "blocked_wi_id"),
			"blocking_wi_id": strArg(args, "blocking_wi_id"),
			"kind":           strArg(args, "kind"),
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		if note := strArg(args, "note"); note != "" {
			body["note"] = note
		}

		result, err := s.client.CreateDependency(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_remove_dependency
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_remove_dependency",
		Description: "Remove a dependency between two work items. Credentials injected from state file.",
		InputSchema: objectSchema(map[string]any{
			"blocked_wi_id":  prop("string", "Work item that is blocked"),
			"blocking_wi_id": prop("string", "Work item that is blocking"),
			"kind":           prop("string", "blocks|supersedes|related"),
			"work_item_id":   prop("string", "Work item ID for credential injection"),
		}, []string{"blocked_wi_id", "blocking_wi_id", "kind", "work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		for _, f := range []string{"blocked_wi_id", "blocking_wi_id", "kind", "work_item_id"} {
			if strArg(args, f) == "" {
				return errResult(fmt.Errorf("%s is required", f))
			}
		}

		wiID := strArg(args, "work_item_id")
		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"blocked_wi_id":  strArg(args, "blocked_wi_id"),
			"blocking_wi_id": strArg(args, "blocking_wi_id"),
			"kind":           strArg(args, "kind"),
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}

		result, err := s.client.RemoveDependency(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_list_dependencies
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_list_dependencies",
		Description: "List dependencies (blocking + blocked_by) for a work item. Cross-project items are folded if caller lacks viewer+ permission.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		result, err := s.client.ListDependencies(ctx, wiID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
