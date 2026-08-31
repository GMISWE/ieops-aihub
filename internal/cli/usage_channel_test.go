package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Cross-channel gate for polyforge's rule text (aihub#294).
//
// polyforge delivers rules to a session through two channels with opposite properties:
//
//	.polyforge/usage.md   user-owned, no size cap, reaches the model via the workspace
//	                      CLAUDE.md @import — and is NEVER regenerated once it exists
//	                      (the os.Stat guard in writeUsageMd). Unfixable in the field.
//	fragments/*.md        plugin-versioned, injected every session by pf-session-start
//	                      under a hard 10,000-character budget. Fixed by the next release.
//
// Both channels carried the same rules, so the copy that was maintained was not the copy
// that could be corrected where it runs. The two drifted — IR1's worktree path format was
// wrong in one of them for three months — and nothing anywhere noticed, because no check
// had ever looked at both channels at once. These tests are that check.
//
// They are Go tests on purpose. The plugin's own suites under plugins/polyforge/tests/ are
// not wired into CI (aihub#293), while .github/workflows/ci.yml runs `go test -race ./...`
// on every push. A gate nobody runs is not a gate.

// usingPolyforgeDir locates the using-polyforge skill tree relative to internal/cli.
func usingPolyforgeDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "plugins", "polyforge", "skills", "using-polyforge")
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		// Fatal, never Skip. A gate that turns green when it cannot find the thing it
		// is meant to check is exactly the silent-success failure this work item exists
		// to remove. The tree is committed at a fixed path, so absence is a real fault.
		t.Fatalf("using-polyforge skill tree not found at %s: %v", dir, err)
	}
	return dir
}

var manifestIncludeRe = regexp.MustCompile(`(?m)^@include:\s*(\S+)\s*$`)

// deliveredSurfaces returns every body of text that actually lands in a session's
// context, keyed by a human-readable name:
//
//   - the .polyforge/usage.md that writeUsageMd generates (read back from the real
//     function, so this can never test a stale copy of the template), and
//   - every fragment the manifest marks resident with `@include:`.
//
// `@ondemand:` fragments are deliberately excluded: they ship as files an agent may Read,
// but they are not injected, so they are not part of what the model was told.
func deliveredSurfaces(t *testing.T) map[string]string {
	t.Helper()
	skillDir := usingPolyforgeDir(t)
	out := map[string]string{}

	usage := filepath.Join(t.TempDir(), "usage.md")
	if err := writeUsageMd(usage); err != nil {
		t.Fatalf("writeUsageMd: %v", err)
	}
	b, err := os.ReadFile(usage)
	if err != nil {
		t.Fatalf("read generated usage.md: %v", err)
	}
	out[".polyforge/usage.md (generated)"] = string(b)

	manifest, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	inc := manifestIncludeRe.FindAllStringSubmatch(string(manifest), -1)
	if len(inc) == 0 {
		t.Fatal("SKILL.md declares no resident fragments — the manifest grammar changed " +
			"and this test is now blind; update manifestIncludeRe")
	}
	for _, m := range inc {
		body, rerr := os.ReadFile(filepath.Join(skillDir, m[1]))
		if rerr != nil {
			t.Fatalf("read resident fragment %s: %v", m[1], rerr)
		}
		out[m[1]] = string(body)
	}
	return out
}

// headingCount counts lines that are a markdown heading naming title.
// Heading lines only: prose references such as bootstrap.md's "establishes Iron Rules
// (IR1-IR3)" point AT the rule, they do not restate it, and counting them would make
// this test fire on a cross-reference.
func headingCount(body, title string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "#") && strings.Contains(s, title) {
			n++
		}
	}
	return n
}

// TestNoRuleSectionIsDeliveredTwice is the property the two-copy bug violated: across
// everything injected into one session, each rule section may be stated at most once,
// and the generated usage.md may state none of them at all.
func TestNoRuleSectionIsDeliveredTwice(t *testing.T) {
	surfaces := deliveredSurfaces(t)

	for _, heading := range skillOwnedUsageSections {
		title := strings.TrimPrefix(heading, "## ")
		var carriers []string
		total := 0
		for name, body := range surfaces {
			if n := headingCount(body, title); n > 0 {
				carriers = append(carriers, fmt.Sprintf("%s (%d)", name, n))
				total += n
			}
		}
		if total > 1 {
			t.Errorf("%q is delivered %d times in one session, by %s.\n"+
				"Two copies of a rule cannot be kept in agreement: only the fragment copy "+
				"can be corrected in the field, because writeUsageMd never overwrites an "+
				"existing .polyforge/usage.md. Keep the fragment; delete the other.",
				title, total, strings.Join(carriers, ", "))
		}
	}

	usage := surfaces[".polyforge/usage.md (generated)"]
	for _, heading := range skillOwnedUsageSections {
		if strings.Contains(usage, heading) {
			t.Errorf("the generated .polyforge/usage.md still emits %q. That file is written "+
				"once and never regenerated, so a rule placed there can never be fixed; it "+
				"belongs in the plugin-versioned using-polyforge fragments.", heading)
		}
	}
}

