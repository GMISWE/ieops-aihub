package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerConflictTools() {
	// pf_predict_conflicts
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_predict_conflicts",
		Description: "Predict resource lock conflicts for a set of declared_resources. Also returns will_unlock (which blocked wi would be unblocked).",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":       prop("string", "Work item ID (optional, for context)"),
			"declared_resources": prop("array", "Resources to check for conflicts"),
			"dry_run":            prop("boolean", "Dry run — do not mutate state"),
		}, []string{"declared_resources"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if _, ok := args["declared_resources"]; !ok {
			return errResult(fmt.Errorf("declared_resources is required"))
		}
		result, err := s.client.PredictConflicts(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
