package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	// Write the repo maps BEFORE slimming the managed block (aihub#291).
	// Order matters for failure, not for success: the block is only useful
	// alongside the maps it points at, so writing the maps first means a failure
	// here leaves the workspace exactly as it was, instead of leaving it worse
	// than before init ran — block already slimmed, detail nowhere on disk.
	// Both are rendered from the same `blocks`, so the one-line positioning and
	// the detail can never come from different snapshots of the server data.
	if err := writeRepoMaps(phaseDir, blocks); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: write .polyforge/repo-map: %v\n", err)
	} else if len(blocks) > 0 {
		fmt.Printf("ok .polyforge/repo-map/ written (%d project(s))\n", len(blocks))
	}

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

// skillOwnedUsageSections are the `## ` headings this template used to emit and that the
// plugin-versioned using-polyforge skill now owns outright (aihub#294).
//
// Two channels carry polyforge's rules into a session, and their properties are exact
// inverses:
//
//	.polyforge/usage.md     workspace-scoped, user-owned, no size cap — and NEVER
//	                        regenerated (the os.Stat guard at the foot of writeUsageMd).
//	                        A wrong rule here cannot be corrected in the field.
//	fragments/*.md under    plugin-versioned, injected by hooks/pf-session-start on every
//	the using-polyforge     session, hard 10,000-character budget. A wrong rule here is
//	skill                   corrected by the next plugin release.
//
// So rules belong on the fragment channel and only workspace/machine specifics belong in
// usage.md. A second copy in usage.md is not redundancy, it is a divergence generator: the
// copy that gets maintained is not the copy that can be fixed where it runs. That is not
// hypothetical — IR1's worktree path format was wrong in one copy for three months, and
// this workspace's own usage.md is still the pre-translation template from 2026-05-25.
//
// This list is the SINGLE source for three consumers, so they cannot drift apart:
// writeUsageMd must not emit these headings, checkUsageMd reports an existing file that
// still carries them, and TestNoRuleSectionIsDeliveredTwice asserts the first.
var skillOwnedUsageSections = []string{
	"## Iron Rules",
	"## NL Routing",
	"## Memory Type Reference",
}

