package engine

import (
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/roles"
)

func loadCatalogOrFail(t *testing.T) []roles.Role {
	t.Helper()
	catalog, err := roles.LoadRoles()
	if err != nil {
		t.Fatalf("roles.LoadRoles: %v", err)
	}
	return catalog
}

// catalogAbsentStepID derives a step id guaranteed to be in NEITHER role's StepIDs, by
// probing the catalog itself rather than hardcoding a string that a future YAML edit could
// silently add. This is deliberate: constraint 4 requires the regression test below to keep
// meaning "absent from the catalog" even after the catalog grows.
func catalogAbsentStepID(t *testing.T, catalog []roles.Role, base string) string {
	t.Helper()
	known := map[string]bool{}
	for _, r := range catalog {
		for _, id := range r.SortedStepIDs() {
			known[id] = true
		}
	}
	candidate := base
	for known[candidate] {
		candidate += "_x"
	}
	return candidate
}

func TestIsReviewStep(t *testing.T) {
	cases := []struct {
		stepID string
		want   bool
	}{
		{"review", true},
		{"code_review", true},
		{"release_review", true},
		{"foo_review", true},
		{"security_review", true},
		{"spec", false},
		{"plan", false},
		{"review_fix", false}, // aihub#358 note: applies findings, is NOT itself a review step
		{"reviewer", false},   // does not end in "_review" and is not an exact match
		{"", false},
	}
	for _, tc := range cases {
		if got := IsReviewStep(tc.stepID); got != tc.want {
			t.Errorf("IsReviewStep(%q) = %v, want %v", tc.stepID, got, tc.want)
		}
	}
}

