package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCommitEntry_RoundTripNewFields verifies that all new aihub#124 fields
// survive a marshal/unmarshal round-trip and appear in the JSON output.
func TestCommitEntry_RoundTripNewFields(t *testing.T) {
	original := CommitEntry{
		ID:            "cm_1",
		AuthorUserID:  "u_1",
		AuthorDisplay: "Alice",
		Body:          "looks wrong",
		CreatedAt:     "2026-06-01T00:00:00Z",
		Anchor: &CommitAnchor{
			HeadingID:   "section-goals",
			HeadingText: "Goals",
		},
		Status:     CommitStatusResolved,
		Reply:      "Fixed in next revision.",
		ResolvedAt: "2026-06-02T00:00:00Z",
		ResolvedBy: "Bob",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify all new keys are present in JSON.
	for _, key := range []string{
		`"anchor"`, `"heading_id"`, `"heading_text"`,
		`"status"`, `"reply"`, `"resolved_at"`, `"resolved_by"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("marshaled JSON missing key %s; got: %s", key, data)
		}
	}

	var got CommitEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("ID mismatch: want %q got %q", original.ID, got.ID)
	}
	if got.Anchor == nil {
		t.Fatal("Anchor is nil after round-trip")
	}
	if got.Anchor.HeadingID != original.Anchor.HeadingID {
		t.Errorf("Anchor.HeadingID: want %q got %q", original.Anchor.HeadingID, got.Anchor.HeadingID)
	}
	if got.Anchor.HeadingText != original.Anchor.HeadingText {
		t.Errorf("Anchor.HeadingText: want %q got %q", original.Anchor.HeadingText, got.Anchor.HeadingText)
	}
	if got.Status != CommitStatusResolved {
		t.Errorf("Status: want %q got %q", CommitStatusResolved, got.Status)
	}
	if got.Reply != original.Reply {
		t.Errorf("Reply: want %q got %q", original.Reply, got.Reply)
	}
	if got.ResolvedAt != original.ResolvedAt {
		t.Errorf("ResolvedAt: want %q got %q", original.ResolvedAt, got.ResolvedAt)
	}
	if got.ResolvedBy != original.ResolvedBy {
		t.Errorf("ResolvedBy: want %q got %q", original.ResolvedBy, got.ResolvedBy)
	}
	if got.IsOpen() {
		t.Error("IsOpen() should be false for resolved entry")
	}
	if !got.IsResolved() {
		t.Error("IsResolved() should be true for resolved entry")
	}
}

// TestCommitEntry_LegacyUnmarshal verifies that pre-aihub#124 entries (no
// anchor/status/reply/resolved_* fields) unmarshal cleanly with nil Anchor,
// empty Status, and IsOpen()==true.
func TestCommitEntry_LegacyUnmarshal(t *testing.T) {
	raw := `{"id":"cm_x","author_user_id":"u_1","author_display":"x","body":"hi","created_at":"2026-01-01T00:00:00Z"}`

	var entry CommitEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if entry.Anchor != nil {
		t.Errorf("Anchor should be nil for legacy entry, got %+v", entry.Anchor)
	}
	if entry.Status != "" {
		t.Errorf("Status should be empty for legacy entry, got %q", entry.Status)
	}
	if !entry.IsOpen() {
		t.Error("IsOpen() should be true for legacy entry with empty Status")
	}
	if entry.IsResolved() {
		t.Error("IsResolved() should be false for legacy entry")
	}
}

// TestCommitEntry_ArrayUnmarshal verifies that a mixed array (legacy + resolved)
// unmarshal correctly, and that IsOpen/IsResolved split works as expected.
func TestCommitEntry_ArrayUnmarshal(t *testing.T) {
	raw := `[
		{"id":"cm_legacy","author_user_id":"u_1","author_display":"Alice","body":"old comment","created_at":"2026-01-01T00:00:00Z"},
		{"id":"cm_resolved","author_user_id":"u_2","author_display":"Bob","body":"fixed this","created_at":"2026-06-01T00:00:00Z","status":"resolved","resolved_at":"2026-06-02T00:00:00Z","resolved_by":"Charlie"}
	]`

	var entries []CommitEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	legacy := entries[0]
	if !legacy.IsOpen() {
		t.Errorf("entry[0] (legacy): IsOpen() should be true")
	}
	if legacy.IsResolved() {
		t.Errorf("entry[0] (legacy): IsResolved() should be false")
	}

	resolved := entries[1]
	if resolved.IsOpen() {
		t.Errorf("entry[1] (resolved): IsOpen() should be false")
	}
	if !resolved.IsResolved() {
		t.Errorf("entry[1] (resolved): IsResolved() should be true")
	}
	if resolved.ResolvedBy != "Charlie" {
		t.Errorf("entry[1]: ResolvedBy want %q got %q", "Charlie", resolved.ResolvedBy)
	}
}

// TestCommitEntry_OmitemptyMinimal verifies that a minimal CommitEntry (only
// id and body set) does NOT emit anchor, status, reply, resolved_at, resolved_by
// in its JSON representation (omitempty must work for all new fields).
func TestCommitEntry_OmitemptyMinimal(t *testing.T) {
	entry := CommitEntry{
		ID:        "cm_min",
		Body:      "just a body",
		CreatedAt: "2026-06-01T00:00:00Z",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{"anchor", "status", "reply", "resolved_at", "resolved_by"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("minimal entry JSON should not contain key %q; got: %s", key, data)
		}
	}
}
