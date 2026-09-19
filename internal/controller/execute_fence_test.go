package controller

// execute_fence_test.go — pure pins for stripSingleCodeFence, the bounded
// single-fence tolerance added by aihub#724.
//
// What these tests pin, in the terms of the fix:
//
//   - the ONE accepted shape: the entire trimmed payload is exactly one outer
//     ``` or ```json fence around a non-empty body — the measured GLM output
//     of drain run 20260919T102517Z, whose honest StepResult was refused
//     byte-zero by the strict decode;
//   - every other shape returns false and the caller's original bytes stay
//     untouched (prose around the fence, double fences, ```yaml, ```JSON,
//     indented fence lines, a fence with no body, bare JSON, empty input);
//   - the strip is a pre-transform only, never a bypass: a fenced StepResult
//     decodes through the same strict #725 envelope, so an unknown top-level
//     key inside the fence is still refused, and a fenced valid StepResult
//     decodes.
//
// No server, no harness, no database — stripSingleCodeFence is a pure
// function and the strict decode is exercised in-process.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// validStepResultJSON is one minimal, contract-legal StepResult envelope used
// by the integration cases below: every top-level key is a known key, so the
// #725 strict decode accepts it once the fence is stripped.
func validStepResultJSON() string {
	return `{"status":"completed","work_item_id":"aihub#724","flow_version":1,` +
		`"step_id":"code_change","step_attempt_id":"01JSAQ724CODESTEP0002A",` +
		`"epoch":1,"producer_id":"pr_1","artifact":{"id":"mem_x","version":1,"hash":"deadbeef"}}`
}

func TestStripSingleCodeFenceAcceptedShapes(t *testing.T) {
	body := `{"status":"completed","work_item_id":"aihub#724"}`
	cases := []struct {
		name string
		in   string
	}{
		{"json-tagged fence", "```json\n" + body + "\n```"},
		{"bare fence", "```\n" + body + "\n```"},
		{"outer whitespace trimmed", "\n  ```json\n" + body + "\n```\n"},
		{"crlf fence lines", "```json\r\n" + body + "\r\n```"},
		{"trailing spaces on fence lines", "```json  \n" + body + "\n```   "},
		{"body kept verbatim with blank lines", "```json\n" + body + "\n\n```"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, ok := stripSingleCodeFence([]byte(tc.in))
			if !ok {
				t.Fatalf("stripSingleCodeFence(%q) = false; want true", tc.in)
			}
			trimmed := string(bytes.TrimSpace(inner))
			if trimmed != body {
				t.Fatalf("stripped body = %q; want %q", trimmed, body)
			}
		})
	}
}

func TestStripSingleCodeFenceRefusedShapes(t *testing.T) {
	body := `{"status":"completed","work_item_id":"aihub#724"}`
	cases := []struct {
		name string
		in   string
	}{
		{"prose after fence", "```json\n" + body + "\n```\nDone."},
		{"prose before fence", "Here is the result:\n```json\n" + body + "\n```"},
		{"double fence", "```json\n```json\n" + body + "\n```\n```"},
		{"yaml tag", "```yaml\n" + body + "\n```"},
		{"uppercase JSON tag", "```JSON\n" + body + "\n```"},
		{"no closing fence", "```json\n" + body},
		{"closing fence not last line", "```json\n" + body + "\n```\nextra"},
		{"indented closing fence", "```json\n" + body + "\n  ```"},
		{"fence with empty body", "```json\n```"},
		{"bare json", body},
		{"empty input", ""},
		{"whitespace only", "   \n\t\n"},
		{"lone opening fence", "```json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, ok := stripSingleCodeFence([]byte(tc.in))
			if ok {
				t.Fatalf("stripSingleCodeFence(%q) = (%q, true); want false", tc.in, inner)
			}
			if inner != nil {
				t.Fatalf("stripSingleCodeFence refused but returned non-nil inner %q; refusing must not touch the input", inner)
			}
		})
	}
}

