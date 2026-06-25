---
name: pf-init
description: >
  Use when setting up a polyforge workspace for the first time on a new machine,
  adding repos to an existing workspace, refreshing repo descriptions, or repairing
  a workspace after /pf-doctor reports failures.
---

# pf-init — Workspace Initialization

## Usage

**Purpose**: Initialize or repair the local polyforge workspace (`.polyforge.yaml`, `.repo/<name>/` clones, CLAUDE.md managed block).

**Pattern**: `/pf-init [--refresh-description] [--force]`

**Required**: none

**Flags**:
- `--refresh-description` — regenerate the structured English description block for each repo and PATCH it to the server (owner only)
- `--force` — reset `.polyforge/ieops-e2e.yaml` back to the built-in template, overwriting local edits (otherwise the file is created only if absent — see Step 5)

## When to use

- First-time setup on a new machine
- Adding new repos to an existing workspace
- Refreshing repo descriptions (`polyforge init --refresh-description`)
- Repairing workspace after `polyforge doctor` reports failures

---

## Mechanic

> **Single pass.** `/pf-init` is ONE invocation. For an owner with stale/missing
> structured descriptions, this skill runs the whole chain itself —
> `polyforge init` (clone+sync) → generate the structured block → validate →
> `pf_update_project` (sync server) → `polyforge init` again (render CLAUDE.md from the
> updated server data). The second `polyforge init` is an internal closing step, **not**
> a manual action — never tell the user to "run it again". When the flow finishes,
> CLAUDE.md already shows the structured repo-map.

### Step 1: Run polyforge init (clone + sync + initial render)

```bash
polyforge init
```

This:
1. Calls GET /v1/projects to get all visible projects
2. For each project, detects owner vs member role (per-project, based on owner_user_id)
3. **Owner path**: reads local .polyforge.yaml repos → diffs with server → clones/syncs → PATCHes server
4. **Member path**: uses server repos → clones/syncs
5. Regenerates CLAUDE.md managed block (project-grouped with descriptions)

**Owner** maintains `.polyforge.yaml` as the source of truth for which repos belong to the project.
**Members** get all config from the server.

---

### Step 2: Generate structured repo descriptions (owner only)

Each repo carries a **structured, English** description block stored server-side in
aihub and rendered into CLAUDE.md by `polyforge init`. All four content fields are
required as an all-or-nothing block (the server rejects a partial block with
`REPO_INCOMPLETE_DESCRIPTION`):

```yaml
positioning:      string              # one line: what this repo is / its role
tech_stack:       [string]            # ["Go", "PostgreSQL", "controller-runtime"]
main_modules:     [{path, role}]      # [{"path":"internal/api","role":"HTTP handlers"}]
change_scenarios: [string]            # ["add MCP tool", "schema migration"]
generated_at:     <RFC3339>           # set at generation; drives age-staleness
generated_commit: <repo HEAD SHA>     # set at generation; drives content-staleness
```

#### 2a. Detect which repos need (re)generation

`pf_list_projects` returns each repo's current block. For every repo, mark it **stale**
if any of:

- **Missing/empty block** — no `positioning` (includes legacy repos with only the old
  single-line `description`, and repos with no description at all, e.g. `proxy-server`).
- **Path drift** — any stored `main_modules[].path` no longer exists in `.repo/<name>/`,
  or a significant new top-level module dir appeared.
- **Content change** — `git -C .repo/<name> diff <generated_commit>..HEAD -- README* go.mod package.json <main_modules paths>` is non-empty (scoped diff, not "any commit").
- **Age** — `now - generated_at > 30d`.

Repos that are current are skipped. Use `--refresh-description` to force all repos.

#### 2b. Generate — one subagent per stale repo (parallel)

Repo scans are independent and are the slowest part of a refresh, so **dispatch one
subagent per stale repo in parallel** (a single message with multiple Agent calls).
Each subagent receives only its own repo and returns a drafted block:

```
For each stale repo <name>, dispatch a subagent that:
  1. git -C .repo/<name> log --oneline -10        # recent activity
  2. Reads .repo/<name>/README* (first ~100 lines)
  3. Lists .repo/<name>/ top-level + key subdirs    # for main_modules
  4. Drafts the four fields in concise English. Seeds positioning from the legacy
     `description` if present. main_modules paths MUST be real paths in the clone
     (file or dir — validated in 2d).
  → Returns the drafted block as structured data. The subagent does NOT call
     pf_update_project itself.
```

Collect every subagent's draft before continuing. The parent persists once in 2d —
one atomic `pf_update_project` with the full repo list, never N racing partial writes.
Single-repo refresh (`init` after adding one repo) skips the fan-out and just runs the
four steps inline.

#### 2c. Human confirmation

Show the drafted block per repo; let the user edit. Generation is never fully
automatic — always confirm before writing.

