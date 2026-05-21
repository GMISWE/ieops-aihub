package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

func (s *Server) registerStepTools() {
	// pf_get_step
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_get_step",
		Description: "Get the current step state for a work item (step graph, current status, progress, previous steps)",
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
		result, err := s.client.GetStep(ctx, wiID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_step
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_step",
		Description: "Update the current step status. Credentials injected from state file. Server auto-emits step_started/step_completed/step_failed events.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":     prop("string", "Work item ID"),
			"step_id":          prop("string", "Step ID to update"),
			"status":           prop("string", "in_progress|completed|failed"),
			"step_attempt_id":  prop("string", "Step attempt ID (required for completed/failed)"),
			"artifact_summary": prop("string", "Brief summary of artifacts produced"),
			"error_type":       prop("string", "Error type (for failed status)"),
			"escalated":        prop("boolean", "Whether to escalate the failure"),
			"expected_version": prop("string", "Expected step state version (for optimistic locking)"),
		}, []string{"work_item_id", "step_id", "status", "expected_version"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		if strArg(args, "step_id") == "" {
			return errResult(fmt.Errorf("step_id is required"))
		}
		if strArg(args, "status") == "" {
			return errResult(fmt.Errorf("status is required"))
		}
		if strArg(args, "expected_version") == "" {
			return errResult(fmt.Errorf("expected_version is required"))
		}

		// Inject credentials from state file
		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"step_id":          strArg(args, "step_id"),
			"status":           strArg(args, "status"),
			"attempt_id":       sf.AttemptID,
			"claim_epoch":      sf.ClaimEpoch,
			"session_secret":   sf.SessionSecret,
			"expected_version": strArg(args, "expected_version"),
		}
		for _, k := range []string{"step_attempt_id", "artifact_summary", "error_type"} {
			if v := strArg(args, k); v != "" {
				body[k] = v
			}
		}
		if boolArg(args, "escalated") {
			body["escalated"] = true
		}

		result, err := s.client.UpdateStep(ctx, wiID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
