package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
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

// seedSettings writes a settings.json fixture and backdates its mtime so that
// "was this file rewritten?" assertions are deterministic rather than relying
// on filesystem timestamp resolution.
func seedSettings(t *testing.T, path string, v map[string]any) time.Time {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal settings fixture: %v", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("backdate settings.json: %v", err)
	}
	return past
}

// sessionStartGroups returns the raw hooks.SessionStart array.
func sessionStartGroups(t *testing.T, settings map[string]any) []any {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	groups, _ := hooks["SessionStart"].([]any)
	return groups
}

// TestRemoveSettingsHook_RemovesExactCommandMatch covers the primary reverse
// cleanup: the legacy pf-session-start.sh entry is dropped from
// hooks.SessionStart while its enclosing group stays in place.
// TestRemoveSettingsHook_PreservesFileMode: the rewrite goes through a temp
// file + rename, which swaps in a new inode. Without explicitly carrying the
// old mode over, a user who ran `chmod 600 ~/.claude/settings.json` would have
// it silently widened to 0644 — that file can hold an env block with API keys,
// so quietly relaxing it is well outside what this cleanup is entitled to do.
func TestRemoveSettingsHook_PreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	seedSettings(t, settingsPath, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": hookCmd},
			}}},
		},
	})
	if err := os.Chmod(settingsPath, 0600); err != nil {
		t.Fatalf("chmod seed settings: %v", err)
	}

	removed, err := removeSettingsHook(settingsPath, hookCmd)
	if err != nil {
		t.Fatalf("removeSettingsHook: %v", err)
	}
	if !removed {
		t.Fatal("removed = false, want true")
	}

	fi, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat settings after rewrite: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("settings.json mode = %04o after rewrite, want 0600 (permissions must not be widened)", got)
	}
}

func TestRemoveSettingsHook_RemovesExactCommandMatch(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	seedSettings(t, settingsPath, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": hookCmd, "timeout": 5000},
					},
				},
			},
		},
	})

	if _, err := removeSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("removeSettingsHook: %v", err)
	}

	settings := readSettings(t, settingsPath)
	if entry := findHookEntry(t, settings, hookCmd); entry != nil {
		t.Fatalf("legacy hook entry still registered: %#v", entry)
	}

	// The now-empty group must survive — we do not prune containers.
	groups := sessionStartGroups(t, settings)
	if len(groups) != 1 {
		t.Fatalf("SessionStart groups = %d, want 1 (empty group must be kept)", len(groups))
	}
	g, _ := groups[0].(map[string]any)
	entries, ok := g["hooks"].([]any)
	if !ok {
		t.Fatalf("group.hooks has wrong type: %T", g["hooks"])
	}
	if len(entries) != 0 {
		t.Errorf("group.hooks = %#v, want empty slice", entries)
	}
}

// TestRemoveSettingsHook_OnlyExactCommandMatch guards against substring or
// prefix matching: entries that merely contain the legacy path must survive.
func TestRemoveSettingsHook_OnlyExactCommandMatch(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	lookalikes := []string{
		hookCmd + " --verbose",
		"bash " + hookCmd,
		"/home/u/.claude/hooks/pf-session-start.sh.bak",
		"/home/u/.claude/hooks/pf-session-start",
	}
	var entries []any
	for _, cmd := range lookalikes {
		entries = append(entries, map[string]any{"type": "command", "command": cmd, "timeout": float64(5000)})
	}
	entries = append(entries, map[string]any{"type": "command", "command": hookCmd, "timeout": float64(5000)})

	seedSettings(t, settingsPath, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": entries}},
		},
	})

	if _, err := removeSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("removeSettingsHook: %v", err)
	}

	settings := readSettings(t, settingsPath)
	if entry := findHookEntry(t, settings, hookCmd); entry != nil {
		t.Fatal("exact-match entry was not removed")
	}
	for _, cmd := range lookalikes {
		if findHookEntry(t, settings, cmd) == nil {
			t.Errorf("non-exact match %q was wrongly removed", cmd)
		}
	}
}

// TestRemoveSettingsHook_PreservesUnrelatedEntries is the adapted form of the
// old hook-registration coverage: sibling hook entries in the same group and
// unrelated top-level keys must come through the surgery untouched.
func TestRemoveSettingsHook_PreservesUnrelatedEntries(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	unrelatedHook := map[string]any{
		"type":    "command",
		"command": "/somewhere/other-hook.sh",
		"timeout": float64(10000),
	}
	otherEvent := []any{
		map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": hookCmd},
		}},
	}
	seedSettings(t, settingsPath, map[string]any{
		"theme":                  "dark",
		"enabledPlugins":         map[string]any{"polyforge@ieops-aihub": true},
		"extraKnownMarketplaces": map[string]any{"ieops-aihub": map[string]any{"source": "github"}},
		"permissions":            map[string]any{"allow": []any{"Bash(go test:*)"}},
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						unrelatedHook,
						map[string]any{"type": "command", "command": hookCmd, "timeout": float64(5000)},
					},
				},
			},
			// A same-command entry under a different event must not be touched:
			// the cleanup is scoped to SessionStart.
			"SessionEnd": otherEvent,
		},
	})

	if _, err := removeSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("removeSettingsHook: %v", err)
	}

	settings := readSettings(t, settingsPath)
	if settings["theme"] != "dark" {
		t.Errorf("unrelated top-level setting 'theme' changed: %v", settings["theme"])
	}
	for _, key := range []string{"enabledPlugins", "extraKnownMarketplaces", "permissions"} {
		if _, ok := settings[key]; !ok {
			t.Errorf("unrelated top-level key %q was dropped", key)
		}
	}

	other := findHookEntry(t, settings, "/somewhere/other-hook.sh")
	if other == nil {
		t.Fatal("sibling hook entry was removed")
	}
	if !reflect.DeepEqual(other, unrelatedHook) {
		t.Errorf("sibling hook entry mutated:\n got: %#v\nwant: %#v", other, unrelatedHook)
	}

	if findHookEntry(t, settings, hookCmd) != nil {
		t.Error("legacy SessionStart hook entry was not removed")
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if !reflect.DeepEqual(hooks["SessionEnd"], otherEvent) {
		t.Errorf("hooks under an unrelated event changed:\n got: %#v\nwant: %#v", hooks["SessionEnd"], otherEvent)
	}
}