// TestStripSingleCodeFenceRefusalLeavesOriginalBytes: the contract the decode
// site depends on — a refusal means the caller proceeds with its ORIGINAL
// bytes, so a fenced-with-prose payload reaches the strict decode fence-first
// and is refused with the same pre-aihub#724 error.
func TestStripSingleCodeFenceRefusalLeavesOriginalBytes(t *testing.T) {
	original := []byte("```json\n{}\n```\ntrailing prose")
	probe := append([]byte(nil), original...)
	if _, ok := stripSingleCodeFence(probe); ok {
		t.Fatal("fenced payload with trailing prose must be refused")
	}
	if !bytes.Equal(probe, original) {
		t.Fatal("stripSingleCodeFence mutated the caller's bytes on refusal; it must leave them untouched")
	}
}

// TestFencedProseInsideStillRefusedByStrictDecode: prose INSIDE the fence is
// a fence-shape the strip accepts (it binds only the fence lines), so the
// refusal must come from the strict decode: the stripped candidate is not one
// JSON value, and json.Unmarshal refuses it exactly as it would unfenced
// prose. Strip tolerance never widens into prose parsing.
func TestFencedProseInsideStillRefusedByStrictDecode(t *testing.T) {
	payload := []byte("```json\nResult below\n{\"status\":\"completed\"}\n```")
	if inner, ok := stripSingleCodeFence(payload); !ok {
		t.Fatal("single outer fence with prose body must strip; the refusal belongs to the strict decode")
	} else {
		payload = inner
	}
	var result workflow.StepResult
	if err := json.Unmarshal(bytes.TrimSpace(payload), &result); err == nil {
		t.Fatal("fenced prose-then-JSON decoded; the strict decode must refuse any payload that is not one JSON value")
	}
}

// TestFencedStepResultDecodesStrictly: the measured aihub#724 failure shape —
// a complete, honest StepResult wrapped in one ```json fence — now decodes
// through strip + the strict envelope.
func TestFencedStepResultDecodesStrictly(t *testing.T) {
	fenced := "```json\n" + validStepResultJSON() + "\n```"
	payload := []byte(fenced)
	if inner, ok := stripSingleCodeFence(payload); !ok {
		t.Fatal("single-fenced StepResult must strip")
	} else {
		payload = inner
	}
	var result workflow.StepResult
	if err := json.Unmarshal(bytes.TrimSpace(payload), &result); err != nil {
		t.Fatalf("fenced StepResult failed strict decode after strip: %v", err)
	}
	if result.Status != workflow.StatusCompleted {
		t.Fatalf("decoded status = %q; want completed", result.Status)
	}
	if result.StepAttemptID != "01JSAQ724CODESTEP0002A" {
		t.Fatalf("decoded step_attempt_id = %q", result.StepAttemptID)
	}
}

// TestFencedStepResultStillRefusesUnknownKey: stripping is a pre-transform,
// never a bypass. The #725 strict decode (workflow.StepResult.UnmarshalJSON)
// must still refuse an envelope carrying an unknown top-level key when that
// envelope arrives inside a fence — and must still refuse the same envelope
// unfenced, proving the two paths converge.
func TestFencedStepResultStillRefusesUnknownKey(t *testing.T) {
	smuggled := strings.TrimSuffix(validStepResultJSON(), "}") +
		`,"bogus_key":"x"}`
	for name, in := range map[string]string{
		"fenced":   "```json\n" + smuggled + "\n```",
		"unfenced": smuggled,
	} {
		payload := []byte(in)
		if inner, ok := stripSingleCodeFence(payload); ok {
			payload = inner
		}
		var result workflow.StepResult
		if err := json.Unmarshal(bytes.TrimSpace(payload), &result); err == nil {
			t.Fatalf("%s envelope with unknown key decoded; the strict decode must refuse it after the strip", name)
		}
	}
}

// TestFencedStepResultStillRefusesApprovalSmuggling: the other #725 wall — an
// envelope that attempts to carry an approval is refused inside a fence too.
func TestFencedStepResultStillRefusesApprovalSmuggling(t *testing.T) {
	smuggled := strings.TrimSuffix(validStepResultJSON(), "}") + `,"approval":{}}`
	payload := []byte("```json\n" + smuggled + "\n```")
	if inner, ok := stripSingleCodeFence(payload); !ok {
		t.Fatal("envelope must still strip before the refusal it deserves")
	} else {
		payload = inner
	}
	var result workflow.StepResult
	if err := json.Unmarshal(bytes.TrimSpace(payload), &result); err == nil {
		t.Fatal("approval-smuggling envelope decoded; the strict decode must refuse it inside a fence")
	}
}
