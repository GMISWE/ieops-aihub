package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagValue(t *testing.T) {
	args := []string{"--step-id=code_review", "--supports-next-step", "--empty="}

	t.Run("present", func(t *testing.T) {
		v, ok := flagValue(args, "step-id")
		if !ok || v != "code_review" {
			t.Fatalf("flagValue = (%q, %v), want (%q, true)", v, ok, "code_review")
		}
	})
	t.Run("present but empty value is still ok=true", func(t *testing.T) {
		v, ok := flagValue(args, "empty")
		if !ok || v != "" {
			t.Fatalf("flagValue = (%q, %v), want (\"\", true)", v, ok)
		}
	})
	t.Run("absent", func(t *testing.T) {
		v, ok := flagValue(args, "missing")
		if ok || v != "" {
			t.Fatalf("flagValue = (%q, %v), want (\"\", false)", v, ok)
		}
	})
}

func TestHasFlag(t *testing.T) {
	args := []string{"--step-id=code_review", "--supports-next-step"}
	if !hasFlag(args, "supports-next-step") {
		t.Error("hasFlag(supports-next-step) = false, want true")
	}
	if hasFlag(args, "other-flag") {
		t.Error("hasFlag(other-flag) = true, want false")
	}
}

func TestRunEngineResolveRole(t *testing.T) {
	t.Run("missing --step-id is an error", func(t *testing.T) {
		if _, err := runEngineResolveRole(nil); err == nil {
			t.Fatal("runEngineResolveRole: want an error when --step-id is absent, got nil")
		}
	})

	t.Run("declared role known to the catalog wins", func(t *testing.T) {
		out, err := runEngineResolveRole([]string{"--step-id=some_step", "--declared-role=reviewer"})
		if err != nil {
			t.Fatalf("runEngineResolveRole: %v", err)
		}
		if out.Role != "reviewer" || out.Source != "declared" {
			t.Errorf("out = %+v, want role=reviewer source=declared", out)
		}
		if out.UnknownDeclaredRole != "" {
			t.Errorf("UnknownDeclaredRole = %q, want \"\" (the declared name was known to the catalog)", out.UnknownDeclaredRole)
		}
	})

	t.Run("unknown declared role falls through and is surfaced via unknown_declared_role", func(t *testing.T) {
		// W6: a typo'd --declared-role must still resolve non-fatally (unchanged) AND be
		// reported back to the caller instead of vanishing the moment source != "declared".
		out, err := runEngineResolveRole([]string{"--step-id=code_review", "--declared-role=not_a_real_role"})
		if err != nil {
			t.Fatalf("runEngineResolveRole: %v", err)
		}
		if out.Source == "declared" {
			t.Errorf("Source = %q, want it to fall through since the declared role is unknown", out.Source)
		}
		if out.Role != "reviewer" {
			t.Errorf("Role = %q, want reviewer (code_review is an exact IsReviewStep match)", out.Role)
		}
		if out.UnknownDeclaredRole != "not_a_real_role" {
			t.Errorf("UnknownDeclaredRole = %q, want %q", out.UnknownDeclaredRole, "not_a_real_role")
		}
	})

	t.Run("catalog-absent, review-shaped step id resolves to reviewer, never executor", func(t *testing.T) {
		// code_review is IsReviewStep-true by exact name (engine/role.go); pass a step id that
		// is unlikely to be a literal catalog entry to also exercise the heuristic tier, while
		// still being unambiguous about the expected outcome via the "_review" suffix rule.
		out, err := runEngineResolveRole([]string{"--step-id=some_future_thing_review"})
		if err != nil {
			t.Fatalf("runEngineResolveRole: %v", err)
		}
		if out.Role != "reviewer" || out.Source != "heuristic" {
			t.Errorf("out = %+v, want role=reviewer source=heuristic (never executor)", out)
		}
	})

	t.Run("catalog-absent, non-review-shaped step id resolves to executor via heuristic", func(t *testing.T) {
		out, err := runEngineResolveRole([]string{"--step-id=some_future_nonreview_thing"})
		if err != nil {
			t.Fatalf("runEngineResolveRole: %v", err)
		}
		if out.Role != "executor" || out.Source != "heuristic" {
			t.Errorf("out = %+v, want role=executor source=heuristic", out)
		}
	})
}

