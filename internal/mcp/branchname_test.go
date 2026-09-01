package mcp

import (
	"os/exec"
	"strings"
	"testing"
)

// TestClaimBranchULID8 locks the aihub#225 regression: the claim branch suffix
// must be derived from the canonical wi id (wi_<ulid>), never the raw slug.
func TestClaimBranchULID8(t *testing.T) {
	cases := []struct {
		name          string
		canonicalWIID string
		want          string
	}{
		{
			// Canonical id: strip "wi_", take the last 8 ulid chars.
			name:          "canonical id yields last 8 ulid chars",
			canonicalWIID: "wi_ABCDEFGHIJKLMNOP",
			want:          "IJKLMNOP",
		},
		{
			// aihub#225: a raw slug has no "wi_" prefix, so the last-8 slice
			// produced the garbled "ihub#225". The fix routes canonicalWIID
			// here; this case documents why the caller must never pass a slug.
			name:          "raw slug is mangled (documents the bug)",
			canonicalWIID: "aihub#225",
			want:          "ihub#225",
		},
		{
			name:          "short id returns empty (caller skips worktree)",
			canonicalWIID: "wi_1234",
			want:          "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claimBranchULID8(tc.canonicalWIID)
			if got != tc.want {
				t.Fatalf("claimBranchULID8(%q) = %q, want %q", tc.canonicalWIID, got, tc.want)
			}
		})
	}
}

// assertLegalGitRef validates name with git itself rather than with this file's
// idea of the ref rules. That distinction is the point: the rules are long
// (".." anywhere, a ".lock" suffix on any component, control characters, "~^:?*[",
// "@{", a trailing "/", ...), they are enforced by C code in refs.c, and a
// hand-rolled restatement of them here would be a second implementation that can
// disagree with the one that actually rejects the branch. `git check-ref-format
// --branch` needs no repository, so it is safe to call from any cwd.
func assertLegalGitRef(t *testing.T, name string) {
	t.Helper()
	if name == "" {
		return // "" is the documented "skip worktree creation" signal, not a ref.
	}
	out, err := exec.Command("git", "check-ref-format", "--branch", name).CombinedOutput()
	if err != nil {
		t.Errorf("git check-ref-format --branch %q rejected it: %v\n%s", name, err, strings.TrimSpace(string(out)))
	}
}

// TestNewClaimBranchNames_Derivation covers the readable-name derivation and,
// for every case, hands the result to git for validation (aihub#322).
//
// The adversarial half is not decoration. Goals in this workspace are frequently
// Chinese, and routinely carry "#", "/", ":", backticks, quotes and emoji — none
// of which are legal in a ref — so the derivation's real contract is not "produces
// a nice name" but "produces a LEGAL name, always". Two of the inputs below, one
// containing a dot-dot and one ending in .lock, are the exact two shapes git
// rejects, and they are here to prove the accept-list construction makes them
// unreachable rather than to prove some filter catches them.
func TestNewClaimBranchNames_Derivation(t *testing.T) {
	long := strings.Repeat("refactor the thing ", 27) // 513 chars
	if len(long) < 500 {
		t.Fatalf("the long-goal fixture is only %d chars; it must exceed 500", len(long))
	}

	cases := []struct {
		name    string
		project string
		seq     string
		goal    string
		ulid8   string
		want    string
	}{
		{
			name:    "ordinary goal becomes a readable kebab name",
			project: "ieops", seq: "961", goal: "patchelf smoke gate", ulid8: "ABCDEFGH",
			want: "polyforge/ieops-961-patchelf-smoke-gate",
		},
		{
			name:    "punctuation and case are normalised, runs collapse",
			project: "aihub", seq: "322", goal: "Readable   TASK/branch: names!!", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322-readable-task-branch-names",
		},
		{
			// The whole reason the slug itself cannot be the branch name.
			name:    "a goal quoting its own slug keeps the # out of the ref",
			project: "aihub", seq: "322", goal: "follow up on aihub#319", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322-follow-up-on-aihub-319",
		},
		{
			name:    "Chinese-only goal degrades to project-seq",
			project: "aihub", seq: "322", goal: "把分支名改成可读的格式", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322",
		},
		{
			// Mixed CJK/emoji/ASCII: the emoji and the Chinese drop out and the
			// ASCII words survive, which is the most useful possible outcome —
			// "gateway-redis-scan" still says what the work item is about.
			name:    "emoji and CJK drop out, the ASCII words survive",
			project: "ieops", seq: "7", goal: "🔴 修复 gateway 的 Redis SCAN 超时", ulid8: "ABCDEFGH",
			want: "polyforge/ieops-7-gateway-redis-scan",
		},
		{
			name:    "emoji-only goal degrades to project-seq",
			project: "ieops", seq: "7", goal: "🔴🟢⚠️", ulid8: "ABCDEFGH",
			want: "polyforge/ieops-7",
		},
		{
			name:    "punctuation-only goal degrades to project-seq",
			project: "aihub", seq: "322", goal: "!!! ??? ***", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322",
		},
		{
			name:    "empty goal degrades to project-seq",
			project: "aihub", seq: "322", goal: "", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322",
		},
		{
			name:    "dot-dot is stripped, not escaped (git rejects '..' in a ref)",
			project: "aihub", seq: "322", goal: "compare a..b in `git log`", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322-compare-a-b-in-git-log",
		},
		{
			name:    "a goal ending in .lock cannot produce a .lock ref suffix",
			project: "aihub", seq: "322", goal: "delete index.lock", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322-delete-index-lock",
		},
		{
			name:    "leading and trailing dashes are trimmed",
			project: "aihub", seq: "322", goal: "---bracketed goal---", ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322-bracketed-goal",
		},
		{
			name:    "a 500+ char goal is capped at a whole word",
			project: "aihub", seq: "322", goal: long, ulid8: "ABCDEFGH",
			want: "polyforge/aihub-322-refactor-the-thing-refactor-the-thing",
		},
		{
			// Preserves the pre-aihub#322 behaviour for the one input shape that
			// cannot produce a readable name at all.
			name:    "no project and no seq falls back to the legacy ulid8 name",
			project: "", seq: "", goal: "anything", ulid8: "ABCDEFGH",
			want: "polyforge/ABCDEFGH",
		},
		{
			name:    "a non-ASCII project name still yields a legal ref via seq alone",
			project: "映坊", seq: "42", goal: "做点什么", ulid8: "ABCDEFGH",
			want: "polyforge/42",
		},
		{
			name:    "nothing usable at all yields the skip-worktree empty string",
			project: "", seq: "", goal: "", ulid8: "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newClaimBranchNames(tc.project, tc.seq, tc.goal, tc.ulid8)
			if got.Branch != tc.want {
				t.Errorf("newClaimBranchNames(%q, %q, %q, %q).Branch = %q, want %q",
					tc.project, tc.seq, tc.goal, tc.ulid8, got.Branch, tc.want)
			}
			assertLegalGitRef(t, got.Branch)
			assertLegalGitRef(t, got.Legacy)
			assertLegalGitRef(t, got.Stem)
			if len(got.Branch) > claimBranchMaxTotal {
				t.Errorf("branch %q is %d chars, over the %d cap", got.Branch, len(got.Branch), claimBranchMaxTotal)
			}
			// Whatever the name degraded to, Stem must stay a prefix of it — the
			// resume shim's tier-3 lookup globs Stem+"-*" and would silently stop
			// finding this work item's branch if the two ever drifted apart.
			if got.Stem != "" && !strings.HasPrefix(got.Branch, got.Stem) {
				t.Errorf("Stem %q is not a prefix of Branch %q", got.Stem, got.Branch)
			}
		})
	}
}

