package mcp

import (
	"context"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

func (s *Server) registerStepTools() {
	// pf_get_step
	//
	// aihub#265: this description used to advertise "step graph, current status,
	// progress, previous steps" and the endpoint returned none of the last two —
	// StepState carried the current row of wi_step_state and nothing else, while
	// the step history sat unread in wi_step_completions. Every scenario step
	// graph therefore opened by telling the agent to read prior context out of a
	// hand-written `.pf_steps.json`, because this tool had nothing to offer — and
	// no code path anywhere creates that file, so a resuming agent read whatever
	// the previous one happened to leave behind, or nothing at all.
	//
	// The description is now the contract: internal/mcp/tools_step_contract_test.go
	// asserts that every response field named here is a bound JSON key on
	// server.StepState, in BOTH directions, so it cannot go back to promising
	// something the struct does not carry.
	//
	// "step graph" is deliberately gone rather than reworded: the graph lives in
	// the scenario template, not in aihub, and naming it here is what made an
	// agent believe one call would tell it what the remaining steps are.
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_get_step",
		Description: "Read the AUTHORITATIVE step record for a work item, and the only one. Returns " +
			"current_step / current_step_status / version, plus completed_steps: the step history, oldest " +
			"first, retries included, each entry carrying step_id, status and that step's artifact_summary. " +
			"A resuming agent should call this FIRST and treat every step_id in completed_steps as done. " +
			"Never take step progress from a file in the worktree; nothing writes one. completed_steps is [] " +
			"when nothing has completed, and absent only on a server older than aihub#265 — not the same " +
			"answer. Takes a slug or a canonical id and echoes the canonical one in work_item_id; pass THAT " +
			"to pf_recall / pf_read_events, which return nothing for a slug. No step graph here — that is " +
			"the scenario template.",
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
		result, err := s.client.GetStep(ctx, wiID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_step
	//
	// Two things changed here in aihub#290, both about round-trips rather than
	// bytes:
	//
	//   - `expected_version` is GONE. It was published and forwarded, and the
	//     server's UpdateStepRequest never had the field, so echo's Bind dropped
	//     it — the pf_get_step that callers made purely to fetch that version
	//     bought a value nothing ever read. Removing the parameter is what
	//     removes the call. (Implementing it server-side would have fixed a
	//     correctness fiction while keeping the round-trip; the two are separate
	//     questions and this one is the token question.)
	//   - `next_step` is NEW: complete a step and start its successor in one
	//     call, because the in_progress call that always followed a completed one
	//     read nothing out of its response.
	s.mcp.AddTool(&sdkmcp.Tool{
		Name: "pf_update_step",
		Description: "Update the current step status. Credentials injected from state file. " +
			"Server auto-emits step_started/step_completed/step_failed events. " +
			"When completing a step that has a successor, pass next_step to complete-and-start in ONE call " +
			"instead of following up with a separate status=\"in_progress\" call — the two transitions then " +
			"share a transaction and emit both events. There is no version/CAS argument: concurrency is " +
			"guarded server-side by the idle-step predicate, so no pf_get_step is needed before this call.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id":     prop("string", "Work item ID"),
			"step_id":          prop("string", "Step ID to update"),
			"status":           prop("string", "in_progress|completed|failed"),
			"step_attempt_id":  prop("string", "Step attempt ID of the step being completed/failed (required for completed/failed)"),
			"artifact_summary": prop("string", "Brief summary of artifacts produced"),
			"error_type":       prop("string", "Error type (for failed status)"),
			"escalated":        prop("boolean", "Whether to escalate the failure"),
			"next_step": prop("string", "Step ID to START in the same call, after the one named by step_id completes. "+
				"Only valid with status=\"completed\"; sending it with any other status is an error, not a no-op."),
			"next_step_attempt_id": prop("string", "Step attempt ID for the step being STARTED via next_step (distinct from step_attempt_id, which belongs to the step being completed)"),
			"heartbeat":            prop("boolean", "Send a heartbeat ping to keep the lease alive (resets step_started_at)"),
		}, []string{"work_item_id", "step_id", "status"}),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}

		// Validate the fused-advance arguments BEFORE the heartbeat branch and
		// before touching the state file, so a caller error costs no round-trip to
		// aihub and does not surface as a confusing "read state file" failure. The
		// server re-checks; this is the fast path, not the authority.
		if err := validateNextStepArgs(strArg(args, "next_step"), strArg(args, "next_step_attempt_id"),
			strArg(args, "status"), boolArg(args, "heartbeat")); err != nil {
			return errResult(err)
		}

		// Inject credentials from state file
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		// Heartbeat mode: only requires work_item_id + credentials
		if boolArg(args, "heartbeat") {
			body := map[string]any{
				"heartbeat":      true,
				"attempt_id":     sf.AttemptID,
				"claim_epoch":    sf.ClaimEpoch,
				"session_secret": sf.SessionSecret,
			}
			result, err := s.client.UpdateStep(ctx, wiID, body)
			if err != nil {
				outErr, deleteState := classifyStepUpdateErr(err)
				if deleteState {
					_ = config.DeleteStateFile(wiID)
				}
				return errResult(outErr)
			}
			return jsonResult(result)
		}

		if strArg(args, "step_id") == "" {
			return errResult(fmt.Errorf("step_id is required"))
		}
		if strArg(args, "status") == "" {
			return errResult(fmt.Errorf("status is required"))
		}

		body := updateStepBody(args, sf.AttemptID, sf.ClaimEpoch, sf.SessionSecret)

		result, err := s.client.UpdateStep(ctx, wiID, body)
		if err != nil {
			outErr, deleteState := classifyStepUpdateErr(err)
			if deleteState {
				_ = config.DeleteStateFile(wiID)
			}
			return errResult(outErr)
		}
		if outErr := checkNextStepHonoured(strArg(args, "next_step"), strArg(args, "next_step_attempt_id"), result); outErr != nil {
			return errResult(outErr)
		}
		return jsonResult(result)
	})
}

