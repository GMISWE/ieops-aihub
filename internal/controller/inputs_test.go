package controller

// inputs_test.go — the aihub#725 review_fix SF4 red-green pin for the
// workflow artifact READ path.
//
// The digest a completed result records is computed over the worker's number
// LITERALS (the strict envelope decode is UseNumber, the sink hashes and
// stores those bytes, the server recomputes over a UseNumber decode of its
// stored copy). ResolveInputs recomputes the same digest from the artifact it
// reads back — and it used to read through a float64 decode, which silently
// rewrites exactly the literals the rest of the pipeline treats as distinct:
//
//   - a trailing-zero decimal: stored "1.50" → float64 1.5 → re-marshal "1.5"
//   - an integer beyond 2^53: stored "12345678901234567890" → float64 →
//     re-marshal "12345678901234567168"
//
// Both make a correctly-recorded artifact fail resolution HERE and only here.
// The green arm below pins that the read path (UseNumber, from pkg/client's
// GetMemory and the RawMessage adapter in structuredArtifactPayload) resolves
// the same artifact; the red arm re-reads through the float64 universe the
// old path used and shows the fixture still discriminates.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// literalPayloadAttrs is a stored-attrs RawMessage whose structured_payload
// carries literals only a UseNumber decode preserves: 1.50 and an integer
// beyond 2^53.
func literalPayloadAttrs() json.RawMessage {
	return json.RawMessage(`{"structured_payload":{"ratio":1.50,"big":12345678901234567890,"summary":"done"}}`)
}

// rawMemoryArtifact is the fake ArtifactReader's answer for one artifact id:
// the raw map an in-process adapter (or a client that handed back raw JSON)
// would produce, with attrs as a RawMessage.
func rawMemoryArtifact(attrs json.RawMessage) map[string]any {
	return map[string]any{
		"id":           "mem_lit_1",
		"work_item_id": "wi_lit",
		"attrs":        attrs,
	}
}

// literalInputState pins the recorded digest at the byte-level form the sink
// would have filled: sha256 over the exact literal bytes.
func literalInputState() *State {
	return &State{
		WorkItemID: "wi_lit",
		Progress: []Progress{{
			StepID: "spec",
			Artifact: workflow.ArtifactRef{
				ID: "mem_lit_1", Version: 1,
				Hash: WorkflowArtifactHashBytes([]byte(`{"big":12345678901234567890,"ratio":1.50,"summary":"done"}`)),
			},
		}},
	}
}

// fakeArtifactReaderFunc adapts a function to the ArtifactReader seam.
type fakeArtifactReaderFunc func(ctx context.Context, id string) (map[string]any, error)

func (f fakeArtifactReaderFunc) GetMemory(ctx context.Context, id string) (map[string]any, error) {
	return f(ctx, id)
}

var _ ArtifactReader = fakeArtifactReaderFunc(nil)

// TestResolveInputsKeepsNumberLiteralsThroughTheReadPath is the green arm:
// the digest recomputed from the read-back artifact equals the recorded one,
// because the read decodes with json.Number semantics — the same universe
// the sink hashed, the server stored, and the server's record-time recompute
// reads. Before SF4 this failed: the plain json.Unmarshal re-decode rewrote
// 1.50 to 1.5 and the big integer to a float-rounded spelling, so resolution
// answered "digest mismatch" for an artifact every other path accepts.
func TestResolveInputsKeepsNumberLiteralsThroughTheReadPath(t *testing.T) {
	api := fakeArtifactReaderFunc(func(ctx context.Context, id string) (map[string]any, error) {
		return rawMemoryArtifact(literalPayloadAttrs()), nil
	})
	inputs, err := ResolveInputs(context.Background(), api, literalInputState(), []workflow.InputRef{
		{Name: "spec", StepID: "spec", Output: "summary"},
	})
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	if len(inputs) != 1 || inputs[0].Value != "done" {
		t.Fatalf("inputs = %+v, want the resolved summary output", inputs)
	}
	if inputs[0].Artifact.ID != "mem_lit_1" {
		t.Fatalf("resolved artifact = %+v", inputs[0].Artifact)
	}
}

// TestFloat64DecodeCannotReproduceTheLiteralDigest is the red arm, kept so
// the green test can never pass vacuously: it re-reads the SAME stored attrs
// through the float64 universe the old read path used and shows the digest
// diverges — i.e. the green test above pins a real property of the UseNumber
// decode, not an accident of the fixture.
func TestFloat64DecodeCannotReproduceTheLiteralDigest(t *testing.T) {
	var attrsMap map[string]any
	if err := json.Unmarshal(literalPayloadAttrs(), &attrsMap); err != nil {
		t.Fatal(err)
	}
	floatPayload := attrsMap["structured_payload"].(map[string]any)
	literalDigest := literalInputState().Progress[0].Artifact.Hash
	if got := WorkflowArtifactHash(floatPayload); got == literalDigest {
		t.Fatalf("float64 decode reproduces the literal digest %q; the fixture no longer discriminates", got)
	}
}

// TestResolveInputsRefusesDigestMismatch still holds with the tightened
// read path: a stored payload that genuinely differs from the recorded
// digest (not a re-spelling of the same literals) must refuse.
func TestResolveInputsRefusesDigestMismatch(t *testing.T) {
	api := fakeArtifactReaderFunc(func(ctx context.Context, id string) (map[string]any, error) {
		return rawMemoryArtifact(json.RawMessage(`{"structured_payload":{"summary":"tampered"}}`)), nil
	})
	_, err := ResolveInputs(context.Background(), api, literalInputState(), []workflow.InputRef{
		{Name: "spec", StepID: "spec", Output: "summary"},
	})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("ResolveInputs err = %v, want a digest mismatch refusal", err)
	}
}
