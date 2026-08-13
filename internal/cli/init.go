package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// repoModuleEntry is one {path, role} pair in a repo's main_modules list.
type repoModuleEntry struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

// repoEntry holds a repo's display data for the managed block. Description is the
// legacy single-line field; the structured fields (when present) render a richer
// per-repo block for AI routing.
type repoEntry struct {
	Name            string
	Description     *string
	Positioning     string
	TechStack       []string
	MainModules     []repoModuleEntry
	ChangeScenarios []string
	GeneratedAt     string
	GeneratedCommit string
}

// hasStructuredDesc reports whether the structured description block is present.
func (r repoEntry) hasStructuredDesc() bool {
	return r.Positioning != "" || len(r.TechStack) > 0 ||
		len(r.MainModules) > 0 || len(r.ChangeScenarios) > 0
}

// projectBlock groups a project's display data for the managed block.
type projectBlock struct {
	Name        string
	Description *string
	Repos       []repoEntry
}

// serverRepoEntry mirrors the JSON shape stored in domain.Project.Repos.
type serverRepoEntry struct {
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	GithubOwnerRepo *string           `json:"github_owner_repo,omitempty"`
	Description     *string           `json:"description,omitempty"`
	Positioning     string            `json:"positioning,omitempty"`
	TechStack       []string          `json:"tech_stack,omitempty"`
	MainModules     []repoModuleEntry `json:"main_modules,omitempty"`
	ChangeScenarios []string          `json:"change_scenarios,omitempty"`
	GeneratedAt     string            `json:"generated_at,omitempty"`
	GeneratedCommit string            `json:"generated_commit,omitempty"`
}

// serverProjectMember mirrors one entry in a project's members list.
type serverProjectMember struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// serverProject mirrors the JSON response shape from GET /v1/projects.
type serverProject struct {
	Name        string                `json:"name"`
	Description *string               `json:"description"`
	OwnerUserID string                `json:"owner_user_id"`
	Visible     bool                  `json:"visible"`
	Scenario    *string               `json:"scenario,omitempty"`
	Members     []serverProjectMember `json:"members,omitempty"`
	Repos       json.RawMessage       `json:"repos"`
}

// callerHasRole reports whether the caller (identified by uid) has an explicit
// role in sp: either project owner or listed in members[]. Used to gate auto-
// init/clone so that public-visible projects without a caller role are skipped.
func callerHasRole(sp serverProject, uid string) bool {
	if uid == "" {
		return false
	}
	if sp.OwnerUserID == uid {
		return true
	}
	for _, m := range sp.Members {
		if m.UserID == uid {
			return true
		}
	}
	return false
}

// parseServerProjects decodes the list response from GET /v1/projects.
// The API wraps results in {"items": [...]} or returns a bare array.
func parseServerProjects(raw map[string]any) ([]serverProject, error) {
	// Try {"items": [...]} first, then bare array at top level.
	var items []any
	if v, ok := raw["items"]; ok {
		if arr, ok := v.([]any); ok {
			items = arr
		}
	} else if v, ok := raw["projects"]; ok {
		if arr, ok := v.([]any); ok {
			items = arr
		}
	}

	if items == nil {
		// Bare-list: the map IS the container — iterate over nothing
		return nil, nil
	}

	b, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	var projects []serverProject
	return projects, json.Unmarshal(b, &projects)
}

// parseServerRepos decodes the repos array from a server project.
func parseServerRepos(raw json.RawMessage) []serverRepoEntry {
	if len(raw) == 0 {
		return nil
	}
	var repos []serverRepoEntry
	_ = json.Unmarshal(raw, &repos)
	return repos
}

// repoEntriesFromServer converts server repos to repoEntry display structs.
func repoEntriesFromServer(repos []serverRepoEntry) []repoEntry {
	entries := make([]repoEntry, 0, len(repos))
	for _, r := range repos {
		entries = append(entries, repoEntry{
			Name:            r.Name,
			Description:     r.Description,
			Positioning:     r.Positioning,
			TechStack:       r.TechStack,
			MainModules:     r.MainModules,
			ChangeScenarios: r.ChangeScenarios,
			GeneratedAt:     r.GeneratedAt,
			GeneratedCommit: r.GeneratedCommit,
		})
	}
	return entries
}

// cloneOrSync clones a repo if it doesn't exist, or fetch+reset if it does.
func cloneOrSync(repoDir, repoName, url string) {
	destPath := filepath.Join(repoDir, repoName)
	if _, err := os.Stat(destPath); err == nil {
		// Already exists — fetch + reset
		fetch := exec.Command("git", "-C", destPath, "fetch", "origin")
		fetch.Stdout = os.Stdout
		fetch.Stderr = os.Stderr
		if ferr := fetch.Run(); ferr != nil {
			fmt.Fprintf(os.Stderr, "pf init: fetch %s: %v (skipping reset)\n", repoName, ferr)
			return
		}
		// Detect the remote default branch dynamically (Bug 2 fix).
		defaultBranch := "origin/main" // fallback
		if headRef, herr := exec.Command("git", "-C", destPath, "symbolic-ref", "refs/remotes/origin/HEAD").Output(); herr == nil {
			ref := strings.TrimSpace(string(headRef))
			// "refs/remotes/origin/main" → "origin/main"
			parts := strings.SplitN(ref, "/", 4) // [refs, remotes, origin, main]
			if len(parts) == 4 {
				defaultBranch = "origin/" + parts[3]
			}
		}
		reset := exec.Command("git", "-C", destPath, "reset", "--hard", defaultBranch)
		reset.Stdout = os.Stdout
		reset.Stderr = os.Stderr
		if rerr := reset.Run(); rerr != nil {
			fmt.Fprintf(os.Stderr, "pf init: reset %s: %v\n", repoName, rerr)
		}
		fmt.Printf("ok synced .repo/%s\n", repoName)
		return
	}
	// Clone
	if err := runClone(url, destPath); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: clone %s: %v (skipping)\n", repoName, err)
		return
	}
	fmt.Printf("ok cloned %s → .repo/%s\n", url, repoName)
}

