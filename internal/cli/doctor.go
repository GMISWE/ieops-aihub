package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

type checkResult struct {
	Name    string
	Status  string // "ok", "warning", "error"
	Message string
	FixCmd  string
}

// RunDoctor runs 7 diagnostic checks and reports their status.
// With --fix: attempts to auto-repair fixable issues.
//
// Checks (§12.1):
//  1. workspace  – can locate .polyforge.yaml from wsRoot
//  2. config     – ~/.polyforge/config not required in v1, checks aihub reachability
//  3. repos      – .repo/<name>/ exist and match .polyforge.yaml remotes
//  4. worktrees  – pf.<project>-<seq>/ list vs server wi list; flag orphans
//  5. version    – GET /v1/version; compare min_client_version vs local binary
//  6. claude_md  – CLAUDE.md managed block format + .polyforge/repo-map/ presence
//  7. usage_md   – .polyforge/usage.md still carrying rules using-polyforge owns
func RunDoctor(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, args []string) {
	fix := len(args) > 0 && args[0] == "--fix"

	checks := []checkResult{
		checkWorkspace(wsRoot, cfg),
		checkConfig(ctx, c),
		checkRepos(wsRoot, cfg),
		checkWorktrees(ctx, c, cfg, wsRoot, fix),
		checkVersion(ctx, c),
		checkClaudeMd(wsRoot),
		checkUsageMd(wsRoot, fix),
	}

	allOk := true
	for _, ch := range checks {
		icon := "ok"
		if ch.Status == "warning" {
			icon = "warn"
		}
		if ch.Status == "error" {
			icon = "FAIL"
			allOk = false
		}
		fmt.Printf("[%s] %s: %s\n", icon, ch.Name, ch.Message)
		if ch.FixCmd != "" && !fix {
			fmt.Printf("       fix: %s\n", ch.FixCmd)
		}
	}
	if !allOk {
		os.Exit(1)
	}
}

// checkWorkspace verifies that .polyforge.yaml was found.
func checkWorkspace(wsRoot string, cfg *config.Config) checkResult {
	if cfg == nil {
		return checkResult{
			Name:    "workspace",
			Status:  "error",
			Message: fmt.Sprintf(".polyforge.yaml not found in %s", wsRoot),
			FixCmd:  "polyforge init",
		}
	}
	return checkResult{
		Name:    "workspace",
		Status:  "ok",
		Message: ".polyforge.yaml found",
	}
}

// checkConfig verifies aihub reachability via GET /health.
func checkConfig(ctx context.Context, c *client.Client) checkResult {
	if c == nil {
		return checkResult{
			Name:    "config",
			Status:  "warning",
			Message: "aihub client not configured (POLYFORGE_API_KEY / POLYFORGE_AIHUB_URL not set)",
		}
	}
	var health map[string]any
	if err := c.Health(ctx, &health); err != nil {
		return checkResult{
			Name:    "config",
			Status:  "error",
			Message: fmt.Sprintf("aihub unreachable: %v", err),
		}
	}
	return checkResult{Name: "config", Status: "ok", Message: "aihub reachable"}
}