// writeUsageMd creates <wsRoot>/.polyforge/usage.md with the polyforge v1 workspace guide.
// This replaces the old .claude/polyforge.md pattern from polyforge-v3.
//
// Deliberately carries no rule text — see skillOwnedUsageSections for why. The existence
// guard below means this function can only ever fix workspaces created after it ships;
// existing ones are handled by checkUsageMd in doctor.go.
func writeUsageMd(path string) error {
	const content = `# polyforge v1 workspace guide

> **State authority = aihub PostgreSQL** at the URL in ~/.polyforge/config.toml.
> Per-wi task worktrees materialize at pf.<project>-<seq>/<repo>/ on /pf-work.

> **No rule lives in this file.** The Iron Rules (IR1-IR3), NL Routing and the
> memory-type vocabulary ship with the ` + "`" + `using-polyforge` + "`" + ` skill and are injected at
> session start, so a correction reaches every workspace on the next plugin update.
> ` + "`" + `polyforge init` + "`" + ` never rewrites this file once it exists — it is yours to edit —
> and that is exactly why no rule may be kept here: a copy parked on this channel
> can never be fixed. Read ` + "`" + `fragments/` + "`" + ` under the skill dir for the full text.

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

# Binary update channel (optional; "dev" is the only published channel and the
# default, so you normally leave this out entirely)
# [binary]
# channel = "dev"
` + "```" + `

---

> Generated by polyforge init. Edit this file to add workspace-specific notes.
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
		return nil
	}

	// Neither ref present. Returning nil here used to be the NORMAL outcome on a fresh
	// workspace, not an edge case: RunInit calls upsertManagedBlock first, which creates
	// CLAUDE.md when it is absent, so the os.IsNotExist branch above is unreachable on
	// the main path and this one ran against a file holding only a managed block — and
	// the import line was never written at all. Prepend it: the managed block is spliced
	// by marker index, so anything above it survives the next init.
	if s != "" && !strings.HasPrefix(s, "\n") {
		s = "\n" + s
	}
	s = newRef + "\n" + s
	if writeErr := os.WriteFile(claudeMd, []byte(s), 0644); writeErr != nil {
		return writeErr
	}
	fmt.Printf("ok CLAUDE.md: added %s\n", newRef)
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
// renderRepoBlock writes one repo's entry into the managed block: exactly one
// line, `- **<name>**: <positioning>`.
//
// It used to also emit stack / modules / changes / generated bullets. Those
// moved to .polyforge/repo-map/<project>.md (renderRepoMap) in aihub#291: the
// managed block is injected into CLAUDE.md at context position 0, so it is
// re-read on every single request and compaction cannot drop it, while that
// detail is only needed at the moment of routing a task to a repo. Measured on
// this workspace, the detail was 29,650 of the block's 34,606 bytes.
//
// Keep this function emitting a single line. If you are tempted to add a
// bullet here, add it to renderRepoMap instead.
func renderRepoBlock(sb *strings.Builder, r repoEntry) {
	fmt.Fprintf(sb, "- **%s**: %s\n", r.Name, repoHeadline(r))
}

// repoHeadline is the one-line positioning shown for a repo, with the legacy
// description and the pending placeholder as fallbacks. Shared by the managed
// block and the repo map so the two can never disagree about a repo's identity.
func repoHeadline(r repoEntry) string {
	switch {
	case r.Positioning != "":
		return oneLine(r.Positioning)
	case r.Description != nil && *r.Description != "":
		return oneLine(*r.Description)
	default:
		return "*(description pending — run /pf-init to generate)*"
	}
}

// repoMapDirName is the .polyforge subdirectory holding the per-project repo
// maps. One file per project (not per repo, and not one combined file) so a
// routing read costs the relevant project's few KB instead of all of them.
const repoMapDirName = "repo-map"

// renderRepoMap renders one project's on-demand repo map: the detail that used
// to live inline in CLAUDE.md's managed block. Every repo in the project gets a
// section — including repos with no structured description — so the file is a
// complete list and a reader can tell "this repo has no detail yet" apart from
// "this repo is missing from the map".
func renderRepoMap(blk projectBlock) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Repo map — %s\n\n", blk.Name)
	sb.WriteString("> Generated by `polyforge init` — do not edit by hand. Every `*.md` in this\n")
	sb.WriteString("> directory is rewritten on each init, and any `*.md` that no longer\n")
	sb.WriteString("> corresponds to a project is DELETED. Keep nothing of your own here.\n")
	sb.WriteString("> On-demand routing detail for this project's repos. CLAUDE.md's `## Workspace`\n")
	sb.WriteString("> block carries only each repo's one-line positioning; read this file when you\n")
	sb.WriteString("> need `tech_stack` / `main_modules` / `change_scenarios`.\n")
	if blk.Description != nil && *blk.Description != "" {
		fmt.Fprintf(&sb, "\n%s\n", oneLine(*blk.Description))
	}
	for _, r := range blk.Repos {
		fmt.Fprintf(&sb, "\n## %s\n\n%s\n", r.Name, repoHeadline(r))
		if !r.hasStructuredDesc() {
			continue
		}
		sb.WriteString("\n")
		if len(r.TechStack) > 0 {
			stack := make([]string, 0, len(r.TechStack))
			for _, t := range r.TechStack {
				stack = append(stack, oneLine(t))
			}
			fmt.Fprintf(&sb, "- stack: %s\n", strings.Join(stack, ", "))
		}
		if len(r.MainModules) > 0 {
			// Nested sub-list (one bullet per module) — far more scannable than
			// a long semicolon-joined line when a repo has many modules.
			sb.WriteString("- modules:\n")
			for _, m := range r.MainModules {
				fmt.Fprintf(&sb, "  - %s — %s\n", oneLine(m.Path), oneLine(m.Role))
			}
		}
		if len(r.ChangeScenarios) > 0 {
			sb.WriteString("- changes:\n")
			for _, c := range r.ChangeScenarios {
				fmt.Fprintf(&sb, "  - %s\n", oneLine(c))
			}
		}
		if line := generatedLine(r); line != "" {
			fmt.Fprintf(&sb, "- generated: %s\n", line)
		}
	}
	return sb.String()
}

// repoMapFileName maps a project name to its repo-map filename. Project names
// come from the server, so the result is constrained to a flat, safe basename:
// anything outside [A-Za-z0-9._-] becomes '-', and leading/trailing dots and
// dashes are trimmed so "." / ".." / hidden files can never be produced.
// Returns "" when nothing usable remains, in which case the project is skipped.
func repoMapFileName(project string) string {
	var b strings.Builder
	for _, r := range project {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), ".-")
	if name == "" {
		return ""
	}
	return name + ".md"
}

