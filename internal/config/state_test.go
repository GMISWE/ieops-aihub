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

// TestWriteClaimState_BySlug_RemovesStubAndResolves is the aihub#141 end-to-end
// regression at the state-file layer: it drives the exact sequence the
// pf_claim_work_item handler uses when a work item is claimed BY SLUG, then
// confirms a later by-slug credential op (complete/emit/update/pause) resolves
// to the canonical credentials instead of the empty-attempt stub.
func TestWriteClaimState_BySlug_RemovesStubAndResolves(t *testing.T) {
	withTempHome(t)
	slug := "aihub#42"
	canonical := "wi_life0001"

	// 1. C6-2 pre-claim partial stub keyed by the slug (empty attempt_id).
	if err := WriteStateFile(&StateFile{WIID: slug, SessionSecret: "s", Claimed: false}); err != nil {
		t.Fatal(err)
	}
	// 2. Server returns the canonical id + real credentials; claim finalizes.
	canon := &StateFile{
		WIID: canonical, Slug: slug, AttemptID: "ra_real042",
		ClaimEpoch: 1, SessionSecret: "s", Claimed: true,
	}
	if err := WriteClaimState(slug, canonical, canon); err != nil {
		t.Fatalf("WriteClaimState: %v", err)
	}

	// The orphan slug stub must be gone.
	if _, err := ReadStateFile(slug); err == nil {
		t.Error("orphan slug stub was not removed")
	}
	// A by-slug credential op must resolve to the canonical state (real attempt_id).
	got, err := ResolveStateFile(slug)
	if err != nil {
		t.Fatalf("ResolveStateFile(slug) after claim: %v", err)
	}
	if got.AttemptID != "ra_real042" || got.WIID != canonical {
		t.Errorf("by-slug resolve = {WIID:%q AttemptID:%q}, want canonical {%q ra_real042}", got.WIID, got.AttemptID, canonical)
	}
	// The canonical file is also directly addressable.
	if _, err := ReadStateFile(canonical); err != nil {
		t.Errorf("canonical state file missing: %v", err)
	}
}

// TestWriteClaimState_ByCanonicalID_NoSpuriousDelete: claiming by the canonical
// id must not delete anything (passedID == canonicalID).
func TestWriteClaimState_ByCanonicalID_NoSpuriousDelete(t *testing.T) {
	withTempHome(t)
	id := "wi_life0002"
	canon := &StateFile{
		WIID: id, Slug: "aihub#43", AttemptID: "ra_real043",
		ClaimEpoch: 1, SessionSecret: "s", Claimed: true,
	}
	if err := WriteClaimState(id, id, canon); err != nil {
		t.Fatalf("WriteClaimState: %v", err)
	}
	got, err := ReadStateFile(id)
	if err != nil {
		t.Fatalf("canonical state file missing after claim-by-id: %v", err)
	}
	if got.AttemptID != "ra_real043" {
		t.Errorf("attempt_id = %q, want ra_real043", got.AttemptID)
	}
}

// TestResolveStateFile_ByCanonicalID: a direct canonical-id lookup returns the
// file immediately.
func TestResolveStateFile_ByCanonicalID(t *testing.T) {
	withTempHome(t)
	want := &StateFile{
		WIID: "wi_canon001", Slug: "aihub#9", AttemptID: "ra_real001",
		ClaimEpoch: 1, SessionSecret: "s", Claimed: true,
	}
	if err := WriteStateFile(want); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveStateFile("wi_canon001")
	if err != nil {
		t.Fatalf("ResolveStateFile by id: %v", err)
	}
	if got.WIID != want.WIID || got.AttemptID != want.AttemptID {
		t.Errorf("got %+v, want canonical %+v", got, want)
	}
}

// TestResolveStateFile_BySlug_OnlyCanonical: addressing by slug resolves to the
// canonical file via its Slug field when no slug-keyed file exists (the state
// after pf_claim cleans up its orphan stub).
func TestResolveStateFile_BySlug_OnlyCanonical(t *testing.T) {
	withTempHome(t)
	want := &StateFile{
		WIID: "wi_canon002", Slug: "aihub#10", AttemptID: "ra_real002",
		ClaimEpoch: 2, SessionSecret: "s", Claimed: true,
	}
	if err := WriteStateFile(want); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveStateFile("aihub#10")
	if err != nil {
		t.Fatalf("ResolveStateFile by slug: %v", err)
	}
	if got.WIID != want.WIID || got.AttemptID != want.AttemptID {
		t.Errorf("got %+v, want canonical %+v", got, want)
	}
}

