package engine

import (
	"errors"
	"strings"
	"testing"
)

// fakeGit builds a GitRunner from a lookup table keyed "<dir>|<args joined by space>" ->
// (output, error). A call not present in the table returns a generic "not found" error, which
// is exactly the behavior a real `git` gives for a directory that doesn't exist or a ref that
// isn't there.
type fakeGitEntry struct {
	out string
	err error
}

func fakeGit(table map[string]fakeGitEntry) GitRunner {
	return func(dir string, args ...string) (string, error) {
		key := dir + "|" + strings.Join(args, " ")
		if e, ok := table[key]; ok {
			return e.out, e.err
		}
		return "", errors.New("fake git: no entry for " + key)
	}
}

// fakeExists builds a gitDirExists closure from a fixed set: exactly the paths in present
// report true, matching the injected-boundary shape ResolveScenarioPath now takes (W3: presence
// is decided by this closure, never by asking git).
func fakeExists(present ...string) func(path string) bool {
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	return func(path string) bool { return set[path] }
}

func TestResolveScenarioPath_OwnerQualifiedTakenWhenPresent(t *testing.T) {
	git := fakeGit(nil)
	exists := fakeExists("/ws/.repo/GMISWE__polyforge-coding")
	path, legacy, err := ResolveScenarioPath(git, exists, "/ws", "git@github.com:GMISWE/polyforge-coding.git")
	if err != nil {
		t.Fatalf("ResolveScenarioPath: %v", err)
	}
	if legacy {
		t.Error("legacy = true, want false (owner-qualified path was present)")
	}
	if path != "/ws/.repo/GMISWE__polyforge-coding" {
		t.Errorf("path = %q, want the owner-qualified path", path)
	}
}

func TestResolveScenarioPath_OwnerQualifiedPresenceIsNotProbedThroughGit(t *testing.T) {
	// W3 regression: git is never consulted for presence. A git table with no entries at all
	// would make any git(...) call return the fake's "no entry" error; if ResolveScenarioPath
	// still took the owner-qualified path, that proves it decided presence purely from
	// gitDirExists, matching the injected-boundary design (production wires it to a filesystem
	// os.Stat of <dir>/.git, never `git rev-parse --git-dir`, which false-positives inside any
	// enclosing git work tree).
	git := fakeGit(nil)
	exists := fakeExists("/ws/.repo/GMISWE__polyforge-coding")
	path, legacy, err := ResolveScenarioPath(git, exists, "/ws", "git@github.com:GMISWE/polyforge-coding.git")
	if err != nil {
		t.Fatalf("ResolveScenarioPath: %v", err)
	}
	if legacy {
		t.Error("legacy = true, want false")
	}
	if path != "/ws/.repo/GMISWE__polyforge-coding" {
		t.Errorf("path = %q, want the owner-qualified path", path)
	}
}

func TestResolveScenarioPath_LegacyTakenOnlyWhenOriginMatches(t *testing.T) {
	git := fakeGit(map[string]fakeGitEntry{
		"/ws/.repo/polyforge-coding|remote get-url origin": {out: "git@github.com:GMISWE/polyforge-coding.git"},
	})
	exists := fakeExists() // owner-qualified path absent
	path, legacy, err := ResolveScenarioPath(git, exists, "/ws", "git@github.com:GMISWE/polyforge-coding.git")
	if err != nil {
		t.Fatalf("ResolveScenarioPath: %v", err)
	}
	if !legacy {
		t.Error("legacy = false, want true (origin matched, owner-qualified path absent)")
	}
	if path != "/ws/.repo/polyforge-coding" {
		t.Errorf("path = %q, want the legacy path", path)
	}
}

func TestResolveScenarioPath_LegacyRejectedWhenOriginDiffers(t *testing.T) {
	// Negative control: same repo NAME, different owner. If this were accepted, two orgs'
	// same-named scenario repos would collide into one directory, the exact aihub#327 defect
	// the owner-qualified layout exists to prevent.
	git := fakeGit(map[string]fakeGitEntry{
		"/ws/.repo/polyforge-coding|remote get-url origin": {out: "git@github.com:SomeOtherOrg/polyforge-coding.git"},
	})
	exists := fakeExists()
	_, _, err := ResolveScenarioPath(git, exists, "/ws", "git@github.com:GMISWE/polyforge-coding.git")
	if err == nil {
		t.Fatal("ResolveScenarioPath: want an error when the legacy clone's origin names a different owner, got nil")
	}
}

