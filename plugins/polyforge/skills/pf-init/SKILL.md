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
- `--force` — re-resolve `target_repo` and reset `.polyforge/ieops-e2e.yaml` back to the template, overwriting local edits; if `target_repo` cannot be resolved, nothing is written and the existing file is kept (otherwise the file is created only if absent — see Step 5)

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

Expected: all 7 checks green.

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

`project` and `target_repo` are **resolved at init time** from the workspace's own
`.polyforge.yaml` and `.repo/` clones — they are deliberately **not** baked into the
template (aihub#263). Baking `target_repo` in is what let the value rot: it named
`ieops-v2` long after ieops moved its e2e assets to `ieops-env`, so every first-time init
on a fresh machine — and every `--force` — pointed the harness at an archived copy whose
scenarios had diverged and which carries no `--execute` runner. Resolution works by
**probing the project's own clones** for the e2e layout, so the value follows wherever the
assets actually live and can never silently name a repo that no longer hosts them.

The write is **create-if-absent (idempotent)**: if `.polyforge/ieops-e2e.yaml` already
exists it is left untouched, so a user's hand-edits are never clobbered across re-inits.
Passing `--force` re-resolves `target_repo` and resets the file back to the template
(discarding local edits) — the escape hatch for a corrupted or drifted config. `--force`
only rewrites the file when resolution succeeds; on a failed resolve it writes nothing and
leaves the existing file alone, so it can never replace a working config with a guess.

**Legacy-location migration**: before #180 the file lived at the workspace root as
`.ieops-e2e.yaml`. An existing root file is **moved** (`mv`, never copied) into
`.polyforge/ieops-e2e.yaml` so user edits survive and no duplicate lingers.

```python
ws = workspace_root
project = <project name from .polyforge.yaml>      # same value Step 1/3 sync against
# Use the repo list as RECONCILED by Step 1/3 (server list ∪ local), not raw .polyforge.yaml:
# a local .polyforge.yaml can be a stale subset of the project's server-side repos, and a
# repo missing from it is a repo this probe would never see (mem_Fimgx1UH).
repos   = <the project's reconciled repo list>     # same list Step 2a/3 iterate

# `attended` = this init was invoked with a human present to answer a question (a
# /pf-init or `polyforge init` typed by a person is attended; a bg-subagent or cron
# invocation is not). When in doubt, treat as UNATTENDED: that direction is the safe
# one, because the unattended path writes nothing rather than guessing. The opposite
# misjudgement is NOT safe — claiming a human is present when none is leaves an agent
# answering its own prompt, which is exactly the guess this probe exists to prevent.
attended = <a human is present to answer>

e2e_path = f"{ws}/.polyforge/ieops-e2e.yaml"
legacy_path = f"{ws}/.ieops-e2e.yaml"   # pre-#180 workspace-root location

# Path layout is the template's contribution; project/target_repo are resolved, not baked.
E2E_LAYOUT = {
    "scenarios_dir": "test/e2e/ai/scenarios/",
    "selection_map": "test/e2e/ai/config/service-map.yaml",
    "env_source":    "test/e2e/ai/config/env.sh",
    "sim_control":   "hack/dev-environment/dev-env.sh",
    "select_helper": "hack/e2e-hooks/run.sh --select",
}
RUNNER = E2E_LAYOUT["select_helper"].split()[0]     # hack/e2e-hooks/run.sh

def render_e2e_config(project, target_repo):
    lines = [f"project: {project}", f"target_repo: {target_repo}", "e2e:"]
    width = max(len(k) for k in E2E_LAYOUT)          # keep the values column-aligned
    for key, value in E2E_LAYOUT.items():
        lines.append(f"  {key + ':':<{width + 1}} {value}")
    return "\n".join(lines) + "\n"

def e2e_capabilities(ws, name):
    """Two independent signals, both cheap and local."""
    root = f"{ws}/.repo/{name}"
    runner = f"{root}/{RUNNER}"
    return {
        # carries the scenario tree at all — necessary, NOT sufficient (see below)
        "layout": os.path.isdir(f"{root}/{E2E_LAYOUT['scenarios_dir']}"),
        # runner implements the deterministic executor (ieops#640) — the discriminator.
        # Ignore comment lines: a bare substring match also fires on a stray mention of
        # --execute in a comment, and a false positive on the ARCHIVE would make it the
        # sole "runnable" repo and be selected silently, with no WARN at all.
        "executor": os.path.isfile(runner) and any(
            "--execute" in line.split("#", 1)[0] for line in read_text(runner).splitlines()),
    }

def resolve_target_repo(repos, ws, attended):
    """Which repo hosts the e2e assets? Probe capabilities; never guess.
    Returns None => the caller MUST NOT write a config file.

    One documented exception to "never guess": a project whose ONLY candidate lacks the
    executor still returns that candidate, with a loud WARN (see below). Refusing there
    would deny a config to any project on an older runner, so the trade is deliberate —
    but that branch returns a repo the probe did not confirm as the live host.

    `scenarios_dir` alone is deliberately NOT the discriminator: an archived pre-split
    copy of the repo carries the scenario tree too — that is the whole premise of
    aihub#263, whose 17 diverged scenario files exist on BOTH sides. Probing only for
    the tree would match both repos and decide nothing. What separates the live host
    from the archive is whether its runner implements `--execute`.
    """
    caps = {r.name: e2e_capabilities(ws, r.name) for r in repos}
    candidates = [n for n, c in caps.items() if c["layout"]]
    runnable   = [n for n in candidates if caps[n]["executor"]]

    # NOTE: there is deliberately no `len(repos) == 1` short-circuit. A single-repo
    # project whose one repo has no scenario tree has no e2e host at all, and naming it
    # anyway is exactly the silently-wrong-value failure this step exists to prevent.
    if len(runnable) == 1:
        return runnable[0]

    if not candidates:
        log(f"no repo under {ws}/.repo/ carries {E2E_LAYOUT['scenarios_dir']} — this "
            f"project has no e2e host; skipping {e2e_path}.")
        return None

    if len(candidates) == 1 and not runnable:
        # Exactly one host, but no deterministic executor. Usable, degraded, and said so.
        # This is also the shape a STALE REPO LIST takes: if the project split its e2e
        # assets into a newer repo that is missing from the reconciled list, the archive is
        # the only candidate left and would be selected here. So name that possibility
        # explicitly — it is the actionable diagnosis, not just a capability note.
        log(f"WARN: {candidates[0]} carries {E2E_LAYOUT['scenarios_dir']} but its {RUNNER} "
            f"has no --execute mode — e2e will run without the deterministic executor. "
            f"If this project moved its e2e assets to another repo, that repo is missing "
            f"from the project's repo list: check the server-side list (pf_list_projects) "
            f"and re-run polyforge init before trusting {e2e_path}.")
        return candidates[0]

    # Genuinely ambiguous: several hosts, and zero or >1 of them runnable.
    if attended:
        # Show the discriminator in the prompt — a bare repo-name list makes the human
        # re-guess exactly what this probe exists to settle. The annotation is for the
        # human's eyes ONLY: map the choice back to a bare repo name before returning,
        # or the label text itself ends up as target_repo and the YAML is corrupt.
        labels = {n: f"{n} — {RUNNER} --execute: "
                     f"{'yes' if caps[n]['executor'] else 'NO'}" for n in candidates}
        SKIP = "skip (write nothing)"
        picked = prompt(
            f"more than one repo carries {E2E_LAYOUT['scenarios_dir']} — pick the live "
            f"e2e host (a repo without --execute is most likely a pre-split archive):",
            list(labels.values()) + [SKIP])
        if picked == SKIP:
            return None
        # invert the label map; never return the label
        chosen = next((n for n, lbl in labels.items() if lbl == picked), None)
        if chosen is None:
            log(f"WARN: unrecognised choice {picked!r} — writing nothing.")
        return chosen

    # Unattended and genuinely ambiguous. THIS is the case that needs a human: candidates
    # exist and we refuse to pick. Warn here, where we still know that — the caller only
    # sees None and cannot tell it apart from "this project simply has no e2e assets".
    log(f"WARN: {len(candidates)} repos carry {E2E_LAYOUT['scenarios_dir']} "
        f"({', '.join(candidates)}) and none is uniquely identifiable as the live e2e host "
        f"(--execute present in: {', '.join(runnable) or 'none'}). Re-run attended to "
        f"choose, or write {e2e_path} by hand.")
    return None

os.makedirs(f"{ws}/.polyforge", exist_ok=True)

# One-time migration of the legacy workspace-root file.
# NOTE: under --force the stale root file is NOT deleted here — deletion is deferred until
# after a config has actually been written, so a failed resolve can never leave the
# workspace with no e2e config at all.
legacy_pending_removal = None
if os.path.exists(legacy_path):
    if force:
        legacy_pending_removal = legacy_path
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
    # absent, or --force reset back to the template
    target_repo = resolve_target_repo(repos, ws, attended)
    if target_repo is None:            # includes an explicit "skip" from the attended prompt
        # NON-DESTRUCTIVE by design. Writing a hardcoded fallback here would be exactly the
        # aihub#263 bug: a stale target_repo aims e2e at an archived repo and still reports
        # green. So: write nothing, keep whatever is already on disk, and leave a stale
        # legacy root file in place rather than deleting the last readable copy.
        # Severity is NOT decided here: resolve_target_repo has already logged the reason at
        # the right level — info when the project simply has no e2e assets (the normal case
        # for every non-ieops project), WARN only when candidates existed and none could be
        # chosen. Re-warning here would flag that normal outcome as a problem.
        log(f"{e2e_path} not written; existing config (if any) left untouched.")
    else:
        write_text(e2e_path, render_e2e_config(project, target_repo))
        notify(f".polyforge/ieops-e2e.yaml written — e2e harness config "
               f"(project={project}, target_repo={target_repo} resolved from {ws}/.repo/; "
               f"pass --force to re-resolve and reset).")
        if legacy_pending_removal:
            os.remove(legacy_pending_removal)   # safe now: a config exists
            log(f"removed legacy {legacy_pending_removal} (--force reset)")
```

For the `ieops` project this resolves `target_repo` to the clone whose
`hack/e2e-hooks/run.sh` implements `--execute` — `ieops-env` today. Note that **both**
`ieops-v2` and `ieops-env` carry `test/e2e/ai/scenarios/`, so the scenario tree cannot be
used to tell them apart; only the executor can. To verify by result rather than by reading
this file, run `polyforge init` in a workspace with **no** `.polyforge/ieops-e2e.yaml` and
confirm the generated `target_repo` resolves to a scenarios dir containing
`00-control-plane-smoke.md` (a file that exists only in `ieops-env`). Under the old
hardcoded template that check necessarily fails, which makes it a clean negative control.

`--force` is interpreted by **this skill** (like `--refresh-description`): the Go binary
does not implement it. When `--force` is set, Step 5 re-resolves `target_repo` and rewrites
`.polyforge/ieops-e2e.yaml` from the template even when the file already exists, and then
removes a leftover legacy workspace-root `.ieops-e2e.yaml` so it can never shadow the new
location. **If resolution fails, `--force` writes nothing**: the existing config and any
legacy root file are both left in place, so `--force` can never strand the workspace
without an e2e config.

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
polyforge init --force                # skill-level: re-resolve target_repo + reset Step 5's config (no-op if unresolvable)
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