#### 2d. Validate paths client-side, then persist

The aihub server cannot check `main_modules` paths (it has no clones), so validate here
**before** writing:

```
for m in main_modules:
    # exists() not isdir() — a path may be a file (flat-package repos like
    # ieops-kube expose modules as cluster.go, flux2.go, … not subdirectories).
    assert os.path.exists(".repo/<name>/" + m.path), f"main_modules path missing: {m.path}"
```

Then write the full block (all four fields together — partial writes are rejected):

```
pf_update_project(
  name=<project_name>,
  repos=[
    {
      "name": "<repo_name>", "url": "<repo_url>", "github_owner_repo": "<owner/repo>",
      "positioning": "...", "tech_stack": [...],
      "main_modules": [{"path": "...", "role": "..."}],
      "change_scenarios": [...],
      "generated_at": "<now RFC3339>",
      "generated_commit": "<git -C .repo/<name> rev-parse HEAD>"
    }
    // include ALL repos in the project's list; unchanged repos keep their stored block
  ]
)
```

#### 2e. Render — closing step of the same pass

Immediately run `polyforge init` once more. It re-GETs the project and re-renders the
CLAUDE.md managed block from the now-updated server data. The server is the single
source-of-truth for the render — the skill never writes the managed block itself, so
there is no risk of skill/binary render drift. This is the closing step of the SAME
/pf-init flow; the user does not re-invoke anything. Confirm every repo (including
`proxy-server`) renders a non-blank multi-line block (positioning + stack + modules +
changes + generated line), not a single fallback line.

---

### Step 3: Verify

```bash
polyforge doctor
```

Expected: all 5 checks green.

---

### Step 4: Take over the statusLine (pf-work status chain)

Display a metro-line wi-progress chain in the statusline during /pf-work
(🟢 done / 🟡 active / ⚪ skipped·pending; auto-hidden when no wi is running).

settings.json cannot reference the versioned plugin cache path, so copy the two
scripts into a stable workspace location and point settings.json there. The takeover
is **idempotent** (never double-wraps) and **reversible** (`/pf-doctor --uninstall`).
It saves any prior `statusLine.command` as a *base* so the chain composes on top of an
existing statusline (e.g. ruflo) instead of replacing it.

