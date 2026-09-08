package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func (s *Server) registerUserTools() {
	// pf_list_users
	s.addTool(&sdkmcp.Tool{
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
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_create_user",
		Description: "Create a new user (admin only)",
		InputSchema: objectSchema(map[string]any{
			"display_name": prop("string", "Human-readable display name"),
			// aihub#463. user_type and role are two-value CHECK constraints on
			// the users table published as prose — "User type: human or machine"
			// — which is a sentence about a set rather than the set. The values
			// come from domain, the package that now refuses anything outside
			// them, so the published set and the accepted set are ONE value and
			// cannot drift into agreeing only today.
			//
			// ⚠️ What this enum does and does not do, stated because aihub#396
			// recorded the stronger claim: it constrains the CLIENT — an LLM
			// reading tools/list, and any client that validates before sending —
			// and NOT this process. The go-sdk's applySchema/resolved.Validate
			// runs in toolForErr, on the generic AddTool[In, Out] path; polyforge
			// registers through the untyped method (*mcp.Server).AddTool (see
			// addTool in server.go) and Server.callTool hands that straight to
			// the handler with no schema step. The hard refusal is
			// domain.ValidateUserType / ValidateUserGlobalRole in
			// handleCreateUser, which answers 400 with the legal values; this
			// enum is how a caller learns them before spending a round trip.
			"user_type": propEnum("string", "User type (default: human). A machine user's email is generated, not supplied.",
				domain.UserTypeList()),
			// "Global" distinguishes this from a project MEMBER role
			// (viewer|writer|maintainer, set through pf_update_project) — two
			// different vocabularies both spelled `role`, and neither contains
			// the other (aihub#411 §6.2 T2-17).
			"role": propEnum("string", "Global role across every project, NOT a project member role (default: writer)",
				domain.UserGlobalRoleList()),
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
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_update_user",
		Description: "Update a user's display name, role or git author aliases (admin only)",
		InputSchema: objectSchema(map[string]any{
			"id":           prop("string", "User ID"),
			"display_name": prop("string", "Updated display name"),
			"role":         prop("string", "Updated global role: writer or admin"),
			// aihub#425/#426. handleUpdateUser has always bound this — PATCH
			// /v1/admin/users/:id sets `author_aliases=$n` whenever the field is
			// present — and this handler has always forwarded it, because it
			// copies its whole args map into the body. Only the schema was
			// missing, so the value was reachable solely by a caller who guessed a
			// name no schema mentions. Measured, not assumed: the argument reaches
			// the PATCH body unpublished (see update_user_param_publication_test.go).
			//
			// The consequence was narrow and total: pf_create_user publishes
			// author_aliases, so aliases could be set at creation and then never
			// changed from MCP again. Aliases are how a git commit author maps to
			// a user, so the one case that could not be fixed was the one that
			// matters — an alias that was wrong, or an author who acquired a new
			// email.
			//
			// The empty-array spelling is published because the server
			// distinguishes it and nothing else says so: the request struct binds
			// []string and the handler tests `req.AuthorAliases != nil`, so an
			// omitted field leaves the column alone while `[]` decodes to a
			// non-nil empty slice and CLEARS it. Absent and empty are different
			// instructions here, and a caller who reads "omit to keep current"
			// nowhere would reasonably send [] meaning "no change".
			"author_aliases": prop("array", "Updated git author aliases — REPLACES the whole list. "+
				"Omit to leave unchanged; send [] to clear every alias."),
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
	s.addTool(&sdkmcp.Tool{
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
	s.addTool(&sdkmcp.Tool{
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
