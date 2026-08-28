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
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// pf_list_work_items forwarding tables.
//
// Publishing a param in the InputSchema while forgetting it in the forwarding
// loop makes the schema state a contract the transport does not keep — the
// caller sends the param, nothing rejects it, and it is silently dropped
// (mem_1SJ12mCz). Keeping the two as named tables lets
// TestListWorkItemsToolForwardsEveryPublishedParam assert they agree, so the
// next param added cannot drift.
var (
	// listWorkItemsStringParams are forwarded verbatim when non-empty.
	listWorkItemsStringParams = []string{
		"project", "status", "kind", "milestone", "label", "user_id",
		"source", "since", "limit", "cursor", "sort", "order", "query",
	}
	// listWorkItemsBoolParams are forwarded as "true" when set.
	listWorkItemsBoolParams = []string{"ready_only", "include_step_state"}
)

// listWorkItemsSchema is the published input schema for pf_list_work_items,
// split out so the forwarding test can read the same value the tool registers.
func listWorkItemsSchema() json.RawMessage {
	return objectSchema(map[string]any{
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
		"query": prop("string", "Semantic search over goal+content (aihub#273): "+
			"embedding cosine when the server has a provider, ILIKE fallback otherwise. "+
			"Results are similarity-ordered; not combinable with sort/order/cursor."),
		"limit": prop("string", "Max items to return"),
		"cursor": prop("string", "Pagination cursor. Carries the value of the column named by `sort`, "+
			"so pass it back unchanged and do not mix cursors between different sort orders."),
		// The enums come from the server's enforced sets (aihub#224) rather than
		// being retyped here, so the published contract cannot drift from the
		// validator that rejects everything outside them.
		"sort": propEnum("string", fmt.Sprintf(
			"Sort column (default %s). %s returns ONLY closed items — a NULL close time has no position in that ordering.",
			domain.ListWorkItemsSortCreatedAt, domain.ListWorkItemsSortClosedAt),
			domain.ListWorkItemsSortValues()),
		"order": propEnum("string", fmt.Sprintf("Sort direction (default %s)", domain.ListWorkItemsOrderDesc),
			domain.ListWorkItemsOrderValues()),
	}, nil)
}

