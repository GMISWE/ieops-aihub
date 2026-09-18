package domain

// workflow.go — the WI-owned workflow (aihub#708 Batch 2A): request types, the
// server's capability→grant policy, and the atomic latest-accessible→pin
// resolution that every workflow generation goes through.
//
// The pure contracts live in internal/workflow (Batch 1B); the registry they
// bind against lives in internal/skillregistry + skill_registry*.go (Batch 1A).
// This file is the authorized, transactional half that turns a caller's step
// specs into an immutable generation row (migration 0044).
//
// ─── The three rules every path here implements ─────────────────────────────
//
//  1. GRANTS ARE SERVER-DERIVED, NEVER REQUESTED (spec D6/D7). The caller's
//     step spec carries no authority field at all; workflowGrantForContract
//     derives each step's grant from the RESOLVED contract's capability set.
//     A client cannot widen what a step may do because there is no input that
//     reaches the grant.
//
//  2. PINNING IS ATOMIC (spec D4). "latest accessible" refs are resolved to
//     exact (skill_id, version) pairs INSIDE the caller's transaction, using
//     skillVersionAccessSQL — the one accessibility predicate — so a WI that
//     pins a flow either exists with a fully validated generation or does not
//     exist at all. An invalid composition (unknown/inaccessible skill,
//     duplicate ids, malformed params, unresolvable inputs, interactive-only
//     step in an unattended flow, missing gates) refuses the whole create or
//     revision with a 400 naming the reason; nothing partial is written.
//
//  3. WI VISIBILITY NEVER WIDENS SKILL CONTENT (spec D3). A generation stores
//     references and derived metadata (capabilities, interactive flag) — never
//     bundles, schemas or any skill body. Reading a work item's workflow
//     discloses which skill versions the flow pins; it discloses no registry
//     content, and content remains reachable only through the registry's own
//     access-checked endpoints.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	wf "github.com/GMISWE/ieops-aihub/internal/workflow"
)

// workflowQuerier is the read surface both pgx.Tx and *pgxpool.Pool satisfy, so
// resolution can run inside the caller's transaction (create/revise) without a
// second code path.
type workflowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// WorkflowStepSpec is the request form of one step: what a caller of
// POST /v1/work_items (steps=...) or PUT /v1/work_items/:id/workflow sends.
// It is NOT the stored flow: SkillVersion 0 means "resolve to the latest
// version ACCESSIBLE TO THE CALLER" and is replaced by the exact pinned
// version before anything is written.
type WorkflowStepSpec struct {
	ID           string              `json:"id"`
	SkillID      string              `json:"skill_id"`
	SkillVersion int                 `json:"skill_version"`
	RHS          *bool               `json:"rhs"`
	Models       []wf.ModelCandidate `json:"models"`
	Params       json.RawMessage     `json:"params,omitempty"`
	Inputs       []wf.InputRef       `json:"inputs,omitempty"`
}

// wiStepMeta is the per-step semantic metadata frozen into the generation:
// what the resolved contract DECLARES, not its content. Result validation and
// the controller's policy assembly read this instead of re-fetching the
// registry, which is what lets a running execution finish under an
// already-authorized invocation (spec D3) while the same generation stays
// interpretable forever (append-only rule).
type wiStepMeta struct {
	Capabilities []skillregistry.Capability `json:"capabilities"`
	Interactive  bool                       `json:"interactive"`
}

