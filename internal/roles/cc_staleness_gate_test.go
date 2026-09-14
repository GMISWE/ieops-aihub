package roles

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCCStalenessGate pins aihub#642 plan step 7: RenderCCAgentFiles's output
// for all 5 roles must byte-match the currently committed
// plugins/polyforge/agents/step-<role>.md files exactly. A red result here
// means either a role definition (internal/roles/definitions/*.yaml),
// cc_aliases.yaml, or compile.go's CC shape changed without regenerating, or
// a committed agent file was hand-edited directly. Fix: run
// `go generate ./internal/roles/...` (or `go run ./internal/roles/gen`) and
// commit the result -- never hand-edit a generated step-*.md file.
//
// This mirrors internal/mcp/contract_cards_gate_test.go's regenerate-and-diff
// pattern, but does TRUE byte diffing rather than a semantic per-field diff:
// these 5 files have no hand-written prose sections that must survive
// regeneration (prepare_context's correction on this wi, mem_fj9AIr3b), so
// the simpler, stricter check is the correct one here.
func TestCCStalenessGate(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}
	aliases, err := LoadCCAliases()
	if err != nil {
		t.Fatalf("LoadCCAliases() error: %v", err)
	}
	rendered, err := RenderCCAgentFiles(roleList, aliases)
	if err != nil {
		t.Fatalf("RenderCCAgentFiles() error: %v", err)
	}
	if len(rendered) != 5 {
		t.Fatalf("RenderCCAgentFiles() produced %d files, want 5: %v", len(rendered), rendered)
	}

	// Committed-file location, relative to this package's own directory (the
	// cwd `go test` runs a package's tests with).
	committedDir := filepath.Join("..", "..", "plugins", "polyforge", "agents")

	for name, want := range rendered {
		path := filepath.Join(committedDir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read committed file at %s: %v (run `go generate ./internal/roles/...` and commit the result)",
				name, path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s is STALE: committed content does not byte-match regeneration.\n"+
				"Run `go generate ./internal/roles/...` (or `go run ./internal/roles/gen`) and commit the result.\n"+
				"--- committed (%d bytes) ---\n%s\n--- regenerated (%d bytes) ---\n%s",
				name, len(got), got, len(want), want)
		}
	}

	// 🔴 The loop above iterates RENDERED -> COMMITTED and never the other way,
	// so on its own it is blind to a file that exists on disk and corresponds to
	// no role: renaming or deleting a role leaves its old step-<role>.md behind
	// and this gate stays green. aihub#676 measured that (dropping a
	// step-zombie.md into plugins/polyforge/agents/ left `go test
	// ./internal/roles/` at exit 0) and the loop below closes it.
	//
	// HONEST SCOPE NOTE, because the measurement above is only half the story:
	// the same orphan IS already caught today, by
	// TestEngineDispatchIsRootedInTheRoleCatalog/NoAgentFileOutlivesItsRole in
	// internal/cli -- re-measured on the same mutant, which failed that test
	// naming step-zombie.md. So this is not an open hole being closed; it is the
	// check moving into the package that OWNS these files and whose doc comment
	// above claims to state everything a red here means. Incidental coverage
	// from a dispatch test in another package would disappear silently if that
	// test were ever refactored, and nothing would notice.
	entries, err := os.ReadDir(committedDir)
	if err != nil {
		t.Fatalf("read %s: %v", committedDir, err)
	}
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "step-") || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		scanned++
		if _, current := rendered[e.Name()]; !current {
			t.Errorf("%s is an ORPHAN: it matches this generator's naming but corresponds to no role "+
				"in internal/roles/definitions/ (which defines %d roles, rendering %v). A role was "+
				"renamed or deleted and its generated file was left behind -- a dispatchable agent "+
				"that no step can ever resolve to. Delete it.",
				filepath.Join(committedDir, e.Name()), len(roleList), sortedNames(rendered))
		}
	}
	if scanned == 0 {
		t.Fatalf("no step-*.md file was found under %s at all, so the orphan check above scanned "+
			"nothing and would have passed whatever the catalog said", committedDir)
	}
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestRenderedPromptsCarryNoForeignHarnessVocabulary pins aihub#676 finding 3.
//
// A role's YAML prompt is rendered VERBATIM (modulo ExpandPrompt) into all four
// harnesses, so any sentence in it naming a tool, a frontmatter key or a
// sandbox mode is a sentence that is false for three of them. The measured
// instance: reviewer and explorer both promised "Bash stays available so you
// can run builds, tests and linters", and pi's read-only shape is an allowlist
// (piReadOnlyTools) containing no shell at all -- so under pi the reviewer was
// told to verify by running tests it could not run.
//
// Two assertions, and the SECOND is the one that generalises:
//
//  1. no rendered file may still contain an unexpanded "{{" placeholder;
//  2. the raw YAML prompts -- the text that is shared -- may not contain any
//     harness-specific vocabulary at all. Only compile.go's per-harness
//     branches may say those words.
func TestRenderedPromptsCarryNoForeignHarnessVocabulary(t *testing.T) {
	roleList, err := LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles() error: %v", err)
	}

	// Vocabulary that belongs to exactly one harness. Each entry is a literal
	// substring; the value names the harness it is true for, purely so the
	// failure message can say why it is wrong to put it in shared text.
	foreign := map[string]string{
		"disallowedTools": "Claude Code",
		"NotebookEdit":    "Claude Code",
		"Bash":            "Claude Code / opencode (pi's read-only allowlist has no shell)",
		"sandbox_mode":    "codex",
		"permission:":     "opencode",
		"`tools`":         "pi",
		"frontmatter":     "cc/pi/opencode (codex role definitions are TOML, not frontmatter)",
	}
	// BOTH shared fields, not just the prompt. `description` is where the wi's
	// first-named instance actually lived -- reviewer.yaml's was "Edit/Write/
	// NotebookEdit disallowed by construction; Bash stays for running builds and
	// tests" -- and all four renderers emit it verbatim. Gating only the prompt
	// would have left the exact regression free to walk back in through the one
	// field it came from. It takes no placeholder (it must stay a single line),
	// so the rule there is simply: keep it harness-neutral.
	for _, r := range roleList {
		for _, field := range []struct{ name, text string }{
			{"prompt", r.Prompt},
			{"description", r.Description},
		} {
			for token, owner := range foreign {
				if strings.Contains(field.text, token) {
					t.Errorf("definitions/%s.yaml's %s contains %q, which is %s vocabulary. This text "+
						"is rendered verbatim into cc, pi, codex AND opencode, so it would be false for "+
						"the others. In a prompt, move it into internal/roles/compile.go's per-harness "+
						"branch behind %s / %s; in a description, reword it to be harness-neutral.",
						r.Name, field.name, token, owner, PlaceholderCapability, PlaceholderModelSource)
				}
			}
		}
	}

	// Every harness, both model-declared states, every role: nothing may ship
	// with a placeholder left in it.
	for _, harness := range DispatchHarnesses() {
		for _, declared := range []bool{true, false} {
			for _, r := range roleList {
				got, expErr := ExpandPrompt(r.Prompt, harness, r.Capability.ReadOnly, declared)
				if expErr != nil {
					t.Fatalf("ExpandPrompt(%q, %q) error: %v", r.Name, harness, expErr)
				}
				if strings.Contains(got, "{{") {
					t.Errorf("role %q under harness %q (modelDeclared=%v) still contains an unexpanded "+
						"placeholder after ExpandPrompt -- a typo'd token would ship to the model as "+
						"literal text:\n%s", r.Name, harness, declared, got)
				}
			}
		}
	}
}

