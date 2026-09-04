package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Dead-pointer gate (aihub#358).
//
// WHAT WENT WRONG
// ---------------
// Two shipped instructions named a target that does not exist. Both read as perfectly ordinary
// prose, which is why neither was noticed:
//
//	A. fragments/bootstrap.md told every session to scan the HOME-scoped `.polyforge/state`
//	   glob. Claims are written to <workspace>/.polyforge/state/ — config.StateDir() has been
//	   workspace-scoped since 2026-05-22 (internal/config/state.go), a month before that
//	   fragment was written, so it was never right. $HOME holds no claim state, so the scan
//	   could not find anything, and it did not fail quietly: it answered "no active work item"
//	   while one was running. A confident false negative is worse than silence, because nobody
//	   re-checks an answer. The same wrong path had a second home in tests/scenarios/README.md.
//
//	B. pf-spec/SKILL.md and pf-plan/SKILL.md pointed at a skill author's work by a misspelled
//	   name and at two skills that are not in that plugin's catalogue (`to-prd`, `to-issues`;
//	   the real names are `to-spec` and `to-tickets`).
//
// WHY A REPO-WIDE SCAN AND NOT A PER-FILE ASSERTION
// The payload suite gates defect A where it does the most damage — the resident session-start
// payload — but that covers reach, not the class. Both defects existed in more than one file
// and were found by grepping for the literal, so the gate greps for the literal too. A check
// scoped to the one file that was reported would have gone green with the second copy intact.
//
// Every "must not appear" assertion here is paired with (a) a positive anchor that fails if the
// text it guards was renamed or emptied, and (b) a self-check that the walk actually visited a
// realistic number of files — a scan that visits nothing reports no violations perfectly.

// homeScopedStateDir is the literal that must never appear again. It is ASSEMBLED from two
// pieces rather than written out, so this file — which the scan below also walks — does not
// itself contain the banned string. That is deliberate and it is the cheap alternative to an
// exemption list: a self-exempting scan is one edit away from being a scan of nothing.
//
// Deliberately NOT the whole "~/.polyforge" prefix: the machine-level config really does live
// at ~/.polyforge/config.toml, and banning that would make this gate wrong rather than strict.
const homeScopedStateDir = "~" + "/.polyforge/state"

// workspaceScopedStateDir is the correct form, used as the positive anchor.
const workspaceScopedStateDir = "/.polyforge/state"

// scannedExtensions are the shipped-text surfaces. Go/py/sh/js are included because a comment
// in a test file is exactly where the third copy of defect A was found living.
var scannedExtensions = map[string]bool{
	".md": true, ".go": true, ".sh": true, ".py": true, ".js": true, ".cjs": true,
	".yml": true, ".yaml": true, ".json": true, ".toml": true,
}

// walkRepoText calls fn(relPath, body) for every text file in the repo, skipping .git and
// vendor-ish trees. It returns the number of files visited so callers can prove the walk ran.
func walkRepoText(t *testing.T, root string, fn func(rel, body string)) int {
	t.Helper()
	visited := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", ".codegraph":
				return filepath.SkipDir
			}
			return nil
		}
		if !scannedExtensions[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if info.Size() > 4<<20 {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		visited++
		fn(filepath.ToSlash(rel), string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v. The scan did not complete, so 'no violations found' below "+
			"would mean 'not looked for'.", root, err)
	}
	return visited
}

// minScannedFiles is a floor, not a count: it exists so a walk that silently stops (a bad root,
// a SkipDir that swallowed the tree, an extension map that stopped matching) cannot report a
// clean bill of health. The repo held well over a thousand matching files when this was written.
const minScannedFiles = 300