// TestRemoveSettingsHook_NoOpDoesNotRewrite verifies the changed-guard: when
// there is nothing to remove (already-clean file, or a second idempotent run)
// settings.json must not be rewritten at all.
func TestRemoveSettingsHook_NoOpDoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	clean := map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "/somewhere/other-hook.sh"},
				}},
			},
		},
	}
	past := seedSettings(t, settingsPath, clean)
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	if _, err := removeSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("removeSettingsHook on clean file: %v", err)
	}

	st, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.ModTime().Truncate(time.Second).Equal(past) {
		t.Error("removeSettingsHook rewrote settings.json when there was nothing to remove")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("removeSettingsHook altered the bytes of an already-clean settings.json")
	}
}

// TestRemoveSettingsHook_IdempotentSecondCall verifies that once the entry has
// been removed, running init again does not touch settings.json.
func TestRemoveSettingsHook_IdempotentSecondCall(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hookCmd := "/home/u/.claude/hooks/pf-session-start.sh"

	seedSettings(t, settingsPath, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": hookCmd, "timeout": float64(5000)},
			}}},
		},
	})

	if _, err := removeSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("removeSettingsHook: %v", err)
	}

	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(settingsPath, past, past); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, err := removeSettingsHook(settingsPath, hookCmd); err != nil {
		t.Fatalf("removeSettingsHook second call: %v", err)
	}
	st, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.ModTime().Truncate(time.Second).Equal(past) {
		t.Error("removeSettingsHook rewrote settings.json on an idempotent second call")
	}
}

// TestRemoveSettingsHook_MissingFile: a machine that never had a
// ~/.claude/settings.json must not have one created by the cleanup.
func TestRemoveSettingsHook_MissingFile(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	if _, err := removeSettingsHook(settingsPath, "/home/u/.claude/hooks/pf-session-start.sh"); err != nil {
		t.Fatalf("removeSettingsHook with no settings.json: %v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("settings.json was created by the cleanup (stat err = %v)", err)
	}
}

// legacyHookBody is a stand-in for the dead hook polyforge used to self-install:
// what matters is that it carries the stale-marketplace marker.
const legacyHookBody = "#!/usr/bin/env bash\nplugin_base=\"$HOME/.claude/plugins/cache/gmi-marketplace/polyforge\"\n"

// seedLegacyHome lays out a fake $HOME containing .claude/hooks and a
// settings.json registering the given hook body (when non-empty).
func seedLegacyHome(t *testing.T, body string) (home, hookPath, settingsPath string) {
	t.Helper()
	home = t.TempDir()
	hooksDir := filepath.Join(home, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir fake home: %v", err)
	}
	hookPath = filepath.Join(hooksDir, "pf-session-start.sh")
	if body != "" {
		if err := os.WriteFile(hookPath, []byte(body), 0755); err != nil {
			t.Fatalf("seed hook: %v", err)
		}
	}
	settingsPath = filepath.Join(home, ".claude", "settings.json")
	seedSettings(t, settingsPath, map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": "/somewhere/other-hook.sh"},
				map[string]any{"type": "command", "command": hookPath, "timeout": float64(5000)},
			}}},
		},
	})
	return home, hookPath, settingsPath
}

// TestRemoveLegacySessionStartHook_RenamesAndUnregisters is the happy path:
// a marker-bearing hook is moved aside (never deleted) and unregistered.
func TestRemoveLegacySessionStartHook_RenamesAndUnregisters(t *testing.T) {
	home, hookPath, settingsPath := seedLegacyHome(t, legacyHookBody)

	removed, err := removeLegacySessionStartHookIn(home)
	if err != nil {
		t.Fatalf("removeLegacySessionStartHookIn: %v", err)
	}
	if !removed {
		t.Fatal("removed = false, want true for a marker-bearing legacy hook")
	}

	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("legacy hook still at %s (stat err = %v)", hookPath, err)
	}
	bak := hookPath + ".removed-by-polyforge.bak"
	b, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("expected backup at %s: %v", bak, err)
	}
	if string(b) != legacyHookBody {
		t.Error("backup content does not match the original hook body")
	}

	settings := readSettings(t, settingsPath)
	if findHookEntry(t, settings, hookPath) != nil {
		t.Error("legacy hook is still registered in settings.json")
	}
	if findHookEntry(t, settings, "/somewhere/other-hook.sh") == nil {
		t.Error("sibling hook entry was removed")
	}
	if settings["theme"] != "dark" {
		t.Errorf("unrelated top-level setting changed: %v", settings["theme"])
	}
}

// TestRemoveLegacySessionStartHook_LeavesHandRolledHookAlone: if the file at
// that path is not the dead polyforge script (no marker), polyforge must not
// touch it — not the file, not its settings.json registration.
func TestRemoveLegacySessionStartHook_LeavesHandRolledHookAlone(t *testing.T) {
	const handRolled = "#!/usr/bin/env bash\n# my own session hook\nexit 0\n"
	home, hookPath, settingsPath := seedLegacyHome(t, handRolled)
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read seed settings: %v", err)
	}

	removed, err := removeLegacySessionStartHookIn(home)
	if err != nil {
		t.Fatalf("removeLegacySessionStartHookIn: %v", err)
	}
	if removed {
		t.Error("removed = true, want false for a hand-rolled hook")
	}

	b, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hand-rolled hook was moved or deleted: %v", err)
	}
	if string(b) != handRolled {
		t.Error("hand-rolled hook content was modified")
	}
	if _, err := os.Stat(hookPath + ".removed-by-polyforge.bak"); !os.IsNotExist(err) {
		t.Error("a backup was created for a hand-rolled hook")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("settings.json was rewritten for a hand-rolled hook")
	}
}