// runClone tries a plain git clone; if that fails for a github.com URL it
// retries using `gh auth token` as the credential.
func runClone(url, destPath string) error {
	cmd := exec.Command("git", "clone", url, destPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err == nil {
		return nil
	}
	// Retry with GH token for GitHub URLs (Bug 3 fix: also handles SSH URLs).
	if strings.Contains(url, "github.com") {
		tokenCmd := exec.Command("gh", "auth", "token")
		tokenBytes, terr := tokenCmd.Output()
		if terr == nil {
			token := strings.TrimSpace(string(tokenBytes))
			// Convert SSH URL to HTTPS so token injection works:
			// git@github.com:org/repo.git → https://github.com/org/repo.git
			httpsURL := url
			if strings.HasPrefix(url, "git@github.com:") {
				httpsURL = "https://github.com/" + strings.TrimPrefix(url, "git@github.com:")
			}
			// Inject token into HTTPS URL: https://TOKEN@github.com/...
			authedURL := strings.Replace(httpsURL, "https://", "https://"+token+"@", 1)
			cmd2 := exec.Command("git", "clone", authedURL, destPath)
			cmd2.Stdout = os.Stdout
			cmd2.Stderr = os.Stderr
			if err2 := cmd2.Run(); err2 == nil {
				return nil
			} else {
				return err2
			}
		}
	}
	// Re-run plain clone to surface the real error message.
	cmd3 := exec.Command("git", "clone", url, destPath)
	cmd3.Stdout = os.Stdout
	cmd3.Stderr = os.Stderr
	return cmd3.Run()
}

// runOwnerInit performs the owner-side init for a single project:
//  1. Reads local .polyforge.yaml repos.
//  2. Diffs against server repos: local-only → PATCH append; server-only → warning.
//  3. Clones/syncs all repos.
//  4. PATCHes server with merged repo list.
//  5. GETs refreshed project for CLAUDE.md block.
//
// Returns the CLAUDE.md block plus the reconciled repo list (server ∪ local-only
// appends) — the same list the clone loop walks, so .polyforge.yaml can be
// refreshed from it instead of from the pre-PATCH server snapshot (aihub#228).
func runOwnerInit(ctx context.Context, c *client.Client, cfg *config.Config, repoDir string, sp serverProject) (projectBlock, []serverRepoEntry) {
	localRepos := []config.Repo{}
	if cfg != nil {
		if lp, ok := cfg.Projects[sp.Name]; ok {
			localRepos = lp.Repos
		}
	}

	serverRepos := parseServerRepos(sp.Repos)

	// Build lookup maps.
	serverByName := make(map[string]serverRepoEntry, len(serverRepos))
	for _, r := range serverRepos {
		serverByName[r.Name] = r
	}
	localByName := make(map[string]config.Repo, len(localRepos))
	for _, r := range localRepos {
		localByName[r.Name] = r
	}

	// local-only → append to server list
	var toAppend []serverRepoEntry
	for _, lr := range localRepos {
		if lr.Name == "" || lr.URL == "" {
			continue
		}
		if _, exists := serverByName[lr.Name]; !exists {
			desc := lr.Description
			var descPtr *string
			if desc != "" {
				descPtr = &desc
			}
			toAppend = append(toAppend, serverRepoEntry{
				Name:        lr.Name,
				URL:         lr.URL,
				Description: descPtr,
			})
		}
	}

	// server-only → warning (exit 0)
	for _, sr := range serverRepos {
		if _, exists := localByName[sr.Name]; !exists {
			fmt.Fprintf(os.Stderr, "pf init: warning: repo %q is on server but not in .polyforge.yaml (skipping removal)\n", sr.Name)
		}
	}

	// Merged repo list = server + appended local-only
	merged := append(serverRepos, toAppend...)

	// Check if any existing repo has a changed description (Bug 1 fix).
	var hasDescriptionChanges bool
	for _, lr := range localRepos {
		if lr.Name == "" {
			continue
		}
		if sr, ok := serverByName[lr.Name]; ok {
			localDesc := lr.Description
			serverDesc := ""
			if sr.Description != nil {
				serverDesc = *sr.Description
			}
			if localDesc != serverDesc {
				hasDescriptionChanges = true
				// Propagate the local description into the merged list.
				if localDesc != "" {
					for i, r := range merged {
						if r.Name == lr.Name {
							desc := localDesc
							merged[i].Description = &desc
							break
						}
					}
				}
			}
		}
	}

	// Clone/sync all repos in merged list
	for _, r := range merged {
		if r.URL == "" {
			continue
		}
		cloneOrSync(repoDir, r.Name, r.URL)
	}

	// PATCH server if we have new repos to add or existing descriptions changed.
	if len(toAppend) > 0 || hasDescriptionChanges {
		reposJSON, jerr := json.Marshal(merged)
		if jerr == nil {
			patch := map[string]any{
				"repos": json.RawMessage(reposJSON),
			}
			if _, perr := c.UpdateProject(ctx, sp.Name, patch); perr != nil {
				fmt.Fprintf(os.Stderr, "pf init: PATCH project %q repos: %v\n", sp.Name, perr)
			} else {
				fmt.Printf("ok updated server repos for project %q (%d added, descriptions synced: %v)\n", sp.Name, len(toAppend), hasDescriptionChanges)
			}
		}
	}

	// GET refreshed project from server.
	raw, gerr := c.GetProject(ctx, sp.Name)
	var block projectBlock
	block.Name = sp.Name
	block.Description = sp.Description
	block.Repos = repoEntriesFromServer(merged)
	if gerr == nil && raw != nil {
		refreshed, perr := projectFromRaw(raw)
		if perr == nil {
			block.Description = refreshed.Description
			block.Repos = repoEntriesFromServer(parseServerRepos(refreshed.Repos))
		}
	}
	// The yaml gets `merged`, not the refreshed GET: if the PATCH above failed,
	// the refreshed response would be missing the local-only repos and writing
	// from it would delete them from the workspace config.
	return block, merged
}

// writePolyforgeYAML generates .polyforge.yaml as a local cache of the server's
// project+repo list. It applies the same callerHasRole filter as the clone loop
// so the cache declares only projects the caller has a role in — listing a
// visible project in GET /v1/projects does not imply the caller should treat its
// repos as part of their workspace.
//
// It runs on EVERY init, rewriting an existing file, which is what the generated
// header has always promised ("Re-run polyforge init to refresh"). Before
// aihub#228 the call was gated on os.IsNotExist, so a repo added to a project
// server-side never reached .polyforge.yaml and claim therefore never built a
// worktree for it.
//
// Refreshing an existing file must not destroy local state that the server does
// not carry, so three things are preserved from the file on disk:
//
//   - the aihub block — ResolveAihubURL() returns "" when there is no
//     POLYFORGE_AIHUB_URL and no ~/.polyforge/config.toml server URL, so
//     rebuilding it from scratch would blank a working workspace's endpoint;
//   - project blocks the server did not return (caller lost visibility, or a
//     hand-authored entry) — dropping them silently would break those repos;
//   - a project description when the server has none.
//
// For projects the server DID return, the repos list is replaced wholesale
// rather than merged, so a repo removed server-side does not linger.
//
// reconciledRepos optionally supplies a per-project repo list that overrides the
// project's server snapshot. The owner path needs this: runOwnerInit merges
// local-only repos into the server list and PATCHes them up, but the `projects`
// slice the caller holds is still the pre-PATCH GET response, so writing from it
// would drop the repo that was just appended.
func writePolyforgeYAML(path string, projects []serverProject, currentUserID string, reconciledRepos map[string][]serverRepoEntry) error {
	// Existing on-disk config, if any. A missing file is the normal first-init
	// case and not an error. A file that exists but does not parse is different:
	// everything this function preserves (the aihub block, unmanaged project
	// blocks) is about to be overwritten, so say so rather than silently
	// clobbering a file the user could otherwise have repaired by hand.
	prev, loadErr := config.Load(filepath.Dir(path))
	if loadErr != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			fmt.Fprintf(os.Stderr, "pf init: warning: %s exists but could not be parsed (%v); "+
				"rewriting it from the server — the previous aihub block and any project "+
				"entries the server does not return will be lost\n", path, loadErr)
		}
		prev = nil
	}

	cfg := config.Config{
		Version:  1,
		Projects: make(map[string]config.Project),
	}

	// Carry the aihub block over verbatim when it is already populated.
	if prev != nil {
		cfg.AIHub = prev.AIHub
	}
	if cfg.AIHub.URL == "" {
		mc, err := config.LoadMachineConfig()
		if err != nil {
			return err
		}
		cfg.AIHub.URL = mc.ResolveAihubURL()
	}

	// Preserve project blocks that the server did not return.
	if prev != nil {
		for name, p := range prev.Projects {
			cfg.Projects[name] = p
		}
	}

	for _, sp := range projects {
		if !sp.Visible {
			continue
		}
		// Mirror the role filter from the main init loop: only declare
		// projects the caller has a role in (owner or member). Public-visible
		// projects without a caller role are not cloned, so declaring them here
		// would produce spurious "missing repos" warnings.
		if !callerHasRole(sp, currentUserID) {
			continue
		}

		serverRepos, ok := reconciledRepos[sp.Name]
		if !ok {
			serverRepos = parseServerRepos(sp.Repos)
		}
		repos := make([]config.Repo, 0, len(serverRepos))
		for _, r := range serverRepos {
			var ghOwnerRepo, desc string
			if r.GithubOwnerRepo != nil {
				ghOwnerRepo = *r.GithubOwnerRepo
			}
			if r.Description != nil {
				desc = *r.Description
			}
			repos = append(repos, config.Repo{
				Name:            r.Name,
				URL:             r.URL,
				GithubOwnerRepo: ghOwnerRepo,
				Description:     desc,
			})
		}

		// Start from the existing block so fields the server does not own are
		// kept, then overwrite what the server is authoritative for.
		proj := cfg.Projects[sp.Name]
		proj.Repos = repos
		if sp.Scenario != nil {
			proj.Scenario = *sp.Scenario
		}
		if sp.Description != nil && *sp.Description != "" {
			proj.Description = *sp.Description
		}
		cfg.Projects[sp.Name] = proj
	}

	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := "# polyforge workspace config — auto-generated by pf init\n" +
		"# Source of truth is the server. Re-run polyforge init to refresh.\n\n"
	return os.WriteFile(path, append([]byte(header), b...), 0644)
}