// workflowGrantForContract derives one step's execution grant from its resolved
// registry contract. This IS the server's capability policy (spec D6): the
// semantic execution contract is typed metadata, and the trusted
// ExecutionContext in internal/workflow is built from exactly this mapping —
// never from anything the caller sent.
//
//	gate capabilities (review, verification)        → read_only + independent:
//	  a gate's whole value is that a DIFFERENT producer inspected the work, so
//	  producer isolation is the grant, and a gate never holds write authority.
//	shipping, authoring                             → write + shared: these are
//	  the producers whose output later gates inspect.
//	deterministic_operation                         → read_only + shared: bounded
//	  operations (diff, cleanup) inspect or maintain the work; they do not
//	  produce the artifact a gate exists to check, and their writes are
//	  authorized at the harness layer, not by the flow contract.
//
// A contract that declares BOTH a gate and a producer capability is refused
// fail-closed: such a skill cannot sit honestly on either side of a gate, and
// granting either side would under-describe it.
func workflowGrantForContract(contract skillregistry.SkillContract) (wf.StepGrant, error) {
	var gate, producer, shipping, authoring bool
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
		return wf.StepGrant{}, fmt.Errorf(
			"contract declares both gate (%s/%s) and producer capabilities; a step cannot gate work it produces",
			skillregistry.CapReview, skillregistry.CapVerification)
	}
	switch {
	case gate:
		return wf.StepGrant{Authority: wf.AuthorityReadOnly, ProducerIsolation: wf.IsolationRequired}, nil
	case shipping, authoring:
		return wf.StepGrant{Authority: wf.AuthorityWrite, ProducerIsolation: wf.IsolationShared}, nil
	default:
		// deterministic_operation, and nothing else: the capability vocabulary
		// is closed (ValidateContract), so this default can only be reached by
		// a pure deterministic operation.
		return wf.StepGrant{Authority: wf.AuthorityReadOnly, ProducerIsolation: wf.IsolationShared}, nil
	}
}

// workflowPinOutcome is what pinWorkflowGeneration produced: the validated
// flow plus the frozen grants/metadata/digest that make up the generation row.
type workflowPinOutcome struct {
	Validated *wf.ValidatedFlow
	Grants    map[string]wf.StepGrant
	StepMeta  map[string]wiStepMeta
	Digest    string
}

// workflowRevisionInput is the shared, already-authorized description of one
// generation a caller wants to pin.
type workflowRevisionInput struct {
	Caller               *UserRecord
	RequiresHumanSession bool
	Steps                []WorkflowStepSpec
}

// resolvedSkillRef is one exact, access-checked registry resolution.
type resolvedSkillRef struct {
	version  int
	contract skillregistry.SkillContract
}

