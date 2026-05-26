package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerUserTools() {
	// pf_list_users
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_list_users",
		Description: "List all users (admin only)",
		InputSchema: emptyObjectSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		result, err := s.client.ListUsers(ctx)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_create_user
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_create_user",
		Description: "Create a new user (admin only)",
		InputSchema: objectSchema(map[string]any{
			"display_name":   prop("string", "Human-readable display name"),
			"user_type":      prop("string", "User type: human or machine (default: human)"),
			"role":           prop("string", "Global role: writer or admin (default: writer)"),
			"email":          prop("string", "Email address (required for human users; auto-generated for machine users)"),
			"author_aliases": prop("array", "Git author aliases for this user"),
		}, []string{"display_name"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if strArg(args, "display_name") == "" {
			return errResult(fmt.Errorf("display_name is required"))
		}
		result, err := s.client.CreateUser(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_user
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_user",
		Description: "Update a user's display name or role (admin only)",
		InputSchema: objectSchema(map[string]any{
			"id":           prop("string", "User ID"),
			"display_name": prop("string", "Updated display name"),
			"role":         prop("string", "Updated global role: writer or admin"),
		}, []string{"id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		userID := strArg(args, "id")
		if userID == "" {
			return errResult(fmt.Errorf("id is required"))
		}
		body := make(map[string]any)
		for k, v := range args {
			if k != "id" {
				body[k] = v
			}
		}
		result, err := s.client.UpdateUser(ctx, userID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_create_api_key
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_create_api_key",
		Description: "Create an API key for a user (admin only). Returns the plain key once — store it securely.",
		InputSchema: objectSchema(map[string]any{
			"user_id":       prop("string", "User ID to create the key for"),
			"name":          prop("string", "Descriptive name for the API key"),
			"project_scope": prop("string", "Optional project scope (project name) to restrict the key"),
		}, []string{"user_id", "name"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		userID := strArg(args, "user_id")
		if userID == "" {
			return errResult(fmt.Errorf("user_id is required"))
		}
		if strArg(args, "name") == "" {
			return errResult(fmt.Errorf("name is required"))
		}
		body := make(map[string]any)
		for k, v := range args {
			if k != "user_id" {
				body[k] = v
			}
		}
		result, err := s.client.CreateAPIKey(ctx, userID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_revoke_api_key
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_revoke_api_key",
		Description: "Revoke an API key (admin only)",
		InputSchema: objectSchema(map[string]any{
			"user_id": prop("string", "User ID that owns the key"),
			"key_id":  prop("string", "API key ID to revoke"),
		}, []string{"user_id", "key_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		userID := strArg(args, "user_id")
		if userID == "" {
			return errResult(fmt.Errorf("user_id is required"))
		}
		keyID := strArg(args, "key_id")
		if keyID == "" {
			return errResult(fmt.Errorf("key_id is required"))
		}
		result, err := s.client.RevokeAPIKey(ctx, userID, keyID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
