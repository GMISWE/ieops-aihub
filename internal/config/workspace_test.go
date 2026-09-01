package config

import "testing"

func TestWorkspaceRoot_EnvWins(t *testing.T) {
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", "/tmp/ws-from-env")
	if got := WorkspaceRoot(); got != "/tmp/ws-from-env" {
		t.Fatalf("WorkspaceRoot() = %q, want /tmp/ws-from-env", got)
	}
}