func TestResolveScenarioPath_LegacyRejectedForHostileOrigins(t *testing.T) {
	// W1 regression: parseOwnerRepo-only comparison looked only at the last two path segments,
	// discarding the host entirely, so all three of these constructed origins were wrongly
	// ACCEPTED as matching project.scenario = git@github.com:GMISWE/polyforge-coding.git. Each
	// must now be rejected: sameRemote compares host (case-insensitively) and full path, not
	// just owner/repo.
	cases := []struct {
		name   string
		origin string
	}{
		{"different forge, same owner/repo", "git@evil.example.com:GMISWE/polyforge-coding.git"},
		{"gitlab nested group", "https://gitlab.com/attacker/GMISWE/polyforge-coding.git"},
		{"deeper namespace on the real host", "https://github.com/OTHER/GMISWE/polyforge-coding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			git := fakeGit(map[string]fakeGitEntry{
				"/ws/.repo/polyforge-coding|remote get-url origin": {out: tc.origin},
			})
			exists := fakeExists()
			_, _, err := ResolveScenarioPath(git, exists, "/ws", "git@github.com:GMISWE/polyforge-coding.git")
			if err == nil {
				t.Fatalf("ResolveScenarioPath: want an error for hostile origin %q, got nil", tc.origin)
			}
		})
	}
}

func TestResolveScenarioPath_LegacyRejectedWhenDirectoryAbsent(t *testing.T) {
	git := fakeGit(nil) // no entry at all for the legacy remote get-url origin call: simulates
	// the directory (and hence the git command) simply not existing.
	exists := fakeExists()
	_, _, err := ResolveScenarioPath(git, exists, "/ws", "git@github.com:GMISWE/polyforge-coding.git")
	if err == nil {
		t.Fatal("ResolveScenarioPath: want an error when the legacy clone is entirely absent, got nil")
	}
}

func TestResolveScenarioPath_SingleSegmentURLHasNoOwner(t *testing.T) {
	git := fakeGit(nil)
	exists := fakeExists("/ws/.repo/polyforge-coding")
	path, legacy, err := ResolveScenarioPath(git, exists, "/ws", "polyforge-coding")
	if err != nil {
		t.Fatalf("ResolveScenarioPath: %v", err)
	}
	if legacy {
		t.Error("legacy = true, want false")
	}
	if path != "/ws/.repo/polyforge-coding" {
		t.Errorf("path = %q, want the bare repo name with no owner prefix", path)
	}
}

func TestResolveScenarioPath_RejectsTraversalPathSegments(t *testing.T) {
	// W2 discretionary hardening: parseOwnerRepo's raw segments were previously joined into a
	// directory name unsanitized. sanitizePathSegment (duplicated from internal/cli/init.go,
	// same rationale as sameRemote) now rejects a ".." segment rather than letting
	// filepath.Join walk it out of workspaceRoot/.repo/.
	git := fakeGit(nil)
	exists := fakeExists()
	_, _, err := ResolveScenarioPath(git, exists, "/ws", "git@github.com:GMISWE/../../etc.git")
	if err == nil {
		t.Fatal("ResolveScenarioPath: want an error for a traversal repo path segment, got nil")
	}
}

