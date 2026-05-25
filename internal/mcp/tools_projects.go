package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerProjectTools() {
	// pf_list_projects
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_list_projects",
		Description: "List all projects visible to the caller (public + member + owned)",
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
			"scenario":    prop("string", "Scenario type: coding|writing|data (default: coding)"),
			"repos":       prop("array", "Repository list [{name,url,github_owner_repo,description}]"),
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
			"scenario":    prop("string", "Updated scenario type"),
			"repos":       prop("array", "Updated repository list"),
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