// pinWorkflowGeneration resolves the caller's step specs to exact registry
// bindings THROUGH THE CALLER'S TRANSACTION, validates the composition with the
// pure policy, and returns everything the generation row stores. It performs no
// writes; the caller inserts the row and moves the pointer in the same
// transaction, which is what makes the pin atomic (spec D4).
//
// Access is the registry's single predicate recomputed here, so pinning a
// version the caller cannot read is refused exactly like a registry GET —
// no-oracle: an unknown skill and an inaccessible one answer the same way.
func pinWorkflowGeneration(ctx context.Context, q workflowQuerier, stepsVersion int, in workflowRevisionInput) (*workflowPinOutcome, *AihubError) {
	if in.Caller == nil || in.Caller.ID == "" {
		return nil, NewErr(ErrInternalError, "workflow pinning requires an authenticated caller")
	}
	if stepsVersion < 1 {
		return nil, NewErr(ErrInternalError, "workflow generation number must be positive")
	}
	if len(in.Steps) == 0 {
		return nil, NewErr(ErrBadRequest, "a workflow needs at least one step")
	}

	// Resolve every DISTINCT skill ref once. SkillVersion 0 = "latest
	// accessible"; a positive version must be accessible exactly as named.
	resolved := make(map[string]*resolvedSkillRef)
	for _, spec := range in.Steps {
		if spec.SkillID == "" {
			return nil, NewErr(ErrBadRequest, fmt.Sprintf("step %q has no skill_id", spec.ID))
		}
		key := fmt.Sprintf("%s@%d", spec.SkillID, spec.SkillVersion)
		if _, seen := resolved[key]; seen {
			continue
		}
		ref, aerr := resolveWorkflowSkillRef(ctx, q, in.Caller, spec.SkillID, spec.SkillVersion)
		if aerr != nil {
			return nil, aerr
		}
		resolved[key] = ref
	}

	// Build the pinned flow and the bindings, then let the pure package judge
	// the composition: ids, model candidates, params against the resolved
	// schemas, input compatibility between producers and consumers, gate
	// requirements — everything the spec calls "invalid composition".
	flow := wf.Flow{Version: stepsVersion, Steps: make([]wf.Step, 0, len(in.Steps))}
	bindings := make([]wf.SkillBinding, 0, len(resolved))
	grants := make(map[string]wf.StepGrant, len(in.Steps))
	stepMeta := make(map[string]wiStepMeta, len(in.Steps))
	for _, spec := range in.Steps {
		ref := resolved[fmt.Sprintf("%s@%d", spec.SkillID, spec.SkillVersion)]
		pinned := wf.Step{
			ID:           spec.ID,
			SkillID:      spec.SkillID,
			SkillVersion: ref.version,
			RHS:          spec.RHS,
			Models:       append([]wf.ModelCandidate(nil), spec.Models...),
			Params:       append(json.RawMessage(nil), spec.Params...),
			Inputs:       append([]wf.InputRef(nil), spec.Inputs...),
		}
		flow.Steps = append(flow.Steps, pinned)
		bindings = append(bindings, wf.SkillBinding{
			SkillID:  spec.SkillID,
			Version:  ref.version,
			Contract: ref.contract,
		})
		grant, gerr := workflowGrantForContract(ref.contract)
		if gerr != nil {
			return nil, NewErr(ErrBadRequest, fmt.Sprintf("step %q: %v", spec.ID, gerr))
		}
		grants[spec.ID] = grant
		stepMeta[spec.ID] = wiStepMeta{
			Capabilities: append([]skillregistry.Capability(nil), ref.contract.Capabilities...),
			Interactive:  ref.contract.Runtime.Interactive,
		}
	}

	validated, verr := wf.Validate(flow, bindings, wf.ExecutionContext{Grants: grants})
	if verr != nil {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("workflow composition is invalid: %v", verr))
	}
	// WI-level RHS composition rule, the server's own (the pure Validate sees
	// only per-step RHS): an interactive-only step in a requires_human_session
	// = false flow can never be driven to a decision (Decide refuses it), so
	// pinning one is refused up front rather than pinning an unrunnable flow.
	if !in.RequiresHumanSession {
		for _, spec := range in.Steps {
			if meta := stepMeta[spec.ID]; meta.Interactive {
				return nil, NewErr(ErrBadRequest, fmt.Sprintf(
					"step %q resolves to an interactive-only skill, which cannot run in a work item with requires_human_session=false",
					spec.ID))
			}
		}
	}

	digest, aerr := workflowGenerationDigest(stepsVersion, in.RequiresHumanSession, flow, grants, stepMeta)
	if aerr != nil {
		return nil, aerr
	}
	return &workflowPinOutcome{
		Validated: validated,
		Grants:    grants,
		StepMeta:  stepMeta,
		Digest:    digest,
	}, nil
}