func TestParseOwnerRepo(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"scp-like", "git@github.com:GMISWE/polyforge-coding.git", "GMISWE", "polyforge-coding", false},
		{"https with .git", "https://github.com/GMISWE/polyforge-coding.git", "GMISWE", "polyforge-coding", false},
		{"https without .git", "https://github.com/GMISWE/polyforge-coding", "GMISWE", "polyforge-coding", false},
		{"ssh scheme", "ssh://git@github.com/GMISWE/polyforge-coding.git", "GMISWE", "polyforge-coding", false},
		{"embedded credentials", "https://user:token@github.com/GMISWE/polyforge-coding.git", "GMISWE", "polyforge-coding", false},
		{"single segment, no owner", "polyforge-coding", "", "polyforge-coding", false},
		{"empty", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := parseOwnerRepo(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseOwnerRepo(%q): want an error, got none", tc.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOwnerRepo(%q): %v", tc.url, err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Errorf("parseOwnerRepo(%q) = (%q, %q), want (%q, %q)",
					tc.url, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

func TestPinScenarioSHA(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		git := fakeGit(map[string]fakeGitEntry{
			"/repo|rev-parse HEAD": {out: "abc123def456\n"},
		})
		sha, err := PinScenarioSHA(git, "/repo")
		if err != nil {
			t.Fatalf("PinScenarioSHA: %v", err)
		}
		if sha != "abc123def456" {
			t.Errorf("sha = %q, want trimmed %q", sha, "abc123def456")
		}
	})

	t.Run("git error propagates wrapped", func(t *testing.T) {
		git := fakeGit(map[string]fakeGitEntry{
			"/repo|rev-parse HEAD": {err: errors.New("not a git repository")},
		})
		if _, err := PinScenarioSHA(git, "/repo"); err == nil {
			t.Fatal("PinScenarioSHA: want an error, got nil")
		}
	})

	t.Run("empty output is an error, not a silently empty sha", func(t *testing.T) {
		git := fakeGit(map[string]fakeGitEntry{
			"/repo|rev-parse HEAD": {out: "   \n"},
		})
		if _, err := PinScenarioSHA(git, "/repo"); err == nil {
			t.Fatal("PinScenarioSHA: want an error for blank output, got nil")
		}
	})
}

func TestResolveTemplate(t *testing.T) {
	t.Run("project rung matches", func(t *testing.T) {
		git := fakeGit(map[string]fakeGitEntry{
			"/repo|show sha1:feature.aihub.md": {out: "project-specific content"},
		})
		content, source, err := ResolveTemplate(git, "/repo", "sha1", "feature", "aihub")
		if err != nil {
			t.Fatalf("ResolveTemplate: %v", err)
		}
		if source != "project" || content != "project-specific content" {
			t.Errorf("got (%q, %q), want (%q, %q)", content, source, "project-specific content", "project")
		}
	})

	t.Run("generic rung matches when project rung misses", func(t *testing.T) {
		git := fakeGit(map[string]fakeGitEntry{
			"/repo|show sha1:feature.aihub.md": {err: errors.New("path not found")},
			"/repo|show sha1:feature.md":       {out: "generic content"},
		})
		content, source, err := ResolveTemplate(git, "/repo", "sha1", "feature", "aihub")
		if err != nil {
			t.Fatalf("ResolveTemplate: %v", err)
		}
		if source != "generic" || content != "generic content" {
			t.Errorf("got (%q, %q), want (%q, %q)", content, source, "generic content", "generic")
		}
	})

	t.Run("no project given: only the generic rung is tried", func(t *testing.T) {
		git := fakeGit(map[string]fakeGitEntry{
			"/repo|show sha1:feature.md": {out: "generic content"},
		})
		content, source, err := ResolveTemplate(git, "/repo", "sha1", "feature", "")
		if err != nil {
			t.Fatalf("ResolveTemplate: %v", err)
		}
		if source != "generic" || content != "generic content" {
			t.Errorf("got (%q, %q), want (%q, %q)", content, source, "generic content", "generic")
		}
	})

	t.Run("neither rung exists: an error, not a panic or empty success", func(t *testing.T) {
		git := fakeGit(map[string]fakeGitEntry{
			"/repo|show sha1:feature.aihub.md": {err: errors.New("path not found")},
			"/repo|show sha1:feature.md":       {err: errors.New("path not found")},
		})
		if _, _, err := ResolveTemplate(git, "/repo", "sha1", "feature", "aihub"); err == nil {
			t.Fatal("ResolveTemplate: want an error when neither rung exists, got nil")
		}
	})
}

func TestScanSteps(t *testing.T) {
	t.Run("basic multi-step document order", func(t *testing.T) {
		tmpl := "intro prose, not a step\n" +
			"## Step: spec\n" +
			"Write the spec.\n" +
			"\n" +
			"## Step: plan\n" +
			"Write the plan.\n"
		steps, err := ScanSteps(tmpl)
		if err != nil {
			t.Fatalf("ScanSteps: %v", err)
		}
		if len(steps) != 2 {
			t.Fatalf("len(steps) = %d, want 2: %+v", len(steps), steps)
		}
		if steps[0].ID != "spec" || steps[0].Content != "Write the spec." {
			t.Errorf("steps[0] = %+v", steps[0])
		}
		if steps[1].ID != "plan" || steps[1].Content != "Write the plan." {
			t.Errorf("steps[1] = %+v", steps[1])
		}
	})

	t.Run("a step heading inside a fenced code block is not a section boundary", func(t *testing.T) {
		tmpl := "## Step: spec\n" +
			"Here is an example of a template:\n" +
			"```\n" +
			"## Step: fake\n" +
			"this is example text, not a real step\n" +
			"```\n" +
			"Back to real prose.\n" +
			"## Step: plan\n" +
			"Real plan content.\n"
		steps, err := ScanSteps(tmpl)
		if err != nil {
			t.Fatalf("ScanSteps: %v", err)
		}
		if len(steps) != 2 {
			t.Fatalf("len(steps) = %d, want 2 (the fenced '## Step: fake' must not count): %+v", len(steps), steps)
		}
		if steps[0].ID != "spec" {
			t.Errorf("steps[0].ID = %q, want %q", steps[0].ID, "spec")
		}
		if !strings.Contains(steps[0].Content, "## Step: fake") {
			t.Errorf("steps[0].Content should still contain the fenced example text verbatim: %q", steps[0].Content)
		}
		if steps[1].ID != "plan" || steps[1].Content != "Real plan content." {
			t.Errorf("steps[1] = %+v", steps[1])
		}
	})

	t.Run("no step headings at all is an error", func(t *testing.T) {
		if _, err := ScanSteps("just prose, no steps here\n"); err == nil {
			t.Fatal("ScanSteps: want an error when no step heading is found, got nil")
		}
	})

	t.Run("heading with trailing content on the same line does not match (strict)", func(t *testing.T) {
		tmpl := "## Step: spec extra words\nprose\n## Step: plan\nplan content"
		steps, err := ScanSteps(tmpl)
		if err != nil {
			t.Fatalf("ScanSteps: %v", err)
		}
		if len(steps) != 1 || steps[0].ID != "plan" {
			t.Errorf("steps = %+v, want exactly one step (\"plan\"); the malformed heading line must not match", steps)
		}
	})
}

func TestExpandIncludes(t *testing.T) {
	fetchOK := func(path string) (string, error) {
		return "FETCHED:" + path, nil
	}

	t.Run("include with a paired level line", func(t *testing.T) {
		content := "before\n@include: common/review/SKILL.md\nlevel: deep\nafter\n"
		got, err := ExpandIncludes(content, fetchOK)
		if err != nil {
			t.Fatalf("ExpandIncludes: %v", err)
		}
		want := "before\nReview level: deep\nFETCHED:common/review/SKILL.md\nafter\n"
		if got != want {
			t.Errorf("got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("include with no following level line", func(t *testing.T) {
		content := "@include: common/spec/SKILL.md\nno level here, just prose\n"
		got, err := ExpandIncludes(content, fetchOK)
		if err != nil {
			t.Fatalf("ExpandIncludes: %v", err)
		}
		want := "FETCHED:common/spec/SKILL.md\nno level here, just prose\n"
		if got != want {
			t.Errorf("got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("unrelated line after @include (not a level line) is preserved, not consumed", func(t *testing.T) {
		content := "@include: common/spec/SKILL.md\nthis is not a level line\nlevel: deep\n"
		got, err := ExpandIncludes(content, fetchOK)
		if err != nil {
			t.Fatalf("ExpandIncludes: %v", err)
		}
		// No "Review level:" line: the level pairing only looks at the line IMMEDIATELY
		// after @include:, which here is "this is not a level line" — so no level is
		// captured, and BOTH following lines survive as ordinary content (the trailing
		// "level: deep" is unpaired and therefore literal prose, not consumed).
		want := "FETCHED:common/spec/SKILL.md\nthis is not a level line\nlevel: deep\n"
		if got != want {
			t.Errorf("got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("multiple includes each carry their own level scope", func(t *testing.T) {
		content := "@include: a.md\nlevel: quick\n@include: b.md\nno level\n"
		got, err := ExpandIncludes(content, fetchOK)
		if err != nil {
			t.Fatalf("ExpandIncludes: %v", err)
		}
		want := "Review level: quick\nFETCHED:a.md\nFETCHED:b.md\nno level\n"
		if got != want {
			t.Errorf("got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("missing include: fetch's error is wrapped with the include path", func(t *testing.T) {
		sentinel := errors.New("path not found at sha")
		fetchFail := func(path string) (string, error) { return "", sentinel }
		_, err := ExpandIncludes("@include: missing.md\n", fetchFail)
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want it to be (or wrap) the sentinel fetch error", err)
		}
		if !strings.Contains(err.Error(), "missing.md") {
			t.Errorf("err = %v, want it to name the failing include path %q", err, "missing.md")
		}
	})
}