// checkNextStepHonoured turns an old server's silent drop of next_step into a
// loud failure. Returns nil when no fused advance was requested.
//
// This binary and the aihub HTTP server it talks to are deployed on SEPARATE
// schedules — the binary republishes on every push to main and updates itself
// through its own channel, while the server is deployed by hand — so "the schema
// publishes next_step" is a fact about the LOCAL binary and says nothing about
// what the remote server binds. Reading the local tool schema to decide whether
// the remote will honour the parameter is inferring the peer's capability from
// the library's, and GET /v1/version carries no capability list to ask instead.
//
// Left unchecked the failure is silent AND corrupting, not merely inert. A
// pre-aihub#290 server binds nothing for next_step, so echo drops it, the
// completion commits, and the call answers 200 {"status":"completed"} while the
// successor never starts. current_step therefore never advances, and because the
// server derives each completion row's step_id from current_step, EVERY
// subsequent completion in the walk is filed under the first step's name, with
// no error anywhere.
//
// The server confirms a fused advance by echoing next_step in its response
// (internal/server/routes_step.go); its absence alongside a next_step request is
// the signal. This is the one capability check available, and it is
// unambiguous — a server that honoured the parameter always echoes it.
func checkNextStepHonoured(nextStep, nextStepAttemptID string, result map[string]any) error {
	if nextStep == "" {
		return nil
	}
	if _, honoured := result["next_step"]; honoured {
		return nil
	}
	retry := fmt.Sprintf("pf_update_step(step_id=%q, status=\"in_progress\"", nextStep)
	if nextStepAttemptID != "" {
		retry += fmt.Sprintf(", step_attempt_id=%q", nextStepAttemptID)
	}
	retry += ")"
	return fmt.Errorf(
		"SERVER_TOO_OLD_FOR_NEXT_STEP: the step WAS completed, but the aihub server ignored next_step=%q, "+
			"so %q was NOT started — this server predates aihub#290 and silently discards the parameter. "+
			"Do NOT re-send the completion; it already landed. Start the next step with a separate call: %s — "+
			"and drop next_step/next_step_attempt_id for the rest of this session, bracketing each step with its own "+
			"in_progress call, or every later completion will be recorded against the wrong step",
		nextStep, nextStep, retry)
}