// projectFromRaw decodes a GET /v1/projects/:name response.
func projectFromRaw(raw map[string]any) (*serverProject, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var sp serverProject
	return &sp, json.Unmarshal(b, &sp)
}

// runMemberInit performs the member-side init for a single project:
// uses server repos directly, clones/syncs them. Returns the CLAUDE.md block plus
// the repo list it cloned, so .polyforge.yaml can be refreshed from the same list.
func runMemberInit(repoDir string, sp serverProject) (projectBlock, []serverRepoEntry) {
	serverRepos := parseServerRepos(sp.Repos)
	for _, r := range serverRepos {
		if r.URL == "" {
			continue
		}
		cloneOrSync(repoDir, r.Name, r.URL)
	}
	return projectBlock{
		Name:        sp.Name,
		Description: sp.Description,
		Repos:       repoEntriesFromServer(serverRepos),
	}, serverRepos
}

// RunInit sets up (or repairs) the workspace: it ensures ~/.polyforge/config.toml,
// writes .polyforge/usage.md and the session-start hook, then iterates over all
// visible projects and performs per-project owner/member init, cloning repos and
// updating CLAUDE.md's managed block. (--apply is deprecated and a no-op.)
func RunInit(ctx context.Context, c *client.Client, cfg *config.Config, wsRoot string, args []string) {
	// Ensure ~/.polyforge/config.toml exists with a stable machine_id (§9.5.3).
	mc, err := config.EnsureMachineConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf init: config.toml: %v\n", err)
	} else if mc.Auth.APIKey == "" && mc.Auth.APIKeyEnv == "" {
		fmt.Fprintf(os.Stderr, "pf init: ~/.polyforge/config.toml created (machine_id=%s)\n", mc.MachineID)
		fmt.Fprintf(os.Stderr, "         Add your API key:\n")
		fmt.Fprintf(os.Stderr, "           [auth]\n")
		fmt.Fprintf(os.Stderr, "           api_key = \"your-key-here\"\n")
	}

	// --apply is deprecated.
	if len(args) > 0 && args[0] == "--apply" {
		fmt.Fprintln(os.Stderr, "polyforge init --apply is deprecated and has no effect. Use polyforge init instead.")
		return
	}

	phaseDir := filepath.Join(wsRoot, ".polyforge")
	if err := os.MkdirAll(phaseDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: mkdir .polyforge: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Join(phaseDir, "state"), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: mkdir .polyforge/state: %v\n", err)
	}

	// Write .polyforge/usage.md — polyforge v1 workspace guide.
	usageFile := filepath.Join(phaseDir, "usage.md")
	if err := writeUsageMd(usageFile); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: write usage.md: %v\n", err)
	} else {
		fmt.Printf("ok .polyforge/usage.md written\n")
	}

	// One-time cleanup of the legacy self-installed SessionStart hook. The
	// polyforge plugin now ships its own ${CLAUDE_PLUGIN_ROOT}/hooks/
	// pf-session-start, so the copy in ~/.claude/hooks/ is dead code that
	// aborted with exit 2 on every session start. Silent when there is
	// nothing to clean up.
	if removed, err := removeLegacySessionStartHook(); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: legacy session start hook cleanup: %v\n", err)
	} else if removed {
		// Deliberately does not claim the script itself was moved aside: this
		// also fires when only a dangling registration was cleared and no
		// script existed, and pointing users at a .bak that is not there is
		// worse than saying less.
		fmt.Printf("ok legacy pf-session-start hook cleaned up (superseded by the polyforge plugin hook)\n")
	}

	// Ensure .gitignore covers .polyforge.yaml and .polyforge/ secrets.
	gitignore := filepath.Join(wsRoot, ".gitignore")
	if err := ensureGitignore(gitignore); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: .gitignore: %v\n", err)
	}

	// --- Project-level init ---

	// Get current user ID from GET /v1/users/me.
	currentUserID := ""
	if me, merr := c.WhoAmI(ctx); merr == nil {
		if id, ok := me["user_id"].(string); ok {
			currentUserID = id
		} else if id, ok := me["id"].(string); ok {
			currentUserID = id
		}
	} else {
		fmt.Fprintf(os.Stderr, "pf init: whoami: %v (owner detection disabled)\n", merr)
	}

	// GET /v1/projects — list all visible projects.
	raw, lerr := c.ListProjects(ctx, nil)
	if lerr != nil {
		fmt.Fprintf(os.Stderr, "pf init: list projects: %v\n", lerr)
		// Still write CLAUDE.md ref even if projects fetch fails.
		claudeMd := filepath.Join(wsRoot, "CLAUDE.md")
		if uerr := ensureClaudeMdRef(claudeMd); uerr != nil {
			fmt.Fprintf(os.Stderr, "pf init: update CLAUDE.md: %v\n", uerr)
		}
		return
	}

	projects, perr := parseServerProjects(raw)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "pf init: parse projects: %v\n", perr)
		projects = nil
	}

	repoDir := filepath.Join(wsRoot, ".repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: mkdir .repo: %v\n", err)
		os.Exit(1)
	}

	var blocks []projectBlock
	// Per-project reconciled repo lists, keyed by project name — what each init
	// path actually cloned. .polyforge.yaml is refreshed from these rather than
	// from the pre-PATCH GET /v1/projects snapshot (aihub#228).
	reconciledRepos := make(map[string][]serverRepoEntry)
	for _, sp := range projects {
		if !sp.Visible {
			continue
		}
		// Only init projects where the caller has an explicit role (owner or
		// member). Public-visible projects without a caller role are skipped
		// — listing them in GET /v1/projects does not imply consent to clone.
		if !callerHasRole(sp, currentUserID) {
			continue
		}
		var blk projectBlock
		var repos []serverRepoEntry
		if currentUserID != "" && sp.OwnerUserID == currentUserID {
			blk, repos = runOwnerInit(ctx, c, cfg, repoDir, sp)
		} else {
			blk, repos = runMemberInit(repoDir, sp)
		}
		blocks = append(blocks, blk)
		reconciledRepos[sp.Name] = repos
	}

	// Refresh .polyforge.yaml from the server on every init — this is what the
	// generated header promises. Previously gated on the file not existing, which
	// meant repos added to a project server-side never reached the local config
	// and claim never built worktrees for them (aihub#228). The writer preserves
	// the aihub block, unmanaged project blocks, and local-only descriptions.
	// Guard on the post-role-filter set, not the raw server list: if the caller
	// holds no role in any returned project there is nothing authoritative to
	// write, and rewriting the file from an empty set would be pure churn.
	polyforgeYAMLPath := filepath.Join(wsRoot, ".polyforge.yaml")
	if len(reconciledRepos) > 0 {
		if werr := writePolyforgeYAML(polyforgeYAMLPath, projects, currentUserID, reconciledRepos); werr != nil {
			fmt.Fprintf(os.Stderr, "pf init: write .polyforge.yaml: %v\n", werr)
		} else {
			fmt.Printf("ok .polyforge.yaml refreshed from server (%d project(s))\n", len(reconciledRepos))
		}
	}

	// Write managed block to CLAUDE.md.
	claudeMd := filepath.Join(wsRoot, "CLAUDE.md")
	if err := upsertManagedBlock(claudeMd, blocks); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: update CLAUDE.md: %v\n", err)
	} else {
		fmt.Printf("ok CLAUDE.md managed block updated (%d project(s))\n", len(blocks))
	}

	// Ensure @.polyforge/usage.md reference is in CLAUDE.md.
	if err := ensureClaudeMdRef(claudeMd); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: update CLAUDE.md ref: %v\n", err)
	}

	// --- Scenario repo cloning ---
	// Clone scenario repos into .repo/ alongside other repos. Source of truth
	// is the server payload so this runs identically for owner and member
	// workspaces (members have no local .polyforge.yaml on first init).
	// Multiple projects sharing the same URL share one clone (dedup by URL).
	seen := map[string]bool{}
	for _, sp := range projects {
		if !sp.Visible || sp.Scenario == nil || *sp.Scenario == "" {
			continue
		}
		// Mirror the role filter from the main init loop: only clone scenarios
		// for projects the caller has a role in. Public-only projects do not
		// pull their scenario repo into .repo/.
		if !callerHasRole(sp, currentUserID) {
			continue
		}
		url := *sp.Scenario
		if seen[url] {
			continue
		}
		seen[url] = true
		name := scenarioRepoName(url)
		if name == "" || name == url {
			fmt.Fprintf(os.Stderr, "pf init: skipping scenario %q for project %s — expected a git URL (e.g. git@github.com:GMISWE/polyforge-coding.git)\n", url, sp.Name)
			continue
		}
		cloneOrSync(repoDir, name, url)
	}
}

