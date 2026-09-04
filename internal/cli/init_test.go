package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
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
	if err := writePolyforgeYAML(path, projects, "u_xxx", nil, nil); err != nil {
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
	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
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

	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
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

	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
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

	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
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

	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
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

	if err := writePolyforgeYAML(path, projects, "u_caller", reconciled, nil); err != nil {
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

	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
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

	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
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

// ─── repo description three-way compare (aihub#310) ──────────────────────────
//
// The defect: `polyforge init` compared the local .polyforge.yaml description
// against the server's and gave every difference to the local file whenever it
// was non-empty, so a description written through MCP or the web UI was
// reverted by the next init — which then printed "descriptions synced: true"
// whichever way the data had flowed.
//
// The fix records a baseline and makes the comparison three-way. Four outcomes,
// and ALL FOUR are pinned below, because the cheapest way to make the pull
// direction pass is to delete the publish path outright — which would silently
// remove the feature aihub#34 shipped (owner edits the local file, init
// publishes it). See TestRunOwnerInit_PublishesLocalEdit for that half.

func TestReconcileDescription_FourCells(t *testing.T) {
	cases := []struct {
		name                    string
		baseline, local, server string
		want                    descSyncDirection
	}{
		// The table from the owner's ruling, one row each.
		{"local changed only → publish", "old", "mine", "old", descPush},
		{"server changed only → adopt", "old", "old", "theirs", descPull},
		{"both changed → refuse", "old", "mine", "theirs", descConflict},
		{"neither changed → no-op", "old", "old", "old", descInSync},

		// The exact shape of the reported incident: MCP wrote the server, the
		// local file was simply stale. Before the fix this direction was the
		// one that lost, every time.
		{"stale local file loses to a server edit", "中文", "中文", "english", descPull},

		// Migration: a workspace written before description_baseline existed has
		// no baseline at all. Where the two sides agree — 27 of 27 repos in the
		// workspace this was measured in — that must be silent, NOT a conflict
		// against the empty baseline.
		{"no baseline, sides agree", "", "same", "same", descInSync},
		{"no baseline, only local has a value", "", "mine", "", descPush},
		{"no baseline, only server has a value", "", "", "theirs", descPull},
		{"no baseline, sides disagree → unknown, refuse", "", "mine", "theirs", descConflict},

		// Empty is a VALUE, not "unset": clearing a description on one side is a
		// change like any other and must move, not be ignored.
		{"local cleared → publish the clearing", "old", "", "old", descPush},
		{"server cleared → adopt the clearing", "old", "old", "", descPull},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reconcileDescription(tc.baseline, tc.local, tc.server)
			if got != tc.want {
				t.Errorf("reconcileDescription(baseline=%q, local=%q, server=%q) = %q, want %q",
					tc.baseline, tc.local, tc.server, got, tc.want)
			}
		})
	}
}

// fakeProjectServer stands in for aihub on the two endpoints runOwnerInit
// touches, and records every PATCH body it receives.
//
// The recorded body is the only place "was it actually published?" can be
// answered. The printed line reports an INTENT; a test that read the line would
// stay green against a build that never sent the request — the same shape of
// unverified done-marker as the "descriptions synced: true" this replaces.
type fakeProjectServer struct {
	mu      sync.Mutex
	repos   json.RawMessage   // current server state; a PATCH replaces it
	patches []json.RawMessage // the repos payload of each PATCH, in order
	fail    bool              // when set, every PATCH answers 500

	// Caller identity and the project's role table. The defaults make the
	// caller the project OWNER, which is what the owner-path tests want; a
	// member-path test overrides them before running init.
	callerUserID string
	ownerUserID  string
	members      []map[string]any

	url string
}

