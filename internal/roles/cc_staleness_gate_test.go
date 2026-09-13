package roles

import (
	"os"
	"path/filepath"
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
}