The block also sets `"refreshInterval": 3` (seconds). Claude Code re-runs the
statusLine command on conversation message updates *and* on this timer. Without it the
chain freezes when `/pf-execute` runs in a background subagent while the watching
session sits idle — zero message updates means zero re-renders, so the wi chain stays
stuck at its claim-time snapshot for the whole run (aihub#122). The timer re-renders the
chain periodically so progress shows live even during idle bg-subagent runs.

```python
import shutil, os
ws = workspace_root
helpers = f"{ws}/.claude/helpers/pf"
os.makedirs(helpers, exist_ok=True)

# Copy renderer + statusline together — pf-statusline.cjs requires ./pf-chain-render.cjs,
# so both must sit in the same dir. Re-copied every init → survives plugin upgrades.
for f in ["pf-statusline.cjs", "pf-chain-render.cjs"]:
    shutil.copyfile(f"{plugin_root}/bin/{f}", f"{helpers}/{f}")

settings_path = f"{ws}/.claude/settings.json"
settings = read_json(settings_path) or {}
pf_cmd = f'node "{helpers}/pf-statusline.cjs"'
sl = settings.get("statusLine", {})
cur = sl.get("command", "")

if "pf-statusline.cjs" in cur:
    # already taken over — idempotent, no double-wrap. Still back-fill refreshInterval
    # so pre-aihub#122 takeovers (command present, refreshInterval absent) get the timer
    # without a full re-takeover. This is what /pf-doctor Check 6 relies on.
    if "refreshInterval" not in sl:
        sl["refreshInterval"] = 3
        settings["statusLine"] = sl
        write_json(settings_path, settings)
else:
    if cur:
        # save the prior command so pf-statusline composes it + so --uninstall can restore
        write_text(f"{ws}/.polyforge/statusline-base", cur)
    # refreshInterval (seconds) drives periodic re-render so the wi chain updates while
    # the watching session idles during bg-subagent /pf-execute runs (aihub#122).
    settings["statusLine"] = {"type": "command", "command": pf_cmd, "refreshInterval": 3}
    write_json(settings_path, settings)
    notify("statusLine taken over — wi progress chain shows during /pf-work; "
           "/pf-doctor --uninstall restores the previous statusline.")
```

> The hook producer (`hooks/hooks.json` → `pf-chain-hook.cjs`) ships with the plugin and
> activates automatically when the plugin is enabled — no settings.json change needed for it.

---

### Step 5: Generate the ieops e2e config file (.polyforge/ieops-e2e.yaml)

Write `.polyforge/ieops-e2e.yaml` (under the workspace's `.polyforge/` directory) from a
built-in template. This file is the
**single source of truth** for where the e2e harness finds its scenarios, selection map,
env source, and sim-control helpers — decoupled from the scenario `.md` so the consumer
(e.g. polyforge-coding e2e steps) reads coordinates from one place instead of hard-coding
paths. The paths inside the template are relative to the `target_repo` root.

The write is **create-if-absent (idempotent)**: if `.polyforge/ieops-e2e.yaml` already
exists it is left untouched, so a user's hand-edits are never clobbered across re-inits.
Passing `--force` resets the file back to the built-in template (discarding local edits) —
the escape hatch for a corrupted or drifted config.

**Legacy-location migration**: before #180 the file lived at the workspace root as
`.ieops-e2e.yaml`. An existing root file is **moved** (`mv`, never copied) into
`.polyforge/ieops-e2e.yaml` so user edits survive and no duplicate lingers.

```python
ws = workspace_root
e2e_path = f"{ws}/.polyforge/ieops-e2e.yaml"
legacy_path = f"{ws}/.ieops-e2e.yaml"   # pre-#180 workspace-root location

E2E_TEMPLATE = """\
project: ieops
target_repo: ieops-v2
e2e:
  scenarios_dir: test/e2e/ai/scenarios/
  selection_map: test/e2e/ai/config/service-map.yaml
  env_source:    test/e2e/ai/config/env.sh
  sim_control:   hack/dev-environment/dev-env.sh
  select_helper: hack/e2e-hooks/run.sh --select
"""

os.makedirs(f"{ws}/.polyforge", exist_ok=True)

# One-time migration of the legacy workspace-root file
if os.path.exists(legacy_path):
    if force:
        os.remove(legacy_path)   # --force discards local edits, incl. a stale root copy
        log(f"removed legacy {legacy_path} (--force reset)")
    elif not os.path.exists(e2e_path):
        os.rename(legacy_path, e2e_path)   # mv — migrate user edits, never duplicate
        log(f"migrated legacy workspace-root config to {e2e_path}")
    else:
        # both exist — .polyforge/ copy is authoritative; root copy is stale
        log(f"WARN: stale legacy {legacy_path} ignored — {e2e_path} is authoritative; "
            f"delete the root file manually (do NOT reach for --force here: it also "
            f"resets {e2e_path} to the template, discarding your edits)")

if os.path.exists(e2e_path) and not force:
    # create-if-absent — never overwrite a user-edited (or just-migrated) file
    log(f"ieops-e2e.yaml already exists at {e2e_path}, leaving untouched")
else:
    # absent, or --force reset back to the built-in template
    write_text(e2e_path, E2E_TEMPLATE)
    notify(".polyforge/ieops-e2e.yaml written — e2e harness config (pass --force to reset).")
```

`--force` is interpreted by **this skill** (like `--refresh-description`): the Go binary
does not implement it. When `--force` is set, Step 5 rewrites `.polyforge/ieops-e2e.yaml`
from the template even when the file already exists, and removes a leftover legacy
workspace-root `.ieops-e2e.yaml` so it can never shadow the new location.

---

## Two paths in detail

### Owner path
Owner has `.polyforge.yaml` with repos configured. `polyforge init` will:
- Sync local repos to server (append new, warn about server-only repos)
- Clone/sync all repos to `.repo/<name>/`
- PATCH server with updated repos (without description — that's done in Step 2 above)

### Member path
Member has no `.polyforge.yaml` (or it's auto-generated). `polyforge init` will:
- Pull all project configs from server
- Clone/sync all repos
- Generate `.polyforge.yaml` as local cache

Members never PATCH the server. Description placeholders show `*(pending)*` until owner runs init + description generation.

---

## Flags

```bash
polyforge init                    # standard init (binary): clone/sync + render
polyforge init --refresh-description  # skill-level: force Step 2 to treat ALL repos as stale
```

`--refresh-description` is interpreted by **this skill** — it forces every repo through
Step 2 regeneration regardless of the staleness detection in 2a. The Go binary does not
implement the flag (it ignores unknown args and just clones/syncs/renders), so
generation always stays skill-side: the server has no repo clones and cannot scan for
module paths. Whether or not the flag is passed, the closing render (2e) happens in the
same pass — no second manual `/pf-init`.

`--apply` is deprecated and has no effect.

---

## Worktree path format

Per-wi task worktrees: `pf.<project>-<seq>/<repo>/`

Example: `pf.aihub-23/aihub/`

---

## NL Triggers

- "initialize workspace" / "init workspace" / "setup"
- "clone repos" / "clone repositories"
- "repair workspace" / "fix the workspace"
- "refresh description" / "update descriptions"
- "polyforge init" / "pf init"