func TestNoHomeScopedStateDirInShippedText(t *testing.T) {
	root := repoRootDir(t)

	var hits []string
	var anchors int
	visited := walkRepoText(t, root, func(rel, body string) {
		if strings.Contains(body, homeScopedStateDir) {
			hits = append(hits, rel)
		}
		// Count only GENUINE workspace-scoped mentions. workspaceScopedStateDir is a substring
		// of homeScopedStateDir, so a naive Contains would also count the very text this test
		// bans — the floor below could then never reach zero, and an anti-vacuity floor that
		// cannot fail is not one. Delete the banned form first, then look at what is left.
		if strings.Contains(strings.ReplaceAll(body, homeScopedStateDir, ""), workspaceScopedStateDir) {
			anchors++
		}
	})

	// ── Anti-vacuity, both halves, before the ban is read as meaningful ──
	if visited < minScannedFiles {
		t.Fatalf("the walk visited only %d files (floor %d). A scan that reaches nothing finds "+
			"no violations, so the assertion below would pass for the wrong reason.",
			visited, minScannedFiles)
	}
	if anchors == 0 {
		t.Fatalf("not one file in %d mentions %q. The state directory has been renamed or the "+
			"scan is looking at the wrong tree; either way the ban below is guarding a string "+
			"nothing uses.", visited, workspaceScopedStateDir)
	}
	// The scanner must be able to SEE the banned literal. Without this the ban passes on a
	// matcher that is simply broken.
	if !strings.Contains("read `"+homeScopedStateDir+"/*.json` for active state files", homeScopedStateDir) {
		t.Fatal("the banned literal does not match its own fixture — the constant is malformed")
	}

	if len(hits) > 0 {
		sort.Strings(hits)
		t.Errorf("%d file(s) still name %q: %v\n"+
			"Claim state is written to <workspace>/.polyforge/state/ (config.StateDir(), "+
			"internal/config/state.go) and $HOME carries none of it. An instruction to scan the "+
			"home path cannot find a running claim and reports that none exists — the aihub#358 "+
			"false negative. Use the workspace-scoped path.", len(hits), homeScopedStateDir, hits)
	}

	// The specific surface the defect shipped on, asserted by name. The repo-wide ban above is
	// satisfied by a bootstrap.md that no longer mentions a state directory at all, which would
	// remove the scan rather than fix it.
	boot := filepath.Join(root, "plugins", "polyforge", "skills", "using-polyforge", "fragments", "bootstrap.md")
	b, err := os.ReadFile(boot)
	if err != nil {
		t.Fatalf("reading bootstrap.md: %v — this gate asserts what it says", err)
	}
	if !strings.Contains(string(b), "<workspace>"+workspaceScopedStateDir) {
		t.Errorf("bootstrap.md no longer names <workspace>%s. The session-start scan must still "+
			"point somewhere, and the workspace state dir is the only place claims are written.",
			workspaceScopedStateDir)
	}
}

// ── Defect C: pointers at another plugin's skills ────────────────────────────────────────────

// externalSkillRefRe matches the shape these SKILL.md example lists use:
//
//   - mattpocock's `grill-with-docs` + `to-spec`
//
// The author segment is captured so a misspelling is a failure rather than a silent miss — the
// original defect was exactly a misspelling, and a regex keyed on the CORRECT spelling would
// have walked straight past it.
var externalSkillRefRe = regexp.MustCompile("(?m)^-[ \t]+([A-Za-z]+)'s[ \t]+(`[^`]+`(?:[ \t]*\\+[ \t]*`[^`]+`)*)")

var backtickedRe = regexp.MustCompile("`([^`]+)`")

// knownReferenceAuthors is every author token these example lists are allowed to use, mapped to
// the installed plugin whose catalogue publishes their skills — or "" for a reference with no
// catalogue this repo can enumerate (OpenSpec ships as a slash command, not a plugin).
//
// The gate fails on an author NOT in this map, and that is the point: `mattpocok` is not a
// spelling variant to be tolerated, it is a name that resolves to nobody. A regex keyed on the
// CORRECT spelling would have walked straight past the very defect this exists to catch.
// This half needs no plugin installed, so it is the half that runs in CI.
var knownReferenceAuthors = map[string]string{
	"mattpocock": "mattpocock-skills",
	"OpenSpec":   "",
}

