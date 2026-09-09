package domain

import (
	"strings"
	"testing"
)

// TestMethodologyTypeEnum locks in the SUGGESTED artifact kinds: exactly the six
// methodology.* members of MemoryTypeEnum, derived so the two never drift.
//
// The list was pf_save_artifact's published `type` enum from aihub#211 until
// aihub#499 withdrew it. This test survives the withdrawal unchanged because it
// only ever asserted the DERIVATION — that the six are exactly MemoryTypeEnum's
// methodology.* subset — and that is still true of a suggestion list. What is no
// longer true is that these six are the accepted set; the accepted set is
// MethodologyTypePrefix, and internal/mcp's TestSaveArtifactTypeIsEnforced owns
// that half.
func TestMethodologyTypeEnum(t *testing.T) {
	if len(MethodologyTypeEnum) != 6 {
		t.Fatalf("MethodologyTypeEnum = %d entries, want 6: %v", len(MethodologyTypeEnum), MethodologyTypeEnum)
	}
	for _, ty := range MethodologyTypeEnum {
		if !strings.HasPrefix(ty, "methodology.") {
			t.Errorf("MethodologyTypeEnum entry %q is not methodology.*", ty)
		}
	}
	var want int
	for _, ty := range MemoryTypeEnum {
		if strings.HasPrefix(ty, "methodology.") {
			want++
		}
	}
	if len(MethodologyTypeEnum) != want {
		t.Errorf("MethodologyTypeEnum len=%d, want %d (all methodology.* in MemoryTypeEnum)", len(MethodologyTypeEnum), want)
	}
}