// writeRepoMaps writes <phaseDir>/repo-map/<project>.md for every rendered
// project and prunes maps for projects that are no longer present, so a project
// removed server-side cannot leave a stale map that routing would still read.
//
// Deliberately best-effort per project rather than fail-stop. By the time this
// runs, CLAUDE.md has already been slimmed, so one unwritable project must not
// cost every *other* project its map — that combination (slim block, no maps)
// leaves the detail unavailable locally with nothing pointing at the cause.
// Errors are collected and returned together after every project has had a try.
//
// Pruning removes only *.md files, only ones we did not just write, and only
// when at least one write succeeded: a transient failure of GET /v1/projects (or
// of the writes themselves) must never delete the maps already on disk.
func writeRepoMaps(phaseDir string, blocks []projectBlock) error {
	dir := filepath.Join(phaseDir, repoMapDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var errs []error
	written := 0
	// keep = every filename this render lays claim to, whether or not the write
	// succeeded. A map whose rewrite just failed is stale, but deleting it too
	// would only widen the outage, so it is kept out of the prune set.
	keep := make(map[string]bool, len(blocks))
	for _, blk := range blocks {
		name := repoMapFileName(blk.Name)
		if name == "" {
			errs = append(errs, fmt.Errorf("project %q: no usable repo-map filename", blk.Name))
			continue
		}
		keep[name] = true
		if err := os.WriteFile(filepath.Join(dir, name), []byte(renderRepoMap(blk)), 0644); err != nil {
			errs = append(errs, fmt.Errorf("project %q: %w", blk.Name, err))
			continue
		}
		written++
	}

	// Only prune once something was actually written: if every write failed we
	// cannot tell a removed project from a broken render, and deleting on that
	// evidence would destroy a working map set.
	if written > 0 {
		entries, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
		} else {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || keep[e.Name()] {
					continue
				}
				if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// managedBlockOf returns the managed block of a CLAUDE.md body, markers
// included, and whether a complete one was found. An unterminated block does
// not count — callers use this to classify block *content*, and half a block
// cannot be classified.
func managedBlockOf(claudeMd string) (string, bool) {
	start := strings.Index(claudeMd, managedBlockStart)
	if start < 0 {
		return "", false
	}
	end := strings.Index(claudeMd[start:], managedBlockEnd)
	if end < 0 {
		return "", false
	}
	return claudeMd[start : start+end+len(managedBlockEnd)], true
}

// managedBlockProjects lists the project names a managed block actually renders,
// read from its `### <name>` headings.
//
// This is deliberately NOT taken from .polyforge.yaml: init only renders
// projects the caller has a role in (callerHasRole), while .polyforge.yaml can
// still carry others. Classifying by the config would report a missing repo map
// for a project that was never supposed to have one.
func managedBlockProjects(block string) []string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		if name := strings.TrimPrefix(line, "### "); name != line {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

// legacyDetailMarkers are the indented bullets that only the pre-aihub#291
// renderer emitted. Detection keys on these rather than on the block's size: a
// workspace with many repos legitimately has a large *slim* block.
//
// All four are listed because each is independently optional upstream — a repo
// with tech_stack and change_scenarios but no main_modules renders "  - stack:"
// and "  - changes:" and never "  - modules:", and keying on modules alone would
// call that fat block slim.
var legacyDetailMarkers = []string{
	"\n  - modules:",
	"\n  - stack:",
	"\n  - changes:",
	"\n  - generated:",
}

// blockIsLegacyFormat reports whether a managed block still inlines the
// per-repo detail that now belongs in .polyforge/repo-map/.
func blockIsLegacyFormat(block string) bool {
	for _, m := range legacyDetailMarkers {
		if strings.Contains(block, m) {
			return true
		}
	}
	return false
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
			// oneLine, like every other field rendered into this block: a
			// description carrying a newline would otherwise be able to forge
			// block structure — a literal "\n  - modules:" makes a freshly
			// rendered slim block classify as legacy forever (so doctor keeps
			// warning and re-running init never clears it), and a "### " line
			// forges a whole project.
			fmt.Fprintf(&sb, "%s\n", oneLine(*blk.Description))
		} else {
			sb.WriteString("*(description pending — ask project owner to run polyforge init)*\n")
		}
		// Point at the detail from inside the generated block itself, rather than
		// describing the layout in the session-start skill text (aihub#291).
		// Two reasons this is the better seam:
		//   - the pointer and the file it names are written by the same function
		//     in the same pass, so they cannot drift out of sync and there is no
		//     version-skew case left for prose to describe;
		//   - skill fragments are injected on EVERY session under a hard
		//     10,000-character budget (aihub#285), while this line is paid once,
		//     in a block that just shed ~29 KB.
		if name := repoMapFileName(blk.Name); name != "" {
			fmt.Fprintf(&sb, "> Repo detail (stack / modules / changes): `%s`\n",
				filepath.ToSlash(filepath.Join(".polyforge", repoMapDirName, name)))
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
