package engine

import (
	"fmt"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/roles"
)

// RoleSource names which tier of ResolveRole's three-tier fallback produced a result.
type RoleSource string

const (
	// RoleSourceDeclared means an explicit `role: <name>` line in the step content, known to
	// the catalog, won.
	RoleSourceDeclared RoleSource = "declared"
	// RoleSourceCatalog means roles.RoleForStepID matched the step id.
	RoleSourceCatalog RoleSource = "catalog"
	// RoleSourceHeuristic means neither of the above applied, and IsReviewStep(stepID) picked
	// between the reviewer and executor shapes.
	RoleSourceHeuristic RoleSource = "heuristic"
)

// heuristicReviewerRole and heuristicExecutorRole are the two role NAMES the tier-3 heuristic
// resolves to. They are internal/roles catalog role names (RoleByName is used to look them up,
// never a hand-built roles.Role literal), so a re-tier of either role in its YAML definition is
// automatically reflected here.
const (
	heuristicReviewerRole = "reviewer"
	heuristicExecutorRole = "executor"
)

// IsReviewStep is the is_review(step_id) predicate engine.native.md / engine-native-details.md
// define verbatim: an "_review" suffix, or one of the three exact names. Kept as a single
// function so every caller (ResolveRole's tier 3, and any future consumer) shares one
// definition rather than re-deriving the predicate.
func IsReviewStep(stepID string) bool {
	if strings.HasSuffix(stepID, "_review") {
		return true
	}
	switch stepID {
	case "review", "code_review", "release_review":
		return true
	}
	return false
}

// ParseDeclaredRole extracts a step's explicit `role: <name>` line from its content, if any.
//
// A `role:` line is read regardless of what precedes it, including a line immediately after an
// `@include:` line: ExpandIncludes (startup.go) only ever consumes a literal `level:` line in
// that position (its pair-scan rule), so a `role:` line there is ordinary content to
// ExpandIncludes and can never be misread as a level value. An earlier version of this function
// skipped `role:` lines placed right after `@include:` on the theory that they might collide
// with that pairing; they cannot, and the guard only meant a `role:` declaration placed there
// was silently dropped instead of read. Returns ("", false) when no `role:` line exists at all.
func ParseDeclaredRole(content string) (name string, ok bool) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "role:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "role:"))
		if value == "" {
			continue
		}
		return value, true
	}
	return "", false
}

// ResolveRole implements the three-tier `role:` fallback (aihub#654 constraint 4 / spec Design
// Decision 3), in this exact order, and it NEVER short-circuits to a hardcoded "executor":
//
//  1. declaredRole != "" and known to catalog -> roles.RoleByName, RoleSourceDeclared.
//  2. else roles.RoleForStepID(catalog, stepID) hit -> RoleSourceCatalog.
//  3. else IsReviewStep(stepID) ? the catalog's "reviewer" role : the catalog's "executor" role,
//     both via roles.RoleByName -> RoleSourceHeuristic.
//
// catalog is whatever roles.LoadRoles() returned; this function never loads or mutates it. An
// error is returned only when tier 3 cannot find "reviewer" or "executor" in catalog at all —
// a catalog data error, since both names are load-bearing constants of internal/roles's YAML
// definitions, never expected in a normally loaded catalog.
//
// unknownDeclared is declaredRole itself, but ONLY when declaredRole != "" and it was not found
// in catalog (tiers 2/3 then decide the result as usual); it is "" whenever declaredRole was
// empty or matched the catalog. Falling through on an unknown declared name is spec-sanctioned
// and stays non-fatal, but "non-fatal" and "silent" are separate decisions: this return value
// lets a caller report the typo (e.g. as an event or a log line) instead of losing it the moment
// source != RoleSourceDeclared.
func ResolveRole(catalog []roles.Role, stepID, declaredRole string) (role roles.Role, source RoleSource, unknownDeclared string, err error) {
	if declaredRole != "" {
		if r, ok := roles.RoleByName(catalog, declaredRole); ok {
			return r, RoleSourceDeclared, "", nil
		}
		// Declared but unknown to the catalog: fall through to tiers 2/3 rather than erroring,
		// per the spec, but keep the name so the caller can surface it.
		unknownDeclared = declaredRole
	}

	if r, ok := roles.RoleForStepID(catalog, stepID); ok {
		return r, RoleSourceCatalog, unknownDeclared, nil
	}

	// Tier 3: the heuristic. This is the single most important line in this file — it must
	// NEVER be a hardcoded "executor" default independent of IsReviewStep. A step id absent
	// from the catalog (e.g. a future or scenario-repo-outside-polyforge-coding id) that is
	// review-shaped by name MUST still resolve to the reviewer role (raised tier, read-only),
	// never to executor (default tier, write-capable), the exact silent demotion this
	// constraint exists to prevent.
	wantName := heuristicExecutorRole
	if IsReviewStep(stepID) {
		wantName = heuristicReviewerRole
	}
	r, ok := roles.RoleByName(catalog, wantName)
	if !ok {
		return roles.Role{}, "", unknownDeclared, fmt.Errorf(
			"engine: ResolveRole heuristic fallback wants role %q for step %q but the catalog has no such role (catalog corrupt or incomplete)",
			wantName, stepID)
	}
	return r, RoleSourceHeuristic, unknownDeclared, nil
}