func newFakeProjectServer(t *testing.T, repos string) *fakeProjectServer {
	t.Helper()
	f := &fakeProjectServer{
		repos:        json.RawMessage(repos),
		callerUserID: "u_owner",
		ownerUserID:  "u_owner",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPatch {
			var body struct {
				Repos json.RawMessage `json:"repos"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.patches = append(f.patches, body.Repos)
			if f.fail {
				http.Error(w, `{"error":{"code":"BOOM","message":"patch refused"}}`, http.StatusInternalServerError)
				return
			}
			f.repos = body.Repos
		}
		project := map[string]any{
			"name": "proj", "owner_user_id": f.ownerUserID, "visible": true,
			"repos": f.repos,
		}
		if len(f.members) > 0 {
			project["members"] = f.members
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"user_id": f.callerUserID})
		case "/v1/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{project}})
		default:
			_ = json.NewEncoder(w).Encode(project)
		}
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// patchedDescriptions flattens the n-th PATCH body into repo → description.
func (f *fakeProjectServer) patchedDescriptions(t *testing.T, n int) map[string]string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.patches) {
		t.Fatalf("wanted PATCH #%d but only %d were sent", n, len(f.patches))
	}
	var repos []serverRepoEntry
	if err := json.Unmarshal(f.patches[n], &repos); err != nil {
		t.Fatalf("decode PATCH #%d: %v", n, err)
	}
	out := map[string]string{}
	for _, r := range repos {
		d := ""
		if r.Description != nil {
			d = *r.Description
		}
		out[r.Name] = d
	}
	return out
}

func (f *fakeProjectServer) patchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.patches)
}

// captureOutput runs fn with os.Stdout and os.Stderr redirected, and returns
// everything it printed. Init's report is user-facing behaviour, so the
// direction it announces is asserted on the real bytes rather than on the pure
// renderer alone — the renderer being right does not prove it is wired in.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	func() {
		defer func() {
			os.Stdout, os.Stderr = origOut, origErr
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// ownerInitResult bundles what one runOwnerInit call decided.
type ownerInitResult struct {
	Output string
	Local  map[string]localRepoDesc
	Merged []serverRepoEntry
}

// runOwnerInitAgainst drives the real runOwnerInit against the fake server.
//
// Repo URLs are deliberately empty: the clone loop skips a repo with no URL, so
// the test exercises the description path without shelling out to git. Nothing
// in that path reads URL.
func runOwnerInitAgainst(t *testing.T, f *fakeProjectServer, localRepos []config.Repo) ownerInitResult {
	t.Helper()
	f.mu.Lock()
	serverRepos := append(json.RawMessage(nil), f.repos...)
	f.mu.Unlock()

	cfg := &config.Config{
		Version:  1,
		Projects: map[string]config.Project{"proj": {Repos: localRepos}},
	}
	sp := serverProject{Name: "proj", OwnerUserID: "u_owner", Visible: true, Repos: serverRepos}
	c := client.New(f.url, "test-key")

	var res ownerInitResult
	res.Output = captureOutput(t, func() {
		_, res.Merged, res.Local = runOwnerInit(t.Context(), c, cfg, t.TempDir(), sp)
	})
	return res
}

func mergedDescription(t *testing.T, merged []serverRepoEntry, name string) string {
	t.Helper()
	for _, r := range merged {
		if r.Name == name {
			if r.Description == nil {
				return ""
			}
			return *r.Description
		}
	}
	t.Fatalf("repo %q not in merged list", name)
	return ""
}

// TestRunOwnerInit_PublishesLocalEdit is the REVERSE criterion, and it is the
// one that makes the rest of this fix safe to land.
//
// The forward criterion (a server edit must survive init) can be satisfied by
// deleting the publish path wholesale. That would pass every other test here
// while silently removing what aihub#34 shipped: the project owner edits
// .polyforge.yaml and init pushes the change up. This test reads the PATCH off
// the wire, so deleting that path turns it red.
func TestRunOwnerInit_PublishesLocalEdit(t *testing.T) {
	f := newFakeProjectServer(t, `[{"name":"repo-a","description":"as last synced"}]`)
	res := runOwnerInitAgainst(t, f, []config.Repo{
		{Name: "repo-a", Description: "edited by hand", DescriptionBaseline: "as last synced"},
	})

	if got := f.patchCount(); got != 1 {
		t.Fatalf("PATCH count = %d, want exactly 1 — a local-only edit MUST be published "+
			"(aihub#34); if this is 0 the publish path is gone", got)
	}
	if got := f.patchedDescriptions(t, 0)["repo-a"]; got != "edited by hand" {
		t.Errorf("PATCHed description = %q, want %q — the local edit did not reach the server",
			got, "edited by hand")
	}
	if got := res.Local["repo-a"]; got.Description != "edited by hand" || got.Baseline != "edited by hand" {
		t.Errorf("local view = %+v, want both fields %q — after a successful publish the "+
			"baseline must advance, or the next init reads the same edit as a server-side change",
			got, "edited by hand")
	}
	if !strings.Contains(res.Output, "local → server") {
		t.Errorf("output did not name the direction:\n%s", res.Output)
	}
}

// TestRunOwnerInit_AdoptsServerEdit is the FORWARD criterion: the exact incident
// that opened this wi. Someone edits a description through MCP; the local file
// is merely stale. Before the fix the stale file won and the server value was
// reverted with a success line.
func TestRunOwnerInit_AdoptsServerEdit(t *testing.T) {
	f := newFakeProjectServer(t, `[{"name":"repo-a","description":"english, written via MCP"}]`)
	res := runOwnerInitAgainst(t, f, []config.Repo{
		{Name: "repo-a", Description: "中文，本地陈旧", DescriptionBaseline: "中文，本地陈旧"},
	})

	if got := f.patchCount(); got != 0 {
		descs := f.patchedDescriptions(t, 0)
		t.Fatalf("PATCH count = %d, want 0 — a stale local file must not be pushed. "+
			"It sent repo-a=%q", got, descs["repo-a"])
	}
	if got := res.Local["repo-a"]; got.Description != "english, written via MCP" ||
		got.Baseline != "english, written via MCP" {
		t.Errorf("local view = %+v, want the server value in both fields", got)
	}
	if got := mergedDescription(t, res.Merged, "repo-a"); got != "english, written via MCP" {
		t.Errorf("merged description = %q, want the server value left alone", got)
	}
	if !strings.Contains(res.Output, "server → local") {
		t.Errorf("output did not name the direction:\n%s", res.Output)
	}
}

// TestRunOwnerInit_RefusesConflict: both sides moved since the baseline. The
// owner's ruling is to stop and report, not to guess — so NEITHER side changes,
// and the baseline does not advance, or the next run would silently adopt
// whichever value this one happened to leave behind.
func TestRunOwnerInit_RefusesConflict(t *testing.T) {
	f := newFakeProjectServer(t, `[{"name":"repo-a","description":"server moved"}]`)
	res := runOwnerInitAgainst(t, f, []config.Repo{
		{Name: "repo-a", Description: "local moved", DescriptionBaseline: "what both last held"},
	})

	if got := f.patchCount(); got != 0 {
		t.Fatalf("PATCH count = %d, want 0 — a conflict must not resolve itself upward", got)
	}
	if got := res.Local["repo-a"]; got.Description != "local moved" {
		t.Errorf("local description = %q, want %q left untouched", got.Description, "local moved")
	}
	if got := res.Local["repo-a"].Baseline; got != "what both last held" {
		t.Errorf("baseline = %q, want the old baseline %q kept — advancing it would silently "+
			"resolve the conflict on the next run", got, "what both last held")
	}
	if got := mergedDescription(t, res.Merged, "repo-a"); got != "server moved" {
		t.Errorf("merged description = %q, want the server value left untouched", got)
	}
	for _, want := range []string{"CONFLICT", "local moved", "server moved", "what both last held"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("conflict report does not mention %q — the user cannot resolve values "+
				"they were not shown. Output:\n%s", want, res.Output)
		}
	}
}

// TestRunOwnerInit_NoOpIsSilentAndSaysSo: nothing to do must cost no request,
// and must still be READABLE as "nothing was published". The output this
// replaces could not be read that way.
func TestRunOwnerInit_NoOpMakesNoRequestAndReportsInSync(t *testing.T) {
	f := newFakeProjectServer(t, `[{"name":"repo-a","description":"same on both sides"}]`)
	res := runOwnerInitAgainst(t, f, []config.Repo{
		{Name: "repo-a", Description: "same on both sides", DescriptionBaseline: "same on both sides"},
	})

	if got := f.patchCount(); got != 0 {
		t.Errorf("PATCH count = %d, want 0 — nothing changed on either side", got)
	}
	if !strings.Contains(res.Output, "1 in sync") {
		t.Errorf("output does not state that the description was in sync:\n%s", res.Output)
	}
	for _, absent := range []string{"local → server", "server → local", "CONFLICT"} {
		if strings.Contains(res.Output, absent) {
			t.Errorf("output claims %q on a no-op run:\n%s", absent, res.Output)
		}
	}
}

// TestRunOwnerInit_BothDirectionsInOneRun is acceptance criterion 5: one repo
// changed only on the server, another only locally, resolved in the SAME init.
// Each must land on its own side — a rule that picks one winner globally cannot
// pass this.
func TestRunOwnerInit_BothDirectionsInOneRun(t *testing.T) {
	f := newFakeProjectServer(t, `[
		{"name":"pull-me","description":"server is newer"},
		{"name":"push-me","description":"as last synced"},
		{"name":"leave-me","description":"untouched"}
	]`)
	res := runOwnerInitAgainst(t, f, []config.Repo{
		{Name: "pull-me", Description: "stale", DescriptionBaseline: "stale"},
		{Name: "push-me", Description: "locally edited", DescriptionBaseline: "as last synced"},
		{Name: "leave-me", Description: "untouched", DescriptionBaseline: "untouched"},
	})

	if got := f.patchCount(); got != 1 {
		t.Fatalf("PATCH count = %d, want 1", got)
	}
	sent := f.patchedDescriptions(t, 0)
	if sent["push-me"] != "locally edited" {
		t.Errorf("PATCH push-me = %q, want the local edit published", sent["push-me"])
	}
	// The PATCH replaces the whole repos array, so the repo being pulled rides
	// along in the same body. It must carry the SERVER's value: sending the
	// stale local one would revert the very edit this run is adopting.
	if sent["pull-me"] != "server is newer" {
		t.Errorf("PATCH pull-me = %q, want %q — the publish body must not revert a repo it "+
			"is adopting from the server", sent["pull-me"], "server is newer")
	}
	if sent["leave-me"] != "untouched" {
		t.Errorf("PATCH leave-me = %q, want %q", sent["leave-me"], "untouched")
	}
	if got := res.Local["pull-me"].Description; got != "server is newer" {
		t.Errorf("local pull-me = %q, want the server value", got)
	}
	if got := res.Local["push-me"].Description; got != "locally edited" {
		t.Errorf("local push-me = %q, want the local value", got)
	}
	if !strings.Contains(res.Output, "local → server (push-me)") ||
		!strings.Contains(res.Output, "server → local (pull-me)") {
		t.Errorf("output does not name both directions with their repos:\n%s", res.Output)
	}
}

// TestRunOwnerInit_FailedPatchKeepsBaseline: if the PATCH does not reach the
// server, the local edit is still pending. Advancing the baseline anyway would
// make the NEXT init read that same edit as a server-side change and pull it
// back down — the fix would then eat exactly the data it was written to save.
func TestRunOwnerInit_FailedPatchKeepsOldBaseline(t *testing.T) {
	f := newFakeProjectServer(t, `[{"name":"repo-a","description":"as last synced"}]`)
	f.fail = true
	res := runOwnerInitAgainst(t, f, []config.Repo{
		{Name: "repo-a", Description: "edited by hand", DescriptionBaseline: "as last synced"},
	})

	if got := f.patchCount(); got != 1 {
		t.Fatalf("PATCH count = %d, want 1 attempt", got)
	}
	got := res.Local["repo-a"]
	if got.Description != "edited by hand" {
		t.Errorf("local description = %q, want the pending edit kept", got.Description)
	}
	if got.Baseline != "as last synced" {
		t.Errorf("baseline = %q, want the OLD baseline %q kept after a failed PATCH — "+
			"otherwise the next init pulls the unpublished edit back down",
			got.Baseline, "as last synced")
	}
}

// TestDescribeDescriptionSync_NamesEveryDirection pins the report itself. The
// defect included an output that printed the same words whichever way the data
// went, so "it printed something" is not the property under test — "the four
// outcomes produce four different, attributable strings" is.
func TestDescribeDescriptionSync_NamesEveryDirection(t *testing.T) {
	ds := []descDecision{
		{Repo: "a", Direction: descInSync},
		{Repo: "b", Direction: descPush},
		{Repo: "c", Direction: descPull},
		{Repo: "d", Direction: descConflict},
	}
	got := describeDescriptionSync("proj", ds)
	for _, want := range []string{"1 in sync", "1 local → server (b)", "1 server → local (c)", "1 conflict (d)"} {
		if !strings.Contains(got, want) {
			t.Errorf("line %q does not contain %q", got, want)
		}
	}

	// Every direction must render differently from every other, or the line is
	// the old `synced: %v` in new words.
	seen := map[string]descSyncDirection{}
	for _, dir := range []descSyncDirection{descInSync, descPush, descPull, descConflict} {
		line := describeDescriptionSync("proj", []descDecision{{Repo: "r", Direction: dir}})
		if prev, dup := seen[line]; dup {
			t.Errorf("directions %q and %q both render as %q", prev, dir, line)
		}
		seen[line] = dir
	}
	if got := describeDescriptionSync("proj", nil); got != "" {
		t.Errorf("a project with no comparable repos printed %q, want nothing", got)
	}
}

// TestConflictReport_DistinguishesMissingBaseline: a repo carried over from
// before this field existed has no baseline, and %q would render that as `""` —
// indistinguishable from a baseline that really was the empty string. The
// difference matters to whoever has to resolve it.
func TestConflictReport_DistinguishesMissingBaseline(t *testing.T) {
	none := conflictReport("proj", descDecision{Repo: "r", Local: "l", Server: "s", Baseline: ""})
	empty := conflictReport("proj", descDecision{Repo: "r", Local: "l", Server: "s", Baseline: "x"})
	if !strings.Contains(none, "none recorded") {
		t.Errorf("missing baseline not called out:\n%s", none)
	}
	if none == empty {
		t.Error("a missing baseline and a recorded one produce the same report")
	}
	for _, want := range []string{`"l"`, `"s"`, "description_baseline", "polyforge init"} {
		if !strings.Contains(none, want) {
			t.Errorf("report does not contain %q:\n%s", want, none)
		}
	}
}

// TestWritePolyforgeYAML_BaselineRoundTrips: the baseline is only useful if it
// survives to the next run. It is written by init and read back by config.Load,
// and if that round trip broke, the three-way compare would silently degrade to
// the two-way one this wi is about — with every test above still green, because
// they all pass the baseline in by hand.
func TestWritePolyforgeYAML_BaselineRoundTrips(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	path := filepath.Join(tmp, ".polyforge.yaml")

	projects := []serverProject{{
		Name: "proj", OwnerUserID: "u_caller", Visible: true,
		Repos: json.RawMessage(`[{"name":"kept","description":"server value"},{"name":"conflicted","description":"server value"}]`),
	}}
	localDescs := map[string]map[string]localRepoDesc{"proj": {
		"conflicted": {Description: "local value", Baseline: "older shared value"},
	}}

	if err := writePolyforgeYAML(path, projects, "u_caller", nil, localDescs); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}
	cfg := loadYAML(t, tmp)
	byName := map[string]config.Repo{}
	for _, r := range cfg.Projects["proj"].Repos {
		byName[r.Name] = r
	}

	// No override supplied: the value in hand is what both sides now hold, so
	// that is the baseline.
	if got := byName["kept"]; got.Description != "server value" || got.DescriptionBaseline != "server value" {
		t.Errorf("repo kept = %+v, want description and baseline both %q", got, "server value")
	}
	// Override supplied: the two differ, and BOTH must survive the round trip.
	if got := byName["conflicted"]; got.Description != "local value" || got.DescriptionBaseline != "older shared value" {
		t.Errorf("repo conflicted = %+v, want description %q with baseline %q — if the baseline "+
			"does not round-trip, the next init has nothing to compare against",
			got, "local value", "older shared value")
	}

	// And the decision this file drives must be reproduced from the file alone.
	if got := reconcileDescription(byName["conflicted"].DescriptionBaseline,
		byName["conflicted"].Description, "server value"); got != descConflict {
		t.Errorf("re-reading the written file yields %q, want %q — the conflict must still be "+
			"detected on the next run", got, descConflict)
	}
}

// TestGeneratedHeaderStatesAuthorityPerField: the old header said "Source of
// truth is the server" while repos[].description was the one field init took
// FROM this file and pushed up — the header asserted the opposite of the code
// for the only field a user is invited to edit.
//
// The gate is structural, not a string match: every yaml key this writer can
// emit under a project must be named in the header. A new field on config.Repo
// with no authority documented turns this red, which a fixed list of expected
// substrings would not.
func TestGeneratedHeaderStatesAuthorityPerField(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	path := filepath.Join(tmp, ".polyforge.yaml")
	projects := []serverProject{{
		Name: "proj", OwnerUserID: "u_caller", Visible: true,
		Repos: json.RawMessage(`[{"name":"r","description":"d"}]`),
	}}
	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	header, _, _ := strings.Cut(string(b), "\nversion:")

	for _, typ := range []reflect.Type{reflect.TypeOf(config.Project{}), reflect.TypeOf(config.Repo{})} {
		for i := 0; i < typ.NumField(); i++ {
			tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
			if tag == "" || tag == "-" {
				continue
			}
			// Whole-token, not substring. strings.Contains passed for any tag
			// that merely occurred INSIDE an already-documented one, so with
			// "github_owner_repo" present a new `yaml:"owner"` field read as
			// documented while nothing described it — and so did "name", "url"
			// and "desc". The gate for undocumented fields was blind to the
			// names most likely to be added next.
			if !headerDocumentsTag(header, tag) {
				t.Errorf("generated header does not say who owns %s.%s (yaml key %q) — "+
					"a field this file carries with undocumented authority is how aihub#310 "+
					"happened", typ.Name(), typ.Field(i).Name, tag)
			}
		}
	}

	if strings.Contains(header, "Source of truth is the server.") {
		t.Error("the header still makes a blanket server-authority claim, which is false for " +
			"repos[].description")
	}
	// The both-sided field must be described as both-sided in ITS OWN entry, not
	// merely somewhere in the header — and the publish half must be qualified by
	// role, because only the owner path publishes. See
	// TestRunInit_MemberHeaderDoesNotPromisePublishing for the measurement.
	entry := headerEntryFor(t, header, "repos", "description")
	toks := map[string]bool{}
	for _, tok := range headerTokens(strings.ToLower(entry)) {
		toks[tok] = true
	}
	for _, want := range []string{"owner", "member", "publishes", "adopts"} {
		if !toks[want] {
			t.Errorf("the repos[].description entry does not describe it as editable on both "+
				"sides subject to role (missing %q):\n%s", want, entry)
		}
	}
}

// TestWritePolyforgeYAML_MemberPathGetsMatchingBaseline: members never publish
// (runMemberInit does not PATCH), so their file is written straight from the
// server. Its baseline must equal what was written, or a member who later
// becomes owner would find every repo in conflict on their first init.
func TestWritePolyforgeYAML_MemberPathGetsMatchingBaseline(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	path := filepath.Join(tmp, ".polyforge.yaml")
	projects := []serverProject{{
		Name: "proj", OwnerUserID: "u_someone_else", Visible: true,
		Members: []serverProjectMember{{UserID: "u_member", Role: "writer"}},
		Repos:   json.RawMessage(`[{"name":"r","description":"server value"}]`),
	}}
	// nil localDescs is exactly what RunInit passes for a member project.
	if err := writePolyforgeYAML(path, projects, "u_member", nil, nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}
	r := loadYAML(t, tmp).Projects["proj"].Repos[0]
	if r.DescriptionBaseline != r.Description {
		t.Errorf("member baseline = %q, description = %q — they must match, or the member's "+
			"first init as owner reports a conflict on every repo", r.DescriptionBaseline, r.Description)
	}
	if got := reconcileDescription(r.DescriptionBaseline, r.Description, "server value"); got != descInSync {
		t.Errorf("a freshly written member file compares as %q, want %q", got, descInSync)
	}
}

// TestRunInit_EndToEndThreeWayDescriptions drives the WHOLE of RunInit — the
// command the acceptance criteria are written against — over a fake aihub, and
// then reads the .polyforge.yaml it left on disk.
//
// The per-function tests above stop one hop short: runOwnerInit returns the
// local view, but nothing there proves RunInit hands it to writePolyforgeYAML.
// That hop is where a correct decision becomes a wrong file, and it is exactly
// the shape of wiring bug this repo keeps shipping. Nothing here touches a real
// server: HOME and the workspace root are temp dirs, and repo URLs are empty so
// the clone loop never shells out to git.
func TestRunInit_EndToEndThreeWayDescriptions(t *testing.T) {
	home, wsRoot := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	f := newFakeProjectServer(t, `[
		{"name":"pull-me","description":"edited on the server via MCP"},
		{"name":"push-me","description":"as last synced"},
		{"name":"stuck","description":"server moved"}
	]`)

	// The conflicted repo is not decoration. For a push or a pull the value
	// destined for the local file happens to equal the one destined for the
	// server, so a RunInit that dropped the local view on the floor would still
	// write a correct file and this test would pass. A conflict is the only
	// outcome where the two differ — it is what gives the wiring hop a gate.
	writeYAMLFixture(t, wsRoot, `version: 1
projects:
    proj:
        repos:
            - name: pull-me
              description: stale local copy
              description_baseline: stale local copy
            - name: push-me
              description: edited by hand in this file
              description_baseline: as last synced
            - name: stuck
              description: local moved
              description_baseline: what both last held
`)
	cfg, err := config.Load(wsRoot)
	if err != nil {
		t.Fatalf("seed config.Load: %v", err)
	}

	out := captureOutput(t, func() {
		RunInit(t.Context(), client.New(f.url, "k"), cfg, wsRoot, nil)
	})

	// Server side: the hand edit was published, and the repo being adopted was
	// NOT reverted by the same request.
	if got := f.patchCount(); got != 1 {
		t.Fatalf("PATCH count = %d, want 1\n%s", got, out)
	}
	sent := f.patchedDescriptions(t, 0)
	if sent["push-me"] != "edited by hand in this file" {
		t.Errorf("PATCH push-me = %q, want the hand edit published (aihub#34)", sent["push-me"])
	}
	if sent["pull-me"] != "edited on the server via MCP" {
		t.Errorf("PATCH pull-me = %q, want %q — this is aihub#310 itself: the stale local "+
			"value must not be pushed over the server's", sent["pull-me"], "edited on the server via MCP")
	}
	if sent["stuck"] != "server moved" {
		t.Errorf("PATCH stuck = %q, want %q — a refused conflict must not resolve itself upward",
			sent["stuck"], "server moved")
	}

	// Local side: read back through the real loader, the same parse path the
	// NEXT init would use.
	after := map[string]config.Repo{}
	reloaded, err := config.Load(wsRoot)
	if err != nil {
		t.Fatalf("config.Load after init: %v", err)
	}
	for _, r := range reloaded.Projects["proj"].Repos {
		after[r.Name] = r
	}
	if got := after["pull-me"]; got.Description != "edited on the server via MCP" {
		t.Errorf("local pull-me = %q, want the server edit adopted", got.Description)
	}
	if got := after["push-me"]; got.Description != "edited by hand in this file" {
		t.Errorf("local push-me = %q, want the hand edit kept", got.Description)
	}
	// Both baselines must have advanced, or the next init re-decides settled
	// repos — the property that makes this file converge instead of ping-pong.
	for name, want := range map[string]string{
		"pull-me": "edited on the server via MCP",
		"push-me": "edited by hand in this file",
	} {
		if got := after[name].DescriptionBaseline; got != want {
			t.Errorf("%s baseline after init = %q, want %q", name, got, want)
		}
		if got := reconcileDescription(after[name].DescriptionBaseline, after[name].Description, want); got != descInSync {
			t.Errorf("%s compares as %q on a hypothetical re-run, want %q — init did not converge",
				name, got, descInSync)
		}
	}

	// The conflicted repo must come out of a full init untouched on BOTH sides,
	// with its baseline un-advanced. This is what fails if RunInit computes the
	// local view and then does not hand it to the writer: the file would take
	// the server's value and the conflict would vanish without anyone deciding.
	if got := after["stuck"].Description; got != "local moved" {
		t.Errorf("local stuck = %q, want %q kept — a refused conflict must not be resolved "+
			"by the file writer", got, "local moved")
	}
	if got := after["stuck"].DescriptionBaseline; got != "what both last held" {
		t.Errorf("stuck baseline = %q, want %q — advancing it silently settles the conflict",
			got, "what both last held")
	}
	if got := reconcileDescription(after["stuck"].DescriptionBaseline,
		after["stuck"].Description, "server moved"); got != descConflict {
		t.Errorf("stuck compares as %q on the next run, want %q — the conflict must persist "+
			"until a human resolves it", got, descConflict)
	}

	if !strings.Contains(out, "local → server (push-me)") || !strings.Contains(out, "server → local (pull-me)") {
		t.Errorf("init did not report both directions:\n%s", out)
	}
	if !strings.Contains(out, "CONFLICT") || !strings.Contains(out, "1 conflict (stuck)") {
		t.Errorf("init did not report the conflict:\n%s", out)
	}
	if strings.Contains(out, "descriptions synced:") {
		t.Errorf("init still prints the direction-blind success line:\n%s", out)
	}
}

// TestConflictReport_InstructionsActuallyResolve: the report tells the user two
// ways out of a conflict. Those two sentences are assertions about
// reconcileDescription, and an assertion in a message is exactly the kind that
// rots unnoticed — nothing else in this file would go red if the advice became
// wrong. So apply each instruction literally and check where it lands.
func TestConflictReport_InstructionsActuallyResolve(t *testing.T) {
	const (
		baseline = "what both last held"
		local    = "local moved"
		server   = "server moved"
	)
	d := descDecision{Repo: "r", Baseline: baseline, Local: local, Server: server}
	if got := reconcileDescription(d.Baseline, d.Local, d.Server); got != descConflict {
		t.Fatalf("fixture is not a conflict: %q", got)
	}
	report := conflictReport("proj", d)

	// "To take the server's value: set this repo's `description` in
	// .polyforge.yaml to it."
	if got := reconcileDescription(baseline, server, server); got != descInSync {
		t.Errorf("following the report's take-the-server advice yields %q, want %q.\n%s",
			got, descInSync, report)
	}
	// "... or just delete BOTH `description` and `description_baseline`."
	// Deleting one alone does NOT work — local "" against a non-empty baseline
	// still reads as a local-side change and stays a conflict — which is why
	// the word BOTH is load-bearing rather than emphasis.
	if got := reconcileDescription("", "", server); got != descPull {
		t.Errorf("following the report's delete-both advice yields %q, want %q.\n%s",
			got, descPull, report)
	}
	if got := reconcileDescription(baseline, "", server); got != descConflict {
		t.Errorf("deleting only `description` yields %q, want %q — if this ever stops being "+
			"a conflict, the report's insistence on BOTH is misleading", got, descConflict)
	}

	// "To publish yours: set this repo's `description_baseline` to the server
	// value above, then re-run."
	if got := reconcileDescription(server, local, server); got != descPush {
		t.Errorf("following the report's publish-mine advice yields %q, want %q — the "+
			"instruction printed to users would be wrong.\n%s", got, descPush, report)
	}
}

// TestRunInit_MemberHeaderDoesNotPromisePublishing: the header is written once
// for the whole file, but publishing is an OWNER capability — runMemberInit
// never PATCHes, and RunInit fills localDescs only in the owner branch. An
// unqualified "edit it here and init publishes it" therefore invites a member
// to make an edit that is silently reverted on their next init: the aihub#310
// incident shape, reproduced one path over by the fix for it.
//
// The test measures the behaviour AND checks the claim, in one function. Either
// half going red is real: if members ever start publishing, the behavioural
// assertions fail; if the qualification is dropped from the header, the text
// assertions fail. A test that only read the text could be satisfied by prose.
func TestRunInit_MemberHeaderDoesNotPromisePublishing(t *testing.T) {
	home, wsRoot := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	f := newFakeProjectServer(t, `[{"name":"repo-a","description":"server value"}]`)
	// The caller is a plain member of a project someone else owns.
	f.callerUserID = "u_member"
	f.ownerUserID = "u_someone_else"
	f.members = []map[string]any{{"user_id": "u_member", "role": "writer"}}

	// A clean push case: valid baseline, no conflict. On the owner path this is
	// published. Here it must not be — and the member must not have been told
	// it would be.
	writeYAMLFixture(t, wsRoot, `version: 1
projects:
    proj:
        repos:
            - name: repo-a
              description: MY LOCAL EDIT
              description_baseline: server value
`)
	cfg, err := config.Load(wsRoot)
	if err != nil {
		t.Fatalf("seed config.Load: %v", err)
	}

	out := captureOutput(t, func() {
		RunInit(t.Context(), client.New(f.url, "k"), cfg, wsRoot, nil)
	})

	// --- measured behaviour: a member publishes nothing, and loses the edit ---
	if got := f.patchCount(); got != 0 {
		t.Errorf("PATCHes sent by a member init = %d, want 0 — runMemberInit must not "+
			"publish (this wi's non-goals)", got)
	}
	reloaded, err := config.Load(wsRoot)
	if err != nil {
		t.Fatalf("config.Load after init: %v", err)
	}
	r := reloaded.Projects["proj"].Repos[0]
	if r.Description != "server value" {
		t.Fatalf("fixture no longer reproduces the hazard: member's local edit survived as %q. "+
			"If members now publish or retain edits, this test needs rewriting, not relaxing",
			r.Description)
	}
	// And the revert is SILENT: runMemberInit is out of scope for this wi, so it
	// still prints no direction line. That is precisely why the warning has to
	// live in the header — the header is the only place a member is told.
	if strings.Contains(out, "descriptions:") {
		t.Errorf("member init now prints a descriptions line; the header wording and this "+
			"test's premise both need revisiting:\n%s", out)
	}

	// --- the claim the member is left holding must match that behaviour ---
	b, err := os.ReadFile(filepath.Join(wsRoot, ".polyforge.yaml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	header, _, _ := strings.Cut(string(b), "\nversion:")
	entry := headerEntryFor(t, header, "repos", "description")

	// The substantive property is that the entry DISTINGUISHES the two roles —
	// an unconditioned "init publishes it" is what misleads a member. Matched as
	// whole tokens, case-insensitively, so the wording stays free to change.
	toks := map[string]bool{}
	for _, tok := range headerTokens(strings.ToLower(entry)) {
		toks[tok] = true
	}
	for _, role := range []string{"owner", "member"} {
		if !toks[role] {
			t.Errorf("the repos[].description authority entry never names the %q role, so its "+
				"publish claim reads as unconditional — and a member acting on it loses the edit "+
				"this same test just measured being silently discarded. Entry:\n%s", role, entry)
		}
	}
	if !toks["overwritten"] {
		t.Errorf("the entry does not warn that a non-owner's local edit is overwritten:\n%s", entry)
	}
}

// headerTokens splits one header line into identifier tokens — the same shape a
// yaml key has. Tokenizing is what makes "owner" distinguishable from
// "github_owner_repo"; a substring test cannot tell them apart.
var headerTokenRe = regexp.MustCompile(`[A-Za-z0-9_]+`)

func headerTokens(line string) []string { return headerTokenRe.FindAllString(line, -1) }

// headerKeyColumn is the indent the generated header gives a key DECLARATION.
// Continuation lines are indented past it, which is what lets prose be told
// apart from a key path.
const headerKeyColumn = "#   "

// headerKeyTokens returns the identifier tokens of one header line's KEY PATH,
// or nil if the line does not declare a key.
//
// Restricting to the key column matters twice over. A bare token match anywhere
// on the line lets ordinary English in the descriptions answer for a yaml tag —
// "repo" is a word in "a cache of the project's repo list", and so are "value",
// "server" and "list". Those are exactly the names a future field might take, so
// prose would silently vouch for an undocumented one.
func headerKeyTokens(line string) []string {
	rest, ok := strings.CutPrefix(line, headerKeyColumn)
	if !ok || rest == "" || rest[0] == ' ' {
		return nil // not a declaration line
	}
	// The key path ends at the first run of two or more spaces; single spaces
	// are used to put several keys on one line ("name / .url / .x").
	if i := strings.Index(rest, "  "); i >= 0 {
		rest = rest[:i]
	}
	return headerTokens(rest)
}

// headerDocumentsTag reports whether the generated header DECLARES a yaml key —
// as a whole token, in the key column.
//
// It replaced strings.Contains, which passed for any tag that merely occurred
// inside a documented one: with "github_owner_repo" present, an undocumented
// `yaml:"owner"` field read as documented, as would "desc" beside
// "description". The gate meant to catch an undocumented field was blind to
// exactly the names most likely to be added next.
func headerDocumentsTag(header, tag string) bool {
	for _, ln := range strings.Split(header, "\n") {
		for _, tok := range headerKeyTokens(ln) {
			if tok == tag {
				return true
			}
		}
	}
	return false
}

// headerEntryFor returns the block of the generated header describing one yaml
// key: the line whose key path ENDS with the given tokens, plus the indented
// continuation lines under it.
//
// Assertions run against the entry rather than the whole header, so a word that
// happens to appear in a neighbouring field's paragraph cannot satisfy a claim
// about this one. Matching the path as a token sequence also keeps
// "repos[].description" apart from both "projects.<p>.description" and
// "repos[].description_baseline".
func headerEntryFor(t *testing.T, header string, keyPath ...string) string {
	t.Helper()
	lines := strings.Split(header, "\n")
	start := -1
	for i, ln := range lines {
		toks := headerKeyTokens(ln)
		if len(toks) < len(keyPath) {
			continue
		}
		if slices.Equal(toks[len(toks)-len(keyPath):], keyPath) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no header entry ending in %v in:\n%s", keyPath, header)
	}
	end := start + 1
	for end < len(lines) && strings.HasPrefix(lines[end], "#") && headerKeyTokens(lines[end]) == nil {
		end++
	}
	return strings.Join(lines[start:end], "\n")
}

// TestHeaderDocumentsTag_MatchesWholeTokens pins the fix for the substring hole
// in the authority gate itself.
//
// A gate is only as good as its matcher, and this one is the single class-level
// check in the aihub#310 fix: it is what turns red when a future field is added
// to this file with nobody saying who owns it. With strings.Contains it answered
// "documented" for any tag that merely occurred inside a documented one, so the
// very names most likely to be added next — `owner` beside `github_owner_repo`,
// `desc` beside `description` — were exactly the ones it could not see.
func TestHeaderDocumentsTag_MatchesWholeTokens(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	path := filepath.Join(tmp, ".polyforge.yaml")
	projects := []serverProject{{
		Name: "proj", OwnerUserID: "u_caller", Visible: true,
		Repos: json.RawMessage(`[{"name":"r","description":"d"}]`),
	}}
	if err := writePolyforgeYAML(path, projects, "u_caller", nil, nil); err != nil {
		t.Fatalf("writePolyforgeYAML: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	header, _, _ := strings.Cut(string(b), "\nversion:")

	// Every key the writer actually emits is documented as a whole token.
	for _, tag := range []string{
		"repos", "description", "scenario",
		"name", "url", "github_owner_repo", "description_baseline",
	} {
		if !headerDocumentsTag(header, tag) {
			t.Errorf("headerDocumentsTag(%q) = false, want true — it is documented", tag)
		}
	}

	// Undocumented tags that the header nonetheless CONTAINS, in two flavours:
	//   - substrings of a documented key ("owner" inside "github_owner_repo",
	//     "desc" inside "description") — these passed the old strings.Contains
	//     check, so a real `yaml:"owner"` field was waved through undescribed;
	//   - ordinary English from the prose columns ("repo", "value", "server",
	//     "list") — these pass a bare whole-token match, so the descriptions
	//     would vouch for a field nobody documented.
	for _, tag := range []string{
		"owner", "desc", "base", "ur", "scenari",
		"repo", "value", "server", "list", "sync",
	} {
		if headerDocumentsTag(header, tag) {
			t.Errorf("headerDocumentsTag(%q) = true, but %q is nowhere in the header as a "+
				"whole token — this is the substring hole reopening", tag, tag)
		}
		if !strings.Contains(header, tag) {
			t.Errorf("test premise broken: %q is not even a substring of the header, so it "+
				"does not exercise the hole", tag)
		}
	}
}
