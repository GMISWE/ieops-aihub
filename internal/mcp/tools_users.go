package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func (s *Server) registerUserTools() {
	// pf_list_users
	//
	// aihub#596 (2026-09-11): this said "List all users" and that was measured
	// false past a hundred users — handleListUsers runs ORDER BY created_at DESC
	// LIMIT 100 with no cursor and no total (the data-layer fact is held by
	// internal/server/list_users_response_shape_test.go). The description now
	// states the cap and the ordering instead of promising totality; adding
	// pagination is a separate feature decision, deliberately not taken here.
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_list_users",
		Description: "List the 100 newest users by creation time (admin only). No pagination: no cursor, no total, rows past the cap are silently omitted",
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
			"email": prop("string", "Email address (required for human users; auto-generated for machine users)"),
			// aihub#587 (2026-09-10): `author_aliases` is WITHDRAWN from this
			// schema, and handleCreateUser no longer binds it. Measured before
			// the withdrawal: users.author_aliases had three write sites and no
			// SQL statement the census could see reading it, anywhere in
			// internal/ or pkg/ — commit records take their author from the
			// authenticated caller, never from this column, so the value was
			// stored for nobody. The owner's ruling on aihub#587 was to
			// withdraw the parameter rather than wire a reader. The COLUMN
			// stays (TEXT[] NOT NULL DEFAULT '{}' in 0001_initial.sql, so the
			// DEFAULT is what lands now): dropping it is a destructive
			// migration and a separate decision, recorded as dormant in
			// docs/mcp-cards/pf_create_user.md. A caller still sending the
			// name gets it disclosed by the aihub#389 echo
			// (request_adjusted.unknown_params) rather than silently dropped —
			// TestAuthorAliasesWithdrawalIsDisclosed
			// (update_user_param_publication_test.go) — and the census arm
			// that used to hold "written, never read" now holds "neither
			// written nor read": TestAuthorAliasesIsNeitherWrittenNorRead
			// (user_admin_surface_test.go).
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
		Description: "Update a user's display name or role (admin only)",
		InputSchema: objectSchema(map[string]any{
			"id":           prop("string", "User ID"),
			"display_name": prop("string", "Updated display name"),
			// aihub#496. Published as prose — "Updated global role: writer or
			// admin" — while the sibling create path has published the same
			// vocabulary as a real enum since aihub#463. A caller reading one tool
			// got a machine-readable set and reading the other got a sentence
			// about one, for the same column.
			//
			// The values come from domain, the package that refuses anything
			// outside them, so the published set and the accepted set are one
			// value. Same caveat as everywhere else on this surface: the enum
			// constrains the CLIENT and not this process (aihub#463 measured that
			// the untyped AddTool path runs no schema step) — the hard refusal is
			// domain.ValidateUserGlobalRole in handleUpdateUser, added by this
			// same change, which answers 400 naming the field.
			//
			// "Global" is load-bearing: this is NOT a project member role
			// (viewer|writer|maintainer), and neither vocabulary contains the
			// other (aihub#411 §6.2 T2-17). `maintainer` is the mistake a caller
			// conflating them actually makes.
			"role": propEnum("string", "Updated global role across every project, NOT a project "+
				"member role", domain.UserGlobalRoleList()),
			// aihub#587 (2026-09-10): `author_aliases` is WITHDRAWN here too —
			// see the pf_create_user comment above for the ruling, the measured
			// no-reader census and the arms. This tool published the field from
			// aihub#425/#426 until aihub#587 so an alias set at creation could be
			// corrected; with no reader anywhere in internal/ or pkg/, that
			// correction capability preserved a value nothing consumes, and
			// handleUpdateUser no longer binds it — a PATCH body carrying the
			// name now behaves exactly as though it carried nothing, the same
			// verdict user_type has always had on this path.
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
		Description: "Create an API key for a user (admin only). Returns the plain key once; store it securely.",
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
