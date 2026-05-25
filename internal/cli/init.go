package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// repoEntry holds a repo name and description for the managed block.
type repoEntry struct {
	Name        string
	Description *string
}

// projectBlock groups a project's display data for the managed block.
type projectBlock struct {
	Name        string
	Description *string
	Repos       []repoEntry
}

// serverRepoEntry mirrors the JSON shape stored in domain.Project.Repos.
type serverRepoEntry struct {
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	GithubOwnerRepo *string `json:"github_owner_repo,omitempty"`
	Description     *string `json:"description,omitempty"`
}

// serverProject mirrors the JSON response shape from GET /v1/projects.
type serverProject struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	OwnerUserID string          `json:"owner_user_id"`
	Visible     bool            `json:"visible"`
	Repos       json.RawMessage `json:"repos"`
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
		entries = append(entries, repoEntry{Name: r.Name, Description: r.Description})
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
func runOwnerInit(ctx context.Context, c *client.Client, cfg *config.Config, repoDir string, sp serverProject) projectBlock {
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
	return block
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
// uses server repos directly, clones/syncs them.
func runMemberInit(repoDir string, sp serverProject) projectBlock {
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
	}
}

// RunInit fetches the scenario phase config from aihub and writes it to
// <wsRoot>/.polyforge/phase.yaml.  It then iterates over all visible
// projects and performs per-project owner/member init, cloning repos and
// updating CLAUDE.md's managed block.
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

	// Write .polyforge/usage.md — polyforge v1 workspace guide.
	usageFile := filepath.Join(phaseDir, "usage.md")
	if err := writeUsageMd(usageFile); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: write usage.md: %v\n", err)
	} else {
		fmt.Printf("ok .polyforge/usage.md written\n")
	}

	// Write pf-session-start.sh and register it in ~/.claude/settings.json.
	if err := ensureSessionStartHook(); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: session start hook: %v\n", err)
	} else {
		fmt.Printf("ok ~/.claude/hooks/pf-session-start.sh registered\n")
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
	for _, sp := range projects {
		if !sp.Visible {
			continue
		}
		var blk projectBlock
		if currentUserID != "" && sp.OwnerUserID == currentUserID {
			blk = runOwnerInit(ctx, c, cfg, repoDir, sp)
		} else {
			blk = runMemberInit(repoDir, sp)
		}
		blocks = append(blocks, blk)
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
	// For each project that has a Scenario URL in .polyforge.yaml, clone/sync
	// the scenario repo into a URL-keyed cache and symlink it per project.
	if cfg != nil {
		syncScenarioRepos(wsRoot, cfg)
	}
}

// syncScenarioRepos clones or updates scenario repos for all projects that
// declare a Scenario URL in .polyforge.yaml.  Multiple projects sharing the
// same URL deduplicate to a single cache directory.
//
// Layout:
//
//	<wsRoot>/.polyforge/scenarios/_cache/<urlhash>/  — bare clone cache
//	<wsRoot>/.polyforge/scenarios/<project>/         — symlink → cache
func syncScenarioRepos(wsRoot string, cfg *config.Config) {
	cacheDir := filepath.Join(wsRoot, ".polyforge", "scenarios", "_cache")
	linkDir := filepath.Join(wsRoot, ".polyforge", "scenarios")

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "pf init: mkdir scenarios cache: %v\n", err)
		return
	}

	for projName, proj := range cfg.Projects {
		if proj.Scenario == "" {
			continue
		}
		url := proj.Scenario
		hash := scenarioURLHash(url)
		cachePath := filepath.Join(cacheDir, hash)

		// Clone or fetch-reset the scenario repo.
		if _, statErr := os.Stat(filepath.Join(cachePath, ".git")); os.IsNotExist(statErr) {
			cmd := exec.Command("git", "clone", url, cachePath)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "pf init: clone scenario %s: %v\n", url, err)
				continue
			}
		} else {
			fetch := exec.Command("git", "-C", cachePath, "fetch", "origin")
			fetch.Stderr = os.Stderr
			fetch.Run() //nolint:errcheck — best-effort
			reset := exec.Command("git", "-C", cachePath, "reset", "--hard", "origin/HEAD")
			reset.Stderr = os.Stderr
			reset.Run() //nolint:errcheck
		}

		// Create symlink <wsRoot>/.polyforge/scenarios/<project> → <cachePath>
		linkPath := filepath.Join(linkDir, projName)
		_ = os.Remove(linkPath) // remove stale link
		if err := os.Symlink(cachePath, linkPath); err != nil && !os.IsExist(err) {
			fmt.Fprintf(os.Stderr, "pf init: symlink scenario for %s: %v\n", projName, err)
			continue
		}
		fmt.Printf("ok synced scenario for project %q\n", projName)
	}
}

