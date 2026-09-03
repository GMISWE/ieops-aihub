package mcp

// Dependency tools carry NO run-attempt credentials, and that is a decision
// rather than an omission (aihub#324).
//
// ─── what used to be here ────────────────────────────────────────────────────
//
// Both handlers resolved the caller's state file and built `attempt_id`,
// `claim_epoch` and `session_secret` into the request body, and both tools took
// a REQUIRED `work_item_id` whose only purpose was naming the state file to
// read those three out of. Nothing consumed any of it:
//
//   - the server authorizes dependency create and delete on project role alone
//     (handleCreateDependency / handleDeleteDependency in
//     internal/server/router.go) and never reads a credential from either; and
//   - pkg/client.RemoveDependency did not even put the body on the wire — it
//     issued the DELETE with a nil body, and do() only marshals a non-nil one,
//     so the remove side's three fields were constructed and dropped inside the
//     same function.
//
// ─── why deleting it is the fix, rather than making it real ──────────────────
//
// A credential that nothing checks is worse than no credential at all: a reader
// of this file — including a reviewer — concludes the path is attempt-gated, and
// the code shape agrees with them. Removing the fields makes the file say what
// the system does. Adding genuine validation is a separate, deliberate,
// BREAKING change; it is not "restoring" anything, because this was never a gate.
//
// The model this leaves in force — project `writer` on the blocked work item's
// project, nothing else — is written out at handleDeleteDependency and pinned by
// TestE2EDependencyMutationsNeedNoAttemptCredential in
// dependency_authz_e2e_db_test.go.

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerDependencyTools() {
	// pf_create_dependency
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_create_dependency",
		Description: "Create a dependency between two work items. Authorized by project role " +
			"(writer on the blocked item's project, viewer on the blocking item's if they differ); " +
			"no run-attempt credential is involved.",
		InputSchema: objectSchema(map[string]any{
			"blocked_wi_id":  prop("string", "Work item that is blocked"),
			"blocking_wi_id": prop("string", "Work item that is blocking"),
			"kind":           prop("string", "blocks|supersedes|related"),
			"note":           prop("string", "Optional note"),
		}, []string{"blocked_wi_id", "blocking_wi_id", "kind"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		for _, f := range []string{"blocked_wi_id", "blocking_wi_id", "kind"} {
			if strArg(args, f) == "" {
				return errResult(fmt.Errorf("%s is required", f))
			}
		}

		body := map[string]any{
			"blocked_wi_id":  strArg(args, "blocked_wi_id"),
			"blocking_wi_id": strArg(args, "blocking_wi_id"),
			"kind":           strArg(args, "kind"),
		}
		if note := strArg(args, "note"); note != "" {
			body["note"] = note
		}

		result, err := s.client.CreateDependency(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_remove_dependency
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_remove_dependency",
		Description: "Remove a dependency between two work items. Authorized by project writer " +
			"on the blocked item's project; no run-attempt credential is involved.",
		InputSchema: objectSchema(map[string]any{
			"blocked_wi_id":  prop("string", "Work item that is blocked"),
			"blocking_wi_id": prop("string", "Work item that is blocking"),
			"kind":           prop("string", "blocks|supersedes|related"),
		}, []string{"blocked_wi_id", "blocking_wi_id", "kind"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		for _, f := range []string{"blocked_wi_id", "blocking_wi_id", "kind"} {
			if strArg(args, f) == "" {
				return errResult(fmt.Errorf("%s is required", f))
			}
		}

		result, err := s.client.RemoveDependency(ctx,
			strArg(args, "blocked_wi_id"), strArg(args, "blocking_wi_id"), strArg(args, "kind"))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_list_dependencies
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_list_dependencies",
		Description: "List dependencies (blocking + blocked_by) for a work item. Cross-project items are folded if caller lacks viewer+ permission.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID"),
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
		result, err := s.client.ListDependencies(ctx, wiID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
