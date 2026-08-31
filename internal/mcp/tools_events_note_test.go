package mcp

// Unit tests for the fused closing note (aihub#290). In-package because the
// functions under test are unexported; the end-to-end behaviour lives in
// tools_fusion_test.go (package mcp_test).

import (
	"errors"
	"strings"
	"testing"
)

// TestNoteOutcomeSuffix covers the reporting that only the FAILURE path uses.
//
// When the terminal call fails after the note was already emitted, the caller is
// about to decide whether to retry — and retrying re-sends the note. The success
// path reports this through note_emitted; on the error path the message is the
// only channel there is, so "" would leave the caller unable to tell an
// already-recorded note from one that never landed.
func TestNoteOutcomeSuffix(t *testing.T) {
	if got := noteOutcomeSuffix(false, nil); got != "" {
		t.Errorf("no note requested must add nothing, got %q", got)
	}
	if got := noteOutcomeSuffix(false, errors.New("ignored")); got != "" {
		t.Errorf("no note requested must add nothing even with an error, got %q", got)
	}

	emitted := noteOutcomeSuffix(true, nil)
	if !strings.Contains(emitted, "WAS already recorded") || !strings.Contains(emitted, "second time") {
		t.Errorf("a recorded note must warn that a retry duplicates it, got %q", emitted)
	}

	failed := noteOutcomeSuffix(true, errors.New("event store down"))
	if !strings.Contains(failed, "NOT recorded") {
		t.Errorf("a failed note must say so, got %q", failed)
	}
	if !strings.Contains(failed, "event store down") {
		t.Errorf("a failed note must carry the cause, got %q", failed)
	}
	// The two outcomes must not be confusable by a substring check.
	if strings.Contains(emitted, "NOT recorded") {
		t.Errorf("the success wording must not contain the failure marker: %q", emitted)
	}
}

// TestNotePayloadShape pins the wire shape. Every note event ever written by
// pf_emit_event callers is {text: "..."}, and the UI renders that key; a fused
// note landing under a different key would be invisible there while still
// looking successful to the caller.
func TestNotePayloadShape(t *testing.T) {
	p := notePayload("wrapped: did the thing")
	if len(p) != 1 {
		t.Fatalf("note payload must carry exactly one key, got %v", p)
	}
	if got := p["text"]; got != "wrapped: did the thing" {
		t.Errorf(`payload["text"] = %v, want the note verbatim`, got)
	}
}

// TestApplyNoteResultDistinguishesAbsentFromFailed: "no note requested" and
// "note requested and lost" must not look alike on the response.
func TestApplyNoteResultDistinguishesAbsentFromFailed(t *testing.T) {
	absent := map[string]any{}
	applyNoteResult(absent, false, nil)
	if _, present := absent["note_emitted"]; present {
		t.Errorf("no note requested must leave note_emitted absent, got %v", absent)
	}

	ok := map[string]any{}
	applyNoteResult(ok, true, nil)
	if ok["note_emitted"] != true || ok["note_error"] != nil {
		t.Errorf("a recorded note must report note_emitted=true and no error, got %v", ok)
	}

	bad := map[string]any{}
	applyNoteResult(bad, true, errors.New("boom"))
	if bad["note_emitted"] != false {
		t.Errorf("a lost note must report note_emitted=false, got %v", bad)
	}
	if msg, _ := bad["note_error"].(string); !strings.Contains(msg, "boom") {
		t.Errorf("a lost note must carry the cause, got %v", bad["note_error"])
	}
}
