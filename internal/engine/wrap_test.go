package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cleanupFixture(t *testing.T, names ...string) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	paths := map[string]string{}
	for _, name := range names {
		wt := filepath.Join(root, "pf.aihub-708", name)
		if err := os.MkdirAll(wt, 0755); err != nil {
			t.Fatal(err)
		}
		paths[name] = wt
	}
	return root, paths
}

// cleanupGit simulates git's own filesystem side effects: `git worktree remove
// <wt>` DELETES the worktree directory (real git does exactly that), so a test
// can observe what the engine does with the parent once its children are
// actually gone — the same shape the emptiness gate in wrap.go reasons over.
func cleanupGit(t *testing.T, log *[]string, dirty, unshipped, detachFail string) GitRunner {
	return func(dir string, args ...string) (string, error) {
		command := strings.Join(args, " ")
		*log = append(*log, command+" @ "+dir)
		switch {
		case strings.HasPrefix(command, "status "):
			if filepath.Base(dir) == dirty {
				return "?? new.txt", nil
			}
			return "", nil
		case strings.HasPrefix(command, "symbolic-ref "):
			return "polyforge/task", nil
		case strings.HasPrefix(command, "show-ref "):
			return "", nil
		case strings.HasPrefix(command, "rev-list "):
			if filepath.Base(dir) == unshipped {
				return "1", nil
			}
			return "0", nil
		case strings.HasPrefix(command, "worktree remove"):
			if filepath.Base(dir) == detachFail {
				return "", errors.New("detach refused")
			}
			// Real git deletes the worktree directory on a successful remove; the
			// target is the LAST argument of the command.
			if target := args[len(args)-1]; target != "" {
				_ = os.RemoveAll(target)
			}
		}
		return "", nil
	}
}

func TestCleanupWorktreesCleanShippedDetachesThenRemovesParent(t *testing.T) {
	root, paths := cleanupFixture(t, "a", "b")
	var log []string
	var parent string
	removed, errs, err := CleanupWorktrees(cleanupGit(t, &log, "", "", ""), func(p string) error { parent = p; return nil }, root, paths)
	if err != nil || len(errs) != 0 || len(removed) != 2 || parent != filepath.Dir(paths["a"]) {
		t.Fatalf("removed=%v errs=%v parent=%q err=%v", removed, errs, parent, err)
	}
	if !strings.Contains(log[len(log)-1], "worktree remove ") {
		t.Fatalf("last git operation must detach: %v", log)
	}
}

// Regression (Batch 4B review): the worktrees map is the ATTEMPT's own view;
// the shared parent may still hold a repo worktree the map omits. A
// RemoveAll-backed rmParent handed that parent would delete the omitted
// sibling with it, so the engine must retain the parent instead.
func TestCleanupWorktreesRetainsParentOverOmittedSibling(t *testing.T) {
	root, paths := cleanupFixture(t, "a", "b")
	// A sibling worktree under the same pf.* parent that the map omits.
	if err := os.MkdirAll(filepath.Join(filepath.Dir(paths["a"]), "other-repo"), 0755); err != nil {
		t.Fatal(err)
	}
	var log []string
	removedParent := false
	removed, errs, err := CleanupWorktrees(cleanupGit(t, &log, "", "", ""), func(string) error { removedParent = true; return nil }, root, paths)
	if err != nil || len(errs) != 0 || len(removed) != 2 {
		t.Fatalf("detach must succeed: removed=%v errs=%v err=%v", removed, errs, err)
	}
	if removedParent {
		t.Fatal("shared parent removed although an omitted sibling still lives under it")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(paths["a"]), "other-repo")); statErr != nil {
		t.Fatalf("omitted sibling was deleted: %v", statErr)
	}
}

// Regression (Batch 4B review): leftover scratch (any entry at all — files
// count too) under the parent retains it; only an EMPTY parent is removed.
func TestCleanupWorktreesRetainsParentOverScratch(t *testing.T) {
	root, paths := cleanupFixture(t, "a")
	scratch := filepath.Join(filepath.Dir(paths["a"]), "notes.txt")
	if err := os.WriteFile(scratch, []byte("left behind"), 0644); err != nil {
		t.Fatal(err)
	}
	var log []string
	removedParent := false
	removed, errs, err := CleanupWorktrees(cleanupGit(t, &log, "", "", ""), func(string) error { removedParent = true; return nil }, root, paths)
	if err != nil || len(errs) != 0 || len(removed) != 1 {
		t.Fatalf("detach must succeed: removed=%v errs=%v err=%v", removed, errs, err)
	}
	if removedParent {
		t.Fatal("shared parent removed although scratch still lives under it")
	}
	if _, statErr := os.Stat(scratch); statErr != nil {
		t.Fatalf("scratch file was deleted: %v", statErr)
	}
}

func TestCleanupWorktreesRefusesDirtyUnshippedAndActiveBeforeDetach(t *testing.T) {
	for _, kind := range []string{"dirty", "unshipped", "active"} {
		t.Run(kind, func(t *testing.T) {
			root, paths := cleanupFixture(t, "a", "b")
			if kind == "active" {
				dir := filepath.Join(root, ".polyforge", "state")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				data := []byte(`{"claimed":true,"worktrees":{"a":"` + paths["a"] + `"}}`)
				if err := os.WriteFile(filepath.Join(dir, "wi_A.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var log []string
			removedParent := false
			dirty, unshipped := "", ""
			if kind == "dirty" {
				dirty = "a"
			}
			if kind == "unshipped" {
				unshipped = "a"
			}
			removed, errs, err := CleanupWorktrees(cleanupGit(t, &log, dirty, unshipped, ""), func(string) error { removedParent = true; return nil }, root, paths)
			if removedParent || len(removed) != 0 {
				t.Fatalf("unsafe cleanup: %v %v", removed, log)
			}
			for _, call := range log {
				if strings.Contains(call, "worktree remove") {
					t.Fatalf("partial detach: %v", log)
				}
			}
			if kind == "active" && err == nil {
				t.Fatal("active refusal not reported")
			}
			if kind != "active" && errs["a"] == nil {
				t.Fatalf("missing %s refusal: %v", kind, errs)
			}
		})
	}
}

func TestCleanupWorktreesDetachFailureKeepsSharedParent(t *testing.T) {
	root, paths := cleanupFixture(t, "a", "b")
	var log []string
	removedParent := false
	removed, errs, err := CleanupWorktrees(cleanupGit(t, &log, "", "", "a"), func(string) error { removedParent = true; return nil }, root, paths)
	if removedParent || err == nil || errs["a"] == nil || len(removed) != 1 {
		t.Fatalf("removed=%v errs=%v parentRemoved=%v err=%v", removed, errs, removedParent, err)
	}
}

func TestCleanupWorktreesRejectsMalformedParentBeforeDetach(t *testing.T) {
	root := t.TempDir()
	for _, paths := range []map[string]string{
		{"a": filepath.Join(root, "a")},
		{"a": filepath.Join(root, "pf.good", "other")},
		{"a": filepath.Join(root, "pf.one", "a"), "b": filepath.Join(root, "pf.two", "b")},
	} {
		var log []string
		_, _, err := CleanupWorktrees(cleanupGit(t, &log, "", "", ""), func(string) error { t.Fatal("removed unsafe parent"); return nil }, root, paths)
		if err == nil || len(log) != 0 {
			t.Fatalf("unsafe map %v: err=%v git=%v", paths, err, log)
		}
	}
}