// checkRepos verifies that .repo/<name>/ directories exist and their remote
// URLs match .polyforge.yaml.
func checkRepos(wsRoot string, cfg *config.Config) checkResult {
	if cfg == nil {
		return checkResult{Name: "repos", Status: "warning", Message: "skipped (no config)"}
	}
	repoBase := filepath.Join(wsRoot, ".repo")
	var missing, mismatch []string

	for _, proj := range cfg.Projects {
		for _, r := range proj.Repos {
			repoPath := filepath.Join(repoBase, r.Name)
			if _, err := os.Stat(repoPath); os.IsNotExist(err) {
				missing = append(missing, r.Name)
				continue
			}
			// Check remote URL.
			out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
			if err != nil {
				mismatch = append(mismatch, r.Name+"(remote-err)")
				continue
			}
			actual := strings.TrimSpace(string(out))
			if r.URL != "" && actual != r.URL {
				mismatch = append(mismatch, fmt.Sprintf("%s(want %s got %s)", r.Name, r.URL, actual))
			}
		}
	}

	if len(missing) == 0 && len(mismatch) == 0 {
		return checkResult{Name: "repos", Status: "ok", Message: "all repos present and remotes match"}
	}
	var msgs []string
	if len(missing) > 0 {
		msgs = append(msgs, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(mismatch) > 0 {
		msgs = append(msgs, fmt.Sprintf("remote mismatch: %s", strings.Join(mismatch, ", ")))
	}
	return checkResult{
		Name:    "repos",
		Status:  "warning",
		Message: strings.Join(msgs, "; "),
		FixCmd:  "polyforge init --apply",
	}
}

// checkWorktrees cross-references pf.* directories with active work items
// from aihub; flags directories with no matching running wi.
func checkWorktrees(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, fix bool) checkResult {
	entries, err := os.ReadDir(wsRoot)
	if err != nil {
		return checkResult{Name: "worktrees", Status: "error", Message: fmt.Sprintf("readdir: %v", err)}
	}

	var wt []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "pf.") {
			wt = append(wt, e.Name())
		}
	}
	if len(wt) == 0 {
		return checkResult{Name: "worktrees", Status: "ok", Message: "no worktrees found"}
	}

	// Fetch active work items from aihub to cross-reference.
	// The server requires a "project" query parameter, so we query each project
	// separately and merge the results. If cfg is nil we skip the cross-reference
	// (cannot determine projects) and list pf.* dirs as a warning only.
	activeIDs := map[string]bool{}   // ulid8 suffix → true (legacy format match)
	activeSlugs := map[string]bool{} // "pf.<project>-<seq>" → true (new format match)
	if c != nil && cfg != nil {
		for projectName := range cfg.Projects {
			params := url.Values{
				"project": []string{projectName},
				"status":  []string{"running,paused,queued"}, // server splits on ","
			}
			result, err := c.ListWorkItems(ctx, params)
			if err != nil {
				// Skip this project on error; orphan check will still run for
				// projects that succeeded.
				continue
			}
			if items, ok := result["items"].([]any); ok {
				for _, item := range items {
					if m, ok := item.(map[string]any); ok {
						if id, ok := m["id"].(string); ok {
							// Extract shortid from wi_01ks510z... → 01ks510z
							if len(id) > 3 {
								activeIDs[id[3:]] = true // strip "wi_" prefix
							}
						}
						// New slug-based format: "aihub#30" → "pf.aihub-30"
						if slug, ok := m["slug"].(string); ok && slug != "" {
							dirName := "pf." + strings.ReplaceAll(slug, "#", "-")
							activeSlugs[dirName] = true
						}
					}
				}
			}
		}
	}

	// Identify orphans: worktree that does not match any active wi.
	// Supports three formats:
	//   pf.<project>-<seq>   — new readable format (matched via activeSlugs)
	//   pf.<seq>.<ulid8>     — previous format (matched via activeIDs ulid8 suffix)
	//   pf.<ulid8>           — legacy format (matched via activeIDs ulid8 suffix)
	var orphans []string
	for _, name := range wt {
		found := false

		// New format: pf.<project>-<seq> — check slug map directly.
		if activeSlugs[name] {
			found = true
		}

		if !found {
			// Old formats: extract ulid8 and match against activeIDs suffix.
			remainder := strings.TrimPrefix(name, "pf.")
			parts := strings.SplitN(remainder, ".", 2)
			var ulid8 string
			if len(parts) == 2 {
				ulid8 = parts[1] // format: pf.<seq>.<ulid8>
			} else {
				ulid8 = remainder // legacy format: pf.<ulid8>
			}
			for active := range activeIDs {
				if strings.HasSuffix(active, ulid8) {
					found = true
					break
				}
			}
		}

		// If aihub unreachable or cfg is nil, we cannot confirm orphan — skip.
		if !found && c != nil && cfg != nil {
			orphans = append(orphans, name)
		}
	}

	if len(orphans) == 0 {
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("%d worktrees, none orphaned", len(wt))}
	}

	if fix {
		removed := 0
		for _, o := range orphans {
			// git worktree remove --force is safest; fall back to rm -rf.
			path := filepath.Join(wsRoot, o)
			if err := exec.CommandContext(ctx, "git", "-C", wsRoot, "worktree", "remove", "--force", o).Run(); err != nil {
				_ = os.RemoveAll(path)
			}
			removed++
		}
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("removed %d orphan worktrees: %s", removed, strings.Join(orphans, ", "))}
	}

	return checkResult{
		Name:    "worktrees",
		Status:  "warning",
		Message: fmt.Sprintf("%d orphan worktrees: %s", len(orphans), strings.Join(orphans, ", ")),
		FixCmd:  "polyforge doctor --fix",
	}
}

