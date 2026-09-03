package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerProjectTools() {
	// pf_list_projects
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_list_projects",
		// aihub#260: each project carries members_version. It is the token
		// pf_update_project's compare-and-set consumes, and this is where a
		// caller reads it, so say so here — a guard nobody can find the input
		// for is a guard nobody passes.
		Description: "List all projects visible to the caller (public + member + owned). Each project includes members_version, the compare-and-set token to pass back to pf_update_project when changing members.",
		InputSchema: emptyObjectSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		result, err := s.client.ListProjects(ctx, nil)
		if err != nil {
			return errResult(err)
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
			"members":     prop("array", "REPLACES the whole member list: [{user_id, role}] where role is viewer|writer|maintainer. Anyone missing from the list you send loses access, so to add one person you must read the current list (pf_list_projects) and send it back with the addition. A write that would drop somebody you did not name in expected_removals is refused with 412 PROJECT_MEMBERS_UNDECLARED_REMOVAL, which lists them."),
			// aihub#260. The counter lives on the project row and is bumped by
			// Postgres on every members write, so it is a token for "the list I
			// read", not a timestamp — see buildProjectUpdate in
			// internal/domain/projects.go for why not updated_at.
			//
			// aihub#333 deleted this description's closing NOTE ("it does NOT
			// protect against sending a short list yourself"). The note was true
			// and was the honest thing to say while the gap was open; it is a
			// LIE now, and a stale warning is worse than none — a caller who
			// reads it either sends expected_removals it says nothing about, or
			// concludes the API cannot protect them and stops looking. The
			// replacement is not a reassurance, it is the parameter below.
			"members_version": prop("integer", "Compare-and-set guard for members: ALWAYS send the members_version you read alongside the list (pf_list_projects returns it). The update is applied only if it still matches, otherwise it fails with 409 CONFLICT_CAS_FAILED and reports the current version in details.current_members_version — reread and retry. Every write of members increments this counter. Leaving it out overwrites unconditionally: a concurrent writer's edit is then silently discarded and you still get a 200."),
			// aihub#333. The redundancy with `members` is the whole point, the
			// same way `git push --force-with-lease` restates what you think the
			// remote is: a truncated list and a deliberate removal are the same
			// bytes, so intent has to be stated somewhere the accident cannot
			// reach.
			"expected_removals": prop("array", "user_ids this members write is allowed to REMOVE. Send it whenever the list you send drops somebody — a same-size swap counts, changing only a role does not — because any removal you do not name here is refused with 412 PROJECT_MEMBERS_UNDECLARED_REMOVAL. Leave it out when you are only adding members or changing their roles. This is what tells the server \"I mean to remove these two\" apart from \"my list was short by two\"; members_version cannot, because a caller who truncates their own list holds a version that matches."),
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
		// aihub#260, mirroring aihub#241: members_version is an INT column and
		// *int on the wire. Coerce before building the body so a quoted "3"
		// from a mixed-version client becomes a JSON number here, instead of
		// failing c.Bind two layers away as an opaque 400 "invalid request
		// body" — which is indistinguishable from the server not knowing the
		// parameter at all.
		if err := normalizeIntArg(args, "members_version"); err != nil {
			return errResult(err)
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
