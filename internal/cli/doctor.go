package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

// doctorOpts is the parsed argument list. Unknown flags are rejected rather than
// ignored: the previous parser was `args[0] == "--fix"`, so `doctor --dry-run --fix`
// silently ran the read-only path and `doctor --fixx` silently did nothing. In a
// command whose --fix branch deletes directories, "the flag you typed was not the
// flag that ran" is not a cosmetic problem.
type doctorOpts struct {
	fix bool
	// forceRemove names worktree directories the caller has explicitly
	// acknowledged, one directory at a time. It is the ONLY way --fix will remove
	// a worktree whose work item is not provably finished; see removeOrphans.
	forceRemove map[string]bool
}

func parseDoctorArgs(args []string) (doctorOpts, error) {
	opts := doctorOpts{forceRemove: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--fix":
			opts.fix = true
		case strings.HasPrefix(a, "--force-remove="):
			for _, d := range strings.Split(strings.TrimPrefix(a, "--force-remove="), ",") {
				if d = strings.TrimSpace(d); d != "" {
					opts.forceRemove[d] = true
				}
			}
		case a == "--force-remove":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--force-remove needs a worktree directory name")
			}
			i++
			for _, d := range strings.Split(args[i], ",") {
				if d = strings.TrimSpace(d); d != "" {
					opts.forceRemove[d] = true
				}
			}
		default:
			return opts, fmt.Errorf("unknown argument %q (accepted: --fix, --force-remove=<dir>[,<dir>])", a)
		}
	}
	if len(opts.forceRemove) > 0 && !opts.fix {
		return opts, fmt.Errorf("--force-remove only means anything together with --fix")
	}
	return opts, nil
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
	opts, err := parseDoctorArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "polyforge doctor: %v\n", err)
		os.Exit(2)
	}

	checks := []checkResult{
		checkWorkspace(wsRoot, cfg),
		checkConfig(ctx, c),
		checkRepos(wsRoot, cfg),
		checkWorktrees(ctx, c, cfg, wsRoot, opts, os.Stdout),
		checkVersion(ctx, c),
		checkClaudeMd(wsRoot),
		checkUsageMd(wsRoot),
	}
	fix := opts.fix

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
	// `polyforge init`, NOT `polyforge init --apply`. --apply has been a hard
	// no-op since it was deprecated: RunInit prints a deprecation line and
	// returns before the clone loop, before the repo map, before CLAUDE.md. So
	// the advice this check used to print did nothing at all, and — worse — it
	// did nothing *quietly enough to look like success*, which is the failure
	// mode a doctor exists to remove rather than create (aihub#307).
	fixCmd := "polyforge init"
	if len(mismatch) > 0 {
		// And `init` does not fix a remote mismatch either: cloneOrSync only
		// fetches and resets an existing checkout, it has no `git remote set-url`
		// path. Naming a command that cannot repair the thing it is offered for
		// is the same defect as --apply, one step further down.
		fixCmd = "polyforge init (clones what is missing; a remote mismatch needs " +
			"`git -C .repo/<name> remote set-url origin <url>` by hand — init only fetches and resets)"
	}
	return checkResult{
		Name:    "repos",
		Status:  "warning",
		Message: strings.Join(msgs, "; "),
		FixCmd:  fixCmd,
	}
}

// listPageLimit is the page size checkWorktrees asks for, and it is exactly 200
// rather than "a big number" on purpose.
//
// The server's cap fails OPEN. domain.ListWorkItems treats `limit <= 0 || limit
// > 200` as "unset" and substitutes 50 (aihub#267), so the two intuitive ways to
// write this — omit limit, or ask for 1000 — are the same wrong answer, and both
// return a full-looking page. Measured against production 2026-08-31 on a
// project with 127 open items: limit absent → 50, limit=50 → 50, limit=200 →
// 127, limit=500 → 50.
const listPageLimit = 200

// listPageBudget bounds the cursor walk so a server that keeps handing back a
// cursor cannot spin forever. Hitting it is reported as a FAILED listing, never
// as a complete one — see fetchActiveWorkItems.
const listPageBudget = 100

