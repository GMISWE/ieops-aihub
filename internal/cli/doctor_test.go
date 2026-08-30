package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWorkspace lays out a fake workspace: CLAUDE.md carrying `block` (empty =
// no CLAUDE.md at all) and a .polyforge/repo-map/ holding `maps` (nil = the
// directory does not exist at all).
func writeWorkspace(t *testing.T, block string, maps []string) string {
	t.Helper()
	ws := t.TempDir()
	if block != "" {
		if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("intro\n\n"+block+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if maps != nil {
		dir := filepath.Join(ws, ".polyforge", repoMapDirName)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, m := range maps {
			if err := os.WriteFile(filepath.Join(dir, m), []byte("# map\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return ws
}

// legacyBlock is the pre-aihub#291 render: per-repo detail inlined.
func legacyBlock() string {
	return managedBlockStart + "\n## Workspace\n\n### aihub\n\n" +
		"- **aihub**: polyforge core API\n" +
		"  - stack: Go, PostgreSQL\n" +
		"  - modules:\n    - internal/api — HTTP handlers\n" +
		"  - changes:\n    - add MCP tool\n" +
		"  - generated: 2026-05-27 @ cef95e2\n" +
		managedBlockEnd
}

// slimBlock is the post-aihub#291 render, for the named projects.
func slimBlock(projects ...string) string {
	var sb strings.Builder
	sb.WriteString(managedBlockStart + "\n## Workspace\n")
	for _, p := range projects {
		fmt.Fprintf(&sb, "\n### %s\n\n- **%s-repo**: positioning line\n", p, p)
	}
	sb.WriteString(managedBlockEnd)
	return sb.String()
}

// TestCheckClaudeMd covers the aihub#291 delivery gate: `polyforge doctor` is
// what tells a workspace its managed block is still the fat legacy format (the
// saving only lands once someone re-runs `polyforge init`, and workspaces go
// months without doing so), and it is also what reports a MISSING repo map
// instead of letting routing silently guess from the one-line positioning.
func TestCheckClaudeMd(t *testing.T) {
	cases := []struct {
		name       string
		block      string
		maps       []string
		wantStatus string
		wantSubstr string
	}{
		{
			name:       "legacy fat block warns and names the cost",
			block:      legacyBlock(),
			maps:       nil,
			wantStatus: "warning",
			wantSubstr: "legacy inline format",
		},
		{
			name:       "legacy block warns even when maps already exist",
			block:      legacyBlock(),
			maps:       []string{"aihub.md"},
			wantStatus: "warning",
			wantSubstr: "legacy inline format",
		},
		{
			name:       "slim block with a map for every project is ok",
			block:      slimBlock("aihub"),
			maps:       []string{"aihub.md"},
			wantStatus: "ok",
		},
		{
			name:       "slim block with the whole repo-map dir gone reports it missing",
			block:      slimBlock("aihub"),
			maps:       nil,
			wantStatus: "warning",
			wantSubstr: "repo map missing",
		},
		{
			name:       "slim block with an empty repo-map dir reports it missing",
			block:      slimBlock("aihub"),
			maps:       []string{},
			wantStatus: "warning",
			wantSubstr: "repo map missing",
		},
		{
			name:       "one project's map deleted names that project",
			block:      slimBlock("aihub", "ieops"),
			maps:       []string{"aihub.md"},
			wantStatus: "warning",
			wantSubstr: "ieops",
		},
		{
			name:       "no CLAUDE.md at all warns rather than failing",
			block:      "",
			wantStatus: "warning",
			wantSubstr: "CLAUDE.md",
		},
		{
			name:       "CLAUDE.md without a managed block warns",
			block:      "## Handwritten\n\nno markers here",
			wantStatus: "warning",
			wantSubstr: "managed block",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := writeWorkspace(t, tc.block, tc.maps)
			got := checkClaudeMd(ws)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (message: %s)", got.Status, tc.wantStatus, got.Message)
			}
			if tc.wantSubstr != "" && !strings.Contains(got.Message+" "+got.FixCmd, tc.wantSubstr) {
				t.Errorf("message %q + fix %q does not mention %q", got.Message, got.FixCmd, tc.wantSubstr)
			}
			// A stale block is a cost, not a broken workspace: RunDoctor exits 1
			// on any "error", and that would make `polyforge doctor` fail for
			// every workspace that has not re-run init yet.
			if got.Status == "error" {
				t.Errorf("must never be an error: %+v", got)
			}
			if got.Name != "claude_md" {
				t.Errorf("check name = %q, want %q", got.Name, "claude_md")
			}
			if got.Status == "warning" && got.FixCmd == "" {
				t.Error("a warning must carry the fix command")
			}
		})
	}
}

// TestCheckClaudeMdIgnoresUnrenderedProjects is a regression test for a bug the
// live negative probe caught: the expected map set must come from the block's
// own `### <project>` headings, NOT from .polyforge.yaml. `polyforge init` only
// renders projects the caller has a role in (callerHasRole), so a workspace can
// legitimately list a project in .polyforge.yaml that has no block section and
// therefore no map. Classifying by the config reported that as missing.
func TestCheckClaudeMdIgnoresUnrenderedProjects(t *testing.T) {
	ws := writeWorkspace(t, slimBlock("aihub"), []string{"aihub.md"})
	// A project present on disk/config but absent from the block must not warn.
	if got := checkClaudeMd(ws); got.Status != "ok" {
		t.Errorf("a project not rendered into the block must not be reported missing; got %q: %s",
			got.Status, got.Message)
	}
}

// TestCheckClaudeMdLegacyDetectionIsNotBytecount pins the detector to the
// legacy `  - modules:` bullet rather than to a size threshold: a workspace with
// many repos can have a large *slim* block, and that is not stale.
func TestCheckClaudeMdLegacyDetectionIsNotBytecount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(managedBlockStart + "\n## Workspace\n\n### big\n\n")
	for i := 0; i < 200; i++ {
		sb.WriteString("- **repo**: a long positioning line that makes this block big without being legacy\n")
	}
	sb.WriteString(managedBlockEnd)

	ws := writeWorkspace(t, sb.String(), []string{"big.md"})
	if got := checkClaudeMd(ws); got.Status != "ok" {
		t.Errorf("a big but slim block must be ok, got %q: %s", got.Status, got.Message)
	}
}

func TestManagedBlockProjects(t *testing.T) {
	block := slimBlock("aihub", "global-routing", "ieops")
	got := managedBlockProjects(block)
	want := []string{"aihub", "global-routing", "ieops"}
	if len(got) != len(want) {
		t.Fatalf("managedBlockProjects = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("managedBlockProjects[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Repo bullets and prose must not be mistaken for project headings.
	if n := len(managedBlockProjects(managedBlockStart + "\n## Workspace\n\n- **r**: x\ntext\n" + managedBlockEnd)); n != 0 {
		t.Errorf("managedBlockProjects found %d headings in a block with none", n)
	}
}
