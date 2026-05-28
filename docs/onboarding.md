# polyforge onboarding

A newcomer guide to going from "zero" to "I just created my first work item
through Claude Code." Aimed at engineers joining the team — not server
operators (see `README.md` for `aihub` server setup).

The end-to-end flow is:

1. [Get an API key from the owner](#1-get-an-api-key)
2. [Pre-write `~/.polyforge/config.toml`](#2-pre-write-polyforgeconfigtoml)
3. [Install the marketplace + plugin in Claude Code](#3-install-the-marketplace--plugin-in-claude-code)
4. [Restart Claude Code and verify with `pf_whoami`](#4-restart-claude-code-and-verify)
5. [(Optional, dev) Build the `pf` binary from source](#5-optional-dev-build-the-pf-binary-from-source)
6. [Demo: create a work item and view it in the Web UI](#6-demo-create-a-work-item)

The team runs a shared `aihub` server at `http://10.146.0.16:8080`, so you do
not need to stand up your own backend.

---

## 1. Get an API key

Ask the owner for a key tied to your account. You will receive a token that
looks like `pf_k1_…` — treat it like a password.

## 2. Pre-write `~/.polyforge/config.toml`

`pf` and the MCP server look up your credential in `~/.polyforge/config.toml`
(machine-level config — distinct from `<workspace>/.polyforge.yaml`, which
holds per-workspace repo and project metadata).

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

Notes:

- `machine_id` is generated automatically on first `pf` invocation; you do
  not need to set it manually.
- `[auth] api_key_env = "POLYFORGE_API_KEY"` is an alternative if you prefer
  to keep the key in an environment variable instead of on disk.
- The credential lookup chain is: `config.toml [auth] api_key`
  → `config.toml api_key_env` → `.polyforge.yaml api_key_env`
  → the `POLYFORGE_API_KEY` env var.
- See `internal/config/machine.go` for the loader (`MachineConfigPath` and
  `LoadMachineConfig`).

## 3. Install the marketplace + plugin in Claude Code

In any Claude Code session, run:

```
/plugin marketplace add GMISWE/GMI-marketplace
/plugin install polyforge@gmi-marketplace
```

The plugin ships an MCP launcher (`${CLAUDE_PLUGIN_ROOT}/bin/polyforge-mcp.sh`)
that downloads the `polyforge` binary on first run, so you do not need to
build anything yourself for the default path.

## 4. Restart Claude Code and verify

Restart Claude Code so the plugin's `mcpServers.polyforge` entry is picked up.
Once it reconnects, every `mcp__plugin_polyforge_polyforge__*` tool is
available. Verify your identity with:

```
pf_whoami
```

You should see your user id, display name, and the server URL from your
`config.toml`. If you see a 401, double-check that the `api_key` in
`~/.polyforge/config.toml` matches the one the admin handed you and that the
file is readable by your user (`ls -l ~/.polyforge/config.toml`).

## 5. (Optional, dev) Build the `pf` binary from source

You only need this if you want to run dev builds of `polyforge` ahead of what
the plugin auto-downloads.

```bash
git clone git@github.com:GMISWE/ieops-aihub.git
cd ieops-aihub
make build
# produces bin/aihub (server) and bin/polyforge (CLI + MCP)
sudo cp bin/polyforge /usr/local/bin/polyforge
```

`make build` has no `install` target, so the copy is manual. The Claude Code
plugin will prefer whatever `polyforge` it finds on `PATH`, so this lets you
test local changes without touching the plugin itself.

## 6. Demo: create a work item

With the plugin loaded, ask Claude Code to start something — for example:

```
/pf-work "write a hello-world script in scratch/hello.sh"
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