func TestParseDeclaredRole(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantName string
		wantOK   bool
	}{
		{
			name:     "simple declaration",
			content:  "role: reviewer\nsome body text\n",
			wantName: "reviewer",
			wantOK:   true,
		},
		{
			name:     "no declaration",
			content:  "## Step: spec\n\nDo the thing.\n",
			wantName: "",
			wantOK:   false,
		},
		{
			// W5: an earlier version guarded this position, on the theory that ExpandIncludes
			// (startup.go) might read it as the include's level value instead. It cannot:
			// ExpandIncludes only ever consumes a literal "level:" line there, so a "role:" line
			// immediately after "@include:" is ordinary content to it and must be honored here.
			name:     "role line immediately after @include is honored, not read as a level pair",
			content:  "@include: common/review/SKILL.md\nrole: deep\n",
			wantName: "deep",
			wantOK:   true,
		},
		{
			name:     "role declaration after an unrelated line following @include is honored",
			content:  "@include: common/review/SKILL.md\nlevel: deep\nrole: designer\n",
			wantName: "designer",
			wantOK:   true,
		},
		{
			name:     "indented role line still recognised",
			content:  "  role: executor  \n",
			wantName: "executor",
			wantOK:   true,
		},
		{
			name:     "empty role value is not a declaration",
			content:  "role:   \nbody\n",
			wantName: "",
			wantOK:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, ok := ParseDeclaredRole(tc.content)
			if name != tc.wantName || ok != tc.wantOK {
				t.Errorf("ParseDeclaredRole(%q) = (%q, %v), want (%q, %v)",
					tc.content, name, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

// TestResolveRole_NeverBottomsOutAtExecutor is THE regression test for aihub#654 constraint 4:
// a review-shaped step id that the catalog has never heard of must still resolve to the
// reviewer role via the is_review heuristic fallback, never to executor.
//
// Why this matters, spelled out because a passing assertion here is easy to mistake for a
// weaker one: polyforge-scenario#26 (which will add `role:` declarations to step templates)
// has NOT landed as of this wi, so TODAY zero templates declare `role:`, and every review step
// id not yet re-derived into internal/roles' YAML catalog is exactly this case. A naive
// "role: absent -> catalog lookup -> default to executor if not found" implementation passes
// every OTHER test in this file while silently demoting such a step from "raised tier +
// read-only" to "default tier + write-capable" — the two real catches the clean-context
// reviewer made came precisely from the read-only constraint this demotion would remove.
func TestResolveRole_NeverBottomsOutAtExecutor(t *testing.T) {
	catalog := loadCatalogOrFail(t)

	reviewerRole, ok := roles.RoleByName(catalog, "reviewer")
	if !ok {
		t.Fatalf("catalog has no %q role; cannot compare against it", "reviewer")
	}

	suffixID := catalogAbsentStepID(t, catalog, "totally_unheard_of_review")
	if !IsReviewStep(suffixID) {
		t.Fatalf("test fixture %q must be review-shaped by IsReviewStep for this test to mean anything", suffixID)
	}

	for _, stepID := range []string{suffixID, "review", "code_review", "release_review"} {
		t.Run(stepID, func(t *testing.T) {
			// Tier 2 must genuinely miss for this test to exercise tier 3. The three exact
			// names ARE in the shipped catalog (reviewer.yaml), so RoleForStepID hits tier 2
			// for them today — but resolves to the SAME reviewer-shaped role either way, so
			// asserting the final Role (not the Source) still pins the never-executor
			// guarantee regardless of which tier produced it. Only the synthetic suffix id is
			// guaranteed to hit tier 3.
			role, source, _, err := ResolveRole(catalog, stepID, "")
			if err != nil {
				t.Fatalf("ResolveRole(%q) returned an error: %v", stepID, err)
			}
			if role.Name == "executor" {
				t.Fatalf("ResolveRole(%q) resolved to %q (source=%s) — a review-shaped step id "+
					"silently fell back to the write-capable default-tier executor role instead "+
					"of the read-only raised-tier reviewer role. This is the exact regression "+
					"aihub#654 constraint 4 exists to prevent: ~10 review occurrences would be "+
					"silently demoted from 'raised tier + read-only' to 'default tier + "+
					"write-capable' the moment polyforge-scenario#26 has not yet landed (which is "+
					"true today, for every template).", stepID, role.Name, source)
			}
			if role.Name != reviewerRole.Name || role.Tier != reviewerRole.Tier ||
				role.Capability.ReadOnly != reviewerRole.Capability.ReadOnly {
				t.Errorf("ResolveRole(%q) = role %+v (source=%s), want the reviewer-shaped role %+v",
					stepID, role, source, reviewerRole)
			}
			if !role.Capability.ReadOnly {
				t.Errorf("ResolveRole(%q) resolved to a WRITE-CAPABLE role — a review step must "+
					"resolve read-only", stepID)
			}
		})
	}

	// And the tier-3 case explicitly via source, for the synthetic id where tier 2 is
	// guaranteed to miss.
	role, source, _, err := ResolveRole(catalog, suffixID, "")
	if err != nil {
		t.Fatalf("ResolveRole(%q): %v", suffixID, err)
	}
	if source != RoleSourceHeuristic {
		t.Errorf("ResolveRole(%q) source = %q, want %q (a step id absent from every role's "+
			"StepIDs must fall through both the declared and catalog tiers to the heuristic)",
			suffixID, source, RoleSourceHeuristic)
	}
	if role.Name != "reviewer" {
		t.Errorf("ResolveRole(%q) via heuristic resolved to role %q, want \"reviewer\"", suffixID, role.Name)
	}
}

// TestResolveRole_HeuristicExecutorForNonReviewUnknownID is the mirror check: an unknown,
// non-review-shaped step id must resolve to executor via the SAME tier-3 heuristic, so the
// never-executor test above is not vacuously true because tier 3 never picks executor either.
func TestResolveRole_HeuristicExecutorForNonReviewUnknownID(t *testing.T) {
	catalog := loadCatalogOrFail(t)
	stepID := catalogAbsentStepID(t, catalog, "totally_unheard_of_step")
	if IsReviewStep(stepID) {
		t.Fatalf("test fixture %q must NOT be review-shaped for this test to mean anything", stepID)
	}

	role, source, _, err := ResolveRole(catalog, stepID, "")
	if err != nil {
		t.Fatalf("ResolveRole(%q): %v", stepID, err)
	}
	if source != RoleSourceHeuristic {
		t.Errorf("ResolveRole(%q) source = %q, want %q", stepID, source, RoleSourceHeuristic)
	}
	if role.Name != "executor" {
		t.Errorf("ResolveRole(%q) via heuristic resolved to role %q, want \"executor\"", stepID, role.Name)
	}
	if role.Capability.ReadOnly {
		t.Errorf("ResolveRole(%q) resolved to a read-only executor — executor must be write-capable", stepID)
	}
}

// TestResolveRole_DeclaredRoleWinsOverCatalogAndHeuristic pins tier 1: an explicit, catalog-known
// `role:` declaration must win even when the step id is ALSO bound to a different role by the
// catalog (tier 2 would otherwise fire) or is review-shaped (tier 3 would otherwise fire).
func TestResolveRole_DeclaredRoleWinsOverCatalogAndHeuristic(t *testing.T) {
	catalog := loadCatalogOrFail(t)

	t.Run("declared beats catalog", func(t *testing.T) {
		// "spec" is catalog-bound to "designer" (internal/roles/definitions/designer.yaml).
		// Declaring "operator" explicitly must still win.
		role, source, unknownDeclared, err := ResolveRole(catalog, "spec", "operator")
		if err != nil {
			t.Fatalf("ResolveRole: %v", err)
		}
		if source != RoleSourceDeclared {
			t.Errorf("source = %q, want %q", source, RoleSourceDeclared)
		}
		if role.Name != "operator" {
			t.Errorf("role = %q, want %q", role.Name, "operator")
		}
		if unknownDeclared != "" {
			t.Errorf("unknownDeclared = %q, want \"\" (the declared name was known to the catalog)", unknownDeclared)
		}
	})

	t.Run("declared beats the review heuristic", func(t *testing.T) {
		unknownReviewID := catalogAbsentStepID(t, catalog, "made_up_review")
		role, source, unknownDeclared, err := ResolveRole(catalog, unknownReviewID, "executor")
		if err != nil {
			t.Fatalf("ResolveRole: %v", err)
		}
		if source != RoleSourceDeclared {
			t.Errorf("source = %q, want %q", source, RoleSourceDeclared)
		}
		if role.Name != "executor" {
			t.Errorf("role = %q, want %q (the explicit declaration, even though the step id is "+
				"review-shaped)", role.Name, "executor")
		}
		if unknownDeclared != "" {
			t.Errorf("unknownDeclared = %q, want \"\" (the declared name was known to the catalog)", unknownDeclared)
		}
	})

	t.Run("unknown declared name falls through to catalog/heuristic instead of erroring", func(t *testing.T) {
		// W6: an unknown declared role must fall through non-fatally (unchanged) AND be
		// surfaced via unknownDeclared (the fix) instead of vanishing the moment
		// source != RoleSourceDeclared, which is all a caller could previously observe.
		role, source, unknownDeclared, err := ResolveRole(catalog, "code_review", "not_a_real_role")
		if err != nil {
			t.Fatalf("ResolveRole: %v", err)
		}
		if source == RoleSourceDeclared {
			t.Errorf("source = %q, want it to fall through since %q is unknown", source, "not_a_real_role")
		}
		if role.Name != "reviewer" {
			t.Errorf("role = %q, want %q", role.Name, "reviewer")
		}
		if unknownDeclared != "not_a_real_role" {
			t.Errorf("unknownDeclared = %q, want %q (the unknown declared name must be surfaced, "+
				"not silently dropped)", unknownDeclared, "not_a_real_role")
		}
	})
}

// TestResolveRole_UnknownDeclaredRoleSurfacedThroughCatalogTier is a second W6 regression,
// distinct from the one above: it hits tier 2 (RoleForStepID), not tier 3, after the unknown
// declared name falls through, so it also guards against a fix that only threads
// unknownDeclared through the tier-3 return path and forgets tier 2's.
func TestResolveRole_UnknownDeclaredRoleSurfacedThroughCatalogTier(t *testing.T) {
	catalog := loadCatalogOrFail(t)

	// "spec" is catalog-bound to "designer" (tier 2 hits before the heuristic is ever reached).
	role, source, unknownDeclared, err := ResolveRole(catalog, "spec", "reviewr")
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if source != RoleSourceCatalog {
		t.Errorf("source = %q, want %q", source, RoleSourceCatalog)
	}
	if role.Name != "designer" {
		t.Errorf("role = %q, want %q", role.Name, "designer")
	}
	if unknownDeclared != "reviewr" {
		t.Errorf("unknownDeclared = %q, want %q (must survive through the tier-2 catalog hit, "+
			"not just the tier-3 heuristic)", unknownDeclared, "reviewr")
	}
}

// TestResolveRole_ValidTierAlways asserts every resolvable role (across all three tiers) has a
// tier from internal/roles.ValidTiers — consuming that exported constant directly, per
// constraint 5 (no parallel data model, and tests exercise the shared vocabulary).
func TestResolveRole_ValidTierAlways(t *testing.T) {
	catalog := loadCatalogOrFail(t)
	validTier := map[string]bool{}
	for _, tier := range roles.ValidTiers {
		validTier[tier] = true
	}

	for _, r := range catalog {
		if !validTier[r.Tier] {
			t.Errorf("catalog role %q has tier %q, not one of %v", r.Name, r.Tier, roles.ValidTiers)
		}
	}

	role, _, _, err := ResolveRole(catalog, catalogAbsentStepID(t, catalog, "some_unknown_step"), "")
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if !validTier[role.Tier] {
		t.Errorf("ResolveRole via heuristic returned tier %q, not one of %v", role.Tier, roles.ValidTiers)
	}
}

func TestResolveRole_CatalogMissingHeuristicRoleIsAnError(t *testing.T) {
	// A deliberately incomplete catalog: neither "reviewer" nor "executor" present, so tier 3
	// cannot resolve either heuristic branch. This must be a returned error, never a panic and
	// never a silently synthesized zero-value role.
	empty := []roles.Role{}
	if _, _, _, err := ResolveRole(empty, "code_review", ""); err == nil {
		t.Error("ResolveRole with an empty catalog and no declared role: want an error (tier 3 " +
			"cannot find \"reviewer\"), got nil")
	}
	if _, _, _, err := ResolveRole(empty, "some_step", ""); err == nil {
		t.Error("ResolveRole with an empty catalog and no declared role: want an error (tier 3 " +
			"cannot find \"executor\"), got nil")
	}
}
