package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StateFile is the per-wi credential file at <workspace>/.polyforge/state/<wi_id>.json.
// Created by pf_claim_work_item, read by credential middleware, deleted (for
// terminal states) by pf_complete_attempt.
// Per design §9.5.4: state files are workspace-scoped, not home-dir global.
// Only ~/.polyforge/config.toml is machine-global.
type StateFile struct {
	WIID          string            `json:"wi_id"`
	Slug          string            `json:"slug,omitempty"`
	Project       string            `json:"project,omitempty"`
	AttemptID     string            `json:"attempt_id"`
	ClaimEpoch    int64             `json:"claim_epoch"`
	SessionSecret string            `json:"session_secret"` // 64-hex plaintext, mode 0600
	ClaimedAt     string            `json:"claimed_at,omitempty"`
	Claimed       bool              `json:"claimed"`
	IdemKey       string            `json:"idem_key,omitempty"`
	Worktrees     map[string]string `json:"worktrees,omitempty"` // repo -> abs path
}

// StateDir returns the workspace-scoped state directory: <wsRoot>/.polyforge/state/
// Per design §9.5.4, state files are workspace-local (not in ~/), so different
// workspaces don't bleed credentials into each other.
// Uses POLYFORGE_WORKSPACE_ROOT env (set by Claude Code), falling back to
// FindWorkspaceRoot (walks up from cwd looking for .polyforge.yaml).
func StateDir() string {
	wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
	if wsRoot == "" {
		wsRoot = FindWorkspaceRoot()
	}
	return filepath.Join(wsRoot, ".polyforge", "state")
}

// FindWorkspaceRoot walks up from the current working directory looking for a
// .polyforge.yaml file, similar to how git locates .git. The directory
// containing .polyforge.yaml is returned as the workspace root.
// Falls back to cwd when no .polyforge.yaml is found (e.g. no workspace
// configured yet, or running outside any workspace).
func FindWorkspaceRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".polyforge.yaml")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// reached filesystem root without finding .polyforge.yaml
			cwd, _ := os.Getwd()
			return cwd
		}
		dir = parent
	}
}

// WriteStateFile writes s to <workspace>/.polyforge/state/<wi_id>.json with mode 0600.
func WriteStateFile(s *StateFile) error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, s.WIID+".json")
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

// ReadStateFile reads <workspace>/.polyforge/state/<wi_id>.json.
func ReadStateFile(wiID string) (*StateFile, error) {
	path := filepath.Join(StateDir(), wiID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("state file not found for %s: %w", wiID, err)
	}
	var s StateFile
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state file for %s: %w", wiID, err)
	}
	return &s, nil
}

// DeleteStateFile removes <workspace>/.polyforge/state/<wi_id>.json.
func DeleteStateFile(wiID string) error {
	return os.Remove(filepath.Join(StateDir(), wiID+".json"))
}

// FindStateFiles returns all state files in the state directory (for startup scan).
func FindStateFiles() ([]*StateFile, error) {
	dir := StateDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var states []*StateFile
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		wiID := e.Name()[:len(e.Name())-5]
		s, err := ReadStateFile(wiID)
		if err == nil {
			states = append(states, s)
		}
	}
	return states, nil
}
