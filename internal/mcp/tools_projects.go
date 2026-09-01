package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// listProjectsSchema is the published input schema for pf_list_projects, split
// out so the schema test reads the same value the tool registers rather than
// retyping it (the drift tools_list_wi_schema_test.go documents).
//
// The description is deliberately terse, and that is a measured decision rather
// than a style one. A tool's name+description+InputSchema is charged on every
// request that has this MCP server loaded, whereas the saving below is charged
// only on the calls that pass the flag — and pf_list_projects is one of the
// least-called tools in the set. Measured 2026-09-01 (cl100k_base) on the
// wire-shaped {name, description, inputSchema} object: 41 tok before, 116 tok
// after, so +75 tok on every request against 11,454 tok saved per flagged call.
// This change therefore only pays for itself at roughly one flagged call per
// 152 requests at full price — around one per 1,500 once the tool block is
// prompt-cached. That is a bar this tool's call frequency may not clear, so keep
// the description short and put maintainer-facing reasoning in Go comments here,
// which are charged to nobody.
func listProjectsSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"include_repos": prop("boolean", "Include each project's full `repos` metadata "+
			"(default true — the pre-existing response shape). false swaps the array for a "+
			"`repo_count` integer and drops ~85% of the response's tokens."),
	}, nil)
}

// listProjectsIncludeRepos reads pf_list_projects' include_repos argument.
//
// Absent means TRUE. That is the opposite default from pf_get_work_item's
// `brief` (tools_lifecycle.go), and for the same stated reason: the default has
// to preserve the current response shape so a new server cannot break an old
// client (aihub#212 mixed-version safety). Here that is not a style preference
// — `polyforge init` calls ListProjects bare (internal/cli/init.go) and reads
// `repos` to decide what to clone and whether its repo map is stale, so a
// default of false would silently stop it cloning.
//
// An unreadable value is rejected rather than defaulted, for the reason
// parseBoolArg's own comment gives: defaulting `include_repos: "false"` to the
// zero value would be indistinguishable from not sending it at all.
func listProjectsIncludeRepos(args map[string]any) (bool, error) {
	value, present, ok := parseBoolArg(args, "include_repos")
	if !present {
		return true, nil
	}
	if !ok {
		return false, fmt.Errorf("include_repos must be a boolean (true/false, \"true\"/\"false\", or 1/0), got %#v", args["include_repos"])
	}
	return value, nil
}

// replaceReposWithCount rewrites a GET /v1/projects response in place, dropping
// each project's `repos` array and putting a `repo_count` integer in its place.
//
// Measured 2026-09-01 against the live 10-project / 44-repo production response,
// through this tool and counted on the CallToolResult text that actually reaches
// a model (tiktoken cl100k_base):
//
//	include_repos absent/true   53,039 B   13,537 tok
//	include_repos=false          6,010 B    2,083 tok   −88.7% bytes, −84.6% tokens
//
// Report the token column, not the byte column: `repos` is ASCII-dense English
// prose, so it packs ~3.9 B/tok against ~2.9 B/tok for what remains, and a byte
// count overstates the win by four points. Both numbers move with the data —
// they are a scale, not a contract, and nothing goes red when they drift.
//
// `repo_count` is not decoration. pf-project/SKILL.md renders a repos count
// column for the project list, so dropping `repos` without a scalar to count
// would trade a token win for a regression in the one skill that would pass
// include_repos=false. It costs 152 B / 50 tok of the saving above.
//
// 🔴 Scope discipline: this touches `repos` and nothing else. `members` and
// `owner_user_id` stay, because pf_whoami enriches its result by calling the
// same client.ListProjects and reading them to derive each project's
// relation/role (tools_lifecycle.go). That enrichment never reaches an LLM
// context, so it has no token problem to solve, and weakening it would be a
// silent privilege downgrade rather than an error. This function is deliberately
// applied to the tool handler's own copy of the response, never inside
// client.ListProjects, so the whoami path cannot see it at all.
//
// A project whose `repos` is JSON null (json.RawMessage with no omitempty, so
// the key is always on the wire) counts as 0 — the same answer a caller would
// get from len() on the empty array.
func replaceReposWithCount(result map[string]any) {
	items, ok := result["items"].([]any)
	if !ok {
		return
	}
	for _, item := range items {
		proj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		raw, present := proj["repos"]
		if !present {
			continue
		}
		count := 0
		if list, ok := raw.([]any); ok {
			count = len(list)
		}
		delete(proj, "repos")
		proj["repo_count"] = count
	}
}