// activeWIStatuses are the statuses whose work item still owns its worktree.
// `blocked` belongs here and used not to be: a step failure moves a claimed wi
// from running to blocked (internal/domain/dependencies.go) without touching its
// worktree, so leaving it out made every escalated wi's uncommitted work look
// like garbage. Terminal statuses are the complement — wrapped, failed,
// cancelled — and only those release a worktree.
var activeWIStatuses = []string{"running", "paused", "queued", "blocked"}

func isTerminalWIStatus(s string) bool {
	switch s {
	case "wrapped", "failed", "cancelled":
		return true
	}
	return false
}

// fetchActiveWorkItems returns every non-terminal work item of one project,
// walking the cursor to the end.
//
// It returns an error whenever it cannot PROVE it reached the end, and that
// distinction is the whole point of the function. Its caller turns "this work
// item was not in the list" into "delete this directory", so a truncated list is
// not a smaller answer — it is a wrong one that destroys uncommitted work. The
// original code had no cursor loop and no limit at all, so it silently compared
// against the server's first 50 rows; a project with 130 open items had 80 of
// them invisible, and five live worktrees (one of them a running wi) were listed
// for deletion (aihub#307).
func fetchActiveWorkItems(ctx context.Context, c *client.Client, projectName string) ([]map[string]any, error) {
	var out []map[string]any
	cursor := ""
	for page := 1; page <= listPageBudget; page++ {
		params := url.Values{
			"project": []string{projectName},
			"status":  []string{strings.Join(activeWIStatuses, ",")}, // server splits on ","
			"limit":   []string{strconv.Itoa(listPageLimit)},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		result, err := c.ListWorkItems(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		items, ok := result["items"].([]any)
		if !ok {
			// A response without an items array is not an empty result. Reading
			// it as one would mean "every worktree of this project is an orphan".
			return nil, fmt.Errorf("page %d: response carries no items array", page)
		}
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		next, _ := result["next_cursor"].(string) // JSON null → "" → done
		if next == "" {
			return out, nil
		}
		if next == cursor {
			return nil, fmt.Errorf("page %d: server repeated cursor %q", page, next)
		}
		cursor = next
	}
	return nil, fmt.Errorf("listing did not terminate within %d pages of %d", listPageBudget, listPageLimit)
}

// worktreeProject returns the project segment of a `pf.<project>-<seq>` directory
// name, or "" for the two legacy formats (pf.<ulid8>, pf.<seq>.<ulid8>) that
// carry no project. Project names may contain "-" (global-routing,
// polyforge-scenario), so the split is on the LAST "-" and the tail must be all
// digits.
func worktreeProject(name string) string {
	rest := strings.TrimPrefix(name, "pf.")
	i := strings.LastIndex(rest, "-")
	if i <= 0 || i == len(rest)-1 {
		return ""
	}
	for _, r := range rest[i+1:] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return rest[:i]
}

// worktreeSlug converts `pf.<project>-<seq>` back into the work item slug
// `<project>#<seq>`, or "" when the name carries no slug.
func worktreeSlug(name string) string {
	p := worktreeProject(name)
	if p == "" {
		return ""
	}
	return p + "#" + strings.TrimPrefix(name, "pf."+p+"-")
}

// orphanVerdict is the per-worktree answer --fix acts on. Each field is here
// because the caller must be able to print WHY before it deletes anything.
type orphanVerdict struct {
	Dir       string
	Slug      string // "" when the directory name carries none
	Status    string // work item status as the server reports it, "" if unknown
	Removable bool
	Note      string
}

func (v orphanVerdict) statusText() string {
	if v.Status == "" {
		return "unknown"
	}
	return v.Status
}

// verifyOrphan re-asks the server about ONE candidate, by slug, before it can be
// deleted.
//
// This is deliberately a second, independent hop rather than a reuse of the list
// result: the list is what was wrong in aihub#307, and a check that answers "is
// this safe to delete?" out of the same data that proposed the deletion cannot
// catch the next version of that bug. It is also what makes it possible to print
// each path together with its work item's status, which is what a human needs to
// veto the batch.
//
// Every uncertain outcome resolves to "do not delete", including 404. A 404 is
// not proof the work item never existed — the far more likely cause is that
// ~/.polyforge/config.toml points at a different aihub than the one this
// workspace was built against, in which case EVERY slug 404s and a delete-on-404
// rule wipes the whole workspace in one command.
func verifyOrphan(ctx context.Context, c *client.Client, dir string) orphanVerdict {
	slug := worktreeSlug(dir)
	if slug == "" {
		// Legacy name: no slug to ask about, so the completed active listing is
		// the only evidence there is. Say so rather than implying a status was
		// checked.
		return orphanVerdict{Dir: dir, Removable: true,
			Note: "legacy directory name carries no slug — classified from the completed active listing alone, no per-item status check was possible"}
	}
	wi, err := c.GetWorkItem(ctx, slug)
	if err != nil {
		return orphanVerdict{Dir: dir, Slug: slug,
			Note: fmt.Sprintf("could not read %s: %v — refusing to delete on an unread work item", slug, err)}
	}
	status, _ := wi["status"].(string)
	if status == "" {
		return orphanVerdict{Dir: dir, Slug: slug,
			Note: fmt.Sprintf("%s came back without a status field — refusing to delete", slug)}
	}
	if !isTerminalWIStatus(status) {
		return orphanVerdict{Dir: dir, Slug: slug, Status: status,
			Note: fmt.Sprintf("%s is %s, not a terminal state — its worktree may hold uncommitted work. "+
				"That it reached this list at all means the active listing missed it; please report that", slug, status)}
	}
	return orphanVerdict{Dir: dir, Slug: slug, Status: status, Removable: true,
		Note: fmt.Sprintf("%s is %s (terminal)", slug, status)}
}

// checkWorktrees cross-references pf.* directories with active work items
// from aihub; flags directories with no matching active wi.
//
// Everything here is arranged around one asymmetry: a false negative costs a
// stale directory, a false positive costs a developer's uncommitted work. So
// every way of not knowing — a failed page, an unparseable response, an
// unreadable work item — has to land on "keep", and has to say so out loud
// instead of being folded into the orphan list.
func checkWorktrees(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, opts doctorOpts, out io.Writer) checkResult {
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
	sort.Strings(wt)

	// Fetch active work items from aihub to cross-reference.
	// The server requires a "project" query parameter, so we query each project
	// separately and merge the results. If cfg is nil we skip the cross-reference
	// (cannot determine projects) and list pf.* dirs as a warning only.
	activeIDs := map[string]bool{}   // ulid8 suffix → true (legacy format match)
	activeSlugs := map[string]bool{} // "pf.<project>-<seq>" → true (new format match)
	failed := map[string]string{}    // project → why its listing is not usable
	if c != nil && cfg != nil {
		projects := make([]string, 0, len(cfg.Projects))
		for projectName := range cfg.Projects {
			projects = append(projects, projectName)
		}
		sort.Strings(projects)
		for _, projectName := range projects {
			items, listErr := fetchActiveWorkItems(ctx, c, projectName)
			if listErr != nil {
				// Was `continue`, which let the orphan scan run against a set
				// this project contributed nothing to — i.e. one failed request
				// nominated every one of its worktrees for deletion.
				failed[projectName] = listErr.Error()
				continue
			}
			for _, m := range items {
				if id, ok := m["id"].(string); ok && len(id) > 3 {
					activeIDs[id[3:]] = true // strip "wi_" prefix → 01ks510z
				}
				// New slug-based format: "aihub#30" → "pf.aihub-30"
				if slug, ok := m["slug"].(string); ok && slug != "" {
					activeSlugs["pf."+strings.ReplaceAll(slug, "#", "-")] = true
				}
			}
		}
	}

	// Identify orphan candidates: worktree that does not match any active wi.
	// Supports three formats:
	//   pf.<project>-<seq>   — new readable format (matched via activeSlugs)
	//   pf.<seq>.<ulid8>     — previous format (matched via activeIDs ulid8 suffix)
	//   pf.<ulid8>           — legacy format (matched via activeIDs ulid8 suffix)
	// With no client or no config there is nothing to cross-reference against, so
	// nothing can be called an orphan. Report that as "did not look" rather than
	// as "looked and found nothing" — a green line here is the same silent
	// failure the rest of this function exists to remove.
	if c == nil || cfg == nil {
		return checkResult{Name: "worktrees", Status: "warning",
			Message: fmt.Sprintf("%d worktrees found, but they could not be cross-referenced "+
				"(no aihub client or no .polyforge.yaml) — this is 'did not look', not 'none orphaned'", len(wt))}
	}

	var candidates, unverifiable []string
	for _, name := range wt {
		if activeSlugs[name] {
			continue
		}
		matched := false
		// Old formats: extract ulid8 and match against activeIDs suffix.
		remainder := strings.TrimPrefix(name, "pf.")
		parts := strings.SplitN(remainder, ".", 2)
		ulid8 := remainder // legacy format: pf.<ulid8>
		if len(parts) == 2 {
			ulid8 = parts[1] // format: pf.<seq>.<ulid8>
		}
		for active := range activeIDs {
			if strings.HasSuffix(active, ulid8) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if why, gap := listingGapFor(name, failed); gap {
			unverifiable = append(unverifiable, fmt.Sprintf("%s (%s)", name, why))
			continue
		}
		candidates = append(candidates, name)
	}

	if opts.fix {
		return removeOrphans(ctx, c, wsRoot, len(wt), candidates, unverifiable, opts, out)
	}

	// Read-only report. Each candidate is still re-checked individually so the
	// message carries the work item status a human needs to sanity-check the
	// list — the old message was bare directory names, which is exactly what let
	// a running wi's worktree sit in it unnoticed.
	var listed []string
	blockers := 0
	for _, dir := range candidates {
		v := verifyOrphan(ctx, c, dir)
		listed = append(listed, fmt.Sprintf("%s [%s]", v.Dir, v.statusText()))
		if !v.Removable {
			blockers++
		}
	}

	switch {
	case len(listed) == 0 && len(unverifiable) == 0:
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("%d worktrees, none orphaned", len(wt))}
	case len(listed) == 0:
		return checkResult{Name: "worktrees", Status: "warning",
			Message: fmt.Sprintf("%d worktrees, none orphaned; %d could not be verified: %s",
				len(wt), len(unverifiable), strings.Join(unverifiable, ", "))}
	}

	msg := fmt.Sprintf("%d orphan worktrees: %s", len(listed), strings.Join(listed, ", "))
	if blockers > 0 {
		msg += fmt.Sprintf("; %d of them are NOT safe to remove (their work item is not in a terminal state) — --fix will refuse", blockers)
	}
	if len(unverifiable) > 0 {
		msg += fmt.Sprintf("; %d more could not be verified and are left alone: %s",
			len(unverifiable), strings.Join(unverifiable, ", "))
	}
	return checkResult{Name: "worktrees", Status: "warning", Message: msg, FixCmd: "polyforge doctor --fix"}
}

// listingGapFor reports whether `name` was compared against an incomplete set,
// in which case "not in the active list" means nothing.
func listingGapFor(name string, failed map[string]string) (string, bool) {
	if len(failed) == 0 {
		return "", false
	}
	if p := worktreeProject(name); p != "" {
		if why, bad := failed[p]; bad {
			return "project " + p + " could not be listed: " + why, true
		}
		return "", false
	}
	// A legacy name is matched against the ulid8 map merged across ALL projects,
	// so any failed listing can be the reason it looks inactive.
	names := make([]string, 0, len(failed))
	for p := range failed {
		names = append(names, p)
	}
	sort.Strings(names)
	return "legacy directory name, and these projects could not be listed: " + strings.Join(names, ", "), true
}

// removeOrphans is the --fix path. It prints one line per candidate — path, work
// item status, and reason — BEFORE touching that candidate, and removes only the
// ones whose work item is provably finished.
//
// The previous implementation took the whole candidate list and deleted it in a
// loop with no per-worktree step, so there was no point at which a non-orphan
// mixed into the batch could be noticed, by a human or by the program.
// `--force-remove <dir>` is the escape hatch, and it is per directory by
// construction: it takes names, not a blanket --force, so acknowledging one
// worktree cannot silently acknowledge the next one.
func removeOrphans(ctx context.Context, c *client.Client, wsRoot string, total int, candidates, unverifiable []string, opts doctorOpts, out io.Writer) checkResult {
	// A --force-remove name that matches no candidate must be said out loud. Left
	// silent, a typo, a stale name, or a directory this run classified as
	// unverifiable all look identical to "it was already cleaned up" — and the
	// caller walks away believing an acknowledgement was acted on.
	isCandidate := make(map[string]bool, len(candidates))
	for _, d := range candidates {
		isCandidate[d] = true
	}
	var unmatched []string
	for d := range opts.forceRemove {
		if !isCandidate[d] {
			unmatched = append(unmatched, d)
		}
	}
	sort.Strings(unmatched)

	if len(candidates) == 0 && len(unmatched) == 0 && len(unverifiable) == 0 {
		return checkResult{Name: "worktrees", Status: "ok",
			Message: fmt.Sprintf("%d worktrees, none orphaned", total)}
	}

	var removed, refused, forced []string
	for _, dir := range candidates {
		v := verifyOrphan(ctx, c, dir)
		override := opts.forceRemove[dir]

		_, _ = fmt.Fprintf(out, "       worktree %s: wi=%s status=%s — %s\n",
			v.Dir, slugText(v.Slug), v.statusText(), v.Note)

		if !v.Removable && !override {
			_, _ = fmt.Fprintf(out, "       worktree %s: KEPT. To remove it anyway: polyforge doctor --fix --force-remove=%s\n", v.Dir, v.Dir)
			refused = append(refused, fmt.Sprintf("%s [%s]", v.Dir, v.statusText()))
			continue
		}
		if !v.Removable {
			_, _ = fmt.Fprintf(out, "       worktree %s: removing anyway, --force-remove named it explicitly\n", v.Dir)
			forced = append(forced, fmt.Sprintf("%s [%s]", v.Dir, v.statusText()))
		}

		// git worktree remove --force is safest; fall back to rm -rf.
		if err := exec.CommandContext(ctx, "git", "-C", wsRoot, "worktree", "remove", "--force", dir).Run(); err != nil {
			_ = os.RemoveAll(filepath.Join(wsRoot, dir))
		}
		_, _ = fmt.Fprintf(out, "       worktree %s: removed\n", v.Dir)
		removed = append(removed, v.Dir)
	}

	// Phrased so that "removed <n> orphan" stays a substring: tests/scenarios/e2e
	// asserts on it.
	msg := fmt.Sprintf("removed %d orphan worktree(s) of %d candidate(s)", len(removed), len(candidates))
	if len(removed) > 0 {
		msg += ": " + strings.Join(removed, ", ")
	}
	if len(forced) > 0 {
		msg += fmt.Sprintf("; %d removed only because --force-remove named them: %s", len(forced), strings.Join(forced, ", "))
	}
	if len(refused) > 0 {
		msg += fmt.Sprintf("; KEPT %d whose work item is not in a terminal state: %s", len(refused), strings.Join(refused, ", "))
	}
	if len(unverifiable) > 0 {
		msg += fmt.Sprintf("; %d could not be verified and were left alone: %s", len(unverifiable), strings.Join(unverifiable, ", "))
	}
	if len(unmatched) > 0 {
		msg += fmt.Sprintf("; --force-remove named %d director(y/ies) that are not orphan candidates, "+
			"so nothing was done with them: %s", len(unmatched), strings.Join(unmatched, ", "))
	}
	status := "ok"
	if len(refused) > 0 || len(unverifiable) > 0 || len(unmatched) > 0 {
		status = "warning"
	}
	return checkResult{Name: "worktrees", Status: status, Message: msg}
}

func slugText(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
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
// REPORT-ONLY, ON PURPOSE — and this is the second time that conclusion was reached the
// hard way. The first cut of this check also removed the sections under `--fix`, deciding
// each section's extent from markdown structure. Review found six input classes where
// that destroyed content the user owned, three of them leaving the file structurally
// broken (an unterminated fence or HTML comment swallows the rest of the document). The
// live check against a real frozen workspace missed all six, because a pristine generated
// template is the one input that cannot exhibit any of them.
//
// Inferring the extent of a generated section from the shape of a file a user has since
// edited is the wrong primitive. The right one is to delete a span only when it is
// byte-identical to something a known template version emitted, and otherwise say so and
// stop. That is a real piece of work — it needs the historical template bodies — and it
// is NOT what this work item is about, which is that a rule was on a channel that could
// never be corrected. Detection is what makes that stop being silent; removal was a
// convenience that cost a data-loss path.
//
// `--fix` deliberately does not reach here. It is also not the operator's considered
// choice as often as it looks: plugins/polyforge/skills/pf-stop/SKILL.md tells agents to
// run `polyforge doctor --fix` to clean up worktrees, so the caller asking for it has
// very likely never read this file.
func checkUsageMd(wsRoot string) checkResult {
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

	found, wellFormed := scanUsageSections(string(b))
	if !wellFormed {
		// An unterminated fence or HTML comment means everything after it was skipped,
		// so a clean result here would mean "stopped looking", not "looked and found
		// nothing". In a check whose entire subject is silent failure, that green would
		// be the very defect it exists to report.
		return checkResult{Name: name, Status: "warning",
			Message: ".polyforge/usage.md has an unterminated code fence or HTML comment — " +
				"the scan could not read past it, so this is 'did not look', not 'found nothing'"}
	}
	if len(found) == 0 {
		return checkResult{Name: name, Status: "ok",
			Message: ".polyforge/usage.md carries no rule section owned by using-polyforge"}
	}
	return checkResult{Name: name, Status: "warning",
		Message: fmt.Sprintf(".polyforge/usage.md still carries %d rule section(s) that using-polyforge "+
			"owns (%s) — that file is never regenerated, so this copy cannot be corrected and a "+
			"session sees both",
			len(found), strings.Join(found, ", ")),
		FixCmd: "edit .polyforge/usage.md and delete those sections by hand — the maintained " +
			"copy ships with the using-polyforge skill (not automated: see checkUsageMd)"}
}

// fenceDelim reports the fence character and run length that opens or closes a fenced
// code block, or (0, 0) for any other line. `line` must already have leading space
// stripped. Character AND length both matter: per CommonMark a fence is closed only by a
// run of the SAME character at least as long as the one that opened it, so a ``` inside a
// ```` block, or a ~~~ inside a ``` block, is content rather than a terminator. Treating
// every ```/~~~ prefix as a toggle got both of those backwards.
func fenceDelim(line string) (byte, int) {
	if line == "" {
		return 0, 0
	}
	c := line[0]
	if c != '`' && c != '~' {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return c, n
}

// scanUsageSections walks a usage.md body once and reports which skillOwnedUsageSections
// headings it really carries, in template order, plus whether the document parsed cleanly.
//
// "Really" is doing work here. A line that merely looks like an owned heading does not
// count when it is inside a fenced block, inside an indented code block (4+ spaces is a
// code block, and CommonMark allows an ATX heading at most 3), or inside an HTML comment.
// Each of those is an example or a note ABOUT the heading, not the heading.
//
// wellFormed is false when the walk ends inside a fence or a comment, i.e. when the tail
// of the file was never examined. The caller must not report a clean bill in that case.
func scanUsageSections(body string) (found []string, wellFormed bool) {
	owned := make(map[string]bool, len(skillOwnedUsageSections))
	for _, h := range skillOwnedUsageSections {
		owned[h] = true
	}
	seen := make(map[string]bool, len(skillOwnedUsageSections))

	var fenceChar byte
	fenceLen := 0
	inComment := false

	for _, line := range strings.Split(body, "\n") {
		stripped := strings.TrimLeft(line, " \t")
		indent := len(line) - len(stripped)

		if inComment {
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		if fenceLen > 0 {
			if c, n := fenceDelim(stripped); c == fenceChar && n >= fenceLen && indent <= 3 {
				fenceLen = 0
			}
			continue
		}
		if c, n := fenceDelim(stripped); n > 0 && indent <= 3 {
			fenceChar, fenceLen = c, n
			continue
		}
		if i := strings.Index(line, "<!--"); i >= 0 {
			if !strings.Contains(line[i:], "-->") {
				inComment = true
			}
			continue
		}
		if indent >= 4 {
			continue // indented code block, not a heading
		}
		if s := strings.TrimSpace(line); owned[s] {
			seen[s] = true
		}
	}

	for _, h := range skillOwnedUsageSections {
		if seen[h] {
			found = append(found, strings.TrimPrefix(h, "## "))
		}
	}
	return found, fenceLen == 0 && !inComment
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
