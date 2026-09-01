package domain

import (
	"strings"
	"testing"
)

// TestValidateSupersedeScope covers the aihub#210 supersede guard: a supersede
// must stay in the same project, and methodology.* must stay within the same wi.
func TestValidateSupersedeScope(t *testing.T) {
	cases := []struct {
		name                                    string
		memType, reqProj, reqWI, tgtProj, tgtWI string
		wantErr                                 bool
	}{
		{"same project, non-methodology, ok", "fact.note", "aihub", "", "aihub", "", false},
		{"cross project rejected", "fact.note", "aihub", "", "ieops", "", true},
		{"methodology same wi ok", "methodology.spec", "aihub", "wi_1", "aihub", "wi_1", false},
		{"methodology cross wi rejected", "methodology.spec", "aihub", "wi_1", "aihub", "wi_2", true},
		{"methodology target without wi rejected", "methodology.plan", "aihub", "wi_1", "aihub", "", true},
		{"methodology cross project rejected", "methodology.spec", "aihub", "wi_1", "ieops", "wi_1", true},
		{"non-methodology cross wi allowed (only project matters)", "experience.debug", "aihub", "wi_1", "aihub", "wi_2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSupersedeScope(tc.memType, tc.reqProj, tc.reqWI, tc.tgtProj, tc.tgtWI)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateSupersedeScope(%q,...) err=%v, wantErr=%v", tc.memType, err, tc.wantErr)
			}
		})
	}
}

// TestPfRememberTypeEnum_NoMethodology verifies the pf_remember enum is
// MemoryTypeEnum minus the six methodology.* entries and drifts with neither.
func TestPfRememberTypeEnum_NoMethodology(t *testing.T) {
	if len(PfRememberTypeEnum) == 0 {
		t.Fatal("PfRememberTypeEnum is empty")
	}
	for _, ty := range PfRememberTypeEnum {
		if strings.HasPrefix(ty, "methodology.") {
			t.Errorf("PfRememberTypeEnum must exclude methodology.*, found %q", ty)
		}
	}
	if want := len(MemoryTypeEnum) - 6; len(PfRememberTypeEnum) != want {
		t.Errorf("PfRememberTypeEnum len=%d, want %d (MemoryTypeEnum minus 6 methodology.*)", len(PfRememberTypeEnum), want)
	}
}