// checkUsageMd reports a .polyforge/usage.md that still carries a rule section the
// plugin-versioned using-polyforge skill owns (aihub#294).
//
// This check has to exist because writeUsageMd cannot fix the problem by itself. That
// function refuses to overwrite an existing usage.md — deliberately; the file is the
// user's — so dropping the rule sections from its template only helps workspaces created
// after this release. Every workspace that already ran init keeps its frozen copy, a
// session then receives the rules twice, and the two copies are under no obligation to
// agree. They already did not: IR1's worktree path was wrong in one of them for three
// months and nothing anywhere reported it.
//
// The removal is NOT done during `polyforge init`. Silently deleting from a file whose
// entire contract is "polyforge does not write here once it exists" would trade a silent
// duplicate for a silent deletion, which is the same class of defect this check exists to
// end. `--fix` is an explicit request from the operator, so it is allowed to rewrite.
func checkUsageMd(wsRoot string, fix bool) checkResult {
	const name = "usage_md"
	path := filepath.Join(wsRoot, ".polyforge", "usage.md")

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Absent is not a finding here: usage.md is written by init, and a workspace
		// that never ran init has a louder problem that checkWorkspace already reports.
		return checkResult{Name: name, Status: "ok", Message: "no .polyforge/usage.md to check"}
	}
	if err != nil {
		// Anything else — a permission or IO fault on a file that IS there — must not
		// report green. "Could not look" and "looked and found nothing" are different.
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf(".polyforge/usage.md not readable: %v", err)}
	}

	found := usageSectionsOwnedBySkill(string(b))
	if len(found) == 0 {
		return checkResult{Name: name, Status: "ok",
			Message: ".polyforge/usage.md carries no rule section owned by using-polyforge"}
	}
	if fix {
		stripped, n := stripUsageSections(string(b))
		// Back the original up first. This rewrite deletes from a file the user owns,
		// and the section boundary is inferred from markdown structure, not from a
		// record of what the generator actually wrote — so it can be wrong on a file
		// edited in a way we did not anticipate. `--fix` is also reached by agents told
		// to run it for unrelated reasons (pf-stop runs it to clean worktrees), so the
		// operator asking for it has not necessarily read this file. Recoverable beats
		// clever.
		mode := os.FileMode(0644)
		if fi, serr := os.Stat(path); serr == nil {
			mode = fi.Mode().Perm()
		}
		backup := path + ".bak"
		if werr := os.WriteFile(backup, b, mode); werr != nil {
			return checkResult{Name: name, Status: "error",
				Message: fmt.Sprintf("refusing to rewrite .polyforge/usage.md — could not write %s first: %v",
					filepath.Base(backup), werr)}
		}
		if werr := os.WriteFile(path, []byte(stripped), mode); werr != nil {
			return checkResult{Name: name, Status: "error",
				Message: fmt.Sprintf("could not rewrite .polyforge/usage.md: %v", werr)}
		}
		return checkResult{Name: name, Status: "ok",
			Message: fmt.Sprintf("removed %d duplicated rule section(s) from .polyforge/usage.md: %s "+
				"(the maintained copy ships with the using-polyforge skill; original saved as %s)",
				n, strings.Join(found, ", "), filepath.Base(backup))}
	}
	return checkResult{Name: name, Status: "warning",
		Message: fmt.Sprintf(".polyforge/usage.md still carries %d rule section(s) that using-polyforge "+
			"owns (%s) — that file is never regenerated, so this copy cannot be corrected and a "+
			"session sees both",
			len(found), strings.Join(found, ", ")),
		FixCmd: "polyforge doctor --fix"}
}