func TestRunEngineParseReview(t *testing.T) {
	t.Run("from --file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "review.txt")
		if err := os.WriteFile(path, []byte("prose\n<!-- REVIEW_RESULT: PASS -->\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		out, err := runEngineParseReview([]string{"--file=" + path})
		if err != nil {
			t.Fatalf("runEngineParseReview: %v", err)
		}
		if out.Result != "PASS" {
			t.Errorf("Result = %q, want PASS", out.Result)
		}
	})

	t.Run("--file pointing at a nonexistent path is an error", func(t *testing.T) {
		if _, err := runEngineParseReview([]string{"--file=/nonexistent/path/for/sure"}); err == nil {
			t.Fatal("runEngineParseReview: want an error for a missing file, got nil")
		}
	})

	t.Run("no --file reads stdin", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		orig := os.Stdin
		os.Stdin = r
		defer func() { os.Stdin = orig }()

		go func() {
			_, _ = w.WriteString("<!-- REVIEW_RESULT: FAIL -->\n")
			_ = w.Close()
		}()

		out, err := runEngineParseReview(nil)
		if err != nil {
			t.Fatalf("runEngineParseReview: %v", err)
		}
		if out.Result != "FAIL" {
			t.Errorf("Result = %q, want FAIL", out.Result)
		}
	})
}

