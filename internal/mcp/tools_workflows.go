package mcp

// tools_workflows.go — the WI-owned workflow tools (aihub#708 Batch 2A):
// read/revise the pinned flow, mint a step invocation, record the worker's
// structured result, approve as a human, and open an explicit repair
// authorization.
//
// The split of authority, which the whole toolset is shaped around:
//
//   - The CONTROLLER-facing tools (start, result, repair) inject the attempt
//     credentials from the state file exactly like pf_update_step, and the
//     server derives everything else. A caller cannot name a step_attempt_id,
//     a producer id, or a grant — those are server-minted.
//   - The AUTHOR-facing tools (update, approve) need no attempt: a revision is
//     a contract-tier edit of the work item, an approval is a human decision.
//     There is no actor parameter anywhere — the authenticated principal is
//     the actor, and the approve route answers 403 for a machine credential
//     whatever the key's project role is.
//   - Every handler forwards its arguments VERBATIM and lets the server
//     validate. That is also what keeps the universal contract gate's probe
//     honest: a local pre-validation would refuse the probe shape and read as
//     a parameter that never leaves this process.

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

func (s *Server) registerWorkflowTools() {
	// pf_get_workflow
	s.addTool(&sdkmcp.Tool{
		Name: "pf_get_workflow",
		Description: "Read a work item's OWNED workflow: the current pinned flow (steps with exact skill_id + " +
			"skill_version, per-step rhs, ordered model candidates, params, input refs), the append-only " +
			"generation history, per-step progress (latest result status, review verdict, artifact and the " +
			"human approval state for that exact artifact), and open repair authorizations. A work item with " +
			"no workflow answers steps_version=0 and null steps. Skill CONTENT is never included: the pinned " +
			"references are work-item data, the bodies stay behind the skill registry's own access checks.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (id or slug)"),
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
		result, err := s.client.GetWorkItemWorkflow(ctx, wiID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_workflow
	s.addTool(&sdkmcp.Tool{
		Name: "pf_update_workflow",
		Description: "Revise (or first pin) a work item's workflow generation. Atomic: skill refs are resolved " +
			"to exact versions and the composition validated server-side in one transaction, so an invalid flow " +
			"refuses with nothing written. skill_version 0 in a step means \"pin my latest ACCESSIBLE version\". " +
			"expected_steps_version is the CAS token (0 = the work item has no workflow yet); a mismatch is a " +
			"409 carrying the current value. requires_human_session must be explicit: a workflow-bearing work " +
			"item is classified at pin time, and WI rhs AND step rhs together gate human approval. Refused while " +
			"a worker is in progress (pause first) and on a work item that already ran scenario-graph steps.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (id or slug)"),
			"expected_steps_version": prop("number", "The steps_version the caller last saw; 0 when pinning "+
				"the first generation. The revision is refused with the current value when it moved."),
			"requires_human_session": prop("boolean", "Explicit human-session classification for the work "+
				"item; required on every revision, never defaulted"),
			"steps": workflowStepsProp(),
		}, []string{"work_item_id", "expected_steps_version", "requires_human_session", "steps"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		body := map[string]any{
			"expected_steps_version": numArg(args, "expected_steps_version"),
			"requires_human_session": boolArg(args, "requires_human_session"),
			"steps":                  args["steps"],
		}
		result, err := s.client.UpdateWorkItemWorkflow(ctx, wiID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_start_workflow_step
	s.addTool(&sdkmcp.Tool{
		Name: "pf_start_workflow_step",
		Description: "Start one workflow step for the current attempt. The server re-checks registry access for " +
			"every pinned skill version, re-validates the composition, enforces step ordering (only an explicit " +
			"repair authorization may reopen a step that already has a result) and MINTS the invocation identity: " +
			"step_attempt_id and producer_id are server-generated. The response is the invocation descriptor " +
			"(grant, pinned skill ref, models, params, inputs, effective_rhs) the worker result must echo exactly. " +
			"Credentials are injected from the state file.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (id or slug)"),
			"step_id":      prop("string", "Step id from the pinned flow"),
			"repair_episode_id": prop("string", "Open repair authorization to bind this invocation to, when "+
				"reopening a step. Empty for an ordinary first invocation."),
		}, []string{"work_item_id", "step_id"}),
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
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
			"step_id":        strArg(args, "step_id"),
		}
		if rid := strArg(args, "repair_episode_id"); rid != "" {
			body["repair_episode_id"] = rid
		}
		result, err := s.client.StartWorkflowStep(ctx, wiID, body)
		if err != nil {
			outErr, deleteState := classifyStepUpdateErr(err)
			if deleteState {
				deleteStaleCredential(sf, wiID)
			}
			return errResult(outErr)
		}
		return jsonResult(result)
	})

	// pf_workflow_result
	s.addTool(&sdkmcp.Tool{
		Name: "pf_workflow_result",
		Description: "Record one worker's structured StepResult for an open invocation. The envelope must echo " +
			"the invocation identity exactly (work_item_id, flow_version, step_id, step_attempt_id, epoch, " +
			"producer_id from pf_start_workflow_step) and may NOT carry an approval: a worker result that " +
			"attempts one is rejected outright. Status is completed|incomplete|blocked|provider_error|" +
			"invalid_result; review steps must carry review_verdict pass|warn|fail and completed review/" +
			"verification needs evidence. A review FAIL pauses the attempt (recoverable, never terminal). " +
			"Stale results change nothing. Credentials are injected from the state file.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (id or slug)"),
			"result": prop("object", "The StepResult envelope: {status, review_verdict?, work_item_id, "+
				"flow_version, step_id, step_attempt_id, epoch, producer_id, artifact{id,version,hash}, "+
				"evidence[{kind,ref,hash}]}"),
		}, []string{"work_item_id", "result"}),
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
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		// result is forwarded VERBATIM: the server is the validator, and a
		// local shape check would refuse shapes the probe (and real callers)
		// legitimately send before they ever reach it.
		body := map[string]any{
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
			"result":         args["result"],
		}
		result, err := s.client.RecordWorkflowResult(ctx, wiID, body)
		if err != nil {
			outErr, deleteState := classifyStepUpdateErr(err)
			if deleteState {
				deleteStaleCredential(sf, wiID)
			}
			return errResult(outErr)
		}
		return jsonResult(result)
	})

	// pf_approve_workflow
	s.addTool(&sdkmcp.Tool{
		Name: "pf_approve_workflow",
		Description: "Record a HUMAN approval or rejection of a workflow step's output. Only an authenticated " +
			"human user may call this (a machine credential is refused whatever role it holds), and there is no " +
			"actor argument anywhere: the authenticated principal is the actor. The artifact triple must EQUAL " +
			"the step's latest recorded result, and the step must actually gate on the human (work item " +
			"requires_human_session AND step rhs). Approvals bind the exact generation: a revision needs its own.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (id or slug)"),
			"steps_version": prop("number", "Workflow generation the approval binds; a mismatch with the "+
				"current generation is refused"),
			"step_id": prop("string", "Step id from the pinned flow"),
			"artifact": prop("object", "The exact artifact of the step's latest recorded result: "+
				"{id, version, hash}"),
			"decision": propEnum("string", "The human decision", []string{"approved", "rejected"}),
		}, []string{"work_item_id", "steps_version", "step_id", "artifact", "decision"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		body := map[string]any{
			"steps_version": numArg(args, "steps_version"),
			"step_id":       strArg(args, "step_id"),
			"artifact":      args["artifact"],
			"decision":      strArg(args, "decision"),
		}
		result, err := s.client.ApproveWorkflowStep(ctx, wiID, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_repair_workflow
	s.addTool(&sdkmcp.Tool{
		Name: "pf_repair_workflow",
		Description: "Open an EXPLICIT recovery authorization for one failed result, binding the exact failed " +
			"step attempt. kind=retry authorizes one bounded retry of a provider_error invocation; the " +
			"infrastructure-failure fallback; any other outcome, and above all a review FAIL, is refused: a " +
			"FAIL recovers through kind=episode: a repair producer step, a fresh verification and a fresh " +
			"independent review, each started with the episode id. An episode stays open until every role has " +
			"recorded a completed result; the pure policy validates the completed set, and open authorizations " +
			"are bounded. A retry opened over an episode role's provider_error derives and returns the immutable " +
			"parent lineage (parent_episode_id): its replacement invocations stand in for that role in the " +
			"episode's effective role set, the failed history stays, and the retry chain is bounded. Requires the " +
			"current attempt's credentials (injected from the state file), so after " +
			"a review FAIL this is reachable only from the resumed attempt.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":           prop("string", "Work item ID (id or slug)"),
			"failed_step_attempt_id": prop("string", "The step_attempt_id of the failed result being recovered"),
			"kind":                   propEnum("string", "The recovery shape", []string{"retry", "episode"}),
			"reason":                 prop("string", "Why recovery is authorized (10-2000 chars); recorded with the authorization"),
		}, []string{"work_item_id", "failed_step_attempt_id", "kind", "reason"}),
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
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		body := map[string]any{
			"attempt_id":             sf.AttemptID,
			"claim_epoch":            sf.ClaimEpoch,
			"session_secret":         sf.SessionSecret,
			"failed_step_attempt_id": strArg(args, "failed_step_attempt_id"),
			"kind":                   strArg(args, "kind"),
			"reason":                 strArg(args, "reason"),
		}
		result, err := s.client.AuthorizeWorkflowRepair(ctx, wiID, body)
		if err != nil {
			outErr, deleteState := classifyStepUpdateErr(err)
			if deleteState {
				deleteStaleCredential(sf, wiID)
			}
			return errResult(outErr)
		}
		return jsonResult(result)
	})

	// pf_reconcile_workflow
	s.addTool(&sdkmcp.Tool{
		Name: "pf_reconcile_workflow",
		Description: "Fence a dead attempt's open workflow invocations: the explicit controller-reconcile transition " +
			"(aihub#708 B3). When an attempt is paused or taken over while it holds OPEN step invocations, those rows stay " +
			"open forever and block the step; this transition supersedes them (ordinary and repair-episode-bound alike), so " +
			"the CURRENT attempt can start replacements. No implicit trust: supersede_attempt_id names the attempt, and the " +
			"server reads that attempt's own row under the work item lock; it must belong to this work item and must NOT be " +
			"running (naming the current attempt or a live one is refused). A superseded invocation's stale result is refused; " +
			"it stops counting toward an episode's role set, and its repair binding is freed for a replacement. Idempotent: " +
			"0 superseded when nothing was left open. Credentials are injected from the state file.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (id or slug)"),
			"supersede_attempt_id": prop("string", "The attempt a pause/takeover ended; its open invocations are being "+
				"abandoned. Its run_attempt id, as seen in invocation events or the takeover response."),
		}, []string{"work_item_id", "supersede_attempt_id"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		supersede := strArg(args, "supersede_attempt_id")
		if supersede == "" {
			return errResult(fmt.Errorf("supersede_attempt_id is required"))
		}
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		body := map[string]any{
			"attempt_id":           sf.AttemptID,
			"claim_epoch":          sf.ClaimEpoch,
			"session_secret":       sf.SessionSecret,
			"supersede_attempt_id": supersede,
		}
		result, err := s.client.ReconcileWorkflowInvocations(ctx, wiID, body)
		if err != nil {
			outErr, deleteState := classifyStepUpdateErr(err)
			if deleteState {
				deleteStaleCredential(sf, wiID)
			}
			return errResult(outErr)
		}
		return jsonResult(result)
	})
}