// resolveWorkflowSkillRef resolves one skill ref — an exact version, or the
// caller's latest ACCESSIBLE version for 0 — through skillVersionAccessSQL
// inside the caller's transaction. Unknown and inaccessible answer the same
// NOT_FOUND (the registry's no-oracle rule, held here for the pin path too).
func resolveWorkflowSkillRef(ctx context.Context, q workflowQuerier, caller *UserRecord, skillID string, version int) (*resolvedSkillRef, *AihubError) {
	if aerr := skillRequireReadCaller(caller); aerr != nil {
		return nil, aerr
	}

	var resolvedVersion int
	var contractRaw []byte
	if version > 0 {
		pred, predArgs, _ := skillVersionAccessSQL("sv", caller, 3)
		args := append([]any{skillID, version}, predArgs...)
		err := q.QueryRow(ctx, fmt.Sprintf(
			`SELECT sv.version, sv.contract FROM skill_versions sv WHERE sv.skill_id = $1 AND sv.version = $2%s`,
			pred), args...).Scan(&resolvedVersion, &contractRaw)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, NewErr(ErrNotFound, "skill version not found")
			}
			return nil, dbErrCause(err, "resolve workflow skill ref")
		}
	} else {
		pred, predArgs, _ := skillVersionAccessSQL("sv", caller, 2)
		args := append([]any{skillID}, predArgs...)
		err := q.QueryRow(ctx, fmt.Sprintf(
			`SELECT sv.version, sv.contract FROM skill_versions sv WHERE sv.skill_id = $1%s
			 ORDER BY sv.version DESC LIMIT 1`, pred), args...).Scan(&resolvedVersion, &contractRaw)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The skill does not exist, or no version of it is accessible
				// to this caller. One answer for both, as everywhere in the
				// registry.
				return nil, NewErr(ErrNotFound, "skill version not found")
			}
			return nil, dbErrCause(err, "resolve workflow skill ref")
		}
	}
	contract, cerr := skillregistry.DecodeContract(contractRaw)
	if cerr != nil {
		// A version in the registry with an invalid contract should be
		// impossible (publication validates), so this is a data defect: fail
		// closed rather than pin against a contract we cannot interpret.
		return nil, NewErr(ErrInternalError, fmt.Sprintf("stored skill contract is invalid: %v", cerr))
	}
	return &resolvedSkillRef{version: resolvedVersion, contract: *contract}, nil
}

// resolvePinnedSkillRefTrusted loads the immutable contract for an already-pinned
// invocation without applying caller visibility. It is an internal server-side
// lookup only: callers never receive this row, and the public registry access
// predicate remains mandatory for pinning and every new invocation.
func resolvePinnedSkillRefTrusted(ctx context.Context, q workflowQuerier, skillID string, version int) (*resolvedSkillRef, *AihubError) {
	var resolvedVersion int
	var contractRaw []byte
	err := q.QueryRow(ctx, `
		SELECT version, contract FROM skill_versions
		WHERE skill_id = $1 AND version = $2`, skillID, version).Scan(&resolvedVersion, &contractRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrInternalError, "pinned workflow skill version is missing")
		}
		return nil, dbErrCause(err, "resolve pinned workflow skill contract")
	}
	contract, cerr := skillregistry.DecodeContract(contractRaw)
	if cerr != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("stored skill contract is invalid: %v", cerr))
	}
	return &resolvedSkillRef{version: resolvedVersion, contract: *contract}, nil
}

// workflowGenerationDigest
// everything a generation freezes, so a reader can verify the pointer's
// integrity without trusting whoever moved it.
func workflowGenerationDigest(stepsVersion int, rhs bool, flow wf.Flow, grants map[string]wf.StepGrant, stepMeta map[string]wiStepMeta) (string, *AihubError) {
	flowJSON, err := json.Marshal(flow)
	if err != nil {
		return "", NewErr(ErrInternalError, "workflow does not serialize")
	}
	grantsJSON, mErr := json.Marshal(grants)
	if mErr != nil {
		return "", NewErr(ErrInternalError, "workflow grants do not serialize")
	}
	metaJSON, mmErr := json.Marshal(stepMeta)
	if mmErr != nil {
		return "", NewErr(ErrInternalError, "workflow metadata does not serialize")
	}
	h := sha256.New()
	h.Write([]byte{byte(stepsVersion >> 24), byte(stepsVersion >> 16), byte(stepsVersion >> 8), byte(stepsVersion)})
	if rhs {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	h.Write(flowJSON)
	h.Write(grantsJSON)
	h.Write(metaJSON)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