// TestRuleSectionsMovedNotDeleted is the other half of the ratchet. Dropping a section
// from the usage.md template is only correct if the content survives somewhere versioned;
// without this, "stop duplicating it" and "lose it" pass the same tests.
func TestRuleSectionsMovedNotDeleted(t *testing.T) {
	skillDir := usingPolyforgeDir(t)
	fragDir := filepath.Join(skillDir, "fragments")
	entries, err := os.ReadDir(fragDir)
	if err != nil {
		t.Fatalf("read fragments dir: %v", err)
	}

	for _, heading := range skillOwnedUsageSections {
		title := strings.TrimPrefix(heading, "## ")
		var home []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			b, rerr := os.ReadFile(filepath.Join(fragDir, e.Name()))
			if rerr != nil {
				t.Fatalf("read %s: %v", e.Name(), rerr)
			}
			if headingCount(string(b), title) > 0 {
				home = append(home, e.Name())
			}
		}
		if len(home) == 0 {
			t.Errorf("%q is no longer emitted into .polyforge/usage.md and no fragment under "+
				"%s carries it either — it was deleted, not moved.", title, fragDir)
		}
	}
}

// TestWorktreePathSpellingIsUniform pins the concrete divergence this work item was filed
// for. IR1 illustrated the worktree layout as pf.<shortid>/<repo>/ in one copy and
// pf.<project>-<seq>/<repo>/ in the other; doctor.go documents the first as the legacy
// layout and the live producer emits the second.
//
// The producer is the oracle: internal/coding/scenario.go builds the path from project and
// seq. Asserting on its format string means that if the layout ever changes, this test
// fails and points at the prose that has to change with it — rather than two hand-written
// doc strings quietly agreeing with each other while both are wrong.
func TestWorktreePathSpellingIsUniform(t *testing.T) {
	const canonical = "pf.<project>-<seq>"
	const legacy = "pf.<shortid>"

	producer, err := os.ReadFile(filepath.Join("..", "coding", "scenario.go"))
	if err != nil {
		t.Fatalf("read worktree path producer: %v", err)
	}
	if !strings.Contains(string(producer), `fmt.Sprintf("pf.%s-%s", sf.Project, seq)`) {
		t.Fatalf("internal/coding/scenario.go no longer builds the worktree path from " +
			"project+seq. The documented spelling " + canonical + " is now unverified — " +
			"re-derive it from the new producer and update every delivered surface.")
	}

	documented := 0
	for name, body := range deliveredSurfaces(t) {
		if strings.Contains(body, legacy) {
			t.Errorf("%s spells the worktree layout %s, but the producer emits %s. "+
				"doctor.go records %s as the legacy layout; a session told the legacy form "+
				"will look for a worktree that is not there.", name, legacy, canonical, legacy)
		}
		documented += strings.Count(body, canonical)
	}
	if documented == 0 {
		t.Errorf("no delivered surface documents the worktree layout %s at all — this test "+
			"would pass vacuously, so the absence is itself the failure.", canonical)
	}
}

const legacyUsageMd = `# polyforge v1 workspace guide

> **State authority = aihub PostgreSQL** at the URL in ~/.polyforge/config.toml.

## Iron Rules

**IR1 — Work-item-gated writes**
Every git commit/push/PR must happen inside a claimed wi worktree.

---

## Daily workflow

/pf-work --goal "..."

---

## Wi 创建规则

All wi creation MUST go through /pf-work.

---

> Generated by polyforge init. Edit this file to add workspace-specific notes.

---

## NL Routing

| intent | operation |
|---|---|
| pause | /pf-stop --pause |

---

## Memory Type Reference

| content | Type |
|---|---|
| domain facts | fact.<subtopic> |
`

