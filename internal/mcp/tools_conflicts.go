package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerConflictTools() {
	// pf_predict_conflicts
	// 🔴 The severity sentence in the description below is aihub#416 §5.3's
	// required public declaration, not a footnote.
	//
	// Retiring the git_branch/deploy_env derivation moves the CEILING of what
	// this tool can answer for a repo or service entry: rule 1 (hard_block, lock
	// table) can no longer fire for either, so a payload declaring only repo
	// and/or service entries now tops out at soft_block (rule 2/4) or info
	// (rule 6). Nothing else about the call changes — same parameters, same
	// response shape, same 200.
	//
	// That is exactly the shape of change a caller cannot detect: pf-work's
	// pre-claim gate branches on `severity`, and an unannounced hard_block ->
	// info move reads as "checked, and clear" rather than as "this question is
	// no longer asked". Publishing it here is the only place the caller who
	// reads the ceiling and the server that computes it meet.
	s.addTool(&sdkmcp.Tool{
		Name: "pf_predict_conflicts",
		Description: "Predict resource conflicts for a set of declared_resources. Also returns will_unlock (which blocked wi would be unblocked). " +
			"⚠️ SEVERITY CEILING CHANGED IN aihub#416: a payload of only repo and/or service entries can no longer return hard_block. " +
			"repo and service derive no lock now, so the lock-table rule cannot fire for them; a repo overlap reports soft_block and a service overlap info, both from a join on other running work items' declared_resources. " +
			"Read severity:\"info\" on a service as \"somebody else declares it, judge for yourself\", NOT as \"checked, no conflict\" — the exclusion it used to stand for no longer exists. " +
			"path/document/section are unaffected and can still return hard_block. " +
			"Predictions for repo and service carry last_active_age_seconds (how long ago that attempt reported activity) so the answer is judgeable; nothing expires on it.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":       prop("string", "Work item ID or slug. Optional, but NOT decorative (aihub#510, aihub#564): it is the only way this call learns which running work item is YOU, and every rule uses it to leave you out — the declaration rules since aihub#510, the lock-table rules (1 hard_block, 3 file_scope) since aihub#564, so a claimed wi re-predicting its own declarations no longer gets its own locks back as somebody else's conflict. Omit it and your own locks and declarations come back as your own hard_block/soft_block/info."),
			"project":            prop("string", "Project the declared resources belong to; namespaces file_scope conflict checks (aihub#222). Optional when work_item_id is set (the wi own project takes precedence)."),
			"declared_resources": declaredResourcesProp("Resources to check for conflicts"),
			"dry_run":            prop("boolean", "Dry run — do not mutate state"),
		}, []string{"declared_resources"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if _, ok := args["declared_resources"]; !ok {
			return errResult(fmt.Errorf("declared_resources is required"))
		}
		result, err := s.client.PredictConflicts(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
