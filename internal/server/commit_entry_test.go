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

// ─── aihub#125 tests ─────────────────────────────────────────────────────────

// TestCommitAnchor_QuoteFieldsRoundTrip verifies that the new Quote/Prefix/Suffix
// fields on CommitAnchor survive marshal/unmarshal and appear in JSON.
func TestCommitAnchor_QuoteFieldsRoundTrip(t *testing.T) {
	original := CommitEntry{
		ID:        "cm_q1",
		Body:      "selection annotation",
		CreatedAt: "2026-06-01T00:00:00Z",
		Anchor: &CommitAnchor{
			HeadingID:   "section-impl",
			HeadingText: "Implementation",
			Quote:       "exact selected text",
			Prefix:      "context before",
			Suffix:      "context after",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{`"quote"`, `"prefix"`, `"suffix"`, `"heading_id"`, `"heading_text"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON missing key %s; got: %s", key, data)
		}
	}

	var got CommitEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Anchor == nil {
		t.Fatal("Anchor nil after round-trip")
	}
	if got.Anchor.Quote != original.Anchor.Quote {
		t.Errorf("Quote: want %q got %q", original.Anchor.Quote, got.Anchor.Quote)
	}
	if got.Anchor.Prefix != original.Anchor.Prefix {
		t.Errorf("Prefix: want %q got %q", original.Anchor.Prefix, got.Anchor.Prefix)
	}
	if got.Anchor.Suffix != original.Anchor.Suffix {
		t.Errorf("Suffix: want %q got %q", original.Anchor.Suffix, got.Anchor.Suffix)
	}
}

// TestCommitAnchor_LegacyHeadingOnlyUnmarshal verifies that a legacy anchor
// with only heading_id/heading_text (no quote/prefix/suffix) unmarshals cleanly
// with empty Quote/Prefix/Suffix (omitempty round-trip stability).
func TestCommitAnchor_LegacyHeadingOnlyUnmarshal(t *testing.T) {
	raw := `{"id":"cm_h","body":"old","created_at":"2026-01-01T00:00:00Z","anchor":{"heading_id":"s1","heading_text":"Section 1"}}`
	var entry CommitEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Anchor == nil {
		t.Fatal("Anchor nil")
	}
	if entry.Anchor.HeadingID != "s1" {
		t.Errorf("HeadingID: want s1 got %q", entry.Anchor.HeadingID)
	}
	if entry.Anchor.Quote != "" {
		t.Errorf("Quote should be empty for legacy anchor, got %q", entry.Anchor.Quote)
	}

	// Re-marshal: Quote/Prefix/Suffix must NOT appear (omitempty).
	out, _ := json.Marshal(entry)
	for _, key := range []string{`"quote"`, `"prefix"`, `"suffix"`} {
		if strings.Contains(string(out), key) {
			t.Errorf("remarshaled legacy anchor should not contain %s; got: %s", key, out)
		}
	}
}

// TestCommitReply_RoundTrip verifies that CommitReply fields survive a
// marshal/unmarshal cycle and the Replies slice on CommitEntry works end-to-end.
func TestCommitReply_RoundTrip(t *testing.T) {
	original := CommitEntry{
		ID:        "cm_r1",
		Body:      "original annotation",
		CreatedAt: "2026-06-01T00:00:00Z",
		Replies: []CommitReply{
			{
				ID:            "cr_a",
				AuthorUserID:  "u_bob",
				AuthorDisplay: "Bob",
				Body:          "first reply",
				CreatedAt:     "2026-06-02T00:00:00Z",
			},
			{
				ID:            "cr_b",
				AuthorUserID:  "u_alice",
				AuthorDisplay: "Alice",
				Body:          "second reply",
				CreatedAt:     "2026-06-03T00:00:00Z",
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{`"replies"`, `"author_user_id"`, `"author_display"`, `"created_at"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON missing key %s; got: %s", key, data)
		}
	}

	var got CommitEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Replies) != 2 {
		t.Fatalf("want 2 replies, got %d", len(got.Replies))
	}
	if got.Replies[0].ID != "cr_a" {
		t.Errorf("reply[0].ID: want cr_a got %q", got.Replies[0].ID)
	}
	if got.Replies[1].Body != "second reply" {
		t.Errorf("reply[1].Body: want %q got %q", "second reply", got.Replies[1].Body)
	}
}

// TestCommitEntry_LegacyNoReplies verifies that a CommitEntry without a replies
// field unmarshals to an empty Replies slice (not nil panic risk), and that
// marshaling a minimal entry does not emit "replies" (omitempty).
func TestCommitEntry_LegacyNoReplies(t *testing.T) {
	raw := `{"id":"cm_old","body":"hi","created_at":"2026-01-01T00:00:00Z"}`
	var entry CommitEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entry.Replies) != 0 {
		t.Errorf("legacy entry Replies should be empty, got %v", entry.Replies)
	}

	out, _ := json.Marshal(entry)
	if strings.Contains(string(out), `"replies"`) {
		t.Errorf("legacy entry JSON must not contain replies key; got: %s", out)
	}
}

// TestCommitEntry_ResolvedWithLegacyReplyAndNewReplies verifies that a
// resolved CommitEntry carrying both the legacy Reply string and the new
// Replies slice unmarshal cleanly (coexistence).
func TestCommitEntry_ResolvedWithLegacyReplyAndNewReplies(t *testing.T) {
	raw := `{
		"id":"cm_coexist",
		"body":"annotation",
		"created_at":"2026-01-01T00:00:00Z",
		"status":"resolved",
		"reply":"AI resolution text",
		"resolved_at":"2026-06-01T00:00:00Z",
		"resolved_by":"Agent",
		"replies":[{"id":"cr_1","author_user_id":"u_1","author_display":"Alice","body":"human reply","created_at":"2026-06-02T00:00:00Z"}]
	}`

	var entry CommitEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Reply != "AI resolution text" {
		t.Errorf("Reply: want %q got %q", "AI resolution text", entry.Reply)
	}
	if len(entry.Replies) != 1 {
		t.Fatalf("Replies: want 1 got %d", len(entry.Replies))
	}
	if entry.Replies[0].ID != "cr_1" {
		t.Errorf("Replies[0].ID: want cr_1 got %q", entry.Replies[0].ID)
	}
	if !entry.IsResolved() {
		t.Error("IsResolved() should be true")
	}

	// Re-marshal should preserve both fields.
	out, _ := json.Marshal(entry)
	for _, key := range []string{`"reply"`, `"replies"`, `"resolved_by"`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("remarshal missing key %s; got: %s", key, out)
		}
	}
}
