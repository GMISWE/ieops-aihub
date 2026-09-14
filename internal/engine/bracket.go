package engine

// StepCall is one pf_update_step / pf_complete_attempt invocation, as plain data:
// internal/engine computes the SEQUENCE; the caller (internal/cli, or today the LLM-driven
// loop) makes the actual MCP call. This is deliberately data-only (Design Decision B) so the
// same []StepCall can later be diffed against a second implementation's output in a
// cross-implementation contract test (aihub#657: "same input -> same []StepCall").
//
// JSON tags are snake_case to match the MCP tools' own parameter names 1:1 — internal/cli's
// `engine bracket-plan` verb marshals a []StepCall directly with no shadow type, so the CLI's
// stdout shape ({"tool":"pf_update_step",...}) IS this struct's json encoding, not a hand-built
// approximation of it. Empty/inapplicable fields are omitted rather than emitted as "".
type StepCall struct {
	// Tool names the MCP tool this call invokes. PlanStepBracket only ever produces
	// "pf_update_step" values (every StepCall it returns is one of the three pf_update_step
	// shapes described on PlanStepBracket's own doc comment); "pf_complete_attempt" is a value
	// this field's type permits but that PlanStepBracket itself never emits; see
	// PlanStepBracket's doc comment for where that call belongs instead.
	Tool string `json:"tool"`
	// StepID is the step this call concerns. Empty for a pf_complete_attempt call.
	StepID string `json:"step_id,omitempty"`
	// Status is "in_progress", "completed", or "failed".
	Status string `json:"status"`
	// StepAttemptID is the attempt id threaded through this call.
	StepAttemptID string `json:"step_attempt_id,omitempty"`
	// NextStep is the step id to start next, "" when absent/not applicable (last step, or a
	// failed status, or a degraded-form second call already carries it as StepID instead).
	NextStep string `json:"next_step,omitempty"`
	// NextStepAttemptID is the attempt id for NextStep, "" when NextStep is "".
	NextStepAttemptID string `json:"next_step_attempt_id,omitempty"`
	// ArtifactSummary is the completing step's summary. Empty for in_progress calls.
	ArtifactSummary string `json:"artifact_summary,omitempty"`
	// ErrorType is set only on a failed completion (e.g. "review_fail").
	ErrorType string `json:"error_type,omitempty"`
}

// BracketInput is every fact PlanStepBracket needs to decide the fused-vs-degraded form of the
// step-bracket sequence (lifecycle-details.md §1).
type BracketInput struct {
	// StepID is the step being completed or failed.
	StepID string
	// StepAttemptID is that step's attempt id.
	StepAttemptID string
	// Status is "completed" or "failed". PlanStepBracket only branches on Status=="failed";
	// any other value (including a typo, or any value that is not literally "failed") takes
	// the "completed" path below. This fail-open handling is deliberate, not an oversight: the
	// one production caller (internal/cli's `engine bracket-plan` verb) validates Status is one
	// of the two accepted strings before calling PlanStepBracket, so a defensive third branch
	// here would duplicate that validation in a place that cannot itself report the error to a
	// user (PlanStepBracket has no error return). Should a second caller appear that skips that
	// validation, add the check there, not by giving this function an error return.
	Status string
	// ArtifactSummary is the completing step's one-line summary (Status=="completed" only).
	ArtifactSummary string
	// ErrorType is set only when Status=="failed" (e.g. "review_fail").
	ErrorType string
	// NextStepID is the step id to start next, "" when StepID is the last step. Ignored when
	// Status=="failed" — there is never a next call on a failure.
	NextStepID string
	// NextStepAttemptID is the attempt id for NextStepID, "" when NextStepID is "".
	NextStepAttemptID string
	// SupportsNextStep reports whether the connected pf_update_step publishes next_step /
	// next_step_attempt_id. true selects the fused single-call form; false selects the
	// degraded two-call form (lifecycle-details.md §1's "look at what pf_update_step
	// publishes, do not guess from the version string" rule).
	SupportsNextStep bool
}

// PlanStepBracket returns the ordered []StepCall for completing (or failing) one step and,
// on success with a next step, starting it:
//
//   - Status=="failed": exactly ONE call, status=failed, carrying ErrorType. NextStepID is
//     never consulted — next_step is not valid on a failure (engine-native-details.md §0c).
//   - Status=="completed" and NextStepID=="" (the last step): exactly ONE call, status=completed,
//     no next_* fields.
//   - Status=="completed", NextStepID!="", SupportsNextStep==true: exactly ONE fused call,
//     status=completed with NextStep/NextStepAttemptID set.
//   - Status=="completed", NextStepID!="", SupportsNextStep==false: exactly TWO calls —
//     status=completed for StepID, then a SEPARATE status=in_progress call for NextStepID —
//     with StepAttemptID correctly threaded as NextStepAttemptID on that second call (the
//     lifecycle-details.md §1 trap: omitting it leaves current_step_attempt NULL).
//
// Callers making the actual MCP calls should also honor the pf_complete_attempt review-FAIL
// path (engine-native-details.md §0c) themselves when Status=="failed" and the failure is a
// review verdict: that path additionally calls pf_complete_attempt(status="failed"), which is
// once-per-wi terminal-attempt state outside PlanStepBracket's per-step scope and therefore not
// modeled as a StepCall here.
func PlanStepBracket(in BracketInput) []StepCall {
	if in.Status == "failed" {
		return []StepCall{{
			Tool:          "pf_update_step",
			StepID:        in.StepID,
			Status:        "failed",
			StepAttemptID: in.StepAttemptID,
			ErrorType:     in.ErrorType,
		}}
	}

	completing := StepCall{
		Tool:            "pf_update_step",
		StepID:          in.StepID,
		Status:          "completed",
		StepAttemptID:   in.StepAttemptID,
		ArtifactSummary: in.ArtifactSummary,
	}

	if in.NextStepID == "" {
		return []StepCall{completing}
	}

	if in.SupportsNextStep {
		completing.NextStep = in.NextStepID
		completing.NextStepAttemptID = in.NextStepAttemptID
		return []StepCall{completing}
	}

	// Degraded two-call form: the completing call carries no next_* fields, and a SEPARATE
	// in_progress call starts the next step, with NextStepAttemptID threaded as ITS
	// StepAttemptID.
	starting := StepCall{
		Tool:          "pf_update_step",
		StepID:        in.NextStepID,
		Status:        "in_progress",
		StepAttemptID: in.NextStepAttemptID,
	}
	return []StepCall{completing, starting}
}
