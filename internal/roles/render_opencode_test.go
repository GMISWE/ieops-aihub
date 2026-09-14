package roles

import (
	"strings"
	"testing"
)

// TestRenderOpencodeAgentFiles_FileNamingMatchesStepPrefix pins the naming
// convention decision: opencode DOES accept an overriding `name:` frontmatter
// field (config/config.ts ~423, agent/agent.ts ~226 -- confirmed against
// opencode's own source, aihub#653 code_review), but RenderOpencodeAgentFiles
// never emits one, so for these generated files the filename is what actually
// determines the agent's name. "step-<role>.md" is what gives each role the
// same logical agent name across cc/codex/opencode ("step-executor",
// "step-reviewer", ...).
func TestRenderOpencodeAgentFiles_FileNamingMatchesStepPrefix(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	out, err := RenderOpencodeAgentFiles(roleList, nil)
	if err != nil {
		t.Fatalf("RenderOpencodeAgentFiles() error: %v", err)
	}
	if len(out) != len(roleList) {
		t.Fatalf("RenderOpencodeAgentFiles() returned %d files, want %d (one per role)", len(out), len(roleList))
	}
	for _, r := range roleList {
		wantName := "step-" + r.Name + ".md"
		if _, ok := out[wantName]; !ok {
			t.Errorf("missing output file %q for role %q", wantName, r.Name)
		}
	}
}

// TestRenderOpencodeAgentFiles_ReadOnlyIsGenuinelyReadOnly pins AC9: a
// read-only role's frontmatter carries `permission:\n  edit: deny` and
// nothing else restrictive -- bash/read/grep/glob/list/webfetch must stay
// unset so they inherit the caller's allow default, matching CC's
// disallowedTools: "Edit, Write, NotebookEdit" parity shape (block mutation,
// keep everything a reviewer needs to actually verify work). A write-capable
// role must get NO permission block at all.
func TestRenderOpencodeAgentFiles_ReadOnlyIsGenuinelyReadOnly(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	out, err := RenderOpencodeAgentFiles(roleList, nil)
	if err != nil {
		t.Fatalf("RenderOpencodeAgentFiles() error: %v", err)
	}
	for _, r := range roleList {
		content := out["step-"+r.Name+".md"]
		frontmatter, _, ok := strings.Cut(strings.TrimPrefix(content, "---\n"), "\n---\n")
		if !ok {
			t.Fatalf("role %q: output has no closing frontmatter delimiter:\n%s", r.Name, content)
		}
		hasPermission := strings.Contains(frontmatter, "permission:")
		if r.Capability.ReadOnly {
			if !strings.Contains(frontmatter, "permission:\n  edit: deny") {
				t.Errorf("role %q is read-only but frontmatter lacks `permission:\\n  edit: deny`:\n%s", r.Name, frontmatter)
			}
			// Only the one restrictive key -- no accidental bash/read/grep/etc denial.
			for _, forbiddenKey := range []string{"bash:", "read:", "grep:", "glob:", "list:", "webfetch:"} {
				if strings.Contains(frontmatter, forbiddenKey) {
					t.Errorf("role %q is read-only but frontmatter unexpectedly restricts %q too:\n%s", r.Name, forbiddenKey, frontmatter)
				}
			}
		} else if hasPermission {
			t.Errorf("role %q is write-capable but frontmatter has a permission block (should be absent, inherit-all):\n%s", r.Name, frontmatter)
		}
	}
}

// TestRenderOpencodeAgentFiles_ModelOmittedWhenUnresolved pins the
// omit-and-inherit convention shared with the other three renderers: a role
// absent from resolvedModels (or mapped to "") gets no `model:` field, not a
// blank one.
func TestRenderOpencodeAgentFiles_ModelOmittedWhenUnresolved(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	out, err := RenderOpencodeAgentFiles(roleList, map[string]string{"executor": ""})
	if err != nil {
		t.Fatalf("RenderOpencodeAgentFiles() error: %v", err)
	}
	for _, r := range roleList {
		content := out["step-"+r.Name+".md"]
		if strings.Contains(content, "model:") {
			t.Errorf("role %q has no resolved model but output contains a `model:` line:\n%s", r.Name, content)
		}
	}
}

// TestRenderOpencodeAgentFiles_ModelPresentWhenResolved pins the positive
// case of the same convention: a resolved model IS emitted, verbatim.
func TestRenderOpencodeAgentFiles_ModelPresentWhenResolved(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	out, err := RenderOpencodeAgentFiles(roleList, map[string]string{"executor": "anthropic/claude-sonnet-4-20250514"})
	if err != nil {
		t.Fatalf("RenderOpencodeAgentFiles() error: %v", err)
	}
	content := out["step-executor.md"]
	if !strings.Contains(content, "model: anthropic/claude-sonnet-4-20250514\n") {
		t.Errorf("role executor: expected resolved model line in output:\n%s", content)
	}
}

// TestRenderOpencodeAgentFiles_ModeSubagentAlways pins that every generated
// role file declares mode: subagent -- all 5 roles are dispatched as
// subagent_type by the pf-execute loop (never a primary/chat agent), per each
// role definition's own description field.
func TestRenderOpencodeAgentFiles_ModeSubagentAlways(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	out, err := RenderOpencodeAgentFiles(roleList, nil)
	if err != nil {
		t.Fatalf("RenderOpencodeAgentFiles() error: %v", err)
	}
	for _, r := range roleList {
		content := out["step-"+r.Name+".md"]
		if !strings.Contains(content, "mode: subagent\n") {
			t.Errorf("role %q: expected `mode: subagent` line in output:\n%s", r.Name, content)
		}
	}
}

// TestRenderOpencodeAgentFiles_PromptBodyVerbatim pins that the role's Prompt
// is carried through unmodified as the file body (same contract render_pi.go/
// render_cc.go/render_codex.go pin for their own harnesses).
func TestRenderOpencodeAgentFiles_PromptBodyVerbatim(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	out, err := RenderOpencodeAgentFiles(roleList, nil)
	if err != nil {
		t.Fatalf("RenderOpencodeAgentFiles() error: %v", err)
	}
	for _, r := range roleList {
		content := out["step-"+r.Name+".md"]
		if !strings.HasSuffix(content, r.Prompt) {
			t.Errorf("role %q: output does not end with the role's Prompt verbatim", r.Name)
		}
	}
}
