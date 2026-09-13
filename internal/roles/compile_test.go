package roles

import "testing"

// TestCompileCapabilityAcrossRolesAndHarnesses pins AC1/decision #6 crossed
// with the harness table in mem_m7iwe3hM decision #6: every one of the 5
// loaded roles compiles to the correct native shape on cc, pi and codex, and
// never leaks a value into a Shape field that belongs to a different harness.
func TestCompileCapabilityAcrossRolesAndHarnesses(t *testing.T) {
	loaded, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	readOnly := map[string]bool{}
	for _, r := range loaded {
		readOnly[r.Name] = r.Capability.ReadOnly
	}
	for _, name := range []string{"executor", "operator", "explorer", "reviewer", "designer"} {
		if _, ok := readOnly[name]; !ok {
			t.Fatalf("role %q missing from LoadRoles() -- fix the test table, not this fatal", name)
		}
	}

	for _, role := range []string{"executor", "operator", "explorer", "reviewer", "designer"} {
		ro := readOnly[role]

		t.Run(role+"/cc", func(t *testing.T) {
			shape, err := CompileCapability(ro, "cc")
			if err != nil {
				t.Fatalf("CompileCapability(%v, cc) error: %v", ro, err)
			}
			want := ""
			if ro {
				want = "Edit, Write, NotebookEdit"
			}
			if shape.CCDisallowedTools != want {
				t.Errorf("CCDisallowedTools = %q, want %q", shape.CCDisallowedTools, want)
			}
			if shape.PiTools != "" || shape.CodexSandboxMode != "" {
				t.Errorf("cc shape leaked another harness's field: %+v", shape)
			}
		})

		t.Run(role+"/pi", func(t *testing.T) {
			shape, err := CompileCapability(ro, "pi")
			if err != nil {
				t.Fatalf("CompileCapability(%v, pi) error: %v", ro, err)
			}
			want := ""
			if ro {
				want = "read, grep, find, ls, polyforge_pf_get_work_item, polyforge_pf_get_step, polyforge_pf_list_work_items, polyforge_pf_recall"
			}
			if shape.PiTools != want {
				t.Errorf("PiTools = %q, want %q", shape.PiTools, want)
			}
			if shape.CCDisallowedTools != "" || shape.CodexSandboxMode != "" {
				t.Errorf("pi shape leaked another harness's field: %+v", shape)
			}
		})

		t.Run(role+"/codex", func(t *testing.T) {
			shape, err := CompileCapability(ro, "codex")
			if err != nil {
				t.Fatalf("CompileCapability(%v, codex) error: %v", ro, err)
			}
			want := ""
			if ro {
				want = "read-only"
			}
			if shape.CodexSandboxMode != want {
				t.Errorf("CodexSandboxMode = %q, want %q", shape.CodexSandboxMode, want)
			}
			if shape.CCDisallowedTools != "" || shape.PiTools != "" {
				t.Errorf("codex shape leaked another harness's field: %+v", shape)
			}
		})
	}
}

// TestCompileCapabilityCCAndPiReadOnlyValuesMatchCommittedAgentFiles pins the
// exact byte values against the two agent files the cc_staleness_gate
// (aihub#642 plan step 7) must byte-match: step-reviewer.md's
// `disallowedTools:` and pf-explore.md's `tools:` line.
func TestCompileCapabilityCCAndPiReadOnlyValuesMatchCommittedAgentFiles(t *testing.T) {
	cc, err := CompileCapability(true, "cc")
	if err != nil {
		t.Fatalf("CompileCapability(true, cc) error: %v", err)
	}
	if want := "Edit, Write, NotebookEdit"; cc.CCDisallowedTools != want {
		t.Errorf("cc.CCDisallowedTools = %q, want %q (step-reviewer.md's committed value)", cc.CCDisallowedTools, want)
	}

	pi, err := CompileCapability(true, "pi")
	if err != nil {
		t.Fatalf("CompileCapability(true, pi) error: %v", err)
	}
	if want := "read, grep, find, ls, polyforge_pf_get_work_item, polyforge_pf_get_step, polyforge_pf_list_work_items, polyforge_pf_recall"; pi.PiTools != want {
		t.Errorf("pi.PiTools = %q, want %q (pf-explore.md's committed value)", pi.PiTools, want)
	}
}

// TestCompileCapabilityRejectsOpencodeAndUnknownHarness pins AC12: this layer
// must not implement opencode (aihub#653's scope), and an unrecognized
// harness name must error rather than silently compile to a zero Shape.
func TestCompileCapabilityRejectsOpencodeAndUnknownHarness(t *testing.T) {
	if _, err := CompileCapability(true, "opencode"); err == nil {
		t.Error("CompileCapability(_, opencode) returned nil error, want an error (aihub#653's scope, not implemented here)")
	}
	if _, err := CompileCapability(false, "opencode"); err == nil {
		t.Error("CompileCapability(false, opencode) returned nil error, want an error")
	}
	if _, err := CompileCapability(true, "nonexistent-harness"); err == nil {
		t.Error("CompileCapability(_, nonexistent-harness) returned nil error, want an error")
	}
}
