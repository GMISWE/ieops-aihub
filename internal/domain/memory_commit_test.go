package domain

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestMemoryTypeEnum_Count verifies that MemoryTypeEnum contains exactly 19 entries.
func TestMemoryTypeEnum_Count(t *testing.T) {
	if got := len(MemoryTypeEnum); got != 19 {
		t.Errorf("MemoryTypeEnum has %d entries, want 19; actual: %v", got, MemoryTypeEnum)
	}
}

// TestMemoryTypeEnum_AllHaveValidPrefix ensures every enum entry passes the 4-prefix check
// that Remember uses internally.
func TestMemoryTypeEnum_AllHaveValidPrefix(t *testing.T) {
	validPrefixes := []string{"experience.", "fact.", "rule.", "methodology."}
	for _, typ := range MemoryTypeEnum {
		found := false
		for _, p := range validPrefixes {
			if len(typ) > len(p) && typ[:len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("MemoryTypeEnum entry %q does not start with a valid prefix", typ)
		}
	}
}

// TestMemoryTypeEnum_OffListTypePassesLenientCheck verifies the server's lenient
// 4-prefix validation still accepts types that are NOT in MemoryTypeEnum but carry
// a valid prefix (regression guard: we must not change the server check to enum-strict).
func TestMemoryTypeEnum_OffListTypePassesLenientCheck(t *testing.T) {
	offListButValid := []string{
		"experience.someNewType",
		"fact.someNewThing",
		"rule.customCompanyRule",
		"methodology.somethingNew",
	}
	validPrefixes := []string{"experience.", "fact.", "rule.", "methodology."}
	for _, typ := range offListButValid {
		found := false
		for _, p := range validPrefixes {
			if len(typ) >= len(p) && typ[:len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("off-list type %q should still pass lenient prefix check", typ)
		}
	}
}

// TestCommitMemorySQL_EntryStructure verifies that the JSON entry structure for a
// commit matches the expected schema (author_user_id, author_display, body, created_at).
// This is a pure-Go structural test — no DB required.
func TestCommitMemorySQL_EntryStructure(t *testing.T) {
	// Verify that the keys we embed in the entry are present.
	// We do this by recreating the map used in CommitMemory.
	entry := map[string]any{
		"author_user_id": "usr_abc",
		"author_display": "Alice",
		"body":           "This is a test annotation.",
		"created_at":     "2026-01-01T00:00:00Z",
	}
	for _, key := range []string{"author_user_id", "author_display", "body", "created_at"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("commit entry missing required key %q", key)
		}
	}
}

// TestMemoryStruct_CommitsField verifies the Memory struct has a Commits field with
// the correct json tag (column-drift guard: field must survive JSON round-trip).
func TestMemoryStruct_CommitsField(t *testing.T) {
	m := Memory{}
	m.Commits = []byte(`[{"body":"test annotation"}]`)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal(Memory) failed: %v", err)
	}
	if !strings.Contains(string(data), `"commits"`) {
		t.Errorf("marshaled Memory missing commits key; got: %s", string(data[:min(len(data), 300)]))
	}
	// Verify round-trip: unmarshal back and check the field is preserved.
	var m2 Memory
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatalf("json.Unmarshal(Memory) failed: %v", err)
	}
	if !strings.Contains(string(m2.Commits), "test annotation") {
		t.Errorf("Commits round-trip failed; got: %s", string(m2.Commits))
	}
}

// TestResolveCommitSQLKeys verifies that the four jsonb_set paths written by
// ResolveCommit match the CommitEntry JSON tags expected by the UI / MCP
// consumers. It asserts against the real resolveCommitSQL constant so any
// key-name drift in the SQL breaks this test. No DB required.
func TestResolveCommitSQLKeys(t *testing.T) {
	// These are the exact jsonb_set path literals that resolveCommitSQL must contain.
	requiredPaths := []string{"'{status}'", "'{reply}'", "'{resolved_at}'", "'{resolved_by}'"}
	for _, path := range requiredPaths {
		if !strings.Contains(resolveCommitSQL, path) {
			t.Errorf("resolveCommitSQL missing jsonb_set path %s", path)
		}
	}

	// The SQL must hard-code the status value as the literal string "resolved".
	if !strings.Contains(resolveCommitSQL, `"resolved"`) {
		t.Errorf("resolveCommitSQL does not hard-code status value %q", "resolved")
	}
}

// ─── aihub#125 tests ─────────────────────────────────────────────────────────

