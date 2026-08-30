---
name: pf-doctor
description: >
  Use when the user asks to diagnose or fix the polyforge workspace, MCP connection,
  repo state, or version mismatch. Also use when any pf-* tool call fails unexpectedly
  or when onboarding to a new machine.
---

# pf-doctor — Workspace Health Check

## Usage

**Purpose**: Diagnose workspace, config, repo, worktree, and version health; optionally auto-fix.

**Pattern**: `/pf-doctor [--fix]`

**Required**: none

**Flags**:
- `--fix` — apply auto-fixable repairs (orphan worktree cleanup, repo re-sync); leaves manual-only issues for the user
- `--uninstall` — restore the statusLine to its pre-polyforge state (undo the pf-work chain takeover)

## When to use

- "pf doctor" / "check workspace" / "diagnose" / "something's broken"
- Any pf-* tool returns connection error, config error, or "state file not found"
- First session on a new machine after `polyforge init`
- After returning from a long absence (stale worktrees, drift)

## Mechanic

Run the CLI diagnostic tool and interpret results:

```bash
polyforge doctor
# With auto-fix for fixable issues:
polyforge doctor --fix
```

The CLI runs 7 checks (§12.1) and prints `[ok]`, `[warn]`, or `[FAIL]` per check:

| # | Check | What it tests | Auto-fix |
|---|-------|---------------|----------|
| 1 | workspace | `.polyforge.yaml` found via upward search from wsRoot | `polyforge init` |
| 2 | config | `~/.polyforge/config.toml` valid; aihub URL reachable (GET /health) | manual |
| 3 | repos | `.repo/<name>/` exists for each project repo; remote URL matches `.polyforge.yaml` | `polyforge init --apply` |
| 4 | worktrees | `pf.*` dirs cross-checked vs server wi list; flags orphans | `polyforge doctor --fix` |
| 5 | version | Server `min_client_version` vs local binary; prompts upgrade if behind | `pf-init` skill |
| 6 | claude_md | CLAUDE.md `## Workspace` block format (slim vs legacy inline) + `.polyforge/repo-map/<project>.md` present for every project | `polyforge init` |
| 7 | usage_md | `.polyforge/usage.md` still carrying rule sections the `using-polyforge` skill owns (Iron Rules / NL Routing / Memory Type Reference). That file is never regenerated, so the copy there cannot be corrected and a session sees two (aihub#294) | `polyforge doctor --fix` (rewrites the file; keeps a `usage.md.bak`) |

Then run the seam-check probe (read-only, pinned to the cached `superpowers` plugin version):

```bash
bash "${CLAUDE_PLUGIN_ROOT}/bin/pf-seam-check"
```

Treat any `[WARN]` line in its output as a `warn` on the report below (it always exits 0, so the exit code carries no signal -- parse the printed lines).

## Interpretation Guide

### Check 1 — workspace FAIL
`.polyforge.yaml` not found. Run `/pf-init` skill to scaffold.

### Check 2 — config FAIL
aihub unreachable. Triage:
1. `cat ~/.polyforge/config.toml` — verify `[server] url` and `[auth] api_key`
2. `curl -s <url>/v1/health` — direct reachability test
3. If API key missing: `polyforge init` → enter key interactively

### Check 3 — repos warn
Missing or mismatched `.repo/` dirs.
- Missing: run `polyforge init --apply` to clone all repos
- Remote mismatch: `git -C .repo/<name> remote set-url origin <correct-url>`

### Check 4 — worktrees warn
Orphan `pf.*` directories found (no matching running/paused wi on server).
- Safe auto-remove: `polyforge doctor --fix`
- If wi is actually still running but server state is stale: use `pf_list_work_items` first to confirm

### Check 5 — version warn
Local binary is behind `min_client_version`. Run `/pf-init` skill to reinstall the latest plugin.

### Check 6 — claude_md warn
Two distinct warnings, both fixed by re-running `polyforge init` in the workspace:

- **"managed block is the legacy inline format"** — the block still repeats every repo's
  `stack` / `modules` / `changes` / `generated` inline. That text sits at context position
  0, is re-read on every request, and compaction cannot drop it. `polyforge init` moves it
  to `.polyforge/repo-map/<project>.md`, read on demand at routing time. Measured on the
  gmi-ws workspace: 29,650 of the block's 34,606 bytes. This never auto-fixes — say what it
  costs and let the user decide when to re-init.
- **"repo map missing …"** — the block is already slim but `.polyforge/repo-map/` is
  absent, empty, or has no file for some project. Routing then has only the one-line
  positioning, which is not enough to locate code inside a repo. Surface it rather than
  letting an agent guess; `codegraph_*` / `Grep` in the worktree are the interim fallback.

Never a FAIL: a stale block is a cost, not a broken workspace.

### Check 7 — usage_md warn
**".polyforge/usage.md still carries N rule section(s) that using-polyforge owns"** — the
workspace was created before aihub#294, when `polyforge init` wrote the Iron Rules, NL
Routing and the memory-type table into `usage.md` as well as shipping them in the skill.
`writeUsageMd` refuses to overwrite an existing `usage.md` (it is the user's file), so a
template change cannot reach this workspace and the session gets both copies — of which
only the skill's can ever be corrected. They have already drifted once.

`polyforge doctor --fix` removes just those sections and keeps the original as
`.polyforge/usage.md.bak`. It is not done during `init`: deleting silently from a file
polyforge has promised not to write to would replace a silent duplicate with a silent
deletion. Never a FAIL — the workspace works, it is just being told the rules twice.

### Check 8 — statusLine (pf-work chain)
Verify the wi-progress chain takeover is healthy:
- `<ws>/.claude/settings.json` `statusLine.command` contains `pf-statusline.cjs`.
- `<ws>/.claude/helpers/pf/pf-statusline.cjs` and `pf-chain-render.cjs` both exist.
- `<ws>/.claude/settings.json` `statusLine.refreshInterval` is present (a number, e.g. `3`).
- If `<ws>/.polyforge/statusline-base` exists, the saved base command is non-empty.

**Missing refreshInterval (pre-aihub#122 takeover).** Workspaces initialized before
aihub#122 have a `statusLine` block without `refreshInterval`, so the wi chain freezes
during bg-subagent `/pf-execute` runs (the watching session idles → no message updates →
no re-render). If `statusLine.command` contains `pf-statusline.cjs` **but**
`statusLine.refreshInterval` is absent → report `warn` and re-run `/pf-init` to re-assert
the takeover. Its Step 4 is idempotent and writes `"refreshInterval": 3` into the block.

**Drift detection (takeover clobbered).** The takeover is last-writer-wins: a later
statusline installer (e.g. ruflo) can overwrite `statusLine.command`. If
`<ws>/.polyforge/statusline-base` EXISTS (pf took over before) **but** the current
`statusLine.command` no longer contains `pf-statusline.cjs`, the takeover was clobbered →
report `warn` and re-assert it by re-running `/pf-init`. Its Step 4 is idempotent: it saves
the current command as the new base and re-points `statusLine` at pf-statusline, so the
clobbering statusline keeps composing underneath.

If the command points at a missing script (e.g. workspace moved) → re-run `/pf-init` to
re-copy the scripts. If not taken over at all → `/pf-init` installs it.

### `--uninstall` — restore the previous statusLine

```python
ws = workspace_root
settings_path = f"{ws}/.claude/settings.json"
settings = read_json(settings_path) or {}
base = read_text(f"{ws}/.polyforge/statusline-base")  # None if file absent
if base and base.strip():
    settings["statusLine"] = {"type": "command", "command": base.strip()}
else:
    settings.pop("statusLine", None)   # no prior statusline → remove the field
write_json(settings_path, settings)
remove_if_exists(f"{ws}/.polyforge/statusline-base")
# leave .claude/helpers/pf/ scripts in place (harmless); they are unreferenced now.
notify("statusLine restored to its pre-polyforge state.")
```

The plugin hook (`pf-chain-hook.cjs`) keeps shipping with the plugin; to fully stop it,
disable the polyforge plugin. `--uninstall` only undoes the statusLine takeover.

### Check 9 - seam-check (superpowers)

`pf-seam-check` pins the 6.1.1 baseline of the cached `superpowers` plugin that pf-execute's
engine pointer hardcodes against: `subagent-driven-development` (the engine pointer itself),
`finishing-a-development-branch` (`_common/lifecycle.md`'s D6 boundary), and
`executing-plans` (the one the router test's negative assertion checks stays absent from the
execute pointer) -- plus the `superpowers@` prefix `pf-skill-router` scans for in
`enabledPlugins`. pf-spec and pf-plan are self-sufficient SKILL.md files with no router
injection and no superpowers dependency, so they are out of scope for this check. Any
`[WARN]` line means the installed superpowers version drifted from one of these assumptions
and pf-execute's engine pointer may now silently no-op (the router falls back to native
engine without telling you). Treat a WARN as: re-verify the seam by hand against the new
superpowers version, then update the pin (`PIN_VERSION` in `bin/pf-seam-check`) and any
name it references.

## Output (three-segment format)

```markdown
## Result
<pass/fail summary; list any FAIL items>

## Status
| field     | value           |
|-----------|-----------------|
| workspace | ok              |
| config    | ok              |
| repos     | warn (missing: ieops-v2) |
| worktrees | ok (2 active)   |
| version   | ok (1.0.0)      |
| claude_md | warn (legacy inline block, 34,606 B) |
| usage_md  | warn (3 duplicated rule sections) |
| statusLine| ok (chain installed, refreshInterval 3) |
| seamcheck | ok (verified against superpowers 6.1.1) |

## Next steps
- `polyforge init --apply` — clone missing repos
- `/pf-work` — resume work once health is green
```

## NL Triggers

- "pf doctor" / "doctor" / "check" / "diagnose" / "health check"
- "something's broken with polyforge"
- "aihub unreachable" / "can't connect" / "MCP error"
- "worktree orphan" / "stale state"