// TestScanUsageSections pins the detector against the input classes an independent review
// found. The first cut of this code also REMOVED these sections, inferring each one's
// extent from markdown structure; on five of the shapes below that destroyed content the
// user owned, three of them leaving the file structurally broken. Removal is gone. What
// is left must still get the DETECTION right, in both directions:
//
//   - a lookalike heading (fenced, indented, commented) must not be counted, or every
//     workspace that documents the migration gets a permanent false warning;
//   - a real heading must be counted even after a fence that only looked unterminated,
//     which is where the shared fence bool silently reported a clean file.
//
// And a document the scan cannot finish must never come back clean.
func TestScanUsageSections(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		want       []string
		wellFormed bool
	}{
		{"a real heading counts", "# g\n\n## Iron Rules\n\nIR1\n", []string{"Iron Rules"}, true},
		{"fenced heading is an example, not a heading",
			"# g\n\n```md\n## Iron Rules\n```\n\nafter\n", nil, true},
		{"~~~ inside a ``` block must not close it",
			"# g\n\n```md\n~~~\n## Iron Rules\n```\n\nafter\n", nil, true},
		{"``` inside a ```` block must not close it",
			"# g\n\n````md\n```\n## Iron Rules\n```\n````\n\nafter\n", nil, true},
		{"a real heading AFTER a closed fence is still found",
			"# g\n\n```md\n~~~\n```\n\n## NL Routing\n\nt\n", []string{"NL Routing"}, true},
		{"an indented code block is not a heading",
			"# g\n\ntext\n\n    ## Iron Rules\n\nmore\n", nil, true},
		{"a heading inside an HTML comment is not a heading",
			"# g\n\n<!--\n## Iron Rules\n-->\n\nafter\n", nil, true},
		{"an unterminated fence is not a clean read",
			"# g\n\n```md\n## Iron Rules\n", nil, false},
		{"an unterminated HTML comment is not a clean read",
			"# g\n\n<!--\n## Iron Rules\n", nil, false},
		{"reported in template order, not file order",
			"## Memory Type Reference\n\nx\n\n## Iron Rules\n\ny\n",
			[]string{"Iron Rules", "Memory Type Reference"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, wf := scanUsageSections(tc.in)
			if wf != tc.wellFormed {
				t.Errorf("wellFormed = %v, want %v", wf, tc.wellFormed)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("found %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOnDemandRuleSectionsAreIndexed closes the demotion gap. "Moved, not deleted" is
// satisfied by a fragment that is never injected, so a section can silently go from
// always-in-context (usage.md rode the CLAUDE.md @import unconditionally) to
// read-it-if-you-think-to. That is a legitimate trade under the size budget, but only
// if something tells an agent the file exists — which is on-demand-index.md's whole job.
func TestOnDemandRuleSectionsAreIndexed(t *testing.T) {
	skillDir := usingPolyforgeDir(t)
	index, err := os.ReadFile(filepath.Join(skillDir, "fragments", "on-demand-index.md"))
	if err != nil {
		t.Fatalf("read on-demand-index.md: %v", err)
	}
	delivered := deliveredSurfaces(t)

	for _, heading := range skillOwnedUsageSections {
		title := strings.TrimPrefix(heading, "## ")
		inContext := false
		for _, body := range delivered {
			if headingCount(body, title) > 0 {
				inContext = true
				break
			}
		}
		if inContext {
			continue
		}
		// Not delivered: find which on-demand fragment owns it, and require the index
		// to name that file.
		entries, rerr := os.ReadDir(filepath.Join(skillDir, "fragments"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		var owner string
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join(skillDir, "fragments", e.Name()))
			if headingCount(string(b), title) > 0 {
				owner = e.Name()
				break
			}
		}
		if owner == "" {
			t.Errorf("%q is delivered nowhere and lives in no fragment", title)
			continue
		}
		if !strings.Contains(string(index), owner) {
			t.Errorf("%q was moved out of the always-delivered channel into %s, which is "+
				"on-demand — but on-demand-index.md never names %s, so no session is told "+
				"it exists. That is a demotion to unreachable, not a move.",
				title, owner, owner)
		}
	}
}

func TestCheckUsageMd(t *testing.T) {
	newWorkspace := func(t *testing.T, body string) string {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".polyforge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".polyforge", "usage.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}

	t.Run("legacy file warns and names the sections", func(t *testing.T) {
		root := newWorkspace(t, legacyUsageMd)
		got := checkUsageMd(root)
		if got.Status != "warning" {
			t.Fatalf("status = %q, want warning (msg: %s)", got.Status, got.Message)
		}
		for _, heading := range skillOwnedUsageSections {
			title := strings.TrimPrefix(heading, "## ")
			if !strings.Contains(got.Message, title) {
				t.Errorf("message does not name %q: %s", title, got.Message)
			}
		}
		if got.FixCmd == "" {
			t.Error("a warning with no remedy leaves the operator nothing to do")
		}
	})

	t.Run("the check never writes, whatever the workspace looks like", func(t *testing.T) {
		// The removal path was deleted after review found six input classes on which it
		// destroyed user content. This asserts the deletion stayed deleted: no rewrite,
		// no .bak, no new file, on the exact input that used to be rewritten.
		root := newWorkspace(t, legacyUsageMd)
		p := filepath.Join(root, ".polyforge", "usage.md")
		for i := 0; i < 2; i++ {
			checkUsageMd(root)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != legacyUsageMd {
			t.Errorf("checkUsageMd modified the user's file:\n%s", b)
		}
		entries, err := os.ReadDir(filepath.Join(root, ".polyforge"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("checkUsageMd created files in .polyforge/: %v", names)
		}
	})

	t.Run("a file the generator writes today is clean", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".polyforge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeUsageMd(filepath.Join(root, ".polyforge", "usage.md")); err != nil {
			t.Fatal(err)
		}
		if got := checkUsageMd(root); got.Status != "ok" {
			t.Fatalf("freshly generated usage.md reported %q: %s", got.Status, got.Message)
		}
	})

	t.Run("a real section behind a lookalike fence is still reported", func(t *testing.T) {
		// The shared fence bool let a ~~~ inside a ``` block flip the scanner back out of
		// the fence and then straight into "found nothing" — a green that meant nothing
		// was examined, in a check whose whole subject is silent failure.
		root := newWorkspace(t, "# g\n\n```md\n~~~\n```\n\n## Iron Rules\n\nIR1\n")
		got := checkUsageMd(root)
		if got.Status != "warning" || !strings.Contains(got.Message, "Iron Rules") {
			t.Fatalf("silently reported clean: status=%q msg=%s", got.Status, got.Message)
		}
	})

	t.Run("an unfinishable document is not reported clean", func(t *testing.T) {
		for _, body := range []string{
			"# g\n\n```md\n## Iron Rules\n",
			"# g\n\n<!--\n## Iron Rules\n",
		} {
			got := checkUsageMd(newWorkspace(t, body))
			if got.Status == "ok" {
				t.Errorf(`"stopped looking" reported as "found nothing": %s`, got.Message)
			}
		}
	})

	t.Run("an unreadable existing path does not report green", func(t *testing.T) {
		// A directory rather than a chmod: os.ReadFile fails with EISDIR for every user
		// including root, so this case runs everywhere instead of skipping — and a
		// skip that counts as a pass is the exact failure mode this file exists to end.
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".polyforge", "usage.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := checkUsageMd(root); got.Status == "ok" {
			t.Errorf(`"could not look" reported as "looked and found nothing": %s`, got.Message)
		}
	})

	t.Run("absent usage.md is not a finding", func(t *testing.T) {
		if got := checkUsageMd(t.TempDir()); got.Status != "ok" {
			t.Fatalf("status = %q, want ok", got.Status)
		}
	})
}

// TestEnsureClaudeMdRefOnAFreshWorkspace covers the order RunInit actually uses:
// upsertManagedBlock runs first and CREATES the workspace CLAUDE.md when it is absent, so
// by the time ensureClaudeMdRef runs the file exists and holds only a managed block. Its
// "neither ref present" path then returned nil without writing, and the
// @.polyforge/usage.md import — the only thing that puts usage.md in front of a model —
// was never added on a fresh workspace at all.
func TestEnsureClaudeMdRefOnAFreshWorkspace(t *testing.T) {
	const ref = "@.polyforge/usage.md"

	t.Run("file created by upsertManagedBlock still gets the import", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "CLAUDE.md")
		desc := "test project"
		blocks := []projectBlock{{Name: "demo", Description: &desc}}

		if err := upsertManagedBlock(path, blocks); err != nil {
			t.Fatalf("upsertManagedBlock: %v", err)
		}
		if err := ensureClaudeMdRef(path); err != nil {
			t.Fatalf("ensureClaudeMdRef: %v", err)
		}

		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), ref) {
			t.Fatalf("CLAUDE.md has no %s import, so .polyforge/usage.md reaches no session:\n%s", ref, b)
		}
		// The managed block must survive, and must still be splice-able next init.
		if _, ok := managedBlockOf(string(b)); !ok {
			t.Fatalf("the managed block did not survive adding the import:\n%s", b)
		}

		// Idempotent: a second init must not stack a second import line.
		if err := ensureClaudeMdRef(path); err != nil {
			t.Fatalf("second ensureClaudeMdRef: %v", err)
		}
		b2, _ := os.ReadFile(path)
		if n := strings.Count(string(b2), ref); n != 1 {
			t.Fatalf("import line appears %d times after a second init, want 1", n)
		}
	})

	t.Run("an existing ref is left alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "CLAUDE.md")
		body := ref + "\n\n# my notes\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureClaudeMdRef(path); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path)
		if string(b) != body {
			t.Fatalf("rewrote a file that was already correct:\n%s", b)
		}
	})

	t.Run("the pre-v1 ref is migrated, not duplicated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "CLAUDE.md")
		if err := os.WriteFile(path, []byte("@.claude/polyforge.md\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureClaudeMdRef(path); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path)
		if n := strings.Count(string(b), ref); n != 1 {
			t.Fatalf("want exactly one %s after migration, got %d:\n%s", ref, n, b)
		}
	})
}
