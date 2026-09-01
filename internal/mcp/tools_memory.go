package mcp

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func (s *Server) registerMemoryTools() {
	// pf_remember
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_remember",
		Description: "Store a memory in aihub. type must use full name (e.g. experience.debug). Rejects methodology.* types — write spec/plan/review/execute/retro/wrap_summary via pf_save_artifact.",
		InputSchema: objectSchema(map[string]any{
			"project":              prop("string", "Project name"),
			"type":                 propEnum("string", "Memory type (full name e.g. experience.debug). methodology.* is not accepted here — use pf_save_artifact.", domain.PfRememberTypeEnum),
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
		if err := validatePfRememberArgs(args); err != nil {
			return errResult(err)
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
		Description: "Recall memories from aihub with optional semantic search. type is an ARRAY of type names, e.g. [\"experience.*\",\"rule.work\"] — one filter per entry; a '|' inside an entry is NOT a separator and is rejected. An entry ending in .* is a prefix wildcard. Any entry matching no memory comes back in unmatched_types, which distinguishes a wrong type name from a project that genuinely holds no such memory. An item with content_truncated=true holds only a prefix of its content (content_full_len = full length); call pf_get_memory(memory_id) for the rest.",
		InputSchema: objectSchema(map[string]any{
			"project": prop("string", "Project name"),
			"query":   prop("string", "Semantic search query"),
			// aihub#289: the shape is the whole point of this description. Three
			// SKILL.md templates taught type="a|b|c", nothing split it, and the
			// resulting empty set read as "no relevant memory". The model reads this
			// string, so this string has to state the contract.
			"type":                 prop("array", "Memory types to filter — an ARRAY of names, one per entry: [\"experience.*\",\"rule.work\"]. Entries ending in .* are prefix wildcards. Do NOT pack several types into one string with '|' — that is not a separator and is rejected with a 400."),
			"visibility":           prop("string", "Filter by visibility"),
			"work_item_id":         prop("string", "Filter by work item ID"),
			"top_k":                prop("string", "Max results"),
			"similarity_threshold": prop("number", "Min similarity score"),
			"min_strength":         prop("number", "Min memory strength (default 0.3)"),
			"include_archived":     prop("boolean", "Include archived memories (default false)"),
			"recency_weight":       prop("number", "Recency weight (default 0.3)"),
			// aihub#313. Wire text stays terse on purpose: this string is charged on
			// EVERY request of EVERY session, whether or not pf_recall is called,
			// which is the standing cost that closed aihub#279 as net negative.
			// Measured on the REAL tools/list payload (+50 for this property, -5 from
			// the Description reword above = +45 net), against a pf_recall tool object
			// of 415 tok and a 50-tool block of 11,634. Do NOT re-measure this with
			// `dump-mcp-schemas`: the contract JSON omits per-property descriptions
			// and reports only +18, understating the real cost 2.5x. One briefed
			// no-top_k call saves 5,239 tok x 47.3 re-billings = 247,800 tok, which
			// pays for 5,507 requests of this. The reasoning lives in Go comments,
			// which are charged to nobody.
			"fields": prop("string", "\"brief\" drops item bodies, keeping id + a 120-rune first line; pf_get_memory(id) for full text. Use for wide exploratory recalls."),
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
		// recall_algo: explicit arg wins, else env (POLYFORGE_RECALL_ALGO) lets a plugin
		// build opt into the opt③ L1 lexical-relevance recall path server-side without
		// changing the tool contract. Empty -> server default (recency).
		if algo := strArg(args, "recall_algo"); algo != "" {
			params.Set("recall_algo", algo)
		} else if algo := os.Getenv("POLYFORGE_RECALL_ALGO"); algo != "" {
			params.Set("recall_algo", algo)
		}
		result, err := s.client.Recall(ctx, params)
		if err != nil {
			return errResult(err)
		}
		// aihub#313: `fields` is deliberately NOT added to the params loop above.
		//
		// That loop is the hop aihub#282 is about. `similarity_threshold` is
		// published in this very InputSchema, is never forwarded here, and is never
		// parsed by handleRecall either, while being fully implemented in domain —
		// so nothing reaches it. Re-confirmed live while writing this: passing 0.99
		// and passing nothing return the same 20 items in the same order, min
		// similarity 0.154, differing only in effective_strength's 10th decimal
		// (decay between the two calls).
		//
		// `fields` cannot repeat that because it has no server hop to be dropped on.
		// The projection is a property of what the MCP process HANDS THE MODEL, and
		// this process is the last hop before the model, so the parameter is consumed
		// exactly where it is read: one hop, no wire contract, no REST parse, no
		// domain change. Adding it to the loop would be strictly worse — handleRecall
		// would ignore the query param (a third instance of aihub#282), and
		// routes_memory.go is aihub#309's declared file besides.
		//
		// This is the OPPOSITE error from aihub#287's 4_wrong_landing_point ("only
		// building the 4th hop"): there domain was complete and the MCP hop missing;
		// here the MCP hop is the only one that can implement the feature at all.
		return jsonResultCompact(slimRecallResultMode(result, strArg(args, "fields") == "brief"))
	})

	// pf_get_memory — aihub#269. pf_recall truncates content to 800 runes and
	// flags it with content_truncated/content_full_len; without a by-id read those
	// flags would tell the model its text is incomplete while giving it no way to
	// complete it. This is the tool half of that escape hatch.
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_get_memory",
		Description: "Fetch one memory by id with its FULL, untruncated content — the follow-up read for a pf_recall item whose content_truncated is true.",
		InputSchema: objectSchema(map[string]any{
			"memory_id": prop("string", "Memory ID (the `id` of a pf_recall item)"),
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
		result, err := s.client.GetMemory(ctx, memID)
		if err != nil {
			return errResult(err)
		}
		// compact, not indented: this payload is read by the model, and it is
		// reached precisely when the content is long (same rationale as pf_recall).
		return jsonResultCompact(result)
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

	// pf_update_memory (aihub#201)
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_memory",
		Description: "Update a memory (creates a new version and advances the latest_id cursor). Credentials injected from state file.",
		InputSchema: objectSchema(map[string]any{
			"memory_id":     prop("string", "Memory ID (any id in the lineage)"),
			"content":       prop("string", "New content (omit to keep current)"),
			"visibility":    prop("string", "New visibility (omit to keep current)"),
			"tags":          prop("array", "New tags (omit to keep current)"),
			"base_strength": prop("number", "New base strength (omit to keep current)"),
			"work_item_id":  prop("string", "Work item ID (for credential injection)"),
		}, []string{"memory_id", "work_item_id"}),
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
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
			"work_item_id":   wiID,
		}
		for _, k := range []string{"content", "visibility", "tags", "base_strength"} {
			if v, ok := args[k]; ok {
				body[k] = v
			}
		}
		result, err := s.client.UpdateMemory(ctx, memID, body)
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
			"type":                 propEnum("string", "Artifact type (must be one of the methodology.* kinds)", domain.MethodologyTypeEnum),
			"work_item_id":         prop("string", "Work item ID"),
			"content":              prop("string", "Artifact content (inline). Provide content OR path, not both."),
			"path":                 prop("string", "Local filesystem path to a UTF-8 markdown file to read as the artifact content (read by the local MCP process; must resolve within the workspace, <=1 MiB). Provide content OR path, not both."),
			"structured_payload":   prop("object", "Optional structured payload"),
			"visibility":           prop("string", "private|project|team|admin (default: project)"),
			"supersedes_memory_id": prop("string", "Memory ID this supersedes"),
			"html":                 prop("string", "Optional pre-rendered HTML stored verbatim in rendered_html (full standalone document or body fragment). Overrides server-side markdown auto-render; use for custom-styled artifact views served by the artifact HTML viewer."),
		}, []string{"type", "work_item_id"}),
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
		artifactContent, err := resolveArtifactContent(args, config.WorkspaceRoot())
		if err != nil {
			return errResult(err)
		}

		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"type":           artifactType,
			"work_item_id":   wiID,
			"content":        artifactContent,
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

	// pf_resolve_commit
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_resolve_commit",
		Description: "Resolve a spec/plan commit annotation with an AI reply (marks status=resolved, emits memory_commit_resolved).",
		InputSchema: objectSchema(map[string]any{
			"memory_id": prop("string", "Memory ID"),
			"commit_id": prop("string", "Commit annotation ID"),
			"reply":     prop("string", "AI reply explaining what was changed or why the annotation is resolved"),
		}, []string{"memory_id", "commit_id", "reply"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		commitID := strArg(args, "commit_id")
		if commitID == "" {
			return errResult(fmt.Errorf("commit_id is required"))
		}
		reply := strArg(args, "reply")
		if reply == "" {
			return errResult(fmt.Errorf("reply is required"))
		}
		result, err := s.client.ResolveCommit(ctx, memID, commitID, map[string]any{"reply": reply})
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}

// validatePfRememberArgs enforces pf_remember's contract before the HTTP call:
// required fields present, and methodology.* rejected — those are wi-bound,
// credentialed artifacts that must be written via pf_save_artifact (aihub#210).
func validatePfRememberArgs(args map[string]any) error {
	for _, f := range []string{"project", "type", "content", "visibility"} {
		if strArg(args, f) == "" {
			return fmt.Errorf("%s is required", f)
		}
	}
	if strings.HasPrefix(strArg(args, "type"), "methodology.") {
		return fmt.Errorf("pf_remember does not accept methodology.* types; save spec/plan/review/execute/retro/wrap_summary artifacts via pf_save_artifact instead")
	}
	return nil
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