// retiredSkillNames is a BAN list, not an allow list, and the distinction is the whole design.
//
// An allow list of "names we may cite" rots the moment somebody adds a fourth legitimate example:
// the gate then fails on a correct pointer, and a gate that cries wolf gets deleted. A ban list
// cannot rot that way — these two names were cited here, do not exist in the upstream catalogue,
// and nothing will ever make them exist. It is a pure regression ratchet, and it is the only
// name-level check that can run in aihub CI, which installs no plugins.
//
// The general question ("does every cited name exist?") is answered by the LIVE catalogue below
// wherever one is installed. That check never goes stale because it reads the real whitelist at
// run time rather than a copy of it.
var retiredSkillNames = map[string]string{
	"to-prd":    "to-spec",
	"to-issues": "to-tickets",
}

func TestExternalSkillReferencesArePinnedAndSpelledRight(t *testing.T) {
	root := repoRootDir(t)

	type ref struct{ file, author, skill string }
	var refs []ref
	visited := walkRepoText(t, root, func(rel, body string) {
		if !strings.HasPrefix(rel, "plugins/") || !strings.HasSuffix(rel, ".md") {
			return
		}
		for _, m := range externalSkillRefRe.FindAllStringSubmatch(body, -1) {
			for _, s := range backtickedRe.FindAllStringSubmatch(m[2], -1) {
				refs = append(refs, ref{rel, m[1], s[1]})
			}
		}
	})
	if visited < minScannedFiles {
		t.Fatalf("walk visited only %d files (floor %d)", visited, minScannedFiles)
	}

	// Anti-vacuity: the extractor must actually match the shape it claims to. If a rewording
	// makes it match nothing, every assertion below passes on an empty list.
	fixture := "- mattpocock's `grill-with-docs` + `to-spec`\n- superpowers:brainstorming\n"
	fm := externalSkillRefRe.FindStringSubmatch(fixture)
	if fm == nil || fm[1] != "mattpocock" {
		t.Fatalf("externalSkillRefRe no longer matches the reference shape it exists to find "+
			"(%q). Every check below would then pass vacuously.", fixture)
	}
	if len(refs) == 0 {
		t.Fatalf("no external skill references found under plugins/**.md, but the extractor " +
			"works on its fixture. Either the reference lines were reworded (update the regex) " +
			"or removed (then remove this gate) — passing silently is not an option.")
	}
	t.Logf("external skill references found: %d", len(refs))

	// Resolve each author's live catalogue ONCE. Where one exists it is the oracle for every
	// name that author publishes; where none does, only the author-spelling and retired-name
	// checks can run and the test says so out loud rather than reporting a quiet pass.
	catalogs := map[string]map[string]bool{}
	for author, plugin := range knownReferenceAuthors {
		if plugin == "" {
			continue
		}
		catalog, path := installedSkillCatalog(plugin)
		if catalog == nil {
			t.Logf("ORACLE UNAVAILABLE for %q: no non-orphaned %s install under "+
				"~/.claude/plugins/cache/*/. Every cited name for this author is therefore "+
				"checked ONLY against retiredSkillNames, which catches a regression to a known "+
				"dead name but cannot tell a new typo from a new skill. This is the expected "+
				"state in aihub CI, which installs no plugins; on a machine that has the plugin "+
				"the stronger check runs automatically.", author, plugin)
			continue
		}
		catalogs[author] = catalog
		t.Logf("oracle for %q: %s (%d skills)", author, path, len(catalog))
	}

	for _, r := range refs {
		if _, known := knownReferenceAuthors[r.author]; !known {
			t.Errorf("%s points at %q's `%s`, but %q is not a known reference author. The "+
				"aihub#358 defect was a MISSPELLED author name that resolved to nobody; if this "+
				"spelling is right add it to knownReferenceAuthors, and if it is wrong fix the "+
				"text.", r.file, r.author, r.skill, r.author)
			continue
		}
		// (a) The regression ratchet. Runs everywhere, never rots, ban not allow.
		if replacement, retired := retiredSkillNames[r.skill]; retired {
			t.Errorf("%s cites `%s`, which does not exist in %s's catalogue — it was removed by "+
				"aihub#358 and the real name is `%s`. A reader following this pointer finds "+
				"nothing.", r.file, r.skill, r.author, replacement)
			continue
		}
		// (b) The class check. Only runs where the live whitelist is readable, and is read at
		// run time from the installed tree — never from a copy, because the installed tree can
		// be re-pointed to a different marketplace without the version string changing.
		if catalog, ok := catalogs[r.author]; ok && !catalog[r.skill] {
			t.Errorf("%s cites %s's `%s`, which the installed catalogue does not publish. Check "+
				"the name against that plugin's own plugin.json; if the skill was renamed "+
				"upstream, fix the pointer here rather than widening this gate.",
				r.file, r.author, r.skill)
		}
	}

	// Anti-vacuity for (b): where an oracle exists it must be able to REJECT something, or
	// "every cited name is in the catalogue" would be satisfied by a catalogue containing
	// everything (e.g. a parse that produced a match-all map).
	for author, catalog := range catalogs {
		if catalog["definitely-not-a-real-skill-aihub358"] {
			t.Errorf("the %q catalogue claims to publish a skill that cannot exist — it is not a "+
				"whitelist, so check (b) accepts any name and proves nothing", author)
		}
		for dead := range retiredSkillNames {
			if catalog[dead] {
				t.Errorf("retiredSkillNames lists %q as dead, but %q's live catalogue publishes "+
					"it. The ban list is wrong and is failing a valid pointer.", dead, author)
			}
		}
	}
}