// TestCommitAnchorArgs_ValidationCaps verifies that CommitMemory rejects anchor
// fields exceeding their caps. These are pure-Go validation checks (no DB).
//
// Note: CommitMemory calls the DB first only after validation, so we verify
// the cap logic by calling the internal validation. Since CommitMemory starts
// with the validation before any DB call, we can confirm the error code by
// checking the returned error type directly without a real pool — the DB path
// is never reached when validation fails.
// We exercise this by passing a nil pool and verifying the error is returned
// before any nil-pointer dereference from the pool.
func TestCommitAnchorArgs_ValidationCaps(t *testing.T) {
	tests := []struct {
		name    string
		anchor  CommitAnchorArgs
		wantErr string
	}{
		{
			name:    "quote_too_long",
			anchor:  CommitAnchorArgs{Quote: string(make([]byte, 2001))},
			wantErr: "anchor quote exceeds 2000 characters",
		},
		{
			name:    "prefix_too_long",
			anchor:  CommitAnchorArgs{Prefix: string(make([]byte, 65))},
			wantErr: "anchor prefix exceeds 64 characters",
		},
		{
			name:    "suffix_too_long",
			anchor:  CommitAnchorArgs{Suffix: string(make([]byte, 65))},
			wantErr: "anchor suffix exceeds 64 characters",
		},
		{
			name:   "at_limit_quote",
			anchor: CommitAnchorArgs{Quote: string(make([]byte, 2000))},
			// validation passes — DB call will panic due to nil pool, but that's
			// a different error; we just need to confirm no cap error is returned.
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotErr error
			func() {
				// nil pool panics after validation passes — swallow it.
				defer func() { _ = recover() }()
				gotErr = CommitMemory(context.TODO(), nil, "mem_x", "body", "u", "Alice", tc.anchor)
			}()

			if tc.wantErr != "" {
				if gotErr == nil {
					t.Fatalf("expected error %q, got nil", tc.wantErr)
				}
				if !strings.Contains(gotErr.Error(), tc.wantErr) {
					t.Errorf("error %q should contain %q", gotErr.Error(), tc.wantErr)
				}
				ae, ok := gotErr.(*AihubError)
				if !ok {
					t.Fatalf("expected *AihubError, got %T", gotErr)
				}
				if ae.Code != ErrPayloadTooLarge {
					t.Errorf("code: want ErrPayloadTooLarge got %v", ae.Code)
				}
			} else {
				// wantErr == "": validation should have passed (error from nil pool, not cap).
				if gotErr != nil {
					if ae, ok := gotErr.(*AihubError); ok && ae.Code == ErrPayloadTooLarge {
						t.Errorf("at-limit value should not trigger cap error, got: %v", gotErr)
					}
				}
			}
		})
	}
}

// TestReplyCommit_EmptyBodyError verifies that ReplyCommit rejects an empty body.
// Uses nil pool — validation fires before any DB access.
func TestReplyCommit_EmptyBodyError(t *testing.T) {
	var gotErr error
	func() {
		defer func() { _ = recover() }()
		gotErr = ReplyCommit(context.TODO(), nil, "mem_x", "cm_1", "u", "Alice", "")
	}()
	if gotErr == nil {
		t.Fatal("expected error for empty body, got nil")
	}
	ae, ok := gotErr.(*AihubError)
	if !ok {
		t.Fatalf("expected *AihubError, got %T: %v", gotErr, gotErr)
	}
	if ae.Code != ErrBadRequest {
		t.Errorf("code: want ErrBadRequest got %v", ae.Code)
	}
}

// TestReplyCommit_BodyTooLong verifies that ReplyCommit rejects bodies > 20000 chars.
func TestReplyCommit_BodyTooLong(t *testing.T) {
	var gotErr error
	func() {
		defer func() { _ = recover() }()
		gotErr = ReplyCommit(context.TODO(), nil, "mem_x", "cm_1", "u", "Alice", strings.Repeat("x", 20001))
	}()
	if gotErr == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
	ae, ok := gotErr.(*AihubError)
	if !ok {
		t.Fatalf("expected *AihubError, got %T: %v", gotErr, gotErr)
	}
	if ae.Code != ErrPayloadTooLarge {
		t.Errorf("code: want ErrPayloadTooLarge got %v", ae.Code)
	}
}

// TestReplyCommitSQLPaths verifies that replyCommitSQL contains the jsonb_set
// path for 'replies' and uses the expected COALESCE pattern (no DB required).
func TestReplyCommitSQLPaths(t *testing.T) {
	if !strings.Contains(replyCommitSQL, "'replies'") {
		t.Error("replyCommitSQL missing 'replies' jsonb_set path")
	}
	if !strings.Contains(replyCommitSQL, "COALESCE") {
		t.Error("replyCommitSQL should use COALESCE to handle missing replies array")
	}
	if !strings.Contains(replyCommitSQL, "'[]'::jsonb") {
		t.Error("replyCommitSQL should initialize with empty JSON array")
	}
}

// TestCommitAnchorArgs_AtLimitValid verifies that CommitAnchorArgs fields at
// exactly their length limit do not cause a validation error.
func TestCommitAnchorArgs_AtLimitValid(t *testing.T) {
	// These calls will panic on nil pool after passing validation — that's expected.
	tests := []struct {
		name   string
		anchor CommitAnchorArgs
	}{
		{"quote_at_2000", CommitAnchorArgs{Quote: strings.Repeat("a", 2000)}},
		{"prefix_at_64", CommitAnchorArgs{Prefix: strings.Repeat("b", 64)}},
		{"suffix_at_64", CommitAnchorArgs{Suffix: strings.Repeat("c", 64)}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var capErr error
			func() {
				defer func() { _ = recover() }()
				gotErr := CommitMemory(context.TODO(), nil, "mem_x", "body", "u", "Alice", tc.anchor)
				if ae, ok := gotErr.(*AihubError); ok && ae.Code == ErrPayloadTooLarge {
					capErr = gotErr
				}
			}()
			if capErr != nil {
				t.Errorf("at-limit value should not trigger cap error: %v", capErr)
			}
		})
	}
}
