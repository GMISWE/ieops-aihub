---
name: pf-update
description: >
  Use when the user has just edited ~/.polyforge/config.toml (a [roles.tiers] or
  [roles] preset change) and wants it to take effect now rather than at the next
  session, or asks to update, refresh, reinstall or re-sync the polyforge role
  agent definitions for their harness.
---

# pf-update - Apply config.toml to the harnesses' agent files now

## Usage

**Purpose**: Re-render this machine's role agent definitions from `~/.polyforge/config.toml` and write them where each harness actually reads them.

**Pattern**: `/pf-update [--dry-run] [--harness <pi|opencode|codex|cc>] [--preset <name>]`

**Required**: none.

**Flags**:
- `--dry-run` - print the exact paths that would be written, write nothing.
- `--harness <h>` - one harness only. This is also the only way to create a harness's directory: without it, a harness whose directory does not exist is skipped.
- `--preset <name>` - render from a named `[roles.presets]` table instead of this machine's selection. Not accepted for `cc`.

## When to use

You almost never *need* this. `polyforge serve` runs the identical code path on every
MCP server boot, so the normal way a config change takes effect is "start a new session".
This skill exists for the one gap that leaves: **"I just edited `config.toml` and I want
it applied now, without waiting."**

Use it also when a harness's agent files look stale or wrong and you want to see, per
path, what polyforge thinks it should have written - `--dry-run` answers that without
touching anything.

Do **not** use it for first-time setup. See "What this does not do" below.

## Mechanic

This skill is a thin wrapper. Run the binary and show the user its output verbatim:

```bash
polyforge roles install            # or: polyforge roles install --dry-run
```

Then **report the paths it printed**. Do not summarise them as "done" or "updated the
agents" - the whole point of the command's output is that the user can tell the
difference between "said it finished" and "actually finished", and a summary throws
that away. If it printed `skipped` lines, relay those too: a skip always carries its
reason, and the reason is usually the answer to the user's next question.

Exit code is non-zero if any harness failed. A failure still lists what was written
before it, so a partial install is visible rather than reported as success.

## What it writes, and where

One built-in table, also printed by `polyforge --help`:

| harness | directory | override |
|---|---|---|
| pi | `~/.pi/agent/agents` | `$PI_AGENT_DIR` |
| opencode | `~/.config/opencode/agent` | `$POLYFORGE_OPENCODE_AGENT_DIR`, else `$OPENCODE_CONFIG_DIR`, else `$XDG_CONFIG_HOME` |
| codex | `~/.codex` (config profiles, not agent files) | `$CODEX_HOME` |
| cc | the installed plugin's own `agents/` | `$CLAUDE_PLUGIN_ROOT` |

Two rules govern what actually runs, and both are deliberate:

1. **The directory must already exist.** That is how polyforge knows this machine
   installed that harness's integration. It will not conjure `~/.pi/` on a machine that
   has never run pi. `--harness <h>` overrides this, because naming a harness is an
   instruction rather than an inference.
2. **This machine's tier table must name the harness.** A harness your `config.toml`
   says nothing about has nothing machine-local to apply, so its installed defaults are
   left alone.

Writes are atomic (temp file + rename) and a file whose contents already match is not
rewritten, so re-running this is free and does not disturb mtimes.

## What this does not do

- **It is not the installer.** First-time setup is still
  `plugins/polyforge/pi/install.sh` or `plugins/polyforge/opencode/install.sh`, which
  also place skills, the hook bridge, the MCP config and (for pi) the subagent tool.
  This skill owns exactly one of those jobs - **updating** the agent definitions - and
  nothing else.
- **It does not change the session you are in.** Every harness reads its agent
  definitions at its own startup, which already happened. The new values apply to the
  next session. (For Claude Code this was measured directly: plugin agent definitions
  are read once, before the MCP server starts, and are not re-read.)
- **It does not edit `config.toml`.** It only reads it. If a model is rejected, the
  command says which entry and what shape it wanted; fix the file and run it again.

## NL Triggers

- "update the agents" / "refresh the agent definitions" / "re-sync the roles"
- "I changed config.toml, apply it now" / "make it take effect now"
- "reinstall the role agents for pi" / "where does polyforge write the agent files?"
- 更新 agent 定义 / 立刻生效 / 重新同步角色
