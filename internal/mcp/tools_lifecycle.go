package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

func (s *Server) registerLifecycleTools() {
	// pf_whoami
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_whoami",
		Description: "Return caller identity and project roles from aihub",
		InputSchema: emptyObjectSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		result, err := s.client.WhoAmI(ctx)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_get_scenario_config
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_get_scenario_config",
		Description: "Read scenario phase config — available wi_types, classification_rules",
		InputSchema: objectSchema(map[string]any{
			"scenario": prop("string", "Scenario name (e.g. coding)"),
		}, []string{"scenario"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		scenario := strArg(args, "scenario")
		if scenario == "" {
			return errResult(fmt.Errorf("scenario is required"))
		}
		result, err := s.client.GetScenarioConfig(ctx, scenario)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_scenario_config
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_scenario_config",
		Description: "Update scenario phase config (CAS). Requires maintainer/admin role.",
		InputSchema: objectSchema(map[string]any{
			"scenario": prop("string", "Scenario name"),
			"content":  prop("object", "Updated config content"),
			"version":  prop("integer", "Current version for CAS (int)"),
		}, []string{"scenario", "content", "version"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		scenario := strArg(args, "scenario")
		if scenario == "" {
			return errResult(fmt.Errorf("scenario is required"))
		}
		// version may arrive as float64 (MCP JSON), int, or string — coerce to int so the
		// server's CAS check (UpdateScenarioConfigRequest.Version int) decodes correctly.
		var versionInt int
		switch v := args["version"].(type) {
		case float64:
			versionInt = int(v)
		case int:
			versionInt = v
		case int64:
			versionInt = int(v)
		case string:
			var n int
			fmt.Sscanf(v, "%d", &n) //nolint:errcheck — parse error -> n stays 0 -> caller validates
			versionInt = n
		}
		body := map[string]any{
			"content": args["content"],
			"version": versionInt,
		}
		result, err := s.client.UpdateScenarioConfig(ctx, scenario, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_create_work_item
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_create_work_item",
		Description: "Create a work item in the specified project",
		InputSchema: objectSchema(map[string]any{
			"project":                prop("string", "Project name"),
			"goal":                   prop("string", "Single-line goal ≤500 chars"),
			"scenario":               prop("string", "Scenario (default: coding)"),
			"priority":               prop("string", "low|normal|high|urgent"),
			"wi_type":                prop("string", "Work item type (fix_bug, feature, chore, etc.)"),
			"requires_human_session": prop("boolean", "Whether this wi requires a human session"),
			"milestone":              prop("string", "Milestone name"),
			"labels":                 prop("array", "Labels"),
			"declared_resources":     prop("array", "Declared resource locks"),
			"parent_work_item_id":    prop("string", "Parent work item ID"),
			"source":                 prop("string", "Source reference"),
			"attrs":                  prop("object", "Additional attributes"),
			"blocked_by":             prop("array", "List of blocking work item IDs"),
			"force_create":           prop("boolean", "Force create bypassing duplicate check"),
			"force_reason":           prop("string", "Reason for force create"),
		}, []string{"project", "goal"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if strArg(args, "project") == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		if strArg(args, "goal") == "" {
			return errResult(fmt.Errorf("goal is required"))
		}
		// Auto-supply force_reason when force_create=true (server requires >=10 chars)
		if boolArg(args, "force_create") && strArg(args, "force_reason") == "" {
			args["force_reason"] = "force_create=true via MCP (admin bypass dedup check)"
		}
		result, err := s.client.CreateWorkItem(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_list_work_items
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_list_work_items",
		Description: "List work items with optional filters",
		InputSchema: objectSchema(map[string]any{
			"project":            prop("string", "Project name"),
			"status":             prop("string", "Filter by status"),
			"kind":               prop("string", "Filter by kind"),
			"milestone":          prop("string", "Filter by milestone"),
			"label":              prop("string", "Filter by label"),
			"user_id":            prop("string", "Filter by user ID"),
			"source":             prop("string", "Filter by source"),
			"ready_only":         prop("boolean", "Only return ready items"),
			"include_step_state": prop("boolean", "Include step state"),
			"since":              prop("string", "Since timestamp (RFC3339)"),
			"limit":              prop("string", "Max items to return"),
			"cursor":             prop("string", "Pagination cursor"),
		}, nil),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		params := url.Values{}
		for _, k := range []string{"project", "status", "kind", "milestone", "label", "user_id", "source", "since", "limit", "cursor"} {
			setIfNonempty(params, k, strArg(args, k))
		}
		if boolArg(args, "ready_only") {
			params.Set("ready_only", "true")
		}
		if boolArg(args, "include_step_state") {
			params.Set("include_step_state", "true")
		}
		result, err := s.client.ListWorkItems(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_get_work_item
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_get_work_item",
		Description: "Get a work item by ID or slug",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		result, err := s.client.GetWorkItem(ctx, id)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_work_item
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_work_item",
		Description: "Update a work item (goal, wi_type, priority, labels, etc.)",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":           prop("string", "Work item ID or slug"),
			"goal":                   prop("string", "Updated goal (status must be queued or paused)"),
			"goal_change_reason":     prop("string", "Reason for goal change (required with goal)"),
			"kind":                   prop("string", "Updated kind"),
			"priority":               prop("string", "Updated priority"),
			"milestone":              prop("string", "Updated milestone"),
			"wi_type":                prop("string", "Updated wi_type"),
			"requires_human_session": prop("boolean", "Updated requires_human_session"),
			"reclassify_reason":      prop("string", "Reason for wi_type change (min 10 chars)"),
			"labels":                 prop("array", "Updated labels"),
			"declared_resources":     prop("array", "Updated declared resources"),
			"resources_version":      prop("string", "Current resources version for CAS"),
			"attrs":                  prop("object", "Updated attributes"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		// Remove work_item_id from body
		body := make(map[string]any)
		for k, v := range args {
			if k != "work_item_id" {
				body[k] = v
			}
		}
		result, err := s.client.UpdateWorkItem(ctx, id, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_claim_work_item
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_claim_work_item",
		Description: "Claim a work item — creates a new run_attempt with typed locks. Writes state file with credentials.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":    prop("string", "Work item ID or slug"),
			"idempotency_key": prop("string", "Idempotency key for DB dedup"),
			"mode":            prop("string", "fresh|resume (default: fresh)"),
			"requested_locks": prop("array", "Resource locks to acquire"),
			"force_takeover":  prop("boolean", "Force takeover if already claimed"),
		}, []string{"work_item_id", "idempotency_key"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		idemKey := strArg(args, "idempotency_key")
		if idemKey == "" {
			return errResult(fmt.Errorf("idempotency_key is required"))
		}

		// C6-2: Generate session_secret BEFORE calling aihub
		sessionSecret, err := generateSessionSecret()
		if err != nil {
			return errResult(fmt.Errorf("generate session_secret: %w", err))
		}

		// Write partial state file first (C6-2 protocol)
		partial := &config.StateFile{
			WIID:          wiID,
			IdemKey:       idemKey,
			SessionSecret: sessionSecret,
			Claimed:       false,
		}
		if err := config.WriteStateFile(partial); err != nil {
			return errResult(fmt.Errorf("write state file: %w", err))
		}

		// Build claim body — server requires session_info.machine_id (FnClaimWorkItem 400 guard).
		machineID := os.Getenv("POLYFORGE_MACHINE_ID")
		if machineID == "" {
			h, _ := os.Hostname()
			machineID = h
		}
		body := map[string]any{
			"idempotency_key": idemKey,
			"session_info": map[string]any{
				"session_secret": sessionSecret,
				"machine_id":     machineID,
			},
		}
		if mode := strArg(args, "mode"); mode != "" {
			body["mode"] = mode
		}
		if v, ok := args["requested_locks"]; ok {
			body["requested_locks"] = v
		}
		if boolArg(args, "force_takeover") {
			body["force_takeover"] = true
		}

		result, err := s.client.ClaimWorkItem(ctx, wiID, body)
		if err != nil {
			// Don't delete the partial state file — let the user retry
			return errResult(fmt.Errorf("claim work item: %w", err))
		}

		// Build complete state file
		sf := &config.StateFile{
			WIID:          wiID,
			IdemKey:       idemKey,
			SessionSecret: sessionSecret,
			Claimed:       true,
			ClaimedAt:     time.Now().UTC().Format(time.RFC3339),
		}
		if v, ok := result["attempt_id"].(string); ok {
			sf.AttemptID = v
		}
		if v, ok := result["claim_epoch"]; ok {
			switch ce := v.(type) {
			case float64:
				sf.ClaimEpoch = int64(ce)
			case int64:
				sf.ClaimEpoch = ce
			}
		}
		if v, ok := result["slug"].(string); ok {
			sf.Slug = v
		}
		if v, ok := result["project"].(string); ok {
			sf.Project = v
		}

		if err := config.WriteStateFile(sf); err != nil {
			return errResult(fmt.Errorf("update state file: %w", err))
		}

		// Create git worktrees for each repo in the project (non-fatal).
		// Worktree path format: pf.<seq>.<ulid8>/<repo>/
		// Branch name: polyforge/<ulid8>
		if s.cfg != nil && sf.Project != "" {
			wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
			if wsRoot == "" {
				wsRoot, _ = os.Getwd()
			}
			if wsRoot != "" {
				// Derive seq from slug (e.g. "marketplace#42" → "42").
				seq := ""
				if sf.Slug != "" {
					if idx := strings.LastIndex(sf.Slug, "#"); idx >= 0 {
						seq = sf.Slug[idx+1:]
					}
				}

				// Derive ulid8: last 8 chars of wi_id after stripping "wi_" prefix.
				ulid8 := ""
				bare := strings.TrimPrefix(wiID, "wi_")
				if len(bare) >= 8 {
					ulid8 = bare[len(bare)-8:]
				}

				if seq != "" && ulid8 != "" {
					wtDir := fmt.Sprintf("pf.%s.%s", seq, ulid8)
					branchName := "polyforge/" + ulid8
					mode := strArg(args, "mode")

					if proj, ok := s.cfg.Projects[sf.Project]; ok {
						worktrees := make(map[string]string)
						for _, repo := range proj.Repos {
							srcPath := filepath.Join(wsRoot, ".repo", repo.Name)
							wtPath := filepath.Join(wsRoot, wtDir, repo.Name)

							var cmd *exec.Cmd
							if mode == "resume" {
								// Branch already exists; just attach.
								cmd = exec.Command("git", "-C", srcPath, "worktree", "add", wtPath, branchName)
							} else {
								// Fresh claim: create branch.
								cmd = exec.Command("git", "-C", srcPath, "worktree", "add", "-b", branchName, wtPath)
								if out, err := cmd.CombinedOutput(); err != nil {
									// Branch may already exist (idempotent retry) — fall back to attach.
									if strings.Contains(string(out), "already exists") || strings.Contains(string(out), "already checked out") {
										cmd = exec.Command("git", "-C", srcPath, "worktree", "add", wtPath, branchName)
									} else {
										// Unexpected error; skip this repo.
										fmt.Fprintf(os.Stderr, "polyforge: worktree add for %s: %s\n", repo.Name, string(out))
										continue
									}
								} else {
									// Success on first try; record and continue.
									worktrees[repo.Name] = wtPath
									continue
								}
							}

							if out, err := cmd.CombinedOutput(); err != nil {
								fmt.Fprintf(os.Stderr, "polyforge: worktree add for %s: %s\n", repo.Name, string(out))
							} else {
								worktrees[repo.Name] = wtPath
							}
						}

						if len(worktrees) > 0 {
							sf.Worktrees = worktrees
							// Best-effort: update state file with worktree paths.
							_ = config.WriteStateFile(sf)
						}
					}
				}
			}
		}

		// Don't return session_secret to LLM (decision A)
		// Return attempt_id and claim_epoch only
		safeResult := map[string]any{
			"attempt_id":  sf.AttemptID,
			"claim_epoch": sf.ClaimEpoch,
			"ok":          true,
		}
		// Pass through other non-secret fields
		for _, k := range []string{"expires_at", "acquired_locks", "current_attempt_epoch", "slug", "project"} {
			if v, ok := result[k]; ok {
				safeResult[k] = v
			}
		}
		if len(sf.Worktrees) > 0 {
			safeResult["worktrees"] = sf.Worktrees
		}
		return jsonResult(safeResult)
	})

	// pf_complete_attempt
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_complete_attempt",
		Description: "Complete the current run attempt (wrapped|failed|paused). Deletes state file for terminal statuses.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":           prop("string", "Work item ID (used to find state file)"),
			"status":                 prop("string", "wrapped|failed|paused"),
			"force_terminate_step":   prop("boolean", "Force terminate in-progress step"),
		}, []string{"work_item_id", "status"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		status := strArg(args, "status")
		if status == "" {
			return errResult(fmt.Errorf("status is required"))
		}

		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		body := map[string]any{
			"status":         status,
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		if boolArg(args, "force_terminate_step") {
			body["force_terminate_step"] = true
		}

		result, err := s.client.CompleteAttempt(ctx, wiID, body)
		if err != nil {
			return errResult(err)
		}

		// Delete state file for terminal statuses; keep for paused
		if status == "wrapped" || status == "failed" {
			_ = config.DeleteStateFile(wiID)
		}

		return jsonResult(result)
	})

	// pf_force_takeover
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_force_takeover",
		Description: "Force-take ownership of a work item from another agent",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
			"reason":       prop("string", "Reason for force takeover"),
		}, []string{"work_item_id", "reason"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}

		// Generate new session_secret for the forced takeover
		sessionSecret, err := generateSessionSecret()
		if err != nil {
			return errResult(fmt.Errorf("generate session_secret: %w", err))
		}

		machineID := os.Getenv("POLYFORGE_MACHINE_ID")
		if machineID == "" {
			h, _ := os.Hostname()
			machineID = h
		}
		body := map[string]any{
			"reason": strArg(args, "reason"),
			"session_info": map[string]any{
				"session_secret": sessionSecret,
				"machine_id":     machineID,
			},
		}
		result, err := s.client.ForceTakeover(ctx, id, body)
		if err != nil {
			return errResult(err)
		}

		// Write state file with new credentials
		sf := &config.StateFile{
			WIID:          id,
			SessionSecret: sessionSecret,
			Claimed:       true,
			ClaimedAt:     time.Now().UTC().Format(time.RFC3339),
		}
		if v, ok := result["new_attempt_id"].(string); ok {
			sf.AttemptID = v
		}
		if v, ok := result["new_claim_epoch"]; ok {
			switch ce := v.(type) {
			case float64:
				sf.ClaimEpoch = int64(ce)
			case int64:
				sf.ClaimEpoch = ce
			}
		}
		_ = config.WriteStateFile(sf)

		// Return result without session_secret.
		// v1.21 ownership-only: no expires_at; do not surface that field.
		safeResult := map[string]any{
			"prior_attempt_id":    result["prior_attempt_id"],
			"prior_actor_display": result["prior_actor_display"],
			"new_attempt_id":      sf.AttemptID,
			"new_claim_epoch":     sf.ClaimEpoch,
			"ok":                  result["ok"],
		}
		return jsonResult(safeResult)
	})

	// pf_get_ready_queue
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_get_ready_queue",
		Description: "Get the LCRS (6-section) ready queue for a project. For Orchestrator use.",
		InputSchema: objectSchema(map[string]any{
			"project":         prop("string", "Project name"),
			"max":             prop("string", "Max items in ready section"),
			"non_conflicting": prop("boolean", "Only return non-conflicting items"),
		}, []string{"project"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		project := strArg(args, "project")
		if project == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		params := url.Values{}
		params.Set("project", project)
		setIfNonempty(params, "max", strArg(args, "max"))
		if boolArg(args, "non_conflicting") {
			params.Set("non_conflicting", "true")
		}
		result, err := s.client.GetReadyQueue(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_cancel_work_item
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_cancel_work_item",
		Description: "Cancel a work item",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
			"reason":       prop("string", "Cancellation reason"),
		}, []string{"work_item_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		id := strArg(args, "work_item_id")
		if id == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		body := map[string]any{}
		if reason := strArg(args, "reason"); reason != "" {
			body["reason"] = reason
		}
		result, err := s.client.CancelWorkItem(ctx, id, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_renew_lease
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_renew_lease",
		Description: "Renew the lease on an active attempt",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (used to find state file)"),
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
		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		result, err := s.client.RenewLease(ctx, sf.WIID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_pause_attempt
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_pause_attempt",
		Description: "Pause the current attempt (releases lease + locks, status → paused). State file is preserved for resume.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (used to find state file)"),
			"pause_reason": prop("string", "Optional reason for pausing"),
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
		sf, err := config.ReadStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		if reason := strArg(args, "pause_reason"); reason != "" {
			body["pause_reason"] = reason
		}
		result, err := s.client.PauseAttempt(ctx, sf.WIID, body)
		if err != nil {
			return errResult(err)
		}
		// State file is kept for paused status (C5-3: resume needs credentials)
		return jsonResult(result)
	})
}

// generateSessionSecret generates a 64-hex random session secret.
func generateSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// emptyObjectSchema returns a JSON schema for an empty object (no required fields).
func emptyObjectSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// objectSchema returns a JSON schema for an object with the given properties.
func objectSchema(props map[string]any, required []string) json.RawMessage {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	b, _ := json.Marshal(schema)
	return json.RawMessage(b)
}

// prop returns a simple property definition.
func prop(typ, description string) map[string]any {
	return map[string]any{
		"type":        typ,
		"description": description,
	}
}