func (s *Server) registerProjectTools() {
	// pf_list_projects
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_list_projects",
		Description: "List all projects visible to the caller (public + member + owned). " +
			"Pass include_repos=false for a repo_count integer instead of full repos metadata.",
		InputSchema: listProjectsSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		includeRepos, err := listProjectsIncludeRepos(args)
		if err != nil {
			return errResult(err)
		}
		result, err := s.client.ListProjects(ctx, nil)
		if err != nil {
			return errResult(err)
		}
		if !includeRepos {
			replaceReposWithCount(result)
		}
		return jsonResult(result)
	})

	// pf_create_project
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_create_project",
		Description: "Create a new project",
		InputSchema: objectSchema(map[string]any{
			"name":        prop("string", "Project name (lowercase letters/digits/dash/underscore, 1-40 chars)"),
			"description": prop("string", "Optional description"),
			"visible":     prop("boolean", "Whether the project is publicly visible (default: true)"),
			"scenario":    prop("string", "Scenario repo URL (e.g. git@github.com:GMISWE/polyforge-coding.git)"),
			"repos":       prop("array", "Repository list. Each: {name, url, github_owner_repo?, description?, and an optional all-or-nothing structured block: positioning(string), tech_stack([string]), main_modules([{path,role}]), change_scenarios([string]), generated_at(RFC3339), generated_commit(string)}. If any structured field is set, all four content fields are required (English)."),
		}, []string{"name"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if strArg(args, "name") == "" {
			return errResult(fmt.Errorf("name is required"))
		}
		result, err := s.client.CreateProject(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_project
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_project",
		Description: "Update a project (owner or admin only)",
		InputSchema: objectSchema(map[string]any{
			"name":        prop("string", "Project name"),
			"description": prop("string", "Updated description"),
			"visible":     prop("boolean", "Updated visibility"),
			"scenario":    prop("string", "Updated scenario repo URL (e.g. git@github.com:GMISWE/polyforge-coding.git)"),
			"repos":       prop("array", "Updated repository list. Each: {name, url, github_owner_repo?, description?, and an optional all-or-nothing structured block: positioning(string), tech_stack([string]), main_modules([{path,role}]), change_scenarios([string]), generated_at(RFC3339), generated_commit(string)}. If any structured field is set, all four content fields are required (English)."),
			"members":     prop("array", "Replace member list: [{user_id, role}] where role is viewer|writer|maintainer"),
		}, []string{"name"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		name := strArg(args, "name")
		if name == "" {
			return errResult(fmt.Errorf("name is required"))
		}
		body := make(map[string]any)
		for k, v := range args {
			if k != "name" {
				body[k] = v
			}
		}
		result, err := s.client.UpdateProject(ctx, name, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_rotate_identifier
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_rotate_identifier",
		Description: "Rotate the project identifier (bcrypt token). Returns plain once — store it securely. Owner/admin only.",
		InputSchema: objectSchema(map[string]any{
			"name": prop("string", "Project name"),
		}, []string{"name"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		name := strArg(args, "name")
		if name == "" {
			return errResult(fmt.Errorf("name is required"))
		}
		// NOTE: result contains plain token — do NOT log it
		result, err := s.client.RotateProjectIdentifier(ctx, name)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