func TestRunEngineBracketPlan(t *testing.T) {
	t.Run("missing required flags is an error", func(t *testing.T) {
		if _, err := runEngineBracketPlan([]string{"--step-id=spec"}); err == nil {
			t.Fatal("runEngineBracketPlan: want an error when --status/--step-attempt-id are absent, got nil")
		}
	})

	t.Run("invalid --status value is an error", func(t *testing.T) {
		args := []string{"--step-id=spec", "--status=bogus", "--step-attempt-id=sa_1"}
		if _, err := runEngineBracketPlan(args); err == nil {
			t.Fatal("runEngineBracketPlan: want an error for an unrecognized --status, got nil")
		}
	})

	t.Run("fused form marshals to the documented JSON shape", func(t *testing.T) {
		args := []string{
			"--step-id=spec", "--status=completed", "--step-attempt-id=sa_1",
			"--next-step-id=plan", "--next-step-attempt-id=sa_2", "--supports-next-step",
			"--artifact-summary=wrote the spec",
		}
		calls, err := runEngineBracketPlan(args)
		if err != nil {
			t.Fatalf("runEngineBracketPlan: %v", err)
		}
		if len(calls) != 1 {
			t.Fatalf("len(calls) = %d, want 1 (fused form)", len(calls))
		}
		b, merr := json.Marshal(calls)
		if merr != nil {
			t.Fatalf("json.Marshal: %v", merr)
		}
		got := string(b)
		for _, want := range []string{
			`"tool":"pf_update_step"`, `"step_id":"spec"`, `"status":"completed"`,
			`"step_attempt_id":"sa_1"`, `"next_step":"plan"`, `"next_step_attempt_id":"sa_2"`,
			`"artifact_summary":"wrote the spec"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("marshaled JSON %s does not contain %s", got, want)
			}
		}
	})

	t.Run("degraded form is two calls, second carries next-step-attempt-id as its own step_attempt_id", func(t *testing.T) {
		args := []string{
			"--step-id=spec", "--status=completed", "--step-attempt-id=sa_1",
			"--next-step-id=plan", "--next-step-attempt-id=sa_2",
		}
		calls, err := runEngineBracketPlan(args)
		if err != nil {
			t.Fatalf("runEngineBracketPlan: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("len(calls) = %d, want 2 (degraded form)", len(calls))
		}
		if calls[1].StepID != "plan" || calls[1].StepAttemptID != "sa_2" || calls[1].Status != "in_progress" {
			t.Errorf("calls[1] = %+v, want step_id=plan step_attempt_id=sa_2 status=in_progress", calls[1])
		}
	})

	t.Run("failed status ignores next-step flags entirely", func(t *testing.T) {
		args := []string{
			"--step-id=code_review", "--status=failed", "--step-attempt-id=sa_1",
			"--error-type=review_fail", "--next-step-id=commit_and_pr", "--supports-next-step",
		}
		calls, err := runEngineBracketPlan(args)
		if err != nil {
			t.Fatalf("runEngineBracketPlan: %v", err)
		}
		if len(calls) != 1 || calls[0].Status != "failed" || calls[0].NextStep != "" {
			t.Errorf("calls = %+v, want exactly one failed call with no next_step", calls)
		}
	})
}

// gitDir runs `git <args...>` in dir, failing the test on error — a small helper for setting up
// real repos/worktrees so runEngineStartup/runEngineCleanupWorktrees are exercised end to end
// against real git, not just fakes (those are already covered exhaustively at the internal/engine
// layer).
func gitDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func TestRunEngineStartup_RealGit(t *testing.T) {
	root := t.TempDir()
	scenarioDir := filepath.Join(root, ".repo", "acme__polyforge-coding")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	gitDir(t, scenarioDir, "init", "-q")
	gitDir(t, scenarioDir, "config", "user.email", "test@example.com")
	gitDir(t, scenarioDir, "config", "user.name", "Test")
	gitDir(t, scenarioDir, "remote", "add", "origin", "git@github.com:acme/polyforge-coding.git")

	template := "## Step: spec\n" +
		"Write the spec.\n" +
		"@include: common/spec/SKILL.md\n" +
		"level: quick\n" +
		"\n" +
		"## Step: plan\n" +
		"Write the plan.\n"
	if err := os.WriteFile(filepath.Join(scenarioDir, "feature.md"), []byte(template), 0o644); err != nil {
		t.Fatalf("WriteFile template: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(scenarioDir, "common", "spec"), 0o755); err != nil {
		t.Fatalf("MkdirAll include dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "common", "spec", "SKILL.md"), []byte("Included spec content."), 0o644); err != nil {
		t.Fatalf("WriteFile include: %v", err)
	}
	gitDir(t, scenarioDir, "add", "-A")
	gitDir(t, scenarioDir, "commit", "-q", "-m", "seed template")

	worktreeRoot := t.TempDir()
	out, err := runEngineStartup(context.Background(), []string{
		"--workspace-root=" + root,
		"--worktree-root=" + worktreeRoot,
		"--scenario-url=git@github.com:acme/polyforge-coding.git",
		"--wi-type=feature",
	})
	if err != nil {
		t.Fatalf("runEngineStartup: %v", err)
	}
	if out.LegacyFallback {
		t.Error("LegacyFallback = true, want false (owner-qualified path was present)")
	}
	if out.TemplateSource != "generic" {
		t.Errorf("TemplateSource = %q, want generic (no --project given)", out.TemplateSource)
	}
	if out.SHA == "" {
		t.Error("SHA is empty")
	}
	if len(out.Steps) != 2 || out.Steps[0].ID != "spec" || out.Steps[1].ID != "plan" {
		t.Fatalf("Steps = %+v, want [spec plan]", out.Steps)
	}
	if !strings.Contains(out.Steps[0].Expanded, "Included spec content.") {
		t.Errorf("Steps[0].Expanded = %q, want it to contain the fetched include", out.Steps[0].Expanded)
	}
	if !strings.Contains(out.Steps[0].Expanded, "Review level: quick") {
		t.Errorf("Steps[0].Expanded = %q, want a \"Review level: quick\" line", out.Steps[0].Expanded)
	}

	metaBytes, rerr := os.ReadFile(filepath.Join(worktreeRoot, ".pf_meta.json"))
	if rerr != nil {
		t.Fatalf("ReadFile .pf_meta.json: %v", rerr)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("json.Unmarshal .pf_meta.json: %v", err)
	}
	if meta["scenario_sha"] != out.SHA {
		t.Errorf(".pf_meta.json scenario_sha = %v, want %q", meta["scenario_sha"], out.SHA)
	}
	if meta["started_at"] == "" || meta["started_at"] == nil {
		t.Error(".pf_meta.json started_at is empty")
	}
}

// setUpCleanupWorktreesFixture seeds a real git main clone at <root>/.repo/aihub, adds one
// worktree at <root>/pf.aihub-654/aihub, and returns (root, wtPath, parent). Factored out of
// TestRunEngineCleanupWorktrees_RealGit so the W4 already-deleted-worktree regression test below
// can build the identical fixture and then additionally delete the worktree directory.
func setUpCleanupWorktreesFixture(t *testing.T) (root, wtPath, parent string) {
	t.Helper()
	root = t.TempDir()
	mainRepo := filepath.Join(root, ".repo", "aihub")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	gitDir(t, mainRepo, "init", "-q")
	gitDir(t, mainRepo, "config", "user.email", "test@example.com")
	gitDir(t, mainRepo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitDir(t, mainRepo, "add", "-A")
	gitDir(t, mainRepo, "commit", "-q", "-m", "seed")
	// Safe cleanup now requires every worktree branch to exist on origin at
	// the same commit. Seed a local bare remote so this success fixture proves
	// delivered-clean cleanup rather than relying on the old forceful delete.
	remote := filepath.Join(root, "remote.git")
	gitDir(t, root, "init", "--bare", "-q", remote)
	gitDir(t, mainRepo, "remote", "add", "origin", remote)
	gitDir(t, mainRepo, "push", "-q", "-u", "origin", "HEAD")

	parent = filepath.Join(root, "pf.aihub-654")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("MkdirAll parent: %v", err)
	}
	wtPath = filepath.Join(parent, "aihub")
	gitDir(t, mainRepo, "worktree", "add", wtPath)
	gitDir(t, wtPath, "push", "-q", "-u", "origin", "HEAD")
	return root, wtPath, parent
}

func TestRunEngineCleanupWorktrees_RealGit(t *testing.T) {
	root, wtPath, parent := setUpCleanupWorktreesFixture(t)

	worktreesJSON, merr := json.Marshal(map[string]string{"aihub": wtPath})
	if merr != nil {
		t.Fatalf("json.Marshal: %v", merr)
	}

	out, err := runEngineCleanupWorktrees(context.Background(), []string{
		"--workspace-root=" + root,
		"--worktrees=" + string(worktreesJSON),
	})
	if err != nil {
		t.Fatalf("runEngineCleanupWorktrees: %v", err)
	}
	if len(out.Removed) != 1 || out.Removed[0] != "aihub" {
		t.Errorf("Removed = %v, want [aihub]", out.Removed)
	}
	if len(out.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", out.Errors)
	}
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Errorf("worktree path %q still exists after cleanup", wtPath)
	}
	if _, statErr := os.Stat(parent); !os.IsNotExist(statErr) {
		t.Errorf("shared parent %q still exists after cleanup", parent)
	}
}

// TestRunEngineCleanupWorktrees_SurvivesAlreadyDeletedWorktree_RealGit is the CLI-level W4
// regression: it simulates an interrupted prior cleanup attempt (the worktree directory itself
// is gone, but git's own worktree administration data still references it) and asserts the
// retry still succeeds and still removes the shared parent. Measured directly against this box's
// git (2.43.0) before writing this test: `git -C <already-deleted-worktree> worktree remove
// --force <wt>` fails at exec's own chdir with "fatal: cannot change to ...: No such file or
// directory" (exit 128), while `git -C <main-clone-.repo/aihub> worktree remove --force <wt>`
// exits 0 and prunes the stale entry - exactly the W4 fix runEngineCleanupWorktrees now depends
// on via execGitRunner's dir=<main clone> convention.
func TestRunEngineCleanupWorktrees_SurvivesAlreadyDeletedWorktree_RealGit(t *testing.T) {
	root, wtPath, parent := setUpCleanupWorktreesFixture(t)

	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatalf("RemoveAll(wtPath) (simulating an interrupted prior cleanup): %v", err)
	}

	worktreesJSON, merr := json.Marshal(map[string]string{"aihub": wtPath})
	if merr != nil {
		t.Fatalf("json.Marshal: %v", merr)
	}

	out, err := runEngineCleanupWorktrees(context.Background(), []string{
		"--workspace-root=" + root,
		"--worktrees=" + string(worktreesJSON),
	})
	if err != nil {
		t.Fatalf("runEngineCleanupWorktrees: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("Errors = %v, want empty (a retry over an already-deleted worktree must succeed)", out.Errors)
	}
	if len(out.Removed) != 1 || out.Removed[0] != "aihub" {
		t.Errorf("Removed = %v, want [aihub]", out.Removed)
	}
	if _, statErr := os.Stat(parent); !os.IsNotExist(statErr) {
		t.Errorf("shared parent %q still exists after cleanup", parent)
	}
}

func TestRunEngineCleanupWorktrees_InvalidJSONIsAnError(t *testing.T) {
	if _, err := runEngineCleanupWorktrees(context.Background(), []string{
		"--workspace-root=/ws", "--worktrees=not json",
	}); err == nil {
		t.Fatal("runEngineCleanupWorktrees: want an error for invalid --worktrees JSON, got nil")
	}
}

func TestRunEngineCleanupWorktrees_MissingWorktreesFlagIsAnError(t *testing.T) {
	if _, err := runEngineCleanupWorktrees(context.Background(), []string{"--workspace-root=/ws"}); err == nil {
		t.Fatal("runEngineCleanupWorktrees: want an error when --worktrees is absent, got nil")
	}
}

func TestRunEngineCleanupWorktrees_MissingWorkspaceRootFlagIsAnError(t *testing.T) {
	worktreesJSON, merr := json.Marshal(map[string]string{"aihub": "/ws/pf.aihub-654/aihub"})
	if merr != nil {
		t.Fatalf("json.Marshal: %v", merr)
	}
	if _, err := runEngineCleanupWorktrees(context.Background(), []string{"--worktrees=" + string(worktreesJSON)}); err == nil {
		t.Fatal("runEngineCleanupWorktrees: want an error when --workspace-root is absent, got nil")
	}
}

// TestEngineCLI_FreshBinary builds a fresh `polyforge` binary (never resolves one via PATH,
// which on this box can be a stale auto-updated copy — mem_11hd6dBQ) and exercises `engine
// resolve-role` end to end as a real subprocess, pinning the top-level RunEngine/main.go wiring
// (subcommand dispatch, JSON-to-stdout, exit code) that the unit tests above call into directly
// and so don't exercise.
func TestEngineCLI_FreshBinary(t *testing.T) {
	repoRoot, err := findRepoRoot(t)
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	binPath := filepath.Join(t.TempDir(), "polyforge")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/polyforge")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "GOWORK=off")
	var buildOut bytes.Buffer
	build.Stdout = &buildOut
	build.Stderr = &buildOut
	if err := build.Run(); err != nil {
		t.Fatalf("go build ./cmd/polyforge: %v\n%s", err, buildOut.String())
	}

	cmd := exec.Command(binPath, "engine", "resolve-role", "--step-id=code_review")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("polyforge engine resolve-role: %v\nstderr: %s", err, stderr.String())
	}

	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal stdout %q: %v", out.String(), err)
	}
	if result["role"] != "reviewer" {
		t.Errorf("role = %v, want reviewer (code_review is an exact IsReviewStep match)", result["role"])
	}

	t.Run("unknown verb exits non-zero with usage on stderr", func(t *testing.T) {
		cmd := exec.Command(binPath, "engine", "not-a-verb")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			t.Fatal("polyforge engine not-a-verb: want a non-zero exit, got nil error")
		}
		if !strings.Contains(stderr.String(), "unknown verb") {
			t.Errorf("stderr = %q, want it to mention the unknown verb", stderr.String())
		}
	})
}

// findRepoRoot walks up from the current package directory to the module root (the directory
// containing go.mod), so the fresh-binary build above works regardless of the test binary's cwd.
func findRepoRoot(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
