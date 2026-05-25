package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
