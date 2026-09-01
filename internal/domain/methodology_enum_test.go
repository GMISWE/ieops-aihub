package domain

import (
	"strings"
	"testing"
)

// TestMethodologyTypeEnum locks in the aihub#211 pf_save_artifact enum: exactly
// the six methodology.* members of MemoryTypeEnum, derived so the two never drift.
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