// isMarkdownFence reports whether a line opens or closes a fenced code block.
func isMarkdownFence(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// isATXHeading reports whether a line is a markdown ATX heading of any level.
// Any level, not just `## `: a user following this file's own closing invitation to
// "add workspace-specific notes" is as likely to write `# Notes` or `### Notes`, and a
// terminator that only recognises `## ` would swallow all of it.
func isATXHeading(line string) bool {
	t := strings.TrimSpace(line)
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && n < len(t) && (t[n] == ' ' || t[n] == '\t')
}

// usageSectionsOwnedBySkill lists, in template order, the skillOwnedUsageSections
// headings present in a usage.md body (reported without the leading "## ").
// Fence-aware, so a heading quoted inside a ``` block is not mistaken for a real one.
func usageSectionsOwnedBySkill(body string) []string {
	var found []string
	for _, h := range skillOwnedUsageSections {
		fence := false
		for _, line := range strings.Split(body, "\n") {
			if isMarkdownFence(line) {
				fence = !fence
				continue
			}
			if !fence && strings.TrimSpace(line) == h {
				found = append(found, strings.TrimPrefix(h, "## "))
				break
			}
		}
	}
	return found
}

// stripUsageSections removes every skillOwnedUsageSections section from a usage.md body
// and reports how many it removed. The `---` rule the template puts *between* sections is
// carried off with the section above it, and the run of blank/`---` lines left dangling
// before a removed heading collapses to a single blank line.
//
// A section runs from its heading to the next ATX heading OF ANY LEVEL, or EOF. Both
// halves of that matter and neither is cosmetic:
//
//   - Any level, because `## Memory Type Reference` is the LAST section the template
//     emits and the line right above it invites the user to append notes. A terminator
//     keyed on `## ` alone deleted every appended `# Appendix` / `### note` with it.
//   - Fence-aware, because a `## ` inside a ``` block is an example, not a heading.
//     Without this, quoting a heading either got the quote stripped as if it were real
//     (leaving an unterminated fence, which swallows the rest of the document) or ended
//     an owned section early, letting content escape that should have gone.
//
// Prose appended directly under an owned section with no heading of its own is still
// indistinguishable from that section's body and goes with it — which is why the caller
// writes a .bak before applying this.
func stripUsageSections(body string) (string, int) {
	owned := make(map[string]bool, len(skillOwnedUsageSections))
	for _, h := range skillOwnedUsageSections {
		owned[h] = true
	}

	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	removed := 0
	fence := false
	for i := 0; i < len(lines); i++ {
		if isMarkdownFence(lines[i]) {
			fence = !fence
			out = append(out, lines[i])
			continue
		}
		if fence || !owned[strings.TrimSpace(lines[i])] {
			out = append(out, lines[i])
			continue
		}
		removed++
		for len(out) > 0 {
			if t := strings.TrimSpace(out[len(out)-1]); t == "" || t == "---" {
				out = out[:len(out)-1]
				continue
			}
			break
		}
		if len(out) > 0 {
			// Only if something precedes it — an owned heading on line 1 must not
			// leave the file starting with a blank line.
			out = append(out, "")
		}
		// Consume the section body, tracking fences opened inside it so that a heading
		// quoted in an example cannot end the section early, and so the fence state is
		// balanced again for the lines that follow.
		inner := false
		for i+1 < len(lines) {
			if isMarkdownFence(lines[i+1]) {
				inner = !inner
			} else if !inner && isATXHeading(lines[i+1]) {
				break // the next section starts here
			}
			i++
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n", removed
}

// checkClaudeMd inspects the CLAUDE.md managed block that `polyforge init`
// writes. Two failure modes, both reported as warnings — a stale block is not a
// broken workspace, so this check must never fail the run:
//
//   - The block still inlines the per-repo detail (the pre-aihub#291 format).
//     That detail sits at context position 0, is re-read on every request, and
//     compaction cannot drop it; re-running `polyforge init` moves it to
//     .polyforge/repo-map/. This is the whole reason the check exists: the
//     saving only lands once a workspace re-runs init, and workspaces go months
//     without doing so.
//   - The block is slim but the repo map it points at is missing, so routing
//     has nothing but the one-line positioning. Say so, rather than leaving an
//     agent to quietly guess which repo a task belongs to.
//
// The expected set of maps comes from the block's own `### <project>` headings,
// not from .polyforge.yaml — see managedBlockProjects.
func checkClaudeMd(wsRoot string) checkResult {
	const name = "claude_md"
	const fix = "polyforge init"

	b, err := os.ReadFile(filepath.Join(wsRoot, "CLAUDE.md"))
	if err != nil {
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("CLAUDE.md not readable: %v", err), FixCmd: fix}
	}
	block, ok := managedBlockOf(string(b))
	if !ok {
		return checkResult{Name: name, Status: "warning",
			Message: "CLAUDE.md has no polyforge managed block", FixCmd: fix}
	}
	if blockIsLegacyFormat(block) {
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("managed block is the legacy inline format (%d B, re-read on every request) — "+
				"re-running init moves per-repo detail to .polyforge/repo-map/", len(block)),
			FixCmd: fix}
	}

	mapDir := filepath.Join(wsRoot, ".polyforge", repoMapDirName)
	present := map[string]bool{}
	entries, _ := os.ReadDir(mapDir) // absent dir → no entries, handled below
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			present[e.Name()] = true
		}
	}
	if len(present) == 0 {
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("repo map missing: no %s/*.md — the block carries only one-line positioning, "+
				"so routing has no main_modules / change_scenarios / tech_stack to read",
				filepath.Join(".polyforge", repoMapDirName)),
			FixCmd: fix}
	}
	var missing []string
	for _, project := range managedBlockProjects(block) {
		if fn := repoMapFileName(project); fn != "" && !present[fn] {
			missing = append(missing, project)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return checkResult{Name: name, Status: "warning",
			Message: fmt.Sprintf("repo map missing for project(s): %s", strings.Join(missing, ", ")),
			FixCmd:  fix}
	}
	return checkResult{Name: name, Status: "ok",
		Message: fmt.Sprintf("managed block slim (%d B), %d repo map(s) present", len(block), len(present))}
}

// checkVersion fetches GET /v1/version and compares min_client_version with
// the locally compiled version.
func checkVersion(ctx context.Context, c *client.Client) checkResult {
	if c == nil {
		return checkResult{Name: "version", Status: "warning", Message: "skipped (no aihub client)"}
	}
	ver, err := c.GetVersion(ctx)
	if err != nil {
		// Non-fatal: server may not expose this endpoint in v1.
		return checkResult{Name: "version", Status: "ok", Message: "version endpoint not available (non-fatal)"}
	}
	minVer, _ := ver["min_client_version"].(string)
	if minVer == "" {
		return checkResult{Name: "version", Status: "ok", Message: "server did not set min_client_version"}
	}
	return checkResult{Name: "version", Status: "ok",
		Message: fmt.Sprintf("server min_client_version=%s (local=dev)", minVer)}
}