// installedSkillCatalog reads a plugin's declared skill names from the tree that is actually
// installed. Returns nil when none is, which the caller reports loudly rather than treating as
// agreement.
//
// It searches EVERY marketplace directory under ~/.claude/plugins/cache/*/ rather than a fixed
// one, and that is not defensive coding — it is a measured requirement. On 2026-09-04 this
// workspace's mattpocock-skills install was re-pointed from the `claude-plugins-official`
// marketplace to `mattpocock`; the old copy was marked .orphaned_at and a fresh copy appeared
// under the new directory. BOTH self-report version "1.2.3", so the version string cannot tell
// them apart. A lookup hard-coded to the old marketplace would have found only an orphaned tree,
// concluded "not installed", and downgraded itself to the weaker check in silence — the exact
// shape of failure this whole file exists to gate.
//
// Orphaned copies are skipped for the same reason: a superseded tree is not what a reader loads,
// so answering from it would be answering about the wrong catalogue.
func installedSkillCatalog(pluginName string) (map[string]bool, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, ""
	}
	markets, err := os.ReadDir(filepath.Join(home, ".claude", "plugins", "cache"))
	if err != nil {
		return nil, ""
	}
	for _, m := range markets {
		if !m.IsDir() {
			continue
		}
		base := filepath.Join(home, ".claude", "plugins", "cache", m.Name(), pluginName)
		versions, verr := os.ReadDir(base)
		if verr != nil {
			continue
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			if _, oerr := os.Stat(filepath.Join(base, v.Name(), ".orphaned_at")); oerr == nil {
				continue
			}
			for _, rel := range []string{
				filepath.Join(".claude-plugin", "plugin.json"),
				"plugin.json",
			} {
				p := filepath.Join(base, v.Name(), rel)
				b, rerr := os.ReadFile(p)
				if rerr != nil {
					continue
				}
				var doc struct {
					Skills []string `json:"skills"`
				}
				if json.Unmarshal(b, &doc) != nil || len(doc.Skills) == 0 {
					continue
				}
				out := map[string]bool{}
				for _, s := range doc.Skills {
					out[filepath.Base(s)] = true
				}
				return out, p
			}
		}
	}
	return nil, ""
}
