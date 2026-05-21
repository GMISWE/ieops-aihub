package mcp

import (
	"context"
	"fmt"
	"net/url"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

func (s *Server) registerEventTools() {
	// pf_emit_event
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_emit_event",
		Description: "Emit an event on a work item. Mutating — credentials injected from state file.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID"),
			"event_type":   prop("string", "Event type (e.g. note, wi_reclassified, step_started)"),
			"payload":      prop("object", "Event payload (arbitrary JSON object)"),
			"pinned":       prop("boolean", "Pin this event (surfaces first in status/resume)"),
			"admin":        prop("boolean", "Admin event (requires role=admin)"),
		}, []string{"work_item_id", "event_type", "payload"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		eventType := strArg(args, "event_type")
		if eventType == "" {
			return errResult(fmt.Errorf("event_type is required"))
		}

		// Inject credentials from state file
		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file (wi must be claimed first): %w", err))
		}

		body := map[string]any{
			"work_item_id":   wiID,
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
			"event_type":     eventType,
			"payload":        args["payload"],
		}
		if boolArg(args, "pinned") {
			body["pinned"] = true
		}
		if boolArg(args, "admin") {
			body["admin"] = true
		}

		result, err := s.client.EmitEvent(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_read_events
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_read_events",
		Description: "Read events for a work item or project. work_item_id or project must be provided.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (or use project)"),
			"project":      prop("string", "Project name (or use work_item_id)"),
			"user_id":      prop("string", "Filter by user"),
			"types":        prop("array", "Filter by event types"),
			"since":        prop("string", "Since timestamp (RFC3339)"),
			"limit":        prop("string", "Max events to return"),
			"pinned_first": prop("boolean", "Return pinned events first"),
		}, nil),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		project := strArg(args, "project")
		if wiID == "" && project == "" {
			return errResult(fmt.Errorf("work_item_id or project is required"))
		}
		params := url.Values{}
		setIfNonempty(params, "work_item_id", wiID)
		setIfNonempty(params, "project", project)
		setIfNonempty(params, "user_id", strArg(args, "user_id"))
		setIfNonempty(params, "since", strArg(args, "since"))
		setIfNonempty(params, "limit", strArg(args, "limit"))
		if boolArg(args, "pinned_first") {
			params.Set("pinned_first", "true")
		}
		result, err := s.client.ReadEvents(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