// TestRemoveLegacySessionStartHook_DanglingRegistration: the script is already
// gone (someone stopped the bleeding by hand with `rm`) but settings.json still
// points at it. That dangling entry is "residual registration" and must be
// cleared too, otherwise the machine keeps a permanently failing SessionStart
// entry that no amount of re-running init would ever heal.
func TestRemoveLegacySessionStartHook_DanglingRegistration(t *testing.T) {
	home, hookPath, settingsPath := seedLegacyHome(t, "")

	removed, err := removeLegacySessionStartHookIn(home)
	if err != nil {
		t.Fatalf("removeLegacySessionStartHookIn: %v", err)
	}
	if !removed {
		t.Error("removed = false, want true for a dangling registration")
	}

	settings := readSettings(t, settingsPath)
	if entry := findHookEntry(t, settings, hookPath); entry != nil {
		t.Errorf("dangling registration for %s survived cleanup", hookPath)
	}
	// The sibling entry and unrelated keys are still none of our business.
	if entry := findHookEntry(t, settings, "/somewhere/other-hook.sh"); entry == nil {
		t.Error("unrelated sibling hook entry was removed")
	}
	if got := settings["theme"]; got != "dark" {
		t.Errorf("unrelated top-level key clobbered: theme = %v, want dark", got)
	}
}

// TestRemoveLegacySessionStartHook_AlreadyClean: no script and no registration
// — nothing to do, and in particular no settings.json rewrite.
func TestRemoveLegacySessionStartHook_AlreadyClean(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "hooks"), 0755); err != nil {
		t.Fatalf("mkdir fake home: %v", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	seedSettings(t, settingsPath, map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": "/somewhere/other-hook.sh"},
			}}},
		},
	})
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read seed settings: %v", err)
	}

	removed, err := removeLegacySessionStartHookIn(home)
	if err != nil {
		t.Fatalf("removeLegacySessionStartHookIn: %v", err)
	}
	if removed {
		t.Error("removed = true, want false on an already-clean home")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("settings.json was rewritten on an already-clean home")
	}
}

// TestRemoveLegacySessionStartHook_OverwritesStaleBackup: a pre-existing
// backup of the same name holds the same dead content, so it is safe (and
// necessary, for idempotency) to overwrite it.
func TestRemoveLegacySessionStartHook_OverwritesStaleBackup(t *testing.T) {
	home, hookPath, _ := seedLegacyHome(t, legacyHookBody)
	bak := hookPath + ".removed-by-polyforge.bak"
	if err := os.WriteFile(bak, []byte("stale backup\n"), 0755); err != nil {
		t.Fatalf("seed stale backup: %v", err)
	}

	removed, err := removeLegacySessionStartHookIn(home)
	if err != nil {
		t.Fatalf("removeLegacySessionStartHookIn: %v", err)
	}
	if !removed {
		t.Fatal("removed = false, want true")
	}
	b, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(b) != legacyHookBody {
		t.Errorf("stale backup was not overwritten: %q", string(b))
	}
}