// TestExpandPromptIsHarnessTruthfulAboutTheShell pins the specific falsehood
// aihub#676 found, in both directions, so it cannot come back as prose drift.
//
// pi is the ONLY harness whose read-only shape removes the shell
// (piReadOnlyTools is an allowlist of read/grep/find/ls plus four MCP reads).
// cc removes three edit tools and leaves Bash; codex runs a read-only sandbox
// in which commands still execute; opencode denies `edit` and leaves bash at
// its inherited default. A read-only role's prose must therefore tell pi it
// cannot run anything, and must NOT tell the other three the same.
func TestExpandPromptIsHarnessTruthfulAboutTheShell(t *testing.T) {
	const probe = "X" + PlaceholderCapability + "Y"

	// The notes are hard-wrapped so the generated files stay readable, so every
	// assertion below compares against whitespace-collapsed text. Matching a
	// multi-word phrase against wrapped prose would otherwise fail or pass on
	// where the line happened to break, not on what the sentence says.
	flat := func(harness string) string {
		note, err := ExpandPrompt(probe, harness, true, true)
		if err != nil {
			t.Fatalf("ExpandPrompt(%s): %v", harness, err)
		}
		if !strings.HasPrefix(note, "X") || !strings.HasSuffix(note, "Y") {
			// ExpandPrompt substitutes; it must not rewrite the surrounding prompt.
			t.Fatalf("ExpandPrompt(%s) did not preserve the text around the placeholder: %q", harness, note)
		}
		return strings.Join(strings.Fields(note), " ")
	}

	piNote := flat("pi")
	if !strings.Contains(piNote, "CANNOT run builds, tests or linters") {
		t.Errorf("pi's read-only note does not say the agent cannot run anything. pi's allowlist is "+
			"%q -- no shell. Telling a pi reviewer to run tests leaves it only 'cannot verify' or an "+
			"unverified approval, which is the aihub#676 defect.\ngot: %s", piReadOnlyTools, piNote)
	}
	if strings.Contains(piNote, "so you can run builds, tests and linters") {
		t.Errorf("pi's read-only note still promises a shell:\n%s", piNote)
	}

	for _, harness := range []string{"cc", "codex", "opencode"} {
		note := flat(harness)
		if strings.Contains(note, "CANNOT run builds, tests or linters") {
			t.Errorf("%s's read-only note claims the agent cannot run commands, but only pi's shape "+
				"removes the shell:\n%s", harness, note)
		}
		if !strings.Contains(note, "builds, tests and linters") {
			t.Errorf("%s's read-only note does not tell the agent it CAN verify, which is the whole "+
				"reason a reviewer is useful:\n%s", harness, note)
		}
	}
}

// TestExpandPromptRejectsUnknownHarness is the negative control for the two
// tests above: they would both pass vacuously if ExpandPrompt silently returned
// the prompt unchanged for a harness it does not know.
func TestExpandPromptRejectsUnknownHarness(t *testing.T) {
	for _, harness := range []string{"", "claude", "pi ", "CC"} {
		if _, err := ExpandPrompt("x"+PlaceholderCapability, harness, true, true); err == nil {
			t.Errorf("ExpandPrompt(harness=%q) returned no error; an unknown harness must not silently "+
				"render a prompt with no harness-specific prose in it", harness)
		}
	}
}
