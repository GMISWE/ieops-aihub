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

## 5. Restart Claude Code and verify

Restart Claude Code so the plugin's `mcpServers.polyforge` entry is picked up.
On first start the launcher downloads the matching `polyforge` binary into
`${CLAUDE_PLUGIN_ROOT}/bin/polyforge` (using the `gh` token from step 3) and,
when `/usr/local/bin` is writable, symlinks `/usr/local/bin/polyforge` to it
so the shell sees the same version as the MCP server. Subsequent starts skip
the download and do a daily update check.

Once the MCP server reconnects, every `mcp__plugin_polyforge_polyforge__*`
tool is available. `pf_whoami` is an MCP tool (not a shell command) — just
ask Claude for it in chat:

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
- `.polyforge/usage.md` — Iron Rules + command cheatsheet
- `~/.claude/hooks/pf-session-start.sh` — a per-user Claude Code hook that
  auto-loads the polyforge skill in every Claude session (installed once
  per machine, idempotent — `polyforge init` re-registers it each time)

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
`aihub` at `http://34.180.90.199:8080` to claim or create a work item for you.

To see the work item land server-side, open the Web UI:

1. Visit `http://34.180.90.199:8080/ui/login` and paste your API key. The
   server mints a 7-day signed session cookie.
2. Browse to `http://34.180.90.199:8080/ui/wi` — the list polls every 5 s, so
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
