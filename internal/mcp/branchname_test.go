package mcp

import "testing"

// TestClaimBranchULID8 locks the aihub#225 regression: the claim branch suffix
// must be derived from the canonical wi id (wi_<ulid>), never the raw slug.
func TestClaimBranchULID8(t *testing.T) {
	cases := []struct {
		name          string
		canonicalWIID string
		want          string
	}{
		{
			// Canonical id: strip "wi_", take the last 8 ulid chars.
			name:          "canonical id yields last 8 ulid chars",
			canonicalWIID: "wi_ABCDEFGHIJKLMNOP",
			want:          "IJKLMNOP",
		},
		{
			// aihub#225: a raw slug has no "wi_" prefix, so the last-8 slice
			// produced the garbled "ihub#225". The fix routes canonicalWIID
			// here; this case documents why the caller must never pass a slug.
			name:          "raw slug is mangled (documents the bug)",
			canonicalWIID: "aihub#225",
			want:          "ihub#225",
		},
		{
			name:          "short id returns empty (caller skips worktree)",
			canonicalWIID: "wi_1234",
			want:          "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claimBranchULID8(tc.canonicalWIID)
			if got != tc.want {
				t.Fatalf("claimBranchULID8(%q) = %q, want %q", tc.canonicalWIID, got, tc.want)
			}
		})
	}
}
