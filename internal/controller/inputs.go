package controller

// inputs.go resolves a workflow step's symbolic predecessor references to the
// exact values recorded by those predecessors.  A descriptor such as
// {step_id:"spec",output:"spec"} is composition metadata, not usable prompt
// input; dispatching it directly asks the worker to invent the missing value.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// ArtifactReader is the access-checked artifact read needed by both the
// unattended and session drivers. *client.Client satisfies it through
// GET /v1/memories/:id; that endpoint applies the caller's project and memory
// visibility checks before returning content or structured_payload.
type ArtifactReader interface {
	GetMemory(context.Context, string) (map[string]any, error)
}

// ResolvedInput is a concrete predecessor value together with the immutable
// artifact identity it came from. The value, not merely the descriptor, is
// placed in a worker/session prompt.
type ResolvedInput struct {
	Name     string               `json:"name"`
	StepID   string               `json:"step_id"`
	Output   string               `json:"output"`
	Artifact workflow.ArtifactRef `json:"artifact"`
	Value    any                  `json:"value"`
}

// ResolveInputs resolves every symbolic input in refs against the latest
// recorded artifact in state. It fails closed when a producer has no exact
// artifact, the caller cannot read it, structured output is absent, the named
// output is absent, or the stored value no longer hashes to the result's
// artifact digest.
func ResolveInputs(ctx context.Context, api ArtifactReader, state *State, refs []workflow.InputRef) ([]ResolvedInput, error) {
	if len(refs) == 0 {
		return []ResolvedInput{}, nil
	}
	if api == nil || state == nil {
		return nil, fmt.Errorf("resolve workflow inputs: artifact reader and workflow state are required")
	}
	out := make([]ResolvedInput, 0, len(refs))
	for _, ref := range refs {
		p := progressFor(state, ref.StepID)
		if p == nil || p.Artifact.ID == "" || p.Artifact.Version <= 0 || p.Artifact.Hash == "" {
			return nil, fmt.Errorf("resolve input %q: producer step %q has no exact recorded artifact", ref.Name, ref.StepID)
		}
		raw, err := api.GetMemory(ctx, p.Artifact.ID)
		if err != nil {
			return nil, fmt.Errorf("resolve input %q from artifact %s: %w", ref.Name, p.Artifact.ID, err)
		}
		artifactWI, _ := raw["work_item_id"].(string)
		if artifactWI != state.WorkItemID {
			return nil, fmt.Errorf("resolve input %q: artifact %s belongs to work item %q, not %q", ref.Name, p.Artifact.ID, artifactWI, state.WorkItemID)
		}
		payload, err := structuredArtifactPayload(raw)
		if err != nil {
			return nil, fmt.Errorf("resolve input %q from artifact %s: %w", ref.Name, p.Artifact.ID, err)
		}
		if got := WorkflowArtifactHash(payload); got != p.Artifact.Hash {
			return nil, fmt.Errorf("resolve input %q: artifact %s digest mismatch (recorded %s, recomputed %s)", ref.Name, p.Artifact.ID, p.Artifact.Hash, got)
		}
		value, ok := payload[ref.Output]
		if !ok {
			return nil, fmt.Errorf("resolve input %q: artifact %s has no recorded output %q", ref.Name, p.Artifact.ID, ref.Output)
		}
		out = append(out, ResolvedInput{Name: ref.Name, StepID: ref.StepID, Output: ref.Output, Artifact: p.Artifact, Value: value})
	}
	return out, nil
}

func structuredArtifactPayload(raw map[string]any) (map[string]any, error) {
	attrs, ok := raw["attrs"].(map[string]any)
	if !ok {
		// Client maps produced by json.Unmarshal normally use map[string]any;
		// tolerate a RawMessage in fakes and in-process adapters. The decode
		// is UseNumber (aihub#725 review_fix SF4): the digest below must
		// recompute over the STORED literals — the same number universe the
		// server's record-time recompute uses — so a trailing-zero decimal or
		// an integer beyond 2^53 cannot be silently rewritten into a different
		// digest by a float64 decode here.
		if b, ok := raw["attrs"].(json.RawMessage); ok {
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			if err := dec.Decode(&attrs); err != nil {
				return nil, fmt.Errorf("attrs do not decode: %w", err)
			}
		}
	}
	if attrs == nil {
		return nil, fmt.Errorf("artifact has no attrs object")
	}
	payload, ok := attrs["structured_payload"].(map[string]any)
	if !ok || payload == nil {
		return nil, fmt.Errorf("artifact has no structured_payload object")
	}
	return payload, nil
}

// WorkflowArtifactHash is the digest used by workflow artifacts: sha256 over
// encoding/json's deterministic object serialization. The full output object
// is stored as attrs.structured_payload, so readers can recompute this
// digest before projecting any named output.
func WorkflowArtifactHash(output map[string]any) string {
	b, _ := json.Marshal(output)
	return WorkflowArtifactHashBytes(b)
}

// WorkflowArtifactHashBytes is the byte-level form of the same digest rule:
// sha256 over encoding/json's deterministic serialization, given the already
// serialized bytes. The controller sink (sinkWorkerOutput, aihub#725 review_fix
// SF2/SF4) hashes the exact bytes it sends as structured_payload — which
// json.Marshal of a UseNumber-decoded output produced — so the server's
// record-time recompute over the stored copy reproduces the same digest. A
// map-in form that re-marshals to these bytes (the session path, ResolveInputs)
// yields the identical answer, which is what keeps the two entry points one
// rule rather than two.
func WorkflowArtifactHashBytes(serialized []byte) string {
	sum := sha256.Sum256(serialized)
	return "sha256:" + hex.EncodeToString(sum[:])
}
