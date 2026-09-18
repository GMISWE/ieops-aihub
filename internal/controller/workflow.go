// Package controller contains the shared, fail-closed selection boundary between
// scenario steps and WI-owned workflows. A failed workflow read is never evidence
// that a WI has no workflow.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// WorkflowReader is the existing client read API; it deliberately does not
// depend on a transport or the legacy step state.
type WorkflowReader interface {
	GetWorkItemWorkflow(context.Context, string) (map[string]any, error)
}

type State struct {
	WorkItemID           string        `json:"work_item_id"`
	StepsVersion         int           `json:"steps_version"`
	Steps                workflow.Flow `json:"steps"`
	RequiresHumanSession *bool         `json:"requires_human_session"`
	Progress             []Progress    `json:"progress"`
	// Repairs mirrors the GET view's repair-authorization summaries. It is
	// decoded tolerantly (absent on an older server means none): the loop uses
	// it to tell an open authorization it can drive from one that needs a
	// decision, never to authorize anything by itself.
	Repairs []RepairSummary `json:"repairs"`
}

// RepairSummary is one repair authorization as the workflow view reports it.
type RepairSummary struct {
	ID                  string `json:"id"`
	Kind                string `json:"kind"`
	FailedStepsVersion  int    `json:"failed_steps_version"`
	FailedStepID        string `json:"failed_step_id"`
	FailedStepAttemptID string `json:"failed_step_attempt_id"`
	Status              string `json:"status"`
	ParentEpisodeID     string `json:"parent_episode_id,omitempty"`
}
type Progress struct {
	StepID           string               `json:"step_id"`
	Status           string               `json:"status"`
	ReviewVerdict    string               `json:"review_verdict"`
	StepAttemptID    string               `json:"step_attempt_id"`
	OpenInvocationID string               `json:"open_invocation_id"`
	Approved         *bool                `json:"approved"`
	RHS              bool                 `json:"rhs"`
	Artifact         workflow.ArtifactRef `json:"artifact"`
}

// Select returns nil ONLY when the server confirms the absence of a pinned
// generation. Malformed or unavailable responses refuse execution.
func Select(ctx context.Context, c WorkflowReader, wiID string) (*State, error) {
	raw, err := c.GetWorkItemWorkflow(ctx, wiID)
	if err != nil {
		return nil, fmt.Errorf("read WI workflow: %w", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("empty WI workflow response")
	}
	if _, ok := raw["steps_version"]; !ok {
		return nil, fmt.Errorf("workflow response lacks generation marker")
	}
	if _, ok := raw["work_item_id"]; !ok {
		return nil, fmt.Errorf("workflow response lacks work item identity")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode WI workflow: %w", err)
	}
	if s.WorkItemID == "" {
		return nil, fmt.Errorf("workflow response lacks a decoded work item identity")
	}
	// The route accepts either a canonical id or a slug and always returns the
	// canonical id. Slugs necessarily contain '#', while ids never do; project
	// names may legally begin with "wi_", so prefix dispatch is not a valid
	// discriminator.
	if !strings.Contains(wiID, "#") && s.WorkItemID != wiID {
		return nil, fmt.Errorf("workflow identity mismatch for %s", wiID)
	}
	if s.StepsVersion == 0 {
		if raw["steps"] != nil {
			return nil, fmt.Errorf("unversioned workflow steps")
		}
		return nil, nil
	}
	if s.StepsVersion < 0 || len(s.Steps.Steps) == 0 || len(s.Progress) != len(s.Steps.Steps) || s.RequiresHumanSession == nil {
		return nil, fmt.Errorf("incomplete pinned workflow generation %d", s.StepsVersion)
	}
	for i, step := range s.Steps.Steps {
		if step.ID == "" || s.Progress[i].StepID != step.ID {
			return nil, fmt.Errorf("workflow progress does not match pinned steps")
		}
	}
	return &s, nil
}

// ActionKind is the typed classification of what a pinned workflow state
// permits next. Every mode driver (interactive session, drain) renders the
// SAME kinds, so a human reading a CLI report and an operator reading a
// drain report see the same vocabulary.
type ActionKind string

const (
	// ActionRun: a step may start now; StepID names it, RHS carries the
	// step's effective human gate.
	ActionRun ActionKind = "run"
	// ActionOpenInvocation: a server-minted invocation has no recorded
	// result. Record its result, or reconcile it away explicitly.
	ActionOpenInvocation ActionKind = "open_invocation"
	// ActionRepairRequired: a step ended incomplete/failed/blocked or its
	// review failed; only an authorized repair may reopen it.
	ActionRepairRequired ActionKind = "repair_required"
	// ActionRevisionRequired: a human explicitly rejected the latest artifact.
	// Nothing later may run; the same step may prepare a fresh invocation and
	// superseding artifact for another exact human decision.
	ActionRevisionRequired ActionKind = "revision_required"
	// ActionApprovalRequired: an RHS step completed but no authenticated
	// human decision applies to its latest artifact yet.
	ActionApprovalRequired ActionKind = "approval_required"
	// ActionComplete: every step of the current generation is completed.
	// Wrap remains a separate, explicit lifecycle act.
	ActionComplete ActionKind = "complete"
)

// NextAction classifies the pinned state. It is the shared, mode-independent
// answer to "what happens next": the interactive verb renders it directly,
// and drain turns every non-run kind into a refusal to act.
func (s *State) NextAction() NextAction {
	for _, p := range s.Progress {
		if p.OpenInvocationID != "" {
			return NextAction{Kind: ActionOpenInvocation, StepID: p.StepID,
				Detail: fmt.Sprintf("step %s has open invocation %s; record its result or reconcile it before continuing", p.StepID, p.OpenInvocationID)}
		}
		if p.Status == "" {
			return NextAction{Kind: ActionRun, StepID: p.StepID, RHS: p.RHS}
		}
		if p.Status != "completed" || p.ReviewVerdict == "fail" {
			return NextAction{Kind: ActionRepairRequired, StepID: p.StepID,
				Detail: fmt.Sprintf("step %s recorded %s (review %q); an authorized repair must reopen it", p.StepID, p.Status, p.ReviewVerdict)}
		}
		if p.RHS && p.Approved != nil && !*p.Approved {
			return NextAction{Kind: ActionRevisionRequired, StepID: p.StepID, RHS: true,
				Detail: fmt.Sprintf("step %s's latest artifact was rejected by an authenticated human; revise and re-record this step before anything later can run", p.StepID)}
		}
		if p.RHS && p.Approved == nil {
			return NextAction{Kind: ActionApprovalRequired, StepID: p.StepID,
				Detail: fmt.Sprintf("step %s awaits an authenticated human approval of its latest artifact", p.StepID)}
		}
	}
	return NextAction{Kind: ActionComplete,
		Detail: "every step of the current generation is completed; wrapping the work item remains an explicit lifecycle act"}
}

// NextAction is one classified next step of a pinned workflow.
type NextAction struct {
	Kind   ActionKind `json:"kind"`
	StepID string     `json:"step_id,omitempty"`
	RHS    bool       `json:"rhs,omitempty"`
	Detail string     `json:"detail,omitempty"`
}

// Next returns the runnable step ID, or an error explaining the hold. It is
// the error-shaped projection of NextAction for callers that only need
// "can anything run"; classification is NextAction's job.
func (s *State) Next() (string, error) {
	a := s.NextAction()
	switch a.Kind {
	case ActionRun:
		return a.StepID, nil
	case ActionComplete:
		return "", nil
	default:
		return "", fmt.Errorf("%s: %s", a.Kind, a.Detail)
	}
}