func (s *Server) registerLifecycleTools() {
	// pf_whoami
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_whoami",
		Description: "Return caller identity, project roles, and accessible projects from aihub",
		InputSchema: emptyObjectSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		result, err := s.client.WhoAmI(ctx)
		if err != nil {
			return errResult(err)
		}

		// Enrich with projects list: [{name, relation, role}]
		// relation: "owner" | "member" | "public"
		projectsResult, listErr := s.client.ListProjects(ctx, nil)
		if listErr == nil {
			callerID, _ := result["user_id"].(string)
			callerRole, _ := result["role"].(string)
			if itemsAny, ok := projectsResult["items"]; ok {
				if items, ok := itemsAny.([]any); ok {
					projectInfos := make([]map[string]any, 0, len(items))
					for _, item := range items {
						if proj, ok := item.(map[string]any); ok {
							name, _ := proj["name"].(string)
							ownerID, _ := proj["owner_user_id"].(string)
							membersRaw := proj["members"]

							relation := "public"
							memberRole := "viewer"

							if callerRole == "admin" || ownerID == callerID {
								relation = "owner"
								memberRole = "owner"
							} else if membersRaw != nil {
								// Parse members to find caller's role
								var membersBytes []byte
								switch m := membersRaw.(type) {
								case string:
									membersBytes = []byte(m)
								case []byte:
									membersBytes = m
								}
								if len(membersBytes) > 0 {
									var members []map[string]any
									if json.Unmarshal(membersBytes, &members) == nil {
										for _, mem := range members {
											uid, _ := mem["user_id"].(string)
											if uid == callerID {
												relation = "member"
												if r, ok := mem["role"].(string); ok {
													memberRole = r
												}
												break
											}
										}
									}
								}
							}

							projectInfos = append(projectInfos, map[string]any{
								"name":     name,
								"relation": relation,
								"role":     memberRole,
							})
						}
					}
					result["projects"] = projectInfos
				}
			}
		}
		// If listing projects fails, still return whoami without projects field (best-effort)

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
			"declared_resources":     declaredResourcesProp("Declared resource locks"),
			"parent_work_item_id":    prop("string", "Parent work item ID"),
			"source":                 prop("string", "Source reference"),
			"attrs":                  prop("object", "Additional attributes"),
			"blocked_by":             prop("array", "List of blocking work item IDs"),
			"content":                prop("string", "Background context for this wi (markdown, max 20000 chars)"),
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
			// Surface PROJECT_NOT_FOUND with a clear message
			if isAihubCode(err, "PROJECT_NOT_FOUND") {
				return errResult(fmt.Errorf("PROJECT_NOT_FOUND: project %q does not exist — create it first with pf_create_project", strArg(args, "project")))
			}
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_list_work_items
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_list_work_items",
		Description: "List work items with optional filters",
		InputSchema: listWorkItemsSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		params := url.Values{}
		for _, k := range listWorkItemsStringParams {
			setIfNonempty(params, k, strArg(args, k))
		}
		for _, k := range listWorkItemsBoolParams {
			if boolArg(args, k) {
				params.Set(k, "true")
			}
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
		Description: "Get a work item by ID or slug. Pass brief=true to omit the (potentially large) content field.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID or slug"),
			"brief":        prop("boolean", "Omit the content field from the response (default false)"),
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
		// brief=true drops the (potentially ~20K-char) content field; default
		// false preserves the current response shape for mixed-version safety (aihub#212).
		if boolArg(args, "brief") {
			delete(result, "content")
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
			"declared_resources":     declaredResourcesProp("Updated declared resources"),
			"resources_version":      prop("integer", "Compare-and-set guard for declared_resources: the resources_version you last read from this work item. The update is applied only if it still matches, otherwise it fails with 409 CONFLICT_CAS_FAILED and reports the current version. Omit to overwrite unconditionally. Every write of declared_resources increments this counter."),
			"attrs":                  prop("object", "Updated attributes"),
			"content":                prop("string", "Background context for this wi (markdown, max 20000 chars)"),
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
		// aihub#241: resources_version is an INT column and *int on the wire.
		// Coerce before building the body so a quoted "0" from a mixed-version
		// client becomes a JSON number here, instead of failing c.Bind two
		// layers away as an opaque 400 "invalid request body".
		if err := normalizeIntArg(args, "resources_version"); err != nil {
			return errResult(err)
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
			"requested_locks": requestedLocksProp("Resource locks to acquire"),
			"force_takeover":  prop("boolean", "Force takeover if already claimed"),
			"scenario_ref":    prop("string", "Git SHA of local scenario clone at claim time (optional)"),
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
		if sr := strArg(args, "scenario_ref"); sr != "" {
			body["scenario_ref"] = sr
		}

		result, err := s.client.ClaimWorkItem(ctx, wiID, body)
		if err != nil {
			// Don't delete the partial state file — let the user retry
			return errResult(fmt.Errorf("claim work item: %w", err))
		}

		// Build complete state file. Key by the canonical work_items.id the server
		// returns (the input wiID may be a slug like "aihub#1"); persisting the slug
		// makes later step/event/complete calls send a slug and hit FK / lookup
		// errors. (aihub#127)
		canonicalWIID := wiID
		if v, ok := result["id"].(string); ok && v != "" {
			canonicalWIID = v
		}
		sf := &config.StateFile{
			WIID:          canonicalWIID,
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

		// Persist the canonical-keyed state file and remove any orphan slug stub
		// the C6-2 pre-claim write left behind (see config.WriteClaimState). (aihub#141)
		if err := config.WriteClaimState(wiID, canonicalWIID, sf); err != nil {
			return errResult(fmt.Errorf("update state file: %w", err))
		}

		// Create git worktrees for each repo in the project (non-fatal).
		// Worktree path format: pf.<project>-<seq>/<repo>/
		// Branch name: polyforge/<ulid8>
		if sf.Project != "" {
			wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
			if wsRoot == "" {
				wsRoot = config.FindWorkspaceRoot()
			}
			if wsRoot != "" {
				effectiveCfg := resolveWorkspaceConfig(wsRoot, s.cfg)

				// Derive seq from slug (e.g. "marketplace#42" → "42").
				seq := ""
				if sf.Slug != "" {
					if idx := strings.LastIndex(sf.Slug, "#"); idx >= 0 {
						seq = sf.Slug[idx+1:]
					}
				}

				// Derive ulid8: last 8 chars of wi_id after stripping "wi_" prefix.
				// Used only for the branch name; directory name uses the readable slug.
				ulid8 := claimBranchULID8(canonicalWIID)

				if effectiveCfg != nil && seq != "" && ulid8 != "" {
					// Directory name uses readable format: pf.<project>-<seq>
					// (e.g. "pf.aihub-26") so developers can identify the wi at a glance.
					wtDir := fmt.Sprintf("pf.%s-%s", sf.Project, seq)
					branchName := "polyforge/" + ulid8
					mode := strArg(args, "mode")

					if proj, ok := effectiveCfg.Projects[sf.Project]; ok {
						worktrees := make(map[string]string)
						for _, repo := range proj.Repos {
							srcPath := filepath.Join(wsRoot, ".repo", repo.Name)
							wtPath := filepath.Join(wsRoot, wtDir, repo.Name)

							// If the worktree directory already exists, reuse it directly.
							if _, statErr := os.Stat(wtPath); statErr == nil {
								worktrees[repo.Name] = wtPath
								writeWorktreeExcludes(wtPath)
								continue
							}

							var cmd *exec.Cmd
							if mode == "resume" {
								// Branch already exists; just attach.
								cmd = exec.Command("git", "-C", srcPath, "worktree", "add", wtPath, branchName)
							} else {
								// Fresh claim: sync local clone from origin so the new branch
								// starts from the latest remote state, not a stale local HEAD.
								if out, err := exec.Command("git", "-C", srcPath, "fetch", "origin").CombinedOutput(); err != nil {
									fmt.Fprintf(os.Stderr, "polyforge: fetch origin for %s: %s\n", repo.Name, string(out))
								}
								// Create branch from origin/main (always fresh after fetch above).
								cmd = exec.Command("git", "-C", srcPath, "worktree", "add", "-b", branchName, wtPath, "origin/main")
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
									writeWorktreeExcludes(wtPath)
									continue
								}
							}

							if out, err := cmd.CombinedOutput(); err != nil {
								fmt.Fprintf(os.Stderr, "polyforge: worktree add for %s: %s\n", repo.Name, string(out))
							} else {
								worktrees[repo.Name] = wtPath
								writeWorktreeExcludes(wtPath)
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
		// Pass through other non-secret fields.
		//
		// aihub#238: `unrecognized_resources` MUST stay in this list. It is the only
		// signal that a declared resource is holding no lock, and reporting at claim
		// is the only remedy available on the stored-data path (rejecting there would
		// make historical mistyped work items unclaimable). Dropped here, the whole
		// remedy is inert and the caller sees exactly the pre-fix output.
		for _, k := range []string{"expires_at", "acquired_locks", "current_attempt_epoch", "slug", "project", "unrecognized_resources"} {
			if v, ok := result[k]; ok {
				safeResult[k] = v
			}
		}
		addWorktrees(safeResult, sf.Worktrees)
		return jsonResult(safeResult)
	})

	// pf_complete_attempt
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_complete_attempt",
		Description: "Complete the current run attempt (wrapped|failed|paused). Deletes state file for terminal statuses.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":         prop("string", "Work item ID (used to find state file)"),
			"status":               prop("string", "wrapped|failed|paused"),
			"force_terminate_step": prop("boolean", "Force terminate in-progress step"),
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

		sf, err := config.ResolveStateFile(wiID)
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

		// Surface the worktree paths from the state file we're about to delete,
		// for all statuses, so the caller doesn't need to have read the state
		// file itself before calling pf_complete_attempt (aihub#207).
		addWorktrees(result, sf.Worktrees)

		// Delete state file for terminal statuses; keep for paused. Delete by the
		// resolved canonical key (sf.WIID), and best-effort the passed key too, so
		// a slug-addressed completion cleans any stale slug-keyed stub. (aihub#141)
		if status == "wrapped" || status == "failed" {
			_ = config.DeleteStateFile(sf.WIID)
			if wiID != sf.WIID {
				_ = config.DeleteStateFile(wiID)
			}
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

	// pf_pause_attempt
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_pause_attempt",
		Description: "Pause the current attempt (releases file_scope locks acquired mid-attempt; git_branch/deploy_env locks are retained for resume; status → paused). State file is preserved for resume.",
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
		sf, err := config.ResolveStateFile(wiID)
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

	// pf_acquire_locks
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_acquire_locks",
		Description: "Acquire file_scope locks for the current running attempt from the work item's declared_resources (reconcile mid-attempt; blocks on conflict, never steals).",
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
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
		}
		result, err := s.client.AcquireLocks(ctx, sf.WIID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}

// writeWorktreeExcludes adds polyforge scratch-file patterns to a worktree's
// per-worktree git exclude file, so they never get accidentally staged.
// Best-effort and non-fatal: a worktree must still be usable if this fails.
func writeWorktreeExcludes(wtPath string) {
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	excludePath := strings.TrimSpace(string(out))
	if excludePath == "" {
		return
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(wtPath, excludePath)
	}

	patterns := []string{".superpowers/", ".pf_meta.json", ".pf_steps.json"}

	existing := ""
	if b, err := os.ReadFile(excludePath); err == nil {
		existing = string(b)
	}
	existingLines := strings.Split(existing, "\n")
	have := make(map[string]bool, len(existingLines))
	for _, l := range existingLines {
		have[strings.TrimSpace(l)] = true
	}

	var toAdd []string
	for _, p := range patterns {
		if !have[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		_, _ = f.WriteString("\n")
	}
	for _, p := range toAdd {
		_, _ = f.WriteString(p + "\n")
	}
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

// propEnum returns a property definition with an enum constraint (aihub#70).
func propEnum(typ, description string, enum []string) map[string]any {
	p := prop(typ, description)
	p["enum"] = enum
	return p
}

// declaredResourcesProp describes the declared_resources array *including its
// entry shape* (aihub#238).
//
// Before this, all three declared_resources schemas were a bare
// prop("array", ...) and the only written record of the real shape was
// pf-plan/SKILL.md Step 5 — invisible to every caller that does not go through
// pf-plan, even though the MCP schema is their sole contract. Combined with a
// server that silently skipped unrecognized types, a wrong guess cost nothing at
// the call and everything later.
//
// The enum is taken from domain.DeclaredResourceTypeList() rather than written
// out here, so the published contract cannot drift from the validator that
// enforces it.
func declaredResourcesProp(description string) map[string]any {
	p := prop("array", description+
		` — entries are {"type","uri","intent"}. NOTE: type takes a DECLARED type (repo/path/document/section/service/external_ref), NOT a lock type: file_scope/git_branch/worktree/tcp_port/deploy_env are resource_locks.resource_type values the server derives. A file path is type="path", uri="file:<repo-relative-path>". The path field is `+"`uri`"+`, not value/path/scope.`)
	p["items"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type": propEnum("string", "Declared resource type (NOT a lock type)", domain.DeclaredResourceTypeList()),
			"uri": prop("string",
				`Resource URI. Scheme by type: "file:<repo-relative-path>" for path/document/section, "repo:<repo-name>" for repo, "service:<name>" for service, a plain URL for external_ref.`),
			// Deliberately NOT an enum. The server does not validate `intent` at all;
			// only two values change behaviour ("read" suppresses the write lock and
			// downgrades path conflicts to info; "refactor" on a repo entry triggers
			// conflict rule 4). Meanwhile this repo's own fixtures use "exclusive" more
			// often than "write". Publishing a closed set would state a contract the
			// server does not keep — the exact failure this wi is about — so describe
			// the semantics instead and let unknown values through as inert. (aihub#238)
			"intent": prop("string",
				`Access intent. Not validated by the server; only two values carry behaviour: "read" (takes no write lock, and path overlaps report as info instead of soft_block) and "refactor" (on a repo entry, flags other refactors of the same repo). "write" is the conventional default; other values are accepted but inert.`),
			"base_branch": prop("string", "Base branch (repo entries only)"),
			"task_branch": prop("string", "Task branch (repo entries only); defaults to main for lock-key derivation"),
		},
		"required": []string{"type", "uri"},
	}
	return p
}

// requestedLocksProp describes the requested_locks array including its entry
// shape (aihub#238).
//
// This array had no item schema and was passed through to the server verbatim, so
// a caller had to guess domain.ResourceLockReq's {resource_type, resource_key}.
// Guessing the neighbouring declared_resources {type, value} shape produced an
// empty resource_type, tripped the resource_locks CHECK constraint, and returned
// 500 INTERNAL_ERROR with a bare SQLSTATE.
//
// Normal polyforge flow leaves this unset and lets the server derive locks from
// the work item's declared_resources.
func requestedLocksProp(description string) map[string]any {
	p := prop("array", description+
		` — usually OMIT this and let the server derive locks from the wi's declared_resources. Entries are {"resource_type","resource_key"} (NOT declared_resources' type/uri).`)
	p["items"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"resource_type": propEnum("string", "Lock type (NOT a declared_resources type)", domain.ResourceLockTypeList()),
			"resource_key": prop("string",
				`Lock key. file_scope is project-namespaced "<project>:<repo-relative-path>" (aihub#222); git_branch is "<repo>/<branch>"; deploy_env is the bare service name.`),
		},
		"required": []string{"resource_type", "resource_key"},
	}
	return p
}

// resolveWorkspaceConfig returns the workspace config to build claim worktrees
// from, reading .polyforge.yaml fresh out of wsRoot rather than trusting the
// snapshot the MCP process loaded at startup.
//
// startupCfg (the server's s.cfg) is read once when the process starts, so a repo
// added to a project mid-session was silently missing from every subsequent claim
// in that session — the worktree loop iterates this config, and users saw a claim
// come back short without any error (aihub#228).
//
// startupCfg remains the fallback for two cases: the fresh read failing, and the
// server having started without POLYFORGE_WORKSPACE_ROOT (cwd with no
// .polyforge.yaml ancestor), where s.cfg is nil but the caller has since resolved
// a usable wsRoot. Returns nil when neither source yields a config, which the
// caller treats as "skip worktree creation".
func resolveWorkspaceConfig(wsRoot string, startupCfg *config.Config) *config.Config {
	if cfg, err := config.Load(wsRoot); err == nil && cfg != nil {
		return cfg
	}
	return startupCfg
}

// claimBranchULID8 derives the 8-char branch suffix (polyforge/<ulid8>) from a
// work item's canonical id. Callers must pass the canonical wi id (wi_<ulid>),
// not a raw slug such as "aihub#225": for a slug the "wi_" prefix is absent, so
// the last-8-chars slice would leak slug characters into the branch name (e.g.
// "ihub#225"). Returns "" when the canonical id is shorter than 8 chars, which
// the caller treats as "skip worktree creation". (aihub#225)
func claimBranchULID8(canonicalWIID string) string {
	bare := strings.TrimPrefix(canonicalWIID, "wi_")
	if len(bare) < 8 {
		return ""
	}
	return bare[len(bare)-8:]
}