// workflowStepsProp describes the steps array *including its element shape*
// (aihub#238's rule, applied to the workflow the way declared_resources got
// it): the entry shape is the contract, and a bare "array of objects" would
// leave every caller guessing the keys.
func workflowStepsProp() map[string]any {
	stepProps := map[string]any{
		"id":       prop("string", "Step id, unique in the flow"),
		"skill_id": prop("string", "Exact skill id from the registry"),
		"skill_version": prop("number", "Exact version to pin; 0 = the caller's latest accessible version, "+
			"resolved and frozen server-side"),
		"rhs": prop("boolean", "Step-level human gate; omitted defaults false. Effective gate is work item "+
			"requires_human_session AND this"),
		"models": prop("array", "Ordered model candidates {harness, model, effort}"),
		"params": prop("object", "Step params, validated against the pinned skill version's params schema"),
		"inputs": prop("array", "Input refs {name, step_id, output} to earlier steps' outputs"),
	}
	p := prop("array", "Steps of the flow, in execution order. Each pins a skill version and carries its own "+
		"model candidates; the server derives grants from the pinned contract's capabilities.")
	p["items"] = map[string]any{
		"type":       "object",
		"properties": stepProps,
		"required":   []string{"id", "skill_id", "skill_version", "models"},
	}
	return p
}

// numArg reads one numeric argument as a float64 so the body carries the
// number the caller sent (the server binds integers from it). Zero when the
// argument is absent or not a number.
func numArg(args map[string]any, key string) any {
	v, ok := args[key]
	if !ok {
		return 0
	}
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return v
}