// TestRemoveLegacySessionStartHook_IsIdempotent: the second `polyforge init`
// on an already-cleaned machine must be a complete no-op.
func TestRemoveLegacySessionStartHook_IsIdempotent(t *testing.T) {
	home, _, settingsPath := seedLegacyHome(t, legacyHookBody)

	if _, err := removeLegacySessionStartHookIn(home); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(settingsPath, past, past); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	removed, err := removeLegacySessionStartHookIn(home)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if removed {
		t.Error("removed = true on the second pass, want false")
	}
	st, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.ModTime().Truncate(time.Second).Equal(past) {
		t.Error("second pass rewrote settings.json")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("second pass changed settings.json content")
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
func TestWritePolyforgeYAML_IncludesScenario(t *testing.T) {
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
	// Caller owns both projects (u_xxx), so both pass the callerHasRole filter.
	if err := writePolyforgeYAML(path, projects, "u_xxx", nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
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

// TestWriteMemberPolyforgeYAML_FiltersRoleless guards the aihub#123 fix: the
// generated member .polyforge.yaml must declare only projects the caller has a
// role in (owner or member), mirroring the clone loop's callerHasRole gate.
// Visible-but-role-less projects (e.g. infra, tether) were previously written
// into the yaml while their repos were never cloned, producing spurious
// "missing repos" warnings in doctor/teammate checks.
func TestWritePolyforgeYAML_FiltersRoleless(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	projects := []serverProject{
		{
			Name:    "aihub",
			Visible: true,
			Members: []serverProjectMember{{UserID: "u_caller", Role: "writer"}},
			Repos:   json.RawMessage(`[{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"}]`),
		},
		{
			Name:        "ieops",
			OwnerUserID: "u_caller",
			Visible:     true,
			Repos:       json.RawMessage(`[{"name":"ieops-v2","url":"git@github.com:GMISWE/ieops-v2.git"}]`),
		},
		{
			Name:        "infra",
			OwnerUserID: "u_other",
			Visible:     true, // visible but caller has no role → must be excluded
			Repos:       json.RawMessage(`[{"name":"vllm","url":"git@github.com:vllm/vllm.git"}]`),
		},
		{
			Name:        "tether",
			OwnerUserID: "u_other",
			Visible:     true,
			Members:     []serverProjectMember{{UserID: "u_someone_else", Role: "writer"}},
			Repos:       json.RawMessage(`[{"name":"tether","url":"git@github.com:GMISWE/tether.git"}]`),
		},
	}

	path := filepath.Join(tmp, ".polyforge.yaml")
	if err := writePolyforgeYAML(path, projects, "u_caller", nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .polyforge.yaml: %v", err)
	}
	got := string(b)

	// Projects the caller has a role in must be present.
	for _, want := range []string{"aihub:", "ieops:"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered yaml missing role-bearing project %q; got:\n%s", want, got)
		}
	}
	// Role-less visible projects must be excluded.
	for _, notWant := range []string{"infra:", "tether:"} {
		if strings.Contains(got, notWant) {
			t.Errorf("rendered yaml includes role-less project %q; got:\n%s", notWant, got)
		}
	}
}

// ─── writePolyforgeYAML refresh (aihub#228) ──────────────────────────────────

// writeYAMLFixture writes a pre-existing .polyforge.yaml and returns its path.
func writeYAMLFixture(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, ".polyforge.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// loadYAML reads the generated file back through the real config loader, so the
// assertions exercise the same parse path pf-execute / claim use.
func loadYAML(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func repoNames(p config.Project) []string {
	names := make([]string, 0, len(p.Repos))
	for _, r := range p.Repos {
		names = append(names, r.Name)
	}
	return names
}

// TestWritePolyforgeYAML_RefreshesStaleRepos is the core aihub#228 regression:
// the generated header promises "Re-run polyforge init to refresh", but the
// write was gated on os.IsNotExist, so an existing file was never rewritten and
// repos added to the project server-side never reached .polyforge.yaml.
func TestWritePolyforgeYAML_RefreshesStaleRepos(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Stale local snapshot: only one repo.
	path := writeYAMLFixture(t, tmp, `version: 1
aihub:
    url: http://localhost:8081
projects:
    aihub:
        repos:
            - name: aihub
              url: git@github.com:GMISWE/ieops-aihub.git
`)

	// Server now has two repos for the same project.
	projects := []serverProject{{
		Name:        "aihub",
		OwnerUserID: "u_caller",
		Visible:     true,
		Repos: json.RawMessage(`[
			{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"},
			{"name":"ieops-core","url":"git@github.com:GMISWE/ieops-core.git"}
		]`),
	}}

	if err := writePolyforgeYAML(path, projects, "u_caller", nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}

	got := repoNames(loadYAML(t, tmp).Projects["aihub"])
	want := []string{"aihub", "ieops-core"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repos = %v, want %v (server-added repo must reach the yaml on re-init)", got, want)
	}
}

// TestWritePolyforgeYAML_DropsServerRemovedRepos proves the write is a true
// refresh from the server, not a union with the stale local list.
func TestWritePolyforgeYAML_DropsServerRemovedRepos(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path := writeYAMLFixture(t, tmp, `version: 1
projects:
    aihub:
        repos:
            - name: aihub
              url: git@github.com:GMISWE/ieops-aihub.git
            - name: retired
              url: git@github.com:GMISWE/retired.git
`)

	projects := []serverProject{{
		Name:        "aihub",
		OwnerUserID: "u_caller",
		Visible:     true,
		Repos:       json.RawMessage(`[{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"}]`),
	}}

	if err := writePolyforgeYAML(path, projects, "u_caller", nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}

	got := repoNames(loadYAML(t, tmp).Projects["aihub"])
	want := []string{"aihub"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repos = %v, want %v (repo removed server-side must not linger)", got, want)
	}
}

// TestWritePolyforgeYAML_PreservesAihubBlock guards the landmine that makes an
// unconditional rewrite dangerous: ResolveAihubURL() returns "" when there is no
// POLYFORGE_AIHUB_URL and no ~/.polyforge/config.toml server URL, so rebuilding
// the aihub block from scratch would blank a working workspace's endpoint.
func TestWritePolyforgeYAML_PreservesAihubBlock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)               // no config.toml → ResolveAihubURL() == ""
	t.Setenv("POLYFORGE_AIHUB_URL", "") // and no env override

	path := writeYAMLFixture(t, tmp, `version: 1
aihub:
    url: http://localhost:8081
    api_key_env: PF_API_KEY
projects:
    aihub:
        repos:
            - name: aihub
              url: git@github.com:GMISWE/ieops-aihub.git
`)

	projects := []serverProject{{
		Name:        "aihub",
		OwnerUserID: "u_caller",
		Visible:     true,
		Repos:       json.RawMessage(`[{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"}]`),
	}}

	if err := writePolyforgeYAML(path, projects, "u_caller", nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}

	cfg := loadYAML(t, tmp)
	if cfg.AIHub.URL != "http://localhost:8081" {
		t.Errorf("aihub.url = %q, want the existing %q preserved (refresh must not blank the endpoint)",
			cfg.AIHub.URL, "http://localhost:8081")
	}
	if cfg.AIHub.APIKeyEnv != "PF_API_KEY" {
		t.Errorf("aihub.api_key_env = %q, want %q preserved", cfg.AIHub.APIKeyEnv, "PF_API_KEY")
	}
}

// TestWritePolyforgeYAML_PreservesUnmanagedProjects: a project block the server
// did not return (caller lost visibility, or a hand-authored entry) must survive
// the refresh rather than being silently deleted.
func TestWritePolyforgeYAML_PreservesUnmanagedProjects(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path := writeYAMLFixture(t, tmp, `version: 1
projects:
    aihub:
        repos:
            - name: aihub
              url: git@github.com:GMISWE/ieops-aihub.git
    handrolled:
        repos:
            - name: local-only
              url: git@github.com:example/local-only.git
`)

	projects := []serverProject{{
		Name:        "aihub",
		OwnerUserID: "u_caller",
		Visible:     true,
		Repos:       json.RawMessage(`[{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"}]`),
	}}

	if err := writePolyforgeYAML(path, projects, "u_caller", nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}

	cfg := loadYAML(t, tmp)
	if _, ok := cfg.Projects["handrolled"]; !ok {
		t.Errorf("project %q was dropped; unmanaged blocks must be preserved. got projects: %v",
			"handrolled", cfg.Projects)
	}
}

// TestWritePolyforgeYAML_PrefersReconciledRepos: on the owner path runOwnerInit
// merges local-only repos into the server list and PATCHes them up, but the
// caller's `projects` slice still holds the pre-PATCH snapshot. The refresh must
// use the reconciled list so a just-appended local repo is not dropped.
func TestWritePolyforgeYAML_PrefersReconciledRepos(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	path := filepath.Join(tmp, ".polyforge.yaml")

	projects := []serverProject{{
		Name:        "aihub",
		OwnerUserID: "u_caller",
		Visible:     true,
		// Pre-PATCH server state: one repo.
		Repos: json.RawMessage(`[{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"}]`),
	}}
	reconciled := map[string][]serverRepoEntry{
		"aihub": {
			{Name: "aihub", URL: "git@github.com:GMISWE/ieops-aihub.git"},
			{Name: "marketplace", URL: "git@github.com:GMISWE/GMI-marketplace.git"},
		},
	}

	if err := writePolyforgeYAML(path, projects, "u_caller", reconciled); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}

	got := repoNames(loadYAML(t, tmp).Projects["aihub"])
	want := []string{"aihub", "marketplace"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repos = %v, want %v (reconciled list must win over the pre-PATCH snapshot)", got, want)
	}
}

// TestWritePolyforgeYAML_ProjectDescription: the server description is written,
// and an existing local one is kept when the server has none (the old writer
// dropped project descriptions entirely).
func TestWritePolyforgeYAML_ProjectDescription(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path := writeYAMLFixture(t, tmp, `version: 1
projects:
    aihub:
        repos: []
        description: local description
    keepsmine:
        repos: []
        description: only local knows this
`)

	serverDesc := "server description"
	projects := []serverProject{
		{
			Name:        "aihub",
			OwnerUserID: "u_caller",
			Visible:     true,
			Description: &serverDesc,
			Repos:       json.RawMessage(`[]`),
		},
		{
			Name:        "keepsmine",
			OwnerUserID: "u_caller",
			Visible:     true,
			Description: nil, // server has none → keep the local one
			Repos:       json.RawMessage(`[]`),
		},
	}

	if err := writePolyforgeYAML(path, projects, "u_caller", nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}

	cfg := loadYAML(t, tmp)
	if got := cfg.Projects["aihub"].Description; got != serverDesc {
		t.Errorf("aihub description = %q, want %q from server", got, serverDesc)
	}
	if got := cfg.Projects["keepsmine"].Description; got != "only local knows this" {
		t.Errorf("keepsmine description = %q, want the local value preserved", got)
	}
}

// TestWritePolyforgeYAML_CorruptExistingFile: an existing file that does not
// parse must not abort the refresh — the server list still lands, so `pf init`
// remains the documented way to repair a mangled workspace config. (The user is
// warned on stderr that unpreservable local state is being dropped.)
func TestWritePolyforgeYAML_CorruptExistingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path := writeYAMLFixture(t, tmp, "projects: [this is not: a map\n\x00garbage")

	projects := []serverProject{{
		Name:        "aihub",
		OwnerUserID: "u_caller",
		Visible:     true,
		Repos:       json.RawMessage(`[{"name":"aihub","url":"git@github.com:GMISWE/ieops-aihub.git"}]`),
	}}

	if err := writePolyforgeYAML(path, projects, "u_caller", nil); err != nil {
		t.Fatalf("writePolyforgeYAML on corrupt file: %v (must repair, not fail)", err)
	}

	got := repoNames(loadYAML(t, tmp).Projects["aihub"])
	want := []string{"aihub"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repos = %v, want %v after repairing a corrupt file", got, want)
	}
}

// ─── renderRepoBlock ─────────────────────────────────────────────────────────

// TestRenderRepoBlock pins the aihub#291 contract: the CLAUDE.md managed block
// carries exactly ONE line per repo — the positioning headline — and nothing
// else. stack / modules / changes / generated moved to
// .polyforge/repo-map/<project>.md (see TestRenderRepoMap), because the block
// sits at context position 0 and is re-read on every request while that detail
// is only needed at routing time.
func TestRenderRepoBlock(t *testing.T) {
	desc := "Go HTTP server + PostgreSQL"
	// Any repo entry, fully populated, must still render as a single line.
	detailBullets := []string{"  - stack:", "  - modules:", "  - changes:", "  - generated:"}
	cases := []struct {
		name       string
		repo       repoEntry
		wantSubstr []string
		notWant    []string
	}{
		{
			name: "structured block renders the headline only",
			repo: repoEntry{
				Name:            "aihub",
				Positioning:     "polyforge core API",
				TechStack:       []string{"Go", "PostgreSQL"},
				MainModules:     []repoModuleEntry{{Path: "internal/api", Role: "HTTP handlers"}, {Path: "internal/store", Role: "PG store"}},
				ChangeScenarios: []string{"add MCP tool", "schema migration"},
				GeneratedAt:     "2026-05-27T05:10:00Z",
				GeneratedCommit: "cef95e2ca68312651e1e147177f80c0c854a87cb",
			},
			wantSubstr: []string{"- **aihub**: polyforge core API\n"},
			notWant: append(append([]string{}, detailBullets...),
				"Go, PostgreSQL", "internal/api", "add MCP tool", "cef95e2"),
		},
		{
			name:       "legacy description-only renders headline, no bullets",
			repo:       repoEntry{Name: "marketplace", Description: &desc},
			wantSubstr: []string{"- **marketplace**: Go HTTP server + PostgreSQL"},
			notWant:    detailBullets,
		},
		{
			name:       "empty repo renders pending placeholder",
			repo:       repoEntry{Name: "proxy-server"},
			wantSubstr: []string{"- **proxy-server**: *(description pending"},
			notWant:    detailBullets,
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
			notWant:    append([]string{"line one\nline two"}, detailBullets...),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			renderRepoBlock(&sb, tc.repo)
			got := sb.String()
			// The whole point of aihub#291: one repo = one line in CLAUDE.md.
			if n := strings.Count(got, "\n"); n != 1 {
				t.Errorf("renderRepoBlock emitted %d lines, want exactly 1:\n%s", n, got)
			}
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

// ─── repo maps (aihub#291) ───────────────────────────────────────────────────

// fullRepo is a repo entry with every structured field populated — the fixture
// the zero-information-loss assertions below are built from.
func fullRepo() repoEntry {
	return repoEntry{
		Name:            "aihub",
		Positioning:     "polyforge core API",
		TechStack:       []string{"Go", "PostgreSQL"},
		MainModules:     []repoModuleEntry{{Path: "internal/api", Role: "HTTP handlers"}, {Path: "internal/store", Role: "PG store"}},
		ChangeScenarios: []string{"add MCP tool", "schema migration"},
		GeneratedAt:     "2026-05-27T05:10:00Z",
		GeneratedCommit: "cef95e2ca68312651e1e147177f80c0c854a87cb",
	}
}

// TestRenderRepoMap is the other half of the aihub#291 contract: every field
// renderRepoBlock stopped emitting must land here instead. Zero information
// loss is the acceptance criterion, so this asserts on the *values*, not just
// that the section exists.
func TestRenderRepoMap(t *testing.T) {
	desc := "polyforge backend platform"
	legacy := "Go HTTP server + PostgreSQL"
	blk := projectBlock{
		Name:        "aihub",
		Description: &desc,
		Repos: []repoEntry{
			fullRepo(),
			{Name: "marketplace", Description: &legacy},
			{Name: "proxy-server"},
		},
	}
	got := renderRepoMap(blk)

	for _, want := range []string{
		"# Repo map — aihub\n",
		"polyforge backend platform",
		"## aihub\n",
		"polyforge core API",
		"- stack: Go, PostgreSQL\n",
		"- modules:\n",
		"  - internal/api — HTTP handlers\n",
		"  - internal/store — PG store\n",
		"- changes:\n",
		"  - add MCP tool\n",
		"  - schema migration\n",
		"- generated: 2026-05-27 @ cef95e2\n",
		// Repos without a structured description still appear, so the map is a
		// complete list of the project's repos rather than a filtered one.
		"## marketplace\n",
		"Go HTTP server + PostgreSQL",
		"## proxy-server\n",
		"*(description pending",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderRepoMap missing %q in:\n%s", want, got)
		}
	}

	// A repo with no structured block must not sprout empty bullets.
	after := got[strings.Index(got, "## marketplace"):]
	for _, no := range []string{"- stack:", "- modules:", "- changes:", "- generated:"} {
		if strings.Contains(after, no) {
			t.Errorf("unstructured repo emitted %q in:\n%s", no, after)
		}
	}
}

// TestRenderRepoMapEmbeddedNewline guards the same one-line invariant
// renderRepoBlock has: a field carrying a newline must not break the bullet
// layout of the generated map.
func TestRenderRepoMapEmbeddedNewline(t *testing.T) {
	blk := projectBlock{Name: "p", Repos: []repoEntry{{
		Name:            "x",
		Positioning:     "line one\nline two",
		TechStack:       []string{"Go"},
		MainModules:     []repoModuleEntry{{Path: "p\nq", Role: "r"}},
		ChangeScenarios: []string{"c\nd"},
	}}}
	got := renderRepoMap(blk)
	for _, want := range []string{"line one line two", "  - p q — r\n", "  - c d\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderRepoMap missing %q in:\n%s", want, got)
		}
	}
}

func TestRepoMapFileName(t *testing.T) {
	cases := map[string]string{
		"aihub":            "aihub.md",
		"global-routing":   "global-routing.md",
		"polyforge_scen.1": "polyforge_scen.1.md",
		"a/b":              "a-b.md",
		"../etc/passwd":    "etc-passwd.md",
		"..":               "",
		".":                "",
		"":                 "",
		"  ":               "",
		"with space":       "with-space.md",
		".hidden":          "hidden.md",
	}
	for in, want := range cases {
		if got := repoMapFileName(in); got != want {
			t.Errorf("repoMapFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteRepoMaps(t *testing.T) {
	phase := t.TempDir()
	dir := filepath.Join(phase, "repo-map")

	blocks := []projectBlock{
		{Name: "aihub", Repos: []repoEntry{fullRepo()}},
		{Name: "ieops", Repos: []repoEntry{{Name: "ieops-v2", Positioning: "scheduler"}}},
	}
	if err := writeRepoMaps(phase, blocks); err != nil {
		t.Fatalf("writeRepoMaps: %v", err)
	}
	for _, name := range []string{"aihub.md", "ieops.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}

	// A project that disappears server-side must not leave a stale map behind:
	// routing would otherwise read a map for a repo set that no longer exists.
	if err := writeRepoMaps(phase, blocks[:1]); err != nil {
		t.Fatalf("writeRepoMaps (prune): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ieops.md")); !os.IsNotExist(err) {
		t.Errorf("stale ieops.md was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aihub.md")); err != nil {
		t.Errorf("aihub.md must survive the prune: %v", err)
	}

	// Non-.md files are not ours to delete.
	keep := filepath.Join(dir, "NOTES.txt")
	if err := os.WriteFile(keep, []byte("hand-written"), 0644); err != nil {
		t.Fatal(err)
	}
	// An empty render (e.g. the project fetch failed) must NOT prune: a
	// transient server error would otherwise wipe every map on disk.
	if err := writeRepoMaps(phase, nil); err != nil {
		t.Fatalf("writeRepoMaps (empty): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aihub.md")); err != nil {
		t.Errorf("empty render must not prune existing maps: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("non-.md file must not be pruned: %v", err)
	}
}

// TestWriteRepoMapsIsBestEffort covers the failure ordering: by the time
// writeRepoMaps runs, CLAUDE.md has already been slimmed. One unwritable project
// must therefore not cost every other project its map — that combination (slim
// block, no maps at all) leaves the detail unavailable with nothing explaining
// why. The bad project must also keep whatever map it already had.
func TestWriteRepoMapsIsBestEffort(t *testing.T) {
	phase := t.TempDir()
	dir := filepath.Join(phase, repoMapDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// A pre-existing map for the project whose rewrite is about to fail.
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("# stale but better than nothing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Make "broken.md" unwritable by turning it into a directory.
	if err := os.Remove(filepath.Join(dir, "broken.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "broken.md"), 0755); err != nil {
		t.Fatal(err)
	}

	blocks := []projectBlock{
		{Name: "broken", Repos: []repoEntry{{Name: "r", Positioning: "p"}}},
		{Name: "aihub", Repos: []repoEntry{fullRepo()}},
	}
	err := writeRepoMaps(phase, blocks)
	if err == nil {
		t.Error("expected an error describing the failed project")
	} else if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error should name the failing project, got: %v", err)
	}
	// The healthy project listed AFTER the failing one must still be written.
	if _, serr := os.Stat(filepath.Join(dir, "aihub.md")); serr != nil {
		t.Errorf("a later healthy project lost its map because an earlier one failed: %v", serr)
	}
	// And the failing project's existing entry must not have been pruned.
	if _, serr := os.Stat(filepath.Join(dir, "broken.md")); serr != nil {
		t.Errorf("the failing project's existing map was pruned, widening the outage: %v", serr)
	}
}

// TestWriteRepoMapsNoPruneWhenEveryWriteFails: if nothing could be written we
// cannot distinguish "project removed" from "render broken", so pruning on that
// evidence would destroy a working map set.
func TestWriteRepoMapsNoPruneWhenEveryWriteFails(t *testing.T) {
	phase := t.TempDir()
	dir := filepath.Join(phase, repoMapDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "survivor.md"), []byte("# keep me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "broken.md"), 0755); err != nil {
		t.Fatal(err)
	}

	_ = writeRepoMaps(phase, []projectBlock{{Name: "broken", Repos: []repoEntry{{Name: "r"}}}})
	if _, err := os.Stat(filepath.Join(dir, "survivor.md")); err != nil {
		t.Errorf("existing maps were pruned even though no write succeeded: %v", err)
	}
}

// TestWriteRepoMapsRoundTrip is the acceptance criterion in test form: every
// value renderRepoBlock used to print into CLAUDE.md is still recoverable from
// the generated map file.
func TestWriteRepoMapsRoundTrip(t *testing.T) {
	phase := t.TempDir()
	r := fullRepo()
	if err := writeRepoMaps(phase, []projectBlock{{Name: "aihub", Repos: []repoEntry{r}}}); err != nil {
		t.Fatalf("writeRepoMaps: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(phase, "repo-map", "aihub.md"))
	if err != nil {
		t.Fatalf("read map: %v", err)
	}
	got := string(b)
	want := append([]string{r.Positioning, generatedLine(r)}, r.TechStack...)
	want = append(want, r.ChangeScenarios...)
	for _, m := range r.MainModules {
		want = append(want, m.Path, m.Role)
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("repo map lost %q; it is no longer anywhere in:\n%s", w, got)
		}
	}
}

// TestRepoEntryFieldsAreAllRendered is the guard the hand-written round-trip
// above cannot be: it walks repoEntry by reflection, so adding a field without
// teaching renderRepoMap about it fails here instead of silently vanishing from
// the map. This repo has been bitten before by a renderer that rebuilt a fresh
// value instead of copying, dropping newly added fields with every test green.
//
// Each field is given a unique sentinel value and must surface in the rendered
// map. Deliberately no "skip unknown fields" escape hatch — a new field should
// force a decision about whether routing needs it.
func TestRepoEntryFieldsAreAllRendered(t *testing.T) {
	rt := reflect.TypeOf(repoEntry{})
	v := reflect.New(rt).Elem()
	sentinels := map[string]string{}

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		sentinel := "SENTINEL" + f.Name
		sentinels[f.Name] = sentinel
		switch f.Type.Kind() {
		case reflect.String:
			v.Field(i).SetString(sentinel)
		case reflect.Pointer: // *string (Description)
			s := sentinel
			v.Field(i).Set(reflect.ValueOf(&s))
		case reflect.Slice:
			switch f.Type.Elem().Kind() {
			case reflect.String: // []string (TechStack, ChangeScenarios)
				v.Field(i).Set(reflect.ValueOf([]string{sentinel}))
			case reflect.Struct: // []repoModuleEntry (MainModules)
				v.Field(i).Set(reflect.ValueOf([]repoModuleEntry{{Path: sentinel, Role: sentinel + "Role"}}))
			default:
				t.Fatalf("repoEntry.%s: unhandled slice element kind %s — teach this test about it", f.Name, f.Type.Elem().Kind())
			}
		default:
			t.Fatalf("repoEntry.%s: unhandled kind %s — teach this test about it", f.Name, f.Type.Kind())
		}
	}
	r := v.Interface().(repoEntry)

	// GeneratedAt/GeneratedCommit are reformatted by generatedLine rather than
	// echoed, so match on what that produces instead of the raw sentinel.
	rendered := renderRepoMap(projectBlock{Name: "p", Repos: []repoEntry{r}})
	for fieldName, sentinel := range sentinels {
		switch fieldName {
		case "GeneratedAt", "GeneratedCommit":
			if line := generatedLine(r); line == "" || !strings.Contains(rendered, line) {
				t.Errorf("repoEntry.%s is not reachable from the rendered map (generated line %q)", fieldName, line)
			}
		case "Name":
			if !strings.Contains(rendered, "## "+sentinel) {
				t.Errorf("repoEntry.Name is not rendered as a section heading")
			}
		case "Description":
			// Only a fallback: Positioning wins when both are set, which is
			// correct. Assert it is reachable when it IS the headline.
			only := repoEntry{Name: "x", Description: &sentinel}
			if !strings.Contains(renderRepoMap(projectBlock{Name: "p", Repos: []repoEntry{only}}), sentinel) {
				t.Errorf("repoEntry.Description is not reachable from the rendered map even as the sole headline")
			}
		default:
			if !strings.Contains(rendered, sentinel) {
				t.Errorf("repoEntry.%s (%q) is NOT in the rendered repo map — a field was added to repoEntry "+
					"without being rendered, so it is silently lost from CLAUDE.md and from the map:\n%s",
					fieldName, sentinel, rendered)
			}
		}
	}
}

// TestManagedBlockPointsAtTheMapItWrote is the aihub#291 pointer contract, and
// the reason the session-start skill text does NOT describe the repo-map layout:
// the pointer in the block and the file it names are produced by the same init
// pass, so they cannot drift. This test asserts that literally — it resolves the
// pointer emitted into the block against the filesystem writeRepoMaps just wrote.
//
// If this ever fails, the block is telling agents to read a file that is not
// there, which is worse than saying nothing.
func TestManagedBlockPointsAtTheMapItWrote(t *testing.T) {
	ws := t.TempDir()
	phase := filepath.Join(ws, ".polyforge")
	blocks := []projectBlock{
		{Name: "aihub", Repos: []repoEntry{fullRepo()}},
		{Name: "global-routing", Repos: []repoEntry{{Name: "gr", Positioning: "routing"}}},
	}
	if err := writeRepoMaps(phase, blocks); err != nil {
		t.Fatalf("writeRepoMaps: %v", err)
	}
	claudeMd := filepath.Join(ws, "CLAUDE.md")
	if err := upsertManagedBlock(claudeMd, blocks); err != nil {
		t.Fatalf("upsertManagedBlock: %v", err)
	}
	b, err := os.ReadFile(claudeMd)
	if err != nil {
		t.Fatal(err)
	}
	block, ok := managedBlockOf(string(b))
	if !ok {
		t.Fatal("no managed block")
	}

	re := regexp.MustCompile("(?m)^> Repo detail \\(stack / modules / changes\\): `([^`]+)`$")
	found := re.FindAllStringSubmatch(block, -1)
	if len(found) != len(blocks) {
		t.Fatalf("got %d pointer lines, want one per project (%d):\n%s", len(found), len(blocks), block)
	}
	for _, m := range found {
		rel := m[1]
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			t.Errorf("pointer %q must be a workspace-relative path", rel)
		}
		if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(rel))); err != nil {
			t.Errorf("block points at %q but that file does not exist: %v", rel, err)
		}
	}

	// The pointer must not be mistaken for legacy inline detail, and must not
	// forge a project heading.
	if blockIsLegacyFormat(block) {
		t.Errorf("the pointer line made a slim block look legacy:\n%s", block)
	}
	if got := managedBlockProjects(block); len(got) != len(blocks) {
		t.Errorf("managedBlockProjects = %v, want %d entries", got, len(blocks))
	}
}

// TestRunInitWiring guards the seam the unit tests cannot: writeRepoMaps must
// actually be called from RunInit. Without this, deleting that one call leaves
// every test green while CLAUDE.md gets slimmed and no map is ever written —
// the worst possible end state, since the detail is then nowhere.
//
// RunInit needs a live server, so this asserts on the source of the wiring
// rather than executing it: both the block write and the map write must be
// present in the same function.
func TestRunInitWiring(t *testing.T) {
	src, err := os.ReadFile("init.go")
	if err != nil {
		t.Fatalf("read init.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func RunInit(")
	if start < 0 {
		t.Fatal("RunInit not found in init.go")
	}
	// End of RunInit = the next top-level func declaration.
	end := strings.Index(body[start+1:], "\nfunc ")
	if end < 0 {
		end = len(body) - start - 1
	}
	fn := body[start : start+1+end]

	for _, want := range []string{"upsertManagedBlock(", "writeRepoMaps("} {
		if !strings.Contains(fn, want) {
			t.Errorf("RunInit does not call %s — CLAUDE.md and .polyforge/repo-map/ must be written "+
				"in the same pass, or the block is slimmed with no map to fall back on", want)
		}
	}
}

func TestManagedBlockOf(t *testing.T) {
	body := "intro\n" + managedBlockStart + "\n## Workspace\nx\n" + managedBlockEnd + "\ntail\n"
	got, ok := managedBlockOf(body)
	if !ok {
		t.Fatal("managedBlockOf did not find the block")
	}
	if !strings.HasPrefix(got, managedBlockStart) || !strings.HasSuffix(got, managedBlockEnd) {
		t.Errorf("block not delimited by the markers: %q", got)
	}
	if strings.Contains(got, "intro") || strings.Contains(got, "tail") {
		t.Errorf("block leaked surrounding text: %q", got)
	}
	if _, ok := managedBlockOf("no markers here"); ok {
		t.Error("managedBlockOf reported a block in text that has none")
	}
	if _, ok := managedBlockOf(managedBlockStart + "\nunterminated"); ok {
		t.Error("managedBlockOf accepted an unterminated block")
	}
}

// TestBlockIsLegacyFormatWithoutModules is a regression test: each of the four
// detail bullets is independently optional upstream, so a legacy block whose
// repos have tech_stack / change_scenarios / generated but NO main_modules never
// emits "  - modules:". Keying detection on that bullet alone called such a
// block slim and reported the wrong remedy.
func TestBlockIsLegacyFormatWithoutModules(t *testing.T) {
	for _, tc := range []struct{ name, bullets string }{
		{"stack only", "  - stack: Go\n"},
		{"changes only", "  - changes:\n    - add MCP tool\n"},
		{"generated only", "  - generated: 2026-05-27 @ cef95e2\n"},
		{"stack+changes, no modules", "  - stack: Go\n  - changes:\n    - c\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := managedBlockStart + "\n## Workspace\n\n### aihub\n\n- **aihub**: core\n" +
				tc.bullets + managedBlockEnd
			if !blockIsLegacyFormat(block) {
				t.Errorf("block with inline detail was classified slim:\n%s", block)
			}
		})
	}
}

// TestManagedBlockRejectsForgedStructureInDescription is a regression test for a
// false positive that was self-perpetuating: the project description is written
// into the block, so if it is not collapsed to one line a description containing
// "\n  - modules:" makes a FRESHLY rendered slim block classify as legacy. Doctor
// would then warn forever and the SessionStart hint would fire on every session
// — the unconditional position-0 line the design explicitly rules out — and
// re-running init would never clear it. A "### " line likewise forges a project.
func TestManagedBlockRejectsForgedStructureInDescription(t *testing.T) {
	for _, tc := range []struct{ name, desc string }{
		{"forged modules bullet", "real description\n  - modules:\n    - fake — fake"},
		{"forged project heading", "real description\n### phantom-project"},
		{"forged stack bullet", "real description\n  - stack: Go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "CLAUDE.md")
			desc := tc.desc
			blocks := []projectBlock{{
				Name:        "aihub",
				Description: &desc,
				Repos:       []repoEntry{{Name: "aihub", Positioning: "core"}},
			}}
			if err := upsertManagedBlock(path, blocks); err != nil {
				t.Fatalf("upsertManagedBlock: %v", err)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			block, ok := managedBlockOf(string(b))
			if !ok {
				t.Fatal("no managed block written")
			}
			if blockIsLegacyFormat(block) {
				t.Errorf("a freshly rendered slim block was classified legacy — the description forged block structure:\n%s", block)
			}
			if got := managedBlockProjects(block); len(got) != 1 || got[0] != "aihub" {
				t.Errorf("managedBlockProjects = %v, want exactly [aihub] — the description forged a project", got)
			}
		})
	}
}

func TestBlockIsLegacyFormat(t *testing.T) {
	legacy := managedBlockStart + "\n## Workspace\n\n### aihub\n\n- **aihub**: core\n" +
		"  - stack: Go\n  - modules:\n    - internal/api — handlers\n" + managedBlockEnd
	if !blockIsLegacyFormat(legacy) {
		t.Error("a block with inline modules must be detected as legacy")
	}
	var sb strings.Builder
	renderRepoBlock(&sb, fullRepo())
	slim := managedBlockStart + "\n## Workspace\n\n### aihub\n\n" + sb.String() + managedBlockEnd
	if blockIsLegacyFormat(slim) {
		t.Errorf("a freshly rendered block must not look legacy:\n%s", slim)
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
