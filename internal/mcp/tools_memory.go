package mcp

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func (s *Server) registerMemoryTools() {
	// pf_remember
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_remember",
		Description: "Store a memory in aihub. type must use full name (e.g. experience.debug). Rejects methodology.* types.",
		InputSchema: objectSchema(map[string]any{
			"project":              prop("string", "Project name"),
			"type":                 propEnum("string", "Memory type (select from enum; full name e.g. experience.debug)", domain.MemoryTypeEnum),
			"content":              prop("string", "Memory content"),
			"visibility":           prop("string", "private|project|team|admin"),
			"work_item_id":         prop("string", "Associated work item ID"),
			"base_strength":        prop("number", "Initial strength (0-1)"),
			"attrs":                prop("object", "Additional attributes"),
			"expires_at":           prop("string", "Expiry timestamp (RFC3339)"),
			"dedup_mode":           prop("string", "Deduplication mode"),
			"related_memory_ids":   prop("array", "Related memory IDs"),
			"context_snippet":      prop("string", "Context snippet for embedding"),
			"supersedes_memory_id": prop("string", "Memory ID this supersedes"),
		}, []string{"project", "type", "content", "visibility"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		for _, f := range []string{"project", "type", "content", "visibility"} {
			if strArg(args, f) == "" {
				return errResult(fmt.Errorf("%s is required", f))
			}
		}
		result, err := s.client.Remember(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_recall
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_recall",
		Description: "Recall memories from aihub with optional semantic search. type supports wildcards (experience.*).",
		InputSchema: objectSchema(map[string]any{
			"project":              prop("string", "Project name"),
			"query":                prop("string", "Semantic search query"),
			"type":                 prop("array", "Memory types to filter (supports wildcards)"),
			"visibility":           prop("string", "Filter by visibility"),
			"work_item_id":         prop("string", "Filter by work item ID"),
			"top_k":                prop("string", "Max results"),
			"similarity_threshold": prop("number", "Min similarity score"),
			"min_strength":         prop("number", "Min memory strength (default 0.3)"),
			"include_archived":     prop("boolean", "Include archived memories (default false)"),
			"recency_weight":       prop("number", "Recency weight (default 0.3)"),
		}, []string{"project"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if strArg(args, "project") == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		params := url.Values{}
		for _, k := range []string{"project", "query", "visibility", "work_item_id", "top_k", "cursor"} {
			setIfNonempty(params, k, strArg(args, k))
		}
		// min_strength and recency_weight are numbers — format as string
		if v := numArg(args, "min_strength"); v != 0 {
			params.Set("min_strength", fmt.Sprintf("%g", v))
		}
		if v := numArg(args, "recency_weight"); v != 0 {
			params.Set("recency_weight", fmt.Sprintf("%g", v))
		}
		// type is an array — join as comma-separated
		if types, ok := args["type"]; ok {
			switch t := types.(type) {
			case []any:
				strs := make([]string, 0, len(t))
				for _, v := range t {
					if s, ok := v.(string); ok {
						strs = append(strs, s)
					}
				}
				if len(strs) > 0 {
					params.Set("type", strings.Join(strs, ","))
				}
			case string:
				params.Set("type", t)
			}
		}
		if boolArg(args, "include_archived") {
			params.Set("include_archived", "true")
		}
		result, err := s.client.Recall(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_activate_memory
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_activate_memory",
		Description: "Activate a memory (increments activation count, updates stability)",
		InputSchema: objectSchema(map[string]any{
			"memory_id": prop("string", "Memory ID"),
		}, []string{"memory_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		result, err := s.client.ActivateMemory(ctx, memID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_reinforce_memory
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_reinforce_memory",
		Description: "Reinforce a memory with additional context (mutating — credentials from state file)",
		InputSchema: objectSchema(map[string]any{
			"memory_id":          prop("string", "Memory ID"),
			"additional_context": prop("string", "Additional context for the memory"),
			"strength_delta":     prop("number", "Strength delta"),
			"work_item_id":       prop("string", "Work item ID (for credential injection)"),
		}, []string{"memory_id", "additional_context", "work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required for credential injection"))
		}
		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}
		body := map[string]any{
			"additional_context": strArg(args, "additional_context"),
			"attempt_id":         sf.AttemptID,
			"claim_epoch":        sf.ClaimEpoch,
			"session_secret":     sf.SessionSecret,
		}
		if v, ok := args["strength_delta"]; ok {
			body["strength_delta"] = v
		}
		result, err := s.client.ReinforceMemory(ctx, memID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_redact_memory
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_redact_memory",
		Description: "Redact (soft-delete) a memory",
		InputSchema: objectSchema(map[string]any{
			"memory_id": prop("string", "Memory ID"),
			"reason":    prop("string", "Reason for redaction"),
		}, []string{"memory_id", "reason"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		body := map[string]any{"reason": strArg(args, "reason")}
		result, err := s.client.RedactMemory(ctx, memID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_save_artifact
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_save_artifact",
		Description: "Save a methodology artifact (methodology.spec|plan|review|execute|retro|wrap_summary). Credentials injected from state file.",
		InputSchema: objectSchema(map[string]any{
			"type":                 prop("string", "Artifact type (methodology.spec, methodology.plan, etc.)"),
			"work_item_id":         prop("string", "Work item ID"),
			"content":              prop("string", "Artifact content"),
			"structured_payload":   prop("object", "Optional structured payload"),
			"visibility":           prop("string", "private|project|team|admin (default: project)"),
			"supersedes_memory_id": prop("string", "Memory ID this supersedes"),
			"html":                 prop("string", "Optional pre-rendered HTML stored verbatim in rendered_html (full standalone document or body fragment). Overrides server-side markdown auto-render; use for custom-styled artifact views served by the artifact HTML viewer."),
		}, []string{"type", "work_item_id", "content"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		artifactType := strArg(args, "type")
		if artifactType == "" {
			return errResult(fmt.Errorf("type is required"))
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		if strArg(args, "content") == "" {
			return errResult(fmt.Errorf("content is required"))
		}

		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"type":           artifactType,
			"work_item_id":   wiID,
			"content":        strArg(args, "content"),
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		if v := strArg(args, "visibility"); v != "" {
			body["visibility"] = v
		}
		if v, ok := args["structured_payload"]; ok {
			body["structured_payload"] = v
		}
		if v := strArg(args, "supersedes_memory_id"); v != "" {
			body["supersedes_memory_id"] = v
		}
		if v := strArg(args, "html"); v != "" {
			body["rendered_html"] = v
		}

		result, err := s.client.Remember(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_adopt_artifact
	// B4: adopt/ignore/close are now pf_emit_event(type='artifact_action', payload={...})
	// These are convenience wrappers around pf_emit_event.
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_adopt_artifact",
		Description: "Mark an artifact as adopted (wrapper around pf_emit_event artifact_action)",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":  prop("string", "Work item ID"),
			"memory_id":     prop("string", "Artifact memory ID"),
			"artifact_type": prop("string", "Artifact type"),
		}, []string{"work_item_id", "memory_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return s.emitArtifactAction(ctx, req, "adopt")
	})

	// pf_close_artifact
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_close_artifact",
		Description: "Mark an artifact as closed (wrapper around pf_emit_event artifact_action)",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":  prop("string", "Work item ID"),
			"memory_id":     prop("string", "Artifact memory ID"),
			"artifact_type": prop("string", "Artifact type"),
		}, []string{"work_item_id", "memory_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return s.emitArtifactAction(ctx, req, "close")
	})

	// pf_ignore_artifact
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_ignore_artifact",
		Description: "Mark an artifact as ignored (wrapper around pf_emit_event artifact_action)",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":  prop("string", "Work item ID"),
			"memory_id":     prop("string", "Artifact memory ID"),
			"artifact_type": prop("string", "Artifact type"),
		}, []string{"work_item_id", "memory_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return s.emitArtifactAction(ctx, req, "ignore")
	})
}

// emitArtifactAction is the shared implementation for adopt/close/ignore artifact wrappers.
func (s *Server) emitArtifactAction(ctx context.Context, req *sdkmcp.CallToolRequest, action string) (*sdkmcp.CallToolResult, error) {
	args, err := parseArgs(req.Params.Arguments)
	if err != nil {
		return errResult(err)
	}
	wiID := strArg(args, "work_item_id")
	if wiID == "" {
		return errResult(fmt.Errorf("work_item_id is required"))
	}
	memID := strArg(args, "memory_id")
	if memID == "" {
		return errResult(fmt.Errorf("memory_id is required"))
	}

	sf, err := config.ReadStateFile(wiID)
	if err != nil {
		return errResult(fmt.Errorf("read state file: %w", err))
	}

	payload := map[string]any{
		"artifact_key":  memID,
		"artifact_type": strArg(args, "artifact_type"),
		"action":        action,
	}
	body := map[string]any{
		"work_item_id":   wiID,
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"event_type":     "artifact_action",
		"payload":        payload,
	}
	result, err := s.client.EmitEvent(ctx, body)
	if err != nil {
		return errResult(err)
	}
	return jsonResult(result)
}
