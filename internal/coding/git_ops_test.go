package coding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitDiffCompletePendingChangeSet(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "origin", r.base)
	r.git(t, "remote", "set-head", "origin", r.base)
	r.commit(t, "committed.txt", "committed")
	write(t, r, "staged.txt", "staged")
	r.git(t, "add", "staged.txt")
	write(t, r, "f.txt", "unstaged")
	write(t, r, "untracked file.txt", "new file")
	for _, tc := range []struct {
		base      bool
		committed bool
	}{{false, false}, {true, true}} {
		diff, err := GitDiff(context.Background(), r.wt, tc.base)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"staged.txt", "unstaged", "untracked file.txt", "new file"} {
			if !strings.Contains(diff, text) {
				t.Errorf("vsBase=%v missing %q:\n%s", tc.base, text, diff)
			}
		}
		if strings.Contains(diff, "committed.txt") != tc.committed {
			t.Errorf("vsBase=%v committed change visibility wrong:\n%s", tc.base, diff)
		}
	}
}

func TestGitDiffVsBaseRequiresExplicitRemoteBase(t *testing.T) {
	r := newTestRepo(t)
	// An unset origin/HEAD must fail loudly rather than silently use HEAD or a
	// guessed main/master branch and omit the committed change set.
	if _, err := GitDiff(context.Background(), r.wt, true); err == nil {
		t.Fatal("missing origin/HEAD accepted")
	}
	if err := os.WriteFile(filepath.Join(r.wt, "untracked.txt"), []byte("pending"), 0644); err != nil {
		t.Fatal(err)
	}
	diff, err := GitDiff(context.Background(), r.wt, false)
	if err != nil || !strings.Contains(diff, "untracked.txt") {
		t.Fatalf("HEAD diff=%q err=%v", diff, err)
	}
}
