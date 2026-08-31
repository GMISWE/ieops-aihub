# polyforge onboarding

A newcomer guide to going from "zero" to "I just created my first work item
through Claude Code." Aimed at engineers joining the team — not server
operators (see `README.md` for `aihub` server setup).

The end-to-end flow is:

1. [Get an API key from the owner](#1-get-an-api-key)
2. [Pre-write `~/.polyforge/config.toml`](#2-pre-write-polyforgeconfigtoml)
3. [GitHub access (`gh` CLI + SSH key)](#3-github-access-gh-cli--ssh-key)
4. [Install the marketplace + plugin in Claude Code](#4-install-the-marketplace--plugin-in-claude-code)
5. [Restart Claude Code and verify with `pf_whoami`](#5-restart-claude-code-and-verify)
6. [(Optional) Switch to the dev channel or build from source](#6-optional-switch-to-the-dev-channel-or-build-from-source)
7. [Demo: create a work item and view it in the Web UI](#7-demo-create-a-work-item)

The team runs a shared `aihub` server at `http://10.146.0.16:8080`, so you do
not need to stand up your own backend.

---

## 1. Get an API key

Ask the owner for a key tied to your account. You will receive a token that
looks like `pf_k1_…` — treat it like a password.

## 2. Pre-write `~/.polyforge/config.toml`

The `polyforge` CLI and its MCP server look up your credential in
`~/.polyforge/config.toml` (machine-level config — distinct from
`<workspace>/.polyforge.yaml`, which holds per-workspace repo and project
metadata).

Create the file before you install the plugin so the MCP server has a
credential to use on its very first launch:

```bash
mkdir -p ~/.polyforge
cat > ~/.polyforge/config.toml <<'EOF'
[auth]
api_key = "pf_k1_REPLACE_ME"

[server]
url = "http://10.146.0.16:8080"
EOF
chmod 600 ~/.polyforge/config.toml
```

## 3. GitHub access (`gh` CLI + SSH key)

You need two kinds of GitHub access for the rest of the flow:

- **`gh` token** — the plugin's MCP launcher pulls the `polyforge` binary
  from the private `GMISWE/ieops-aihub` repo on first run (step 5).
- **SSH key** — `polyforge init` (step 7) clones the team's repos via
  `git@github.com:…`, which requires an SSH key attached to your GitHub
  account.

```bash
# 1) gh CLI authenticated
gh --version          # install from https://cli.github.com if missing
gh auth status        # should print "Logged in to github.com..."
gh auth login         # only if not already logged in

# 2) SSH to GitHub
ssh -T git@github.com 2>&1 | head -1
# expect: "Hi <username>! You've successfully authenticated..."
```

If `gh auth token` prints a token and the `ssh -T` check succeeds, you are
set. Without `gh`, step 5 will fail to download the binary; without SSH,
step 7's `polyforge init` will fail mid-clone.

## 4. Install the marketplace + plugin in Claude Code

In any Claude Code session, run:

```
/plugin marketplace add GMISWE/GMI-marketplace
/plugin install polyforge@gmi-marketplace
```

The plugin itself ships only an MCP launcher
(`${CLAUDE_PLUGIN_ROOT}/bin/polyforge-mcp.sh`); the actual `polyforge`
binary is downloaded by that launcher on the first MCP start (next step),
using the `gh` token from step 3.

### Codex (codex-cli)

The same plugin runs under Codex. Codex does not use Claude Code's
`/plugin install`; once the plugin is available on disk (so
`$CLAUDE_PLUGIN_ROOT` is set), register the MCP server once — Codex does not
register it from the manifest:

```
codex mcp add polyforge -- "$CLAUDE_PLUGIN_ROOT/bin/polyforge-mcp.sh"
```

(If `$CLAUDE_PLUGIN_ROOT` is unset, pass the absolute path to
`<plugin-root>/bin/polyforge-mcp.sh`.) The launcher downloads the `polyforge`
binary on first start, the same as Claude Code. Skills load natively — type
`$pf-work` or run `/skills` (there is no `Skill` tool). MCP tools surface as
`mcp__polyforge__pf_*`. Verify with `codex mcp list` (or `/mcp` in session),
then ask Codex to run `pf_whoami`. See
`plugins/polyforge/skills/using-polyforge/references/codex-tools.md` for the
full Claude Code -> Codex tool mapping.

### Updating the plugin, skills, and binary

polyforge ships in **two layers that update independently** — know which one your
change lives in:

- **Skills + hooks** (the `/pf-*` workflow instructions) live in the plugin
  package, versioned by the plugin `version`. Pull the latest with:
  ```
  /plugin marketplace update GMISWE/GMI-marketplace   # refresh the catalog
  /plugin install polyforge@gmi-marketplace           # re-install to the new version
  ```
  (or use the interactive `/plugin` menu). Publishing a skill change: edit
  `plugins/polyforge/skills/*`, bump `version` in **all five stamps** (both
  catalogs plus the three `plugin.json` variants — `scripts/pf_version_check.py`
  enforces that they agree), and merge that together with the change.

  > 🔴 **`version` is the update signal; `catalog_revision` is inert.**
  > `claude plugin validate` says verbatim: *"Unknown field 'catalog_revision'.
  > Claude Code ignores it at load time."* The install cache is keyed on
  > `version` (`installPath` is `<cache>/<marketplace>/polyforge/<version>`), so
  > a new build reaches a user only when `version` changes — restamping
  > `catalog_revision` alone ships a release that reaches **nobody**, and
  > `/plugin update` is a no-op for anyone already on that version. This page,
  > `pf_version_check.py` and team memory `mem_7yldi6xb` all taught the opposite
  > until aihub#302. The corrected memory is **`mem_zZ3xWv4g`** — `mem_7yldi6xb`
  > is its archived predecessor and still returns the wrong text verbatim if you
  > fetch it by id. The field is kept only so `pf_version_check.py` can hold its
  > two carriers consistent, and is changed alongside `version` by convention.
  > Since aihub#302, CI fails a PR that edits anything under `plugins/polyforge/`
  > without moving `version` (`[NO_VERSION_BUMP]` in the Contract Lint job), so
  > you do not have to remember this — but do not "fix" that failure by
  > restamping `catalog_revision`.

- **The `polyforge` binary** (the MCP server — ALL `pf_*` tool behavior, e.g.
  `pf_recall` result-slimming) is NOT in the plugin package. The launcher
  auto-downloads it once per day from the `bins-<channel>` branch, where
  `<channel>` is the `[binary] channel` in `~/.polyforge/config.toml`
  (`stable` by default; `dev` = latest `main`).
  - **Channels**: a push to `main` publishes the binary to `bins-dev`; cutting a
    `v*` tag (via `/pf-release`) publishes to `bins-stable`. So `main` changes
    reach **dev** users automatically, but reach **stable** users only after a
    tagged release.
  - **Force an update now** (skip the daily wait):
    ```
    rm -f ~/.polyforge/.last_binary_check    # forces the version check on next MCP start
    ```
    then restart Claude Code. Confirm with `polyforge version` (prints the
    published commit SHA).

> Rule of thumb: **skill / workflow change → update the plugin (marketplace);
> tool behavior / token or recall changes → update the binary (channel +
> force-refresh).**

## 5. Restart Claude Code and verify

Restart Claude Code so the plugin's `mcpServers.polyforge` entry is picked up.
On first start the launcher downloads the matching `polyforge` binary into
`${CLAUDE_PLUGIN_ROOT}/bin/polyforge` (using the `gh` token from step 3) and,
when `/usr/local/bin` is writable, symlinks `/usr/local/bin/polyforge` to it
so the shell sees the same version as the MCP server. Subsequent starts skip
the download and do a daily update check.

Once the MCP server reconnects, every `mcp__plugin_polyforge_polyforge__*`
tool is available. (Codex users: tools surface as `mcp__polyforge__pf_*`;
verify with `codex mcp list` or `/mcp` in session.) `pf_whoami` is an MCP
tool (not a shell command) — just ask the agent for it in chat:

```
pf_whoami
```

You should see your user id, display name, and the server URL from your
`config.toml`. If you see a 401, double-check that the `api_key` in
`~/.polyforge/config.toml` matches the one the owner handed you and that
the file is readable by your user (`ls -l ~/.polyforge/config.toml`). If
the binary failed to download, re-run `gh auth status` and check the MCP
server logs.

## 6. (Optional) Switch to the dev channel or build from source

To run pre-release builds, **you do not need to compile anything** — set the
channel in `~/.polyforge/config.toml` and restart Claude Code:

```toml
[binary]
channel = "dev"
```

The launcher reads `[binary] channel` and auto-downloads from `bins-stable`
(default) or `bins-dev`.

You only need a local build when you want a `polyforge` with **your own
unpublished changes** (a branch not yet on either channel):

```bash
git clone git@github.com:GMISWE/ieops-aihub.git
cd ieops-aihub
make build
# produces bin/aihub (server) and bin/polyforge (CLI + MCP)
cp bin/polyforge "${CLAUDE_PLUGIN_ROOT}/bin/polyforge"
```

Overwriting the plugin-managed path beats `PATH` (the plugin only consults
`PATH` as a download fallback). The daily auto-update check will replace
this with the channel binary the next time it runs, so re-`cp` after each
rebuild.

In practice you do not have to remember any of this — just ask Claude in
this CLI session (e.g. "update the polyforge binary from this branch") and
it will run `make build` and the copy step inside the worktree for you.

## 7. Demo: create a work item

`/pf-work` (and the other lifecycle skills) read your workspace's
`.polyforge.yaml`, so first set one up by running `polyforge init` in any
directory you want to use as the workspace root:

```bash
mkdir -p ~/<workspace> && cd ~/<workspace>
'/pf-init' in Claude Code chat # or: 'polyforge init' in the shell
```

`polyforge init` clones every project's repos via SSH into `.repo/` (~13
repos, ~250 MB, ~30 s on a good connection) and also drops in:

- `.polyforge.yaml` — workspace config pulled from the server
- `CLAUDE.md` — managed repo-map block Claude Code reads at session start
- `.polyforge/usage.md` — command cheatsheet + machine config (the Iron Rules live in the
  `using-polyforge` skill, not here — aihub#294)

You may see a warning like `pf init: skipping scenario "coding" for project
ieops` near the end — that is a harmless server-side config quirk, not an
error you need to act on.

Open Claude Code in that directory and ask it to start something — for
example:

```
/pf-work "write a hello-world script in scratch/hello.sh"
# or
/pf-status # to see existing work items and their states
```

That triggers the `polyforge:pf-work` skill, which talks to the shared
`aihub` at `http://10.146.0.16:8080` to claim or create a work item for you.

To see the work item land server-side, open the Web UI:

1. Visit `http://10.146.0.16:8080/ui/login` and paste your API key. The
   server mints a 7-day signed session cookie.
2. Browse to `http://10.146.0.16:8080/ui/wi` — the list polls every 5 s, so
   your new wi shows up without a manual refresh.
3. Click through to `/ui/wi/<id>` for the full timeline, declared resources,
   and step state.

Routing for the UI lives in `internal/server/ui_routes.go` (the
`RegisterUIRoutes` entry point) if you want to dig into how queue, list, and
detail views are wired up.

---

## Where to go next

- `README.md` — server-side operation and configuration.
- `docs/design/polyforge-v1-design.md` — the long-form architecture document
  covering wi lifecycle, memory, and MCP semantics.
- `polyforge-coding` scenario repo — the step definitions (`feature.md`,
  `chore.md`, `fix_bug.md`, …) that drive `/pf-execute`.