// validateNextStepArgs mirrors the server's check of the same name
// (internal/server/routes_step.go), which is the authority; this copy exists so
// an unhonourable combination fails before it costs a round-trip.
//
// The heartbeat case is the subtle one: `heartbeat` is selected by its own flag,
// not by the status, so a heartbeat may carry status="completed" and slip past a
// status-only check — and the heartbeat branch below returns early with a body
// containing nothing but credentials, silently discarding next_step. That is the
// aihub#290 defect reappearing on the parameter that replaced it.
func validateNextStepArgs(nextStep, nextStepAttemptID, status string, heartbeat bool) error {
	if nextStep != "" {
		if heartbeat {
			return fmt.Errorf("next_step cannot be combined with heartbeat=true: a heartbeat only refreshes the lease and completes no step, so the successor would never start")
		}
		if status != "completed" {
			return fmt.Errorf(`next_step is only valid with status="completed"; got status=%q`, status)
		}
		return nil
	}
	if nextStepAttemptID != "" {
		return fmt.Errorf("next_step_attempt_id was sent without next_step; it names the attempt of the step being STARTED, so with no next_step nothing reads it (use step_attempt_id for the step being completed)")
	}
	return nil
}

// updateStepBody renders pf_update_step's arguments into the PATCH
// /v1/work_items/:id/step body.
//
// Extracted from the tool handler so a test can hold it against the struct that
// actually binds on the other side (server.UpdateStepRequest). That comparison is
// the whole point: the aihub#290 defect was a key this function emitted for which
// no bound field existed, which costs nothing at the wire and everything at the
// contract — the caller is told the parameter works, and it is discarded on
// arrival with no error anywhere. A key added here without a matching json tag
// over there now fails a test instead of going quiet in production.
//
// The optional keys are forwarded only when non-empty, so that omitting one stays
// distinguishable from sending "" — the server binds them as *string, and an
// explicit empty string is not the same fact as an absent field.
func updateStepBody(args map[string]any, attemptID string, claimEpoch int64, sessionSecret string) map[string]any {
	body := map[string]any{
		"step":           strArg(args, "step_id"), // server reads json:"step"
		"status":         strArg(args, "status"),
		"attempt_id":     attemptID,
		"claim_epoch":    claimEpoch,
		"session_secret": sessionSecret,
	}
	// Left side = the MCP argument name, right side = the server's json tag. They
	// differ for step_id/step and next_step_attempt_id is deliberately its own key
	// rather than a reuse of step_attempt_id.
	for argKey, bodyKey := range map[string]string{
		"step_attempt_id":      "step_attempt_id",
		"artifact_summary":     "artifact_summary",
		"error_type":           "error_type",
		"next_step":            "next_step",
		"next_step_attempt_id": "next_step_attempt_id",
	} {
		if v := strArg(args, argKey); v != "" {
			body[bodyKey] = v
		}
	}
	if boolArg(args, "escalated") {
		body["escalated"] = true
	}
	return body
}

// classifyStepUpdateErr maps an UpdateStep error to the MCP-facing error and
// whether the local state file should be deleted:
//   - ATTEMPT_PAUSED (distinct server code, aihub#209): the attempt is only
//     paused, so KEEP the state file and point the user at resume.
//   - CONFLICT_EPOCH_MISMATCH / ATTEMPT_MISMATCH: the credential is stale
//     (superseded / wrong attempt), so delete the file and ask for a re-claim.
//   - anything else: pass the error through untouched, keep the file.
//
// The ATTEMPT_PAUSED case is checked first; its code shares no substring with
// the mismatch codes, so ordering only affects clarity, not correctness.
func classifyStepUpdateErr(err error) (out error, deleteState bool) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "ATTEMPT_PAUSED"):
		return fmt.Errorf("attempt is paused — resume it first with `/pf-work <slug> --resume` before continuing (local state file kept)"), false
	case strings.Contains(msg, "CONFLICT_EPOCH_MISMATCH") || strings.Contains(msg, "ATTEMPT_MISMATCH"):
		return fmt.Errorf("STALE_LOCAL_CREDENTIAL: state file deleted — please re-claim this work item"), true
	default:
		return err, false
	}
}
