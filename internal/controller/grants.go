package controller

// grants.go — the controller's trusted half of the capability→grant policy
// for the DB workflow path (aihub#708 Batch 2B).
//
// internal/workflow's pure policy (Decide) consumes a *ValidatedFlow whose
// grants come from an ExecutionContext the TRUSTED controller establishes —
// "a contract saying 'shipping' declares semantics; it does not authorize
// writes". On the server, internal/domain derives those grants from each
// pinned contract at pin time and again at start time. This file is the
// controller-side derivation of the SAME mapping, so the pure policy can run
// where the flow executes, and so the server-minted grant that
// StartWorkflowStep returns can be CROSS-CHECKED against it: a divergence
// between the two copies of this mapping is a drift bug, and the cross-check
// (RunStep's ExpectedGrant) turns it into a loud refusal instead of a
// capability decision made by whichever copy drifted.
//
// The mapping is duplicated rather than imported because internal/domain is
// the server's transactional layer (pgx pools, row locks) and dragging it into
// a client-side controller couples this package to the database. The mirror
// is small, pure, and failure-mode-contained: the server never READS grants
// from this file, and this file never AUTHORIZES anything the server did not
// already mint — it only feeds the pure policy and the cross-check.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// GrantForContract derives one step's execution grant from its resolved
// registry contract, mirroring internal/domain's workflowGrantForContract:
//
//	gate capabilities (review, verification)  → read_only + independent
//	shipping, authoring                       → write + shared
//	deterministic_operation (and nothing else) → read_only + shared
//
// A contract declaring BOTH a gate and a producer capability is refused
// fail-closed, exactly as the server refuses it at pin time.
func GrantForContract(contract skillregistry.SkillContract) (workflow.StepGrant, error) {
	var gate, shipping, authoring, producer bool
	for _, c := range contract.Capabilities {
		switch c {
		case skillregistry.CapReview, skillregistry.CapVerification:
			gate = true
		case skillregistry.CapShipping, skillregistry.CapAuthoring:
			producer = true
			if c == skillregistry.CapShipping {
				shipping = true
			}
			if c == skillregistry.CapAuthoring {
				authoring = true
			}
		case skillregistry.CapDeterministicOperation:
			producer = true
		}
	}
	if gate && producer {
		return workflow.StepGrant{}, fmt.Errorf(
			"contract declares both gate (%s/%s) and producer capabilities; a step cannot gate work it produces",
			skillregistry.CapReview, skillregistry.CapVerification)
	}
	switch {
	case gate:
		return workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired}, nil
	case shipping, authoring:
		return workflow.StepGrant{Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared}, nil
	default:
		return workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationShared}, nil
	}
}

// LoadValidatedFlow resolves every pinned skill version of the CURRENT
// generation through the registry API, derives each step's grant with the
// same policy the server applies, and runs the pure authoring validation
// (workflow.Validate) over the result. It returns the ValidatedFlow that
// workflow.Decide consumes together with the derived per-step grants, which
// the driver cross-checks against every server-minted invocation grant
// (StepOptions.ExpectedGrant).
//
// A skill version the registry refuses is a hard error: the server re-checks
// access at start time anyway (spec D3), and a flow the controller cannot
// bind to contracts is a flow it cannot run the policy over.
func LoadValidatedFlow(ctx context.Context, api API, state *State) (*workflow.ValidatedFlow, map[string]workflow.StepGrant, error) {
	if state == nil || state.StepsVersion <= 0 || len(state.Steps.Steps) == 0 {
		return nil, nil, fmt.Errorf("load workflow: state has no pinned generation")
	}

	type refKey struct {
		id      string
		version int
	}
	contracts := make(map[refKey]*skillregistry.SkillContract)
	bindings := make([]workflow.SkillBinding, 0, len(state.Steps.Steps))
	grants := make(map[string]workflow.StepGrant, len(state.Steps.Steps))

	for _, step := range state.Steps.Steps {
		key := refKey{id: step.SkillID, version: step.SkillVersion}
		contract, seen := contracts[key]
		if !seen {
			raw, err := api.GetSkillVersion(ctx, step.SkillID, step.SkillVersion)
			if err != nil {
				return nil, nil, fmt.Errorf("resolve pinned skill %s@%d: %w", step.SkillID, step.SkillVersion, err)
			}
			b, err := json.Marshal(raw)
			if err != nil {
				return nil, nil, err
			}
			var sv struct {
				Contract json.RawMessage `json:"contract"`
			}
			if err := json.Unmarshal(b, &sv); err != nil {
				return nil, nil, err
			}
			decoded, err := skillregistry.DecodeContract(sv.Contract)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid pinned contract for %s@%d: %w", step.SkillID, step.SkillVersion, err)
			}
			contract = decoded
			contracts[key] = decoded
		}
		grant, err := GrantForContract(*contract)
		if err != nil {
			return nil, nil, fmt.Errorf("step %q: %w", step.ID, err)
		}
		grants[step.ID] = grant
		bindings = append(bindings, workflow.SkillBinding{SkillID: step.SkillID, Version: step.SkillVersion, Contract: *contract})
	}

	validated, err := workflow.Validate(state.Steps, bindings, workflow.ExecutionContext{Grants: grants})
	if err != nil {
		return nil, nil, fmt.Errorf("validate pinned generation %d: %w", state.StepsVersion, err)
	}
	return validated, grants, nil
}