// scenarioRepoName extracts a filesystem-safe repo name from a scenario URL.
// "git@github.com:GMISWE/polyforge-coding.git" → "polyforge-coding"
// "https://github.com/GMISWE/polyforge-coding.git" → "polyforge-coding"
func scenarioRepoName(url string) string {
	url = strings.TrimSuffix(url, ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		return url[i+1:]
	}
	return url
}

// writeUsageMd creates <wsRoot>/.polyforge/usage.md with the polyforge v1 workspace guide.
// This replaces the old .claude/polyforge.md pattern from polyforge-v3.
func writeUsageMd(path string) error {
	const content = `# polyforge v1 workspace guide

> **State authority = aihub PostgreSQL** at the URL in ~/.polyforge/config.toml.
> Per-wi task worktrees materialize at pf.<project>-<seq>/<repo>/ on /pf-work.

## Iron Rules

**IR1 — Work-item-gated writes**
Every git commit/push/PR and Edit/Write under .repo/ must happen inside a
claimed wi worktree (pf.<project>-<seq>/<repo>/). No env-var bypass.

**IR2 — Analyze obstacles; track blockers as wi's**
When you hit an obstacle, find the root cause. If it's a bug or out of
scope, create a wi to track it — don't route around it.

**IR3 — MCP unavailable → stop**
If the polyforge MCP can't reach aihub, stop and report. Do not fall back
to direct HTTP calls. Use /reload-plugins or restart to reconnect.

---

## Daily workflow

` + "```" + `bash
# Start work
/pf-work --goal "..."           # create + claim wi
/pf-work <wi_id>                # claim existing wi

# Check status
/pf-status                      # LCRS six-segment ready queue

# Layer 2 methodology (inside a claimed wi)
/pf-spec  /pf-plan  /pf-execute  /pf-retro

# Stop work
/pf-stop --pause                # release, keep state
/pf-stop --wrap                 # terminal success
/pf-stop --fail                 # terminal failure

# Misc
/pf-status <wi_id>              # single wi detail + timeline
` + "```" + `

---

## Wi creation rules

**All wi creation MUST go through the ` + "`" + `/pf-work` + "`" + ` skill**, whether human or AI.

- **dialog mode** (default): a human/AI decides to create a wi during a session discussion -> after creation, ask whether to claim it
- **silent mode**: an AI finds an issue mid-step -> when calling pf-work, state "silent mode" -> create and queue only, no prompt

Do not call the ` + "`" + `pf_create_work_item` + "`" + ` MCP tool directly; always go through pf-work for consistent behavior.

---

## ~/.polyforge/config.toml (machine-level)

` + "```" + `toml
machine_id = "<auto-generated UUID>"
[auth]
api_key = "your-key-here"
[server]
url = "http://your-aihub-host"

# Binary update channel (optional; default: stable)
# [binary]
# channel = "stable"   # stable | dev
` + "```" + `

---

> Generated by polyforge init. Edit this file to add workspace-specific notes.

---

## NL Routing

| intent | operation |
|--------|---------|
| what's ready today / dispatch work / ready queue | ` + "`" + `pf_get_ready_queue` + "`" + ` + fan-out subagents |
| what needs my decision / needs attention | ` + "`" + `pf_get_ready_queue` + "`" + ` → ` + "`" + `needs_human_session[]` + "`" + ` |
| begin / new task / new / start | ` + "`" + `/pf-work` + "`" + ` (Mode A) |
| claim + slug | ` + "`" + `/pf-work <slug>` + "`" + ` (Mode B) |
| resume + slug | ` + "`" + `/pf-work <slug> --resume` + "`" + ` (Mode C) |
| takeover + slug | ` + "`" + `/pf-work <slug> --force` + "`" + ` (Mode D) |
| pause | ` + "`" + `/pf-stop --pause` + "`" + ` |
| done / wrap / finished | ` + "`" + `/pf-stop --wrap` + "`" + ` |
| fail / abandon | ` + "`" + `/pf-stop --fail` + "`" + ` |
| status / progress | ` + "`" + `/pf-status` + "`" + ` |
| design / spec / brainstorm | ` + "`" + `/pf-spec` + "`" + ` |
| plan | ` + "`" + `/pf-plan` + "`" + ` |
| execute / run it | ` + "`" + `/pf-execute` + "`" + ` |
| retro | ` + "`" + `/pf-retro` + "`" + ` |
| this bug / debug | ` + "`" + `/pf-spec` + "`" + ` (debug variant) |
| note / log | ` + "`" + `pf_emit_event(event_type="note", ...)` + "`" + ` |
| init / setup workspace | ` + "`" + `/pf-init` + "`" + ` |
| doctor / can't connect | ` + "`" + `/pf-doctor` + "`" + ` |
| release / cut | ` + "`" + `/pf-release` + "`" + ` |

---

## Memory Type Reference

When calling ` + "`" + `pf_remember` + "`" + ` manually, pick the type by **consumer**, not by content description.
` + "`" + `experience.*` + "`" + ` is written automatically by pf-retro; for manual memories prefer ` + "`" + `rule.*` + "`" + ` / ` + "`" + `fact.*` + "`" + `.

| content | Type | recalled by which skills |
|------|------|-----------------|
| init/setup experience | ` + "`" + `experience.init` + "`" + ` | pf-init |
| bug patterns found during execution | ` + "`" + `experience.debug` + "`" + ` | pf-plan, pf-execute, pf-retro |
| an approach that solved a class of problem | ` + "`" + `experience.approach` + "`" + ` | pf-plan, pf-execute, pf-retro |
| pitfalls to avoid | ` + "`" + `experience.pitfall` + "`" + ` | pf-plan, pf-execute, pf-retro |
| wi lifecycle operating rules | ` + "`" + `rule.work` + "`" + ` | using-polyforge, pf-spec |
| init-phase operating rules | ` + "`" + `rule.init` + "`" + ` | pf-init |
| scheduling rules | ` + "`" + `rule.scheduling` + "`" + ` | pf-init (managed block) |
| domain facts | ` + "`" + `fact.<subtopic>` + "`" + ` | pf-spec |
| spec output | ` + "`" + `methodology.spec` + "`" + ` | pf-plan, pf-execute, pf-retro |
| plan output | ` + "`" + `methodology.plan` + "`" + ` | pf-execute, pf-retro |
| release record | ` + "`" + `methodology.release` + "`" + ` | pf-release |
`
	if _, err := os.Stat(path); err == nil {
		return nil // already exists — don't overwrite user edits
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// ensureGitignore ensures .gitignore contains .polyforge.yaml and .polyforge/ secrets.
func ensureGitignore(path string) error {
	const entry = ".polyforge.yaml"
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(b)
	if strings.Contains(content, entry) {
		return nil
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += entry + "\n"
	return os.WriteFile(path, []byte(content), 0644)
}

// ensureClaudeMdRef ensures CLAUDE.md references @.polyforge/usage.md.
// If CLAUDE.md already references @.claude/polyforge.md, replaces it.
// If CLAUDE.md doesn't exist yet, creates a minimal one.
func ensureClaudeMdRef(claudeMd string) error {
	const newRef = "@.polyforge/usage.md"
	const oldRef = "@.claude/polyforge.md"

	b, err := os.ReadFile(claudeMd)
	if os.IsNotExist(err) {
		// Create a minimal CLAUDE.md
		content := newRef + "\n"
		if writeErr := os.WriteFile(claudeMd, []byte(content), 0644); writeErr != nil {
			return writeErr
		}
		fmt.Printf("ok CLAUDE.md created with %s\n", newRef)
		return nil
	}
	if err != nil {
		return err
	}

	s := string(b)
	if strings.Contains(s, newRef) {
		return nil // already correct
	}
	if strings.Contains(s, oldRef) {
		s = strings.ReplaceAll(s, oldRef, newRef)
		if writeErr := os.WriteFile(claudeMd, []byte(s), 0644); writeErr != nil {
			return writeErr
		}
		fmt.Printf("ok CLAUDE.md updated: %s → %s\n", oldRef, newRef)
	}
	return nil
}

// legacySessionStartHookName is the hook file that `polyforge init` used to
// self-install into ~/.claude/hooks/. It is fully superseded by the
// plugin-bundled ${CLAUDE_PLUGIN_ROOT}/hooks/pf-session-start, which
// self-locates instead of hardcoding a plugin cache path.
const legacySessionStartHookName = "pf-session-start.sh"

// legacySessionStartHookMarker is the giveaway string inside the dead script:
// it pointed at the pre-rename `gmi-marketplace` plugin cache, so after the
// marketplace was renamed to `ieops-aihub` the path never resolved — and under
// `set -euo pipefail` the failed lookup aborted the hook with exit 2 and no
// stderr on every session start. Only a file containing this marker is ours to
// clean up; anything else at that path is the user's own hook, left untouched.
const legacySessionStartHookMarker = "plugins/cache/gmi-marketplace/polyforge"

// legacySessionStartHookBackupSuffix is appended to the legacy hook when it is
// moved aside. The cleanup renames rather than deletes so the removal stays
// reversible by hand.
const legacySessionStartHookBackupSuffix = ".removed-by-polyforge.bak"

// removeLegacySessionStartHook runs the one-time reverse cleanup of the legacy
// self-installed SessionStart hook against the current user's home directory.
// It reports whether anything was actually removed. Silent and idempotent: on
// an already-clean machine it returns (false, nil) without touching a byte.
func removeLegacySessionStartHook() (removed bool, err error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	return removeLegacySessionStartHookIn(homeDir)
}

// removeLegacySessionStartHookIn is removeLegacySessionStartHook with an
// explicit home directory, so the cleanup can be exercised against a fake HOME.
func removeLegacySessionStartHookIn(homeDir string) (removed bool, err error) {
	hookPath := filepath.Join(homeDir, ".claude", "hooks", legacySessionStartHookName)
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	body, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			// The script is already gone but its registration may not be —
			// e.g. someone stopped the bleeding by hand with `rm` and left
			// settings.json pointing at a path that no longer exists. That
			// dangling entry is exactly the "residual registration" the
			// cleanup is meant to clear, so drop it. Safe without the content
			// gate below: there is no file to gate on, and an entry naming a
			// missing script is broken no matter who wrote it.
			return removeSettingsHook(settingsPath, hookPath)
		}
		return false, err
	}
	// Content gate: never touch a hook we did not write.
	if !strings.Contains(string(body), legacySessionStartHookMarker) {
		return false, nil
	}

	// Unregister first, move the file aside second. If the second step fails
	// the leftover is an orphan script nothing invokes; the reverse order would
	// leave settings.json pointing at a missing file.
	if _, err := removeSettingsHook(settingsPath, hookPath); err != nil {
		return false, err
	}
	// Overwrites a pre-existing backup of the same name — it holds the same
	// dead content, and keeping the rename unconditional keeps this idempotent.
	if err := os.Rename(hookPath, hookPath+legacySessionStartHookBackupSuffix); err != nil {
		return false, err
	}
	return true, nil
}

// removeSettingsHook deletes the SessionStart hook entries whose command is
// exactly hookCmd from settings.json, leaving every sibling entry, the
// enclosing group (even when it ends up empty), other hook events and all
// unrelated top-level keys alone. It reports whether an entry was actually
// removed. A no-op — including a missing settings.json — writes nothing at all,
// so re-running init does not disturb the file's mtime.
func removeSettingsHook(settingsPath, hookCmd string) (removed bool, err error) {
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	var settings map[string]any
	if err := json.Unmarshal(b, &settings); err != nil {
		return false, err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	sessionStart, _ := hooks["SessionStart"].([]any)

	changed := false
	for _, grp := range sessionStart {
		g, _ := grp.(map[string]any)
		if g == nil {
			continue
		}
		entries, ok := g["hooks"].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(entries))
		for _, e := range entries {
			// Exact command match only — never substring or prefix.
			if m, _ := e.(map[string]any); m != nil {
				if cmd, _ := m["command"].(string); cmd == hookCmd {
					continue
				}
			}
			kept = append(kept, e)
		}
		if len(kept) != len(entries) {
			// Keep the group itself: pruning empty containers would edit more
			// of the user's file than this cleanup is entitled to.
			g["hooks"] = kept
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(settingsPath, append(out, '\n'), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so an interrupted write cannot leave settings.json
// truncated or half-written.
//
// The replacement inherits the existing file's permissions. The rename swaps in
// a new inode, so without this a user who ran `chmod 600 ~/.claude/settings.json`
// — reasonable, since that file can carry an env block with API keys — would
// silently get it widened back. fallbackPerm applies only when path does not
// exist yet.
func writeFileAtomic(path string, data []byte, fallbackPerm os.FileMode) error {
	perm := fallbackPerm
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// No-op once the rename below has succeeded.
	defer func() { _ = os.Remove(tmp) }()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	// Flush before the rename so a crash cannot leave the new name pointing at
	// an empty file on filesystems that would otherwise defer the data write.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

const managedBlockStart = `<!-- polyforge:managed:version="1.0" -->`
const managedBlockEnd = `<!-- /polyforge:managed -->`

// upsertManagedBlock writes or replaces the managed block in CLAUDE.md.
// The block is grouped per project with a description and repo table.
// oneLine collapses any embedded newlines/CRs into spaces so a field can't break
// the single-line markdown bullet it's rendered into. (Server validation only
// trims the ends; it doesn't reject interior newlines.)
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// Remote URLs are NOT included in the managed block.
// renderRepoBlock writes one repo's entry into the managed block. When the
// structured description is present it renders a positioning line plus stack /
// modules / typical-change bullets (for AI routing); otherwise it falls back to
// the legacy single-line description or a pending placeholder.
func renderRepoBlock(sb *strings.Builder, r repoEntry) {
	headline := ""
	switch {
	case r.Positioning != "":
		headline = oneLine(r.Positioning)
	case r.Description != nil && *r.Description != "":
		headline = oneLine(*r.Description)
	default:
		headline = "*(description pending — run /pf-init to generate)*"
	}
	fmt.Fprintf(sb, "- **%s**: %s\n", r.Name, headline)

	if !r.hasStructuredDesc() {
		return
	}
	if len(r.TechStack) > 0 {
		stack := make([]string, 0, len(r.TechStack))
		for _, t := range r.TechStack {
			stack = append(stack, oneLine(t))
		}
		fmt.Fprintf(sb, "  - stack: %s\n", strings.Join(stack, ", "))
	}
	if len(r.MainModules) > 0 {
		// Nested sub-list (one bullet per module) — far more scannable than a
		// long semicolon-joined line when a repo has many modules.
		sb.WriteString("  - modules:\n")
		for _, m := range r.MainModules {
			fmt.Fprintf(sb, "    - %s — %s\n", oneLine(m.Path), oneLine(m.Role))
		}
	}
	if len(r.ChangeScenarios) > 0 {
		// Nested sub-list, matching the modules style (a semicolon-joined line
		// clashes with the surrounding bullet layout).
		sb.WriteString("  - changes:\n")
		for _, c := range r.ChangeScenarios {
			fmt.Fprintf(sb, "    - %s\n", oneLine(c))
		}
	}
	if line := generatedLine(r); line != "" {
		fmt.Fprintf(sb, "  - generated: %s\n", line)
	}
}

// generatedLine formats the freshness metadata as "<date> @ <short-sha>", using
// whichever parts are present. generated_at is an RFC3339 timestamp (we keep the
// date), generated_commit is a full SHA (we shorten to 7 chars).
func generatedLine(r repoEntry) string {
	date := r.GeneratedAt
	if len(date) >= 10 {
		date = date[:10] // YYYY-MM-DD from RFC3339
	}
	commit := r.GeneratedCommit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	switch {
	case date != "" && commit != "":
		return date + " @ " + commit
	case date != "":
		return date
	case commit != "":
		return "@ " + commit
	default:
		return ""
	}
}

func upsertManagedBlock(claudeMd string, blocks []projectBlock) error {
	// Build the managed block content.
	var sb strings.Builder
	sb.WriteString(managedBlockStart + "\n")
	sb.WriteString("## Workspace\n")
	for _, blk := range blocks {
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "### %s\n", blk.Name)
		if blk.Description != nil && *blk.Description != "" {
			fmt.Fprintf(&sb, "%s\n", *blk.Description)
		} else {
			sb.WriteString("*(description pending — ask project owner to run polyforge init)*\n")
		}
		sb.WriteString("\n")
		for _, r := range blk.Repos {
			renderRepoBlock(&sb, r)
		}
	}
	sb.WriteString(managedBlockEnd + "\n")
	block := sb.String()

	// Read existing CLAUDE.md (or create from scratch).
	existing := ""
	b, err := os.ReadFile(claudeMd)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		existing = string(b)
	}

	startIdx := strings.Index(existing, managedBlockStart)
	endIdx := strings.Index(existing, managedBlockEnd)

	var updated string
	if startIdx >= 0 && endIdx >= 0 && endIdx > startIdx {
		// Replace existing managed block (including the end tag + trailing newline).
		endTagEnd := endIdx + len(managedBlockEnd)
		if endTagEnd < len(existing) && existing[endTagEnd] == '\n' {
			endTagEnd++
		}
		updated = existing[:startIdx] + block + existing[endTagEnd:]
	} else {
		// Append managed block at the end.
		if existing != "" && !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		updated = existing + "\n" + block
	}

	return os.WriteFile(claudeMd, []byte(updated), 0644)
}