// scenarioURLHash returns a short deterministic hash of a URL for use as a
// cache directory name.  Uses the first 16 hex chars of SHA-256(url).
func scenarioURLHash(url string) string {
	// Import crypto/sha256 inline to avoid a new import block.
	h := scenarioSHA256(url)
	if len(h) > 16 {
		return h[:16]
	}
	return h
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

## ~/.polyforge/config.toml (machine-level)

` + "```" + `toml
machine_id = "<auto-generated UUID>"
[auth]
api_key = "your-key-here"
[server]
url = "http://your-aihub-host"
` + "```" + `

---

> Generated by polyforge init. Edit this file to add workspace-specific notes.

---

## NL Routing

| 说什么 | 对应操作 |
|--------|---------|
| 今天有哪些活 / 派活 / ready queue | ` + "`" + `pf_get_ready_queue` + "`" + ` + fan-out subagents |
| 哪些活需要我拍板 / needs attention | ` + "`" + `pf_get_ready_queue` + "`" + ` → ` + "`" + `needs_human_session[]` + "`" + ` |
| 开始 / 新任务 / new / start | ` + "`" + `/pf-work` + "`" + ` (Mode A) |
| 认领 / claim + slug | ` + "`" + `/pf-work <slug>` + "`" + ` (Mode B) |
| 继续 / resume + slug | ` + "`" + `/pf-work <slug> --resume` + "`" + ` (Mode C) |
| 接管 / takeover + slug | ` + "`" + `/pf-work <slug> --force` + "`" + ` (Mode D) |
| 暂停 / pause | ` + "`" + `/pf-stop --pause` + "`" + ` |
| 完成 / done / wrap / 搞定 | ` + "`" + `/pf-stop --wrap` + "`" + ` |
| 失败 / abandon | ` + "`" + `/pf-stop --fail` + "`" + ` |
| 状态 / status / 进度 | ` + "`" + `/pf-status` + "`" + ` |
| 设计 / spec / brainstorm | ` + "`" + `/pf-spec` + "`" + ` |
| 计划 / plan | ` + "`" + `/pf-plan` + "`" + ` |
| 执行 / execute / run it | ` + "`" + `/pf-execute` + "`" + ` |
| 回顾 / retro | ` + "`" + `/pf-retro` + "`" + ` |
| 这个 bug / 调试 / debug | ` + "`" + `/pf-spec` + "`" + ` (debug variant) |
| 记录 / note / log | ` + "`" + `pf_emit_event(event_type="note", ...)` + "`" + ` |
| 初始化 / init / setup workspace | ` + "`" + `/pf-init` + "`" + ` |
| 诊断 / doctor / 连不上 | ` + "`" + `/pf-doctor` + "`" + ` |
| 发布 / release / cut | ` + "`" + `/pf-release` + "`" + ` |

---

## Memory Type Reference

手动调用 ` + "`" + `pf_remember` + "`" + ` 时，按**消费方**选 type，不按内容描述选。
` + "`" + `experience.*` + "`" + ` 由 pf-retro 自动写入，手动存记忆优先用 ` + "`" + `rule.*` + "`" + ` / ` + "`" + `fact.*` + "`" + `。

| 内容 | Type | 被哪些 skill 召回 |
|------|------|-----------------|
| init/setup 经验 | ` + "`" + `experience.init` + "`" + ` | pf-init |
| 执行中发现的 bug 模式 | ` + "`" + `experience.debug` + "`" + ` | pf-plan, pf-execute, pf-retro |
| 成功解决某类问题的方案 | ` + "`" + `experience.approach` + "`" + ` | pf-plan, pf-execute, pf-retro |
| 需要避开的坑 | ` + "`" + `experience.pitfall` + "`" + ` | pf-plan, pf-execute, pf-retro |
| wi 生命周期操作规则 | ` + "`" + `rule.work` + "`" + ` | using-polyforge, pf-spec |
| init 阶段操作规则 | ` + "`" + `rule.init` + "`" + ` | pf-init |
| 调度/排期规则 | ` + "`" + `rule.scheduling` + "`" + ` | pf-init (managed block) |
| 领域事实 | ` + "`" + `fact.<subtopic>` + "`" + ` | pf-spec |
| spec 产出 | ` + "`" + `methodology.spec` + "`" + ` | pf-plan, pf-execute, pf-retro |
| plan 产出 | ` + "`" + `methodology.plan` + "`" + ` | pf-execute, pf-retro |
| release 记录 | ` + "`" + `methodology.release` + "`" + ` | pf-release |
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

// pfSessionStartScript is written to ~/.claude/hooks/pf-session-start.sh.
// It injects the using-polyforge SKILL.md into the Claude Code session context
// whenever the session is opened inside a polyforge workspace.
const pfSessionStartScript = `#!/usr/bin/env bash
# SessionStart hook: inject polyforge v1 using-polyforge SKILL.md as
# additionalContext when the session is opened inside a polyforge workspace.

set -euo pipefail

# Walk up from $PWD looking for .polyforge.yaml.
dir="${CLAUDE_PROJECT_DIR:-$PWD}"
in_workspace=0
while [ -n "$dir" ] && [ "$dir" != "/" ]; do
  if [ -f "$dir/.polyforge.yaml" ]; then
    in_workspace=1
    break
  fi
  parent="$(dirname "$dir")"
  [ "$parent" = "$dir" ] && break
  dir="$parent"
done
[ "$in_workspace" = "0" ] && exit 0

# Locate the most recently modified polyforge v1 plugin install.
plugin_base="$HOME/.claude/plugins/cache/gmi-marketplace/polyforge"
plugin_root="$(ls -td "$plugin_base"/*/ 2>/dev/null | head -1)"
plugin_root="${plugin_root%/}"
[ -z "$plugin_root" ] && exit 0

skill="$plugin_root/skills/using-polyforge/SKILL.md"
[ -f "$skill" ] || exit 0

SKILL_PATH="$skill" python3 <<'PY'
import json, os
skill = open(os.environ["SKILL_PATH"]).read()
ctx = (
    "<EXTREMELY_IMPORTANT>\n"
    "You are in a polyforge workspace (.polyforge.yaml detected). "
    "Lifecycle skills /pf-* and ` + "`" + `mcp__plugin_polyforge_polyforge__*` + "`" + ` MCP tools are "
    "authoritative; do not bypass them with raw git / Edit / Bash on work_items.\n\n"
    "**Below is the full content of the ` + "`" + `using-polyforge` + "`" + ` skill:**\n\n"
    + skill
    + "\n</EXTREMELY_IMPORTANT>"
)
print(json.dumps({
    "hookSpecificOutput": {
        "hookEventName": "SessionStart",
        "additionalContext": ctx,
    }
}))
PY
`

// ensureSessionStartHook writes pf-session-start.sh to ~/.claude/hooks/ and
// registers it in ~/.claude/settings.json. Idempotent.
func ensureSessionStartHook() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	hooksDir := filepath.Join(homeDir, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return err
	}

	hookPath := filepath.Join(hooksDir, "pf-session-start.sh")
	if err := os.WriteFile(hookPath, []byte(pfSessionStartScript), 0755); err != nil {
		return err
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	return ensureSettingsHook(settingsPath, hookPath)
}

// ensureSettingsHook adds hookCmd to the SessionStart hooks in settings.json.
// Idempotent: skips if the command is already registered.
func ensureSettingsHook(settingsPath, hookCmd string) error {
	var settings map[string]any

	b, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(b, &settings); err != nil {
			return err
		}
	} else {
		settings = make(map[string]any)
	}

	// Navigate to hooks.SessionStart[0].hooks, creating the path if needed.
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}

	sessionStart, _ := hooks["SessionStart"].([]any)
	if len(sessionStart) == 0 {
		sessionStart = []any{map[string]any{"hooks": []any{}}}
		hooks["SessionStart"] = sessionStart
	}

	group, _ := sessionStart[0].(map[string]any)
	if group == nil {
		group = map[string]any{"hooks": []any{}}
		sessionStart[0] = group
	}

	entries, _ := group["hooks"].([]any)

	// Check if already registered.
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			if m["command"] == hookCmd {
				return nil // already present
			}
		}
	}

	// Append the new hook entry.
	entries = append(entries, map[string]any{
		"type":    "command",
		"command": hookCmd,
		"timeout": 5,
	})
	group["hooks"] = entries

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, append(out, '\n'), 0644)
}

const managedBlockStart = `<!-- polyforge:managed:version="1.0" -->`
const managedBlockEnd = `<!-- /polyforge:managed -->`

// upsertManagedBlock writes or replaces the managed block in CLAUDE.md.
// The block is grouped per project with a description and repo table.
// Remote URLs are NOT included in the managed block.
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
		if len(blk.Repos) > 0 {
			sb.WriteString("| repo | description |\n")
			sb.WriteString("|------|-------------|\n")
			for _, r := range blk.Repos {
				desc := "*(pending)*"
				if r.Description != nil && *r.Description != "" {
					desc = *r.Description
				}
				fmt.Fprintf(&sb, "| %s | %s |\n", r.Name, desc)
			}
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

// scenarioSHA256 returns the hex-encoded SHA-256 of s.
func scenarioSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
