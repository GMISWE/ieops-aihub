package client

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFormatDetails covers the aihub#209 fix: the server already computes a
// `details` object on conflict errors (lock holder, dedup candidates,
// superseded_by, …) but the client used to decode only {code,message} and drop
// it. formatDetails renders that object into the error string so the metadata
// reaches the caller. Cases map to the wi's acceptance criteria.
func TestFormatDetails(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantEmpty bool
		wantSubs  []string // substrings that must appear in the suffix
	}{
		{name: "nil", raw: "", wantEmpty: true},
		{name: "literal null", raw: "null", wantEmpty: true},
		{
			// AC: lock-conflict error string contains actor_display + slug.
			name: "lock conflict conflict_with",
			raw:  `{"conflict_with":{"attempt_id":"ra_abc","actor_display":"monte","work_item_slug":"aihub#207"}}`,
			wantSubs: []string{
				" details=", "conflict_with", "actor_display", "monte", "aihub#207",
			},
		},
		{
			// AC: dedup conflict carries the candidate list.
			name: "dedup candidates",
			raw:  `{"candidates":[{"slug":"aihub#100","goal":"x"},{"slug":"aihub#101","goal":"y"}]}`,
			wantSubs: []string{"candidates", "aihub#100", "aihub#101"},
		},
		{
			name:     "superseded_by",
			raw:      `{"superseded_by":{"actor_display":"xqr","at":"2026-07-06T14:00:00Z"}}`,
			wantSubs: []string{"superseded_by", "xqr"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDetails(json.RawMessage(tc.raw))
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("want empty suffix, got %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, " details=") {
				t.Fatalf("suffix must start with %q, got %q", " details=", got)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("suffix %q missing %q", got, sub)
				}
			}
			// Compact: no newline or tab from indentation should survive.
			if strings.ContainsAny(got, "\n\t") {
				t.Errorf("suffix not compacted: %q", got)
			}
		})
	}
}

// TestFormatDetails_Truncation verifies the ~500B cap so a pathological details
// blob cannot flood the error string.
func TestFormatDetails_Truncation(t *testing.T) {
	big := make([]string, 200)
	for i := range big {
		big[i] = `"k` + strings.Repeat("x", 5) + `":1`
	}
	raw := "{" + strings.Join(big, ",") + "}"
	got := formatDetails(json.RawMessage(raw))
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("oversized details must be truncated, got %q", got)
	}
	// " details=" (9) + 500 + "...(truncated)" (14) = 523.
	if len(got) != 9+500+len("...(truncated)") {
		t.Errorf("truncated length = %d, want %d", len(got), 9+500+len("...(truncated)"))
	}
}
