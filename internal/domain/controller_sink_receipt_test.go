package domain

// controller_sink_receipt_test.go — the pure (no-database) pins for the
// durable controller-sink replay receipt (aihub#725 review_fix B2).
//
// The receipt is what makes a lost sink save replayable instead of
// duplicable: attrs.controller_sink_receipt = {invocation_id,
// step_attempt_id, attempt_id} names ONE artifact, and Remember resolves it
// against the stored rows BEFORE dedup, embedding or INSERT. These tests pin
// the two halves that do not need a database: extraction/validation of the
// receipt from caller attrs, and the payload-equality verdict that decides
// replay (same row returned) from collision (409 refused). The query itself
// is pinned DB-gated in internal/server.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControllerSinkReceiptFromAttrs(t *testing.T) {
	// Absent: the ordinary memory-write path — no receipt, no error.
	for name, attrs := range map[string]string{
		"empty attrs":        ``,
		"no receipt key":     `{"similar_to":"mem_x"}`,
		"other attrs only":   `{"reinforcements":[],"structured_payload":{"a":1}}`,
		"not an object":      `[]`,
		"unparseable object": `{`,
	} {
		_, present, err := controllerSinkReceiptFromAttrs(json.RawMessage(attrs))
		require.NoError(t, err, name)
		require.False(t, present, name)
	}

	// Well-formed: the three trusted components come back intact.
	receipt, present, err := controllerSinkReceiptFromAttrs(json.RawMessage(
		`{"controller_sink_receipt":{"invocation_id":"inv_1","step_attempt_id":"sa_1","attempt_id":"ra_1"}}`))
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, ControllerSinkReceipt{InvocationID: "inv_1", StepAttemptID: "sa_1", AttemptID: "ra_1"}, receipt)

	// Malformed: only the controller sink writes this key, so a half-shape is
	// a bug in the one writer — refuse loudly (400) instead of storing it as
	// if it meant something.
	for name, attrs := range map[string]string{
		"empty components":  `{"controller_sink_receipt":{"invocation_id":"","step_attempt_id":"sa_1","attempt_id":"ra_1"}}`,
		"missing component": `{"controller_sink_receipt":{"invocation_id":"inv_1","step_attempt_id":"sa_1"}}`,
		"not an object":     `{"controller_sink_receipt":"inv_1"}`,
		"wrong value type":  `{"controller_sink_receipt":{"invocation_id":7,"step_attempt_id":"sa_1","attempt_id":"ra_1"}}`,
	} {
		_, present, err := controllerSinkReceiptFromAttrs(json.RawMessage(attrs))
		require.Error(t, err, name)
		require.False(t, present, name)
	}
}

func TestSameControllerSinkPayload(t *testing.T) {
	// Literal fidelity: the comparison decodes BOTH sides with UseNumber, so
	// byte differences that decode to the same JSON value are equal (key
	// order, spacing), and a real literal difference — 1.50 against 1.5 — is
	// a DIFFERENT payload, exactly as the digest pipeline treats it.
	stored := json.RawMessage(`{"structured_payload":{  "ratio" : 1.50, "big":12345678901234567890,"summary":"done"}}`)
	same, differentSpelling := json.RawMessage(`{"big":12345678901234567890,"ratio":1.50,"summary":"done"}`), json.RawMessage(`{"ratio":1.5,"big":12345678901234567890,"summary":"done"}`)

	require.True(t, sameControllerSinkPayload(stored, sameSpelling),
		"the same JSON value in a different byte spelling must compare equal")

	require.False(t, sameControllerSinkPayload(stored, differentSpelling),
		"1.50 and 1.5 are different payloads in the literal universe the digest pipeline uses")

	// Both sides absent of a structured_payload: equal (the ordinary compare
	// is content + type + payload, and payload may be empty on either side).
	require.True(t, sameControllerSinkPayload(json.RawMessage(`{}`), nil))

	// One side carries one and the other does not: not equal.
	require.False(t, sameControllerSinkPayload(stored, nil))
	require.False(t, sameControllerSinkPayload(json.RawMessage(`{}`), same))
}

// sameSpelling is the byte-level re-spelling arm's payload: same value, different bytes.
var sameSpelling = json.RawMessage(`{"ratio":1.50,"big":12345678901234567890,"summary":"done"}`)

// TestDecodeJSONUseNumberToleratesFloat64Range: the attrs decoders must
// accept every legal JSON number — 1e400 is legal JSON and overflows float64,
// and the OLD plain json.Unmarshal answer to it was a silent empty map (the
// aihub#725 review_fix SF3 swallow, which dropped every caller attr when one
// number was out of float64 range).
func TestDecodeJSONUseNumberToleratesFloat64Range(t *testing.T) {
	var attrs map[string]any
	require.NoError(t, decodeJSONUseNumber(json.RawMessage(`{"a":1e400,"b":2}`), &attrs))
	require.Equal(t, "1e400", attrs["a"].(json.Number).String())
	require.Equal(t, "2", attrs["b"].(json.Number).String())

	// And the same input through the OLD decoder shape fails — the red side
	// that keeps this fixture discriminating.
	var floatAttrs map[string]any
	require.Error(t, json.Unmarshal([]byte(`{"a":1e400,"b":2}`), &floatAttrs),
		"the fixture stopped discriminating: plain json.Unmarshal now accepts 1e400")
}