// TestResolveStateFile_BySlug_ShadowedByStub is the aihub#141 regression: a
// slug-keyed stub with empty attempt_id sits alongside the canonical file (the
// pre-fix claim-by-slug state). Resolving by slug must return the canonical file
// with the real attempt_id, never the empty-attempt stub.
func TestResolveStateFile_BySlug_ShadowedByStub(t *testing.T) {
	withTempHome(t)
	// Orphan C6-2 stub keyed by the slug, empty attempt_id.
	if err := WriteStateFile(&StateFile{
		WIID: "aihub#11", SessionSecret: "s", Claimed: false,
	}); err != nil {
		t.Fatal(err)
	}
	// Canonical file with the real credentials.
	canon := &StateFile{
		WIID: "wi_canon003", Slug: "aihub#11", AttemptID: "ra_real003",
		ClaimEpoch: 3, SessionSecret: "s", Claimed: true,
	}
	if err := WriteStateFile(canon); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveStateFile("aihub#11")
	if err != nil {
		t.Fatalf("ResolveStateFile shadowed: %v", err)
	}
	if got.AttemptID != "ra_real003" {
		t.Errorf("resolved attempt_id = %q, want %q (must not return empty stub)", got.AttemptID, "ra_real003")
	}
	if got.WIID != "wi_canon003" {
		t.Errorf("resolved WIID = %q, want canonical wi_canon003", got.WIID)
	}
}

// TestForceTakeover_BySlug_WritesSlugResolvableState covers the WriteClaimState
// -> ReadStateFile/ResolveStateFile round trip for the record shape that
// pf_force_takeover writes when a work item is force-taken BY SLUG: keyed by the
// canonical id, with Slug and Project populated. Given that input it pins two
// outputs — no orphan slug-keyed file survives, and a later by-slug lookup
// resolves to the canonical, non-empty-attempt record.
//
// ⚠️ Read what this does NOT do before relying on it. It does not call the
// pf_force_takeover handler; it hand-writes the post-fix StateFile and exercises
// only WriteClaimState / ReadStateFile / ResolveStateFile, none of which aihub#149
// changed. It therefore CANNOT go red on the pre-#149 build — verified by
// reverting the six source files that commit touched and re-running it, which
// passes. Its earlier comment claimed to drive "the exact write sequence the
// handler now uses", which is the false-green shape this repo keeps getting
// bitten by: a marker that reads like a regression test for a fix it cannot
// observe.
//
// The assertion that actually discriminates on the handler is
// TestForceTakeoverBySlugWritesASlugResolvableStateFile in
// internal/mcp/state_resolve_wiring_test.go — it drives the registered MCP tool
// against a fake aihub and reads the resulting state directory off disk, and it
// dies on the pre-#149 build with "no state file under the canonical key".
// (aihub#319)
func TestForceTakeover_BySlug_WritesSlugResolvableState(t *testing.T) {
	withTempHome(t)
	slug := "aihub#149"
	canonical := "wi_take0149"

	// pf_force_takeover, addressed by slug, with the server echoing the canonical
	// id + slug + project. Mirrors the MCP handler's post-fix state-file write.
	sf := &StateFile{
		WIID: canonical, Slug: slug, Project: "aihub",
		AttemptID: "ra_take149", ClaimEpoch: 5, SessionSecret: "s", Claimed: true,
	}
	if err := WriteClaimState(slug, canonical, sf); err != nil {
		t.Fatalf("WriteClaimState: %v", err)
	}

	// The state file must NOT be keyed by the slug (no orphan slug-keyed file).
	if _, err := ReadStateFile(slug); err == nil {
		t.Error("force_takeover wrote a slug-keyed file; want canonical-keyed only")
	}
	// A later by-slug credential op must resolve to the canonical takeover state.
	got, err := ResolveStateFile(slug)
	if err != nil {
		t.Fatalf("ResolveStateFile(slug) after force_takeover: %v", err)
	}
	if got.WIID != canonical || got.AttemptID != "ra_take149" {
		t.Errorf("by-slug resolve = {WIID:%q AttemptID:%q}, want {%q ra_take149}", got.WIID, got.AttemptID, canonical)
	}
	if got.Slug != slug || got.Project != "aihub" {
		t.Errorf("resolved Slug/Project = {%q %q}, want {%q aihub} (empty Slug breaks slug-scan)", got.Slug, got.Project, slug)
	}
}

// TestResolveStateFile_Missing: unknown id/slug surfaces an error.
func TestResolveStateFile_Missing(t *testing.T) {
	withTempHome(t)
	if _, err := ResolveStateFile("aihub#404"); err == nil {
		t.Error("expected error for unknown id/slug")
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
