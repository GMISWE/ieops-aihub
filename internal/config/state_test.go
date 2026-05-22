package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHome points HOME and POLYFORGE_WORKSPACE_ROOT at a fresh tempdir for
// the test, so StateDir() resolves under it. Restored on test cleanup.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", tmp)
	return tmp
}

func TestWriteReadDeleteStateFile_RoundTrip(t *testing.T) {
	withTempHome(t)

	want := &StateFile{
		WIID:          "wi_abc12345",
		Slug:          "fix-login",
		Project:       "marketplace",
		AttemptID:     "ra_xyz12345",
		ClaimEpoch:    7,
		SessionSecret: "deadbeefcafebabe",
		Claimed:       true,
		Worktrees:     map[string]string{"aihub": "/tmp/wt/aihub"},
	}

	if err := WriteStateFile(want); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}

	got, err := ReadStateFile(want.WIID)
	if err != nil {
		t.Fatalf("ReadStateFile: %v", err)
	}
	if got.WIID != want.WIID || got.Project != want.Project ||
		got.AttemptID != want.AttemptID || got.ClaimEpoch != want.ClaimEpoch ||
		got.SessionSecret != want.SessionSecret || got.Claimed != want.Claimed {
		t.Errorf("read mismatch: got %+v, want %+v", got, want)
	}
	if got.Worktrees["aihub"] != "/tmp/wt/aihub" {
		t.Errorf("worktrees not preserved: %+v", got.Worktrees)
	}

	if err := DeleteStateFile(want.WIID); err != nil {
		t.Fatalf("DeleteStateFile: %v", err)
	}
	if _, err := ReadStateFile(want.WIID); err == nil {
		t.Error("ReadStateFile after delete: expected error")
	}
}

func TestWriteStateFile_Mode0600(t *testing.T) {
	tmp := withTempHome(t)
	s := &StateFile{WIID: "wi_perm0001", AttemptID: "ra_x", ClaimEpoch: 1, SessionSecret: "s"}
	if err := WriteStateFile(s); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
	path := filepath.Join(tmp, ".polyforge", "state", s.WIID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Only check the user-readable/group/world bits; umask may not affect explicit perms.
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("file mode = %v, want 0600", mode)
	}
}

func TestFindStateFiles_NoDir(t *testing.T) {
	withTempHome(t)
	// Directory does not exist yet.
	got, err := FindStateFiles()
	if err != nil {
		t.Fatalf("FindStateFiles: %v", err)
	}
	if got != nil {
		t.Errorf("got %d files, want nil", len(got))
	}
}

func TestFindStateFiles_EmptyDir(t *testing.T) {
	tmp := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(tmp, ".polyforge", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := FindStateFiles()
	if err != nil {
		t.Fatalf("FindStateFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d files, want 0", len(got))
	}
}

func TestFindStateFiles_WithFiles(t *testing.T) {
	withTempHome(t)
	for _, id := range []string{"wi_aaa11111", "wi_bbb22222", "wi_ccc33333"} {
		if err := WriteStateFile(&StateFile{
			WIID: id, AttemptID: "ra_x", ClaimEpoch: 1, SessionSecret: "s",
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindStateFiles()
	if err != nil {
		t.Fatalf("FindStateFiles: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d files, want 3", len(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.WIID] = true
	}
	for _, want := range []string{"wi_aaa11111", "wi_bbb22222", "wi_ccc33333"} {
		if !seen[want] {
			t.Errorf("missing wi %s", want)
		}
	}
}

func TestFindStateFiles_IgnoresNonJSON(t *testing.T) {
	tmp := withTempHome(t)
	dir := filepath.Join(tmp, ".polyforge", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteStateFile(&StateFile{WIID: "wi_keep0001", AttemptID: "ra", ClaimEpoch: 1, SessionSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	got, err := FindStateFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].WIID != "wi_keep0001" {
		t.Errorf("got %+v, want only wi_keep0001", got)
	}
}

func TestReadStateFile_MissingError(t *testing.T) {
	withTempHome(t)
	_, err := ReadStateFile("wi_does_not_exist")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestReadStateFile_BadJSON(t *testing.T) {
	tmp := withTempHome(t)
	dir := filepath.Join(tmp, ".polyforge", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wi_bad11111.json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadStateFile("wi_bad11111")
	if err == nil {
		t.Error("expected parse error")
	}
}

func TestStateDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", tmp)
	got := StateDir()
	want := filepath.Join(tmp, ".polyforge", "state")
	if got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
}

func TestFindWorkspaceRoot_EnvOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", tmp)
	// StateDir must respect the env var (FindWorkspaceRoot not called).
	got := StateDir()
	if got != filepath.Join(tmp, ".polyforge", "state") {
		t.Errorf("StateDir with env = %q, want under %q", got, tmp)
	}
}

func TestFindWorkspaceRoot_WalksUp(t *testing.T) {
	// Build a temp tree:  root/ .polyforge.yaml
	//                     root/sub/subsub/
	// Start from root/sub/subsub — FindWorkspaceRoot must return root.
	root := t.TempDir()
	yamlPath := filepath.Join(root, ".polyforge.yaml")
	if err := os.WriteFile(yamlPath, []byte("workspace: test\n"), 0644); err != nil {
		t.Fatalf("write .polyforge.yaml: %v", err)
	}
	deep := filepath.Join(root, "sub", "subsub")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatalf("mkdir deep: %v", err)
	}

	// Temporarily chdir into the deep directory so FindWorkspaceRoot starts there.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(deep); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	got := FindWorkspaceRoot()
	if got != root {
		t.Errorf("FindWorkspaceRoot = %q, want %q", got, root)
	}
}

func TestFindWorkspaceRoot_FallsBackToCwd(t *testing.T) {
	// Use a temp dir with no .polyforge.yaml anywhere above it.
	// FindWorkspaceRoot must fall back to cwd.
	tmp := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	got := FindWorkspaceRoot()
	// The result should be tmp (the cwd), not empty or ".".
	if got != tmp {
		t.Errorf("FindWorkspaceRoot fallback = %q, want %q", got, tmp)
	}
}
