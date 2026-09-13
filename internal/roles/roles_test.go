package roles

import "testing"

// TestLoadRolesParsesAllFive pins AC1: exactly the 5 FINAL roles load, each with a
// tier from the fixed vocabulary and a non-empty step_ids/prompt.
func TestLoadRolesParsesAllFive(t *testing.T) {
	got, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("LoadRoles() returned %d roles, want 5: %v", len(got), got)
	}
	wantNames := map[string]bool{
		"executor": true, "operator": true, "explorer": true, "reviewer": true, "designer": true,
	}
	for _, r := range got {
		if !wantNames[r.Name] {
			t.Errorf("unexpected role name %q", r.Name)
		}
		delete(wantNames, r.Name)
		if r.Prompt == "" {
			t.Errorf("role %q has empty prompt", r.Name)
		}
		if r.Description == "" {
			t.Errorf("role %q has empty description", r.Name)
		}
	}
	if len(wantNames) != 0 {
		t.Errorf("roles missing from LoadRoles(): %v", wantNames)
	}
}

// TestOccurrenceCountTotals108 pins the spec's measured total (aihub#642 mem_m7iwe3hM
// decision #2): the 5 roles' occurrence_count fields, summed, equal 108 -- the
// scenario-repo occurrence count this classification was derived to reproduce
// exactly (see executor.yaml's header comment for the derivation).
func TestOccurrenceCountTotals108(t *testing.T) {
	got, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	total := 0
	perRole := map[string]int{}
	for _, r := range got {
		total += r.OccurrenceCount
		perRole[r.Name] = r.OccurrenceCount
	}
	if total != 108 {
		t.Errorf("sum of occurrence_count = %d, want 108 (per-role: %v)", total, perRole)
	}
	want := map[string]int{"executor": 46, "operator": 33, "explorer": 12, "reviewer": 10, "designer": 7}
	for name, wantCount := range want {
		if perRole[name] != wantCount {
			t.Errorf("role %q occurrence_count = %d, want %d", name, perRole[name], wantCount)
		}
	}
}

// TestPlanCarveOutStaysWithExecutor pins the spec's explicit carve-out: `plan` is
// bound to executor, not promoted to designer alongside `spec` (Non-goals / decision
// #2's footnote, verbatim in the spec).
func TestPlanCarveOutStaysWithExecutor(t *testing.T) {
	got, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	r, ok := RoleForStepID(got, "plan")
	if !ok {
		t.Fatal(`step id "plan" is not bound to any role`)
	}
	if r.Name != "executor" {
		t.Errorf(`step id "plan" bound to role %q, want "executor" (spec carve-out)`, r.Name)
	}
}

// TestDeployProdCarveOutExcludedFromOperator pins the spec's other explicit
// carve-out: `deploy_prod` is mechanical but stays OUT of operator because its
// error cost is asymmetric (Non-goals, verbatim in the spec).
func TestDeployProdCarveOutExcludedFromOperator(t *testing.T) {
	got, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	operator, ok := RoleByName(got, "operator")
	if !ok {
		t.Fatal("operator role not found")
	}
	for _, id := range operator.StepIDs {
		if id == "deploy_prod" {
			t.Fatal(`"deploy_prod" must NOT be in operator's step_ids (spec carve-out)`)
		}
	}
	// And it must be bound SOMEWHERE (not silently dropped) -- executor, per
	// executor.yaml's derivation.
	r, ok := RoleForStepID(got, "deploy_prod")
	if !ok {
		t.Fatal(`step id "deploy_prod" is not bound to any role`)
	}
	if r.Name != "executor" {
		t.Errorf(`step id "deploy_prod" bound to role %q, want "executor"`, r.Name)
	}
}

// TestCapabilityReadOnlyMatchesSpecTable pins AC1/decision #6: explorer and
// reviewer are read-only; executor, operator and designer are write-capable.
func TestCapabilityReadOnlyMatchesSpecTable(t *testing.T) {
	got, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	want := map[string]bool{
		"executor": false, "operator": false, "explorer": true, "reviewer": true, "designer": false,
	}
	for _, r := range got {
		if r.Capability.ReadOnly != want[r.Name] {
			t.Errorf("role %q capability.read_only = %v, want %v", r.Name, r.Capability.ReadOnly, want[r.Name])
		}
	}
}

// TestNoStepIDBoundToMoreThanOneRole guards the data model's implicit invariant:
// RoleForStepID's first-match-wins behavior is only safe if no step id appears in
// two roles' step_ids.
func TestNoStepIDBoundToMoreThanOneRole(t *testing.T) {
	got, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	owner := map[string]string{}
	for _, r := range got {
		for _, id := range r.StepIDs {
			if prev, ok := owner[id]; ok {
				t.Errorf("step id %q bound to both %q and %q", id, prev, r.Name)
				continue
			}
			owner[id] = r.Name
		}
	}
}

// TestLoadCCAliasesHasAllFourTiers pins the cc_aliases.yaml sibling file's
// contract: every tier in ValidTiers resolves to a CC model alias.
func TestLoadCCAliasesHasAllFourTiers(t *testing.T) {
	aliases, err := LoadCCAliases()
	if err != nil {
		t.Fatalf("LoadCCAliases() error: %v", err)
	}
	for _, tier := range ValidTiers {
		if aliases[tier] == "" {
			t.Errorf("cc_aliases.yaml missing/empty entry for tier %q", tier)
		}
	}
	// Pin the exact values decision #2 of the plan settled on, so a future edit to
	// this file that silently drops a tier's meaning is caught here rather than
	// only downstream in the CC staleness gate.
	want := map[string]string{"lowest": "haiku", "low": "haiku", "default": "sonnet", "raised": "opus"}
	for tier, wantAlias := range want {
		if aliases[tier] != wantAlias {
			t.Errorf("cc_aliases.yaml[%q] = %q, want %q", tier, aliases[tier], wantAlias)
		}
	}
}

// TestRoleByNameAndForStepIDMiss covers the not-found paths of both lookup
// helpers, so a future refactor cannot make them panic instead of returning ok=false.
func TestRoleByNameAndForStepIDMiss(t *testing.T) {
	got, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	if _, ok := RoleByName(got, "nonexistent-role"); ok {
		t.Error("RoleByName(nonexistent-role) = ok, want !ok")
	}
	if _, ok := RoleForStepID(got, "nonexistent-step"); ok {
		t.Error("RoleForStepID(nonexistent-step) = ok, want !ok")
	}
}