// TestNewClaimBranchNames_LegacyAndStem locks the two names the resume shim
// depends on. Legacy must keep the exact pre-aihub#322 spelling — it is matched
// against branches already sitting in remotes (58 of them on ieops-ctlchain
// alone), so a cosmetic change here silently breaks every one of them.
func TestNewClaimBranchNames_LegacyAndStem(t *testing.T) {
	n := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	if n.Legacy != "polyforge/SosL0kmU" {
		t.Errorf("Legacy = %q, want the pre-aihub#322 name polyforge/SosL0kmU", n.Legacy)
	}
	if n.Stem != "polyforge/aihub-322" {
		t.Errorf("Stem = %q, want polyforge/aihub-322", n.Stem)
	}
	if empty := newClaimBranchNames("aihub", "322", "goal", ""); empty.Legacy != "" {
		t.Errorf("Legacy = %q with no ulid8, want empty so the resume shim skips that tier", empty.Legacy)
	}
}

// TestNewClaimBranchNames_ProjectDisambiguates is the collision guard.
//
// config.Config is map[project]Project and each Project has its own []Repo, with
// no constraint anywhere in config.Load forbidding one repo from appearing under
// two projects. <seq> is unique per project, not per repo, so in such a repo
// aihub#42 and ieops#42 would land on one branch — and the fresh-claim path
// treats "branch already exists" as "attach to it", so the collision would not
// error, it would put two work items on one branch. Including <project> is what
// makes that unrepresentable.
func TestNewClaimBranchNames_ProjectDisambiguates(t *testing.T) {
	a := newClaimBranchNames("aihub", "42", "", "AAAAAAAA")
	b := newClaimBranchNames("ieops", "42", "", "BBBBBBBB")
	if a.Branch == b.Branch {
		t.Fatalf("aihub#42 and ieops#42 both derive %q — a repo shared by two projects gets one branch for two work items", a.Branch)
	}
	// The degraded (goal-less) form is where a collision is most likely, so the
	// disambiguation has to survive it; the assertion above already uses it.
	if a.Branch != "polyforge/aihub-42" || b.Branch != "polyforge/ieops-42" {
		t.Fatalf("degraded names = %q / %q, want polyforge/aihub-42 / polyforge/ieops-42", a.Branch, b.Branch)
	}
}

// TestKebabToken_Truncation pins the cap behaviour separately from the branch
// assembly, including the case that has no word boundary to cut back to.
func TestKebabToken_Truncation(t *testing.T) {
	cases := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"one two three four", 12, "one-two"},           // cut back to a whole word
		{"internationalisation", 10, "internatio"},      // no boundary: hard cut
		{"one two three four", 0, "one-two-three-four"}, // 0 means no cap
		{"ab-----cd", 40, "ab-cd"},                      // runs collapse
		{"  spaced  ", 40, "spaced"},                    // ends trimmed
	}
	for _, tc := range cases {
		if got := kebabToken(tc.in, tc.maxLen); got != tc.want {
			t.Errorf("kebabToken(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
		}
	}
}
