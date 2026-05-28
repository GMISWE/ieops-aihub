package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// readSettings parses settings.json into a generic map for assertions.
func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	return settings
}

// findHookEntry returns the SessionStart hook entry whose command matches the
// given hook path, or nil if none is present.
func findHookEntry(t *testing.T, settings map[string]any, hookCmd string) map[string]any {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	sessionStart, _ := hooks["SessionStart"].([]any)
	for _, grp := range sessionStart {
		g, _ := grp.(map[string]any)
		entries, _ := g["hooks"].([]any)
		for _, e := range entries {
			m, _ := e.(map[string]any)
			if m == nil {
				continue
			}
			if cmd, _ := m["command"].(string); cmd == hookCmd {
				return m
			}
		}
	}
	return nil
}

// TestEnsureSettingsHook_FreshInstall covers the case where settings.json does
// not exist yet: the file should be created with a single SessionStart hook
// entry whose timeout matches sessionStartHookTimeoutMs (5000 ms).
func TestEnsureSettingsHook_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	if err := ensureSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("ensureSettingsHook: %v", err)
	}

	settings := readSettings(t, settingsPath)
	entry := findHookEntry(t, settings, hookCmd)
	if entry == nil {
		t.Fatal("expected SessionStart hook entry to be created")
	}

	if entry["type"] != "command" {
		t.Errorf(`type = %v, want "command"`, entry["type"])
	}
	ms, ok := entry["timeout"].(float64)
	if !ok {
		t.Fatalf("timeout has wrong type: %T", entry["timeout"])
	}
	if int(ms) != sessionStartHookTimeoutMs {
		t.Errorf("timeout = %v, want %d", ms, sessionStartHookTimeoutMs)
	}
}

// TestEnsureSettingsHook_ReconcileTimeout covers the regression case: an
// existing settings.json with a polyforge hook entry whose timeout is the
// old buggy value (5) must be reconciled to the current value (5000) when
// init is re-run. The fix relies on this — without reconcile, legacy installs
// stay broken forever.
func TestEnsureSettingsHook_ReconcileTimeout(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	// Seed settings.json with the legacy timeout-5 shape.
	legacy := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": hookCmd,
							"timeout": 5,
						},
					},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(settingsPath, b, 0644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	if err := ensureSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("ensureSettingsHook: %v", err)
	}

	settings := readSettings(t, settingsPath)
	entry := findHookEntry(t, settings, hookCmd)
	if entry == nil {
		t.Fatal("expected polyforge SessionStart hook entry to still be present")
	}
	ms, ok := entry["timeout"].(float64)
	if !ok {
		t.Fatalf("timeout has wrong type: %T", entry["timeout"])
	}
	if int(ms) != sessionStartHookTimeoutMs {
		t.Errorf("timeout = %v after reconcile, want %d", ms, sessionStartHookTimeoutMs)
	}

	// Calling again with the correct shape must be a no-op (no rewrite).
	stat1, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := ensureSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("ensureSettingsHook idempotent call: %v", err)
	}
	stat2, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Error("ensureSettingsHook rewrote settings.json on an idempotent call")
	}
}

// TestEnsureSettingsHook_PreservesUnrelatedEntries verifies that calling
// ensureSettingsHook does not touch other SessionStart hook entries (e.g.
// superpowers, session-restore) or unrelated top-level settings.
func TestEnsureSettingsHook_PreservesUnrelatedEntries(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	unrelatedHook := map[string]any{
		"type":    "command",
		"command": "/somewhere/other-hook.sh",
		"timeout": float64(10000),
	}
	original := map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{unrelatedHook},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(original, "", "  ")
	if err := os.WriteFile(settingsPath, b, 0644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	if err := ensureSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("ensureSettingsHook: %v", err)
	}

	settings := readSettings(t, settingsPath)
	if settings["theme"] != "dark" {
		t.Errorf("unrelated top-level setting 'theme' changed: %v", settings["theme"])
	}

	other := findHookEntry(t, settings, "/somewhere/other-hook.sh")
	if other == nil {
		t.Fatal("unrelated hook entry was removed")
	}
	if !reflect.DeepEqual(other, unrelatedHook) {
		t.Errorf("unrelated hook entry mutated:\n got: %#v\nwant: %#v", other, unrelatedHook)
	}

	added := findHookEntry(t, settings, hookCmd)
	if added == nil {
		t.Fatal("polyforge hook entry was not appended")
	}
	if int(added["timeout"].(float64)) != sessionStartHookTimeoutMs {
		t.Errorf("polyforge hook timeout = %v, want %d", added["timeout"], sessionStartHookTimeoutMs)
	}
}

// TestParseServerProjects_PreservesScenario guards the wi#58 fix: the CLI
// must decode the `scenario` field returned by GET /v1/projects so that
// member workspaces can clone the scenario repo and persist it into
// .polyforge.yaml. Regression coverage for the silent-drop bug where the
// field existed on the server but not on the CLI struct.
func TestParseServerProjects_PreservesScenario(t *testing.T) {
	const scenarioURL = "git@github.com:GMISWE/polyforge-coding.git"
	raw := map[string]any{
		"items": []any{
			map[string]any{
				"name":          "aihub",
				"owner_user_id": "u_xxx",
				"visible":       true,
				"scenario":      scenarioURL,
				"repos":         []any{},
			},
			map[string]any{
				"name":          "no-scenario",
				"owner_user_id": "u_xxx",
				"visible":       true,
				"repos":         []any{},
			},
		},
	}

	projects, err := parseServerProjects(raw)
	if err != nil {
		t.Fatalf("parseServerProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	if projects[0].Scenario == nil {
		t.Fatal("projects[0].Scenario is nil; want server scenario field to be decoded")
	}
	if *projects[0].Scenario != scenarioURL {
		t.Errorf("projects[0].Scenario = %q, want %q", *projects[0].Scenario, scenarioURL)
	}
	if projects[1].Scenario != nil {
		t.Errorf("projects[1].Scenario = %v, want nil for project without scenario", *projects[1].Scenario)
	}
}

// TestWriteMemberPolyforgeYAML_IncludesScenario guards the wi#58 fix: the
// generated member .polyforge.yaml must carry the per-project `scenario:`
// line whenever the server returned one. Without this, re-running pf init
// loses the scenario binding and downstream tools (pf-execute) can't
// resolve the scenario repo.
func TestWriteMemberPolyforgeYAML_IncludesScenario(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	scenarioURL := "git@github.com:GMISWE/polyforge-coding.git"
	projects := []serverProject{
		{
			Name:        "aihub",
			OwnerUserID: "u_xxx",
			Visible:     true,
			Scenario:    &scenarioURL,
			Repos:       json.RawMessage(`[{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"}]`),
		},
		{
			Name:        "no-scenario",
			OwnerUserID: "u_xxx",
			Visible:     true,
			Repos:       json.RawMessage(`[]`),
		},
	}

	path := filepath.Join(tmp, ".polyforge.yaml")
	if err := writeMemberPolyforgeYAML(path, projects); err != nil {
		t.Fatalf("writeMemberPolyforgeYAML: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .polyforge.yaml: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "scenario: "+scenarioURL) {
		t.Errorf("rendered yaml missing `scenario: %s` line; got:\n%s", scenarioURL, got)
	}
	// Project without a server scenario must not emit a scenario line
	// (config.Project tag is `scenario,omitempty`). Count `scenario: `
	// occurrences — the trailing space avoids matching the `no-scenario:`
	// project key.
	if n := strings.Count(got, "scenario: "); n != 1 {
		t.Errorf("rendered yaml has %d `scenario: ` lines, want exactly 1; got:\n%s", n, got)
	}
}

// ─── renderRepoBlock ─────────────────────────────────────────────────────────

func TestRenderRepoBlock(t *testing.T) {
	desc := "Go HTTP server + PostgreSQL"
	cases := []struct {
		name       string
		repo       repoEntry
		wantSubstr []string
		notWant    []string
	}{
		{
			name: "structured block renders headline + bullets",
			repo: repoEntry{
				Name:            "aihub",
				Positioning:     "polyforge core API",
				TechStack:       []string{"Go", "PostgreSQL"},
				MainModules:     []repoModuleEntry{{Path: "internal/api", Role: "HTTP handlers"}, {Path: "internal/store", Role: "PG store"}},
				ChangeScenarios: []string{"add MCP tool", "schema migration"},
				GeneratedAt:     "2026-05-27T05:10:00Z",
				GeneratedCommit: "cef95e2ca68312651e1e147177f80c0c854a87cb",
			},
			wantSubstr: []string{
				"- **aihub**: polyforge core API",
				"  - stack: Go, PostgreSQL",
				"  - modules:\n",
				"    - internal/api — HTTP handlers\n",
				"    - internal/store — PG store\n",
				"  - changes:\n",
				"    - add MCP tool\n",
				"    - schema migration\n",
				"  - generated: 2026-05-27 @ cef95e2\n",
			},
			notWant: []string{"  - changes: add MCP tool; schema migration"},
		},
		{
			name:       "legacy description-only renders headline, no bullets",
			repo:       repoEntry{Name: "marketplace", Description: &desc},
			wantSubstr: []string{"- **marketplace**: Go HTTP server + PostgreSQL"},
			notWant:    []string{"  - stack:", "  - modules:", "  - changes:"},
		},
		{
			name:       "empty repo renders pending placeholder",
			repo:       repoEntry{Name: "proxy-server"},
			wantSubstr: []string{"- **proxy-server**: *(description pending"},
			notWant:    []string{"  - stack:"},
		},
		{
			name: "embedded newline is collapsed to a single line",
			repo: repoEntry{
				Name:            "x",
				Positioning:     "line one\nline two",
				TechStack:       []string{"Go"},
				MainModules:     []repoModuleEntry{{Path: "p", Role: "r"}},
				ChangeScenarios: []string{"c"},
			},
			wantSubstr: []string{"- **x**: line one line two"},
			notWant:    []string{"line one\nline two"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			renderRepoBlock(&sb, tc.repo)
			got := sb.String()
			for _, want := range tc.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, no := range tc.notWant {
				if strings.Contains(got, no) {
					t.Errorf("unexpected %q in:\n%s", no, got)
				}
			}
		})
	}
}

// ─── callerHasRole ────────────────────────────────────────────────────────────

// TestCallerHasRole guards the aihub#87 fix: polyforge init must only clone
// projects where the caller is owner or appears in members[]. Public-visible
// projects without an explicit caller role must be skipped.
func TestCallerHasRole(t *testing.T) {
	owner := serverProject{
		Name:        "owned",
		OwnerUserID: "u_owner",
		Visible:     true,
	}
	member := serverProject{
		Name:        "joined",
		OwnerUserID: "u_other",
		Visible:     true,
		Members: []serverProjectMember{
			{UserID: "u_alice", Role: "writer"},
			{UserID: "u_bob", Role: "viewer"},
		},
	}
	publicOnly := serverProject{
		Name:        "public",
		OwnerUserID: "u_other",
		Visible:     true,
		// no members[] containing the caller
	}

	cases := []struct {
		name string
		sp   serverProject
		uid  string
		want bool
	}{
		{"owner matches by owner_user_id", owner, "u_owner", true},
		{"non-owner without membership is false", owner, "u_alice", false},
		{"member listed in members[] is true", member, "u_alice", true},
		{"viewer listed in members[] is true (role doesn't gate, presence does)", member, "u_bob", true},
		{"unrelated uid against member project is false", member, "u_eve", false},
		{"public-only project (no membership) is false", publicOnly, "u_alice", false},
		{"empty uid never has role even if owner_user_id is empty", serverProject{}, "", false},
		{"empty uid against real project is false", member, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := callerHasRole(tc.sp, tc.uid); got != tc.want {
				t.Errorf("callerHasRole(%s, %q) = %v, want %v", tc.sp.Name, tc.uid, got, tc.want)
			}
		})
	}
}

// TestServerProjectMembersParse guards that the JSON shape returned by
// GET /v1/projects (members: [{user_id, role}]) deserializes into
// serverProject.Members. Regression for aihub#87 — without this field,
// callerHasRole can never see member entries.
func TestServerProjectMembersParse(t *testing.T) {
	raw := `{
        "items": [
            {
                "name": "aihub",
                "owner_user_id": "u_owner",
                "visible": true,
                "members": [
                    {"user_id": "u_alice", "role": "writer"},
                    {"user_id": "u_bob", "role": "viewer"}
                ],
                "repos": []
            }
        ]
    }`
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	projects, err := parseServerProjects(m)
	if err != nil {
		t.Fatalf("parseServerProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	if got := len(projects[0].Members); got != 2 {
		t.Fatalf("got %d members, want 2", got)
	}
	if projects[0].Members[0].UserID != "u_alice" || projects[0].Members[0].Role != "writer" {
		t.Errorf("members[0] = %+v, want {u_alice writer}", projects[0].Members[0])
	}
	if !callerHasRole(projects[0], "u_alice") {
		t.Errorf("callerHasRole(aihub, u_alice) = false, want true")
	}
	if callerHasRole(projects[0], "u_eve") {
		t.Errorf("callerHasRole(aihub, u_eve) = true, want false")
	}
}
