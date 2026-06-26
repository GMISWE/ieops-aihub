# Codex tool mapping & MCP registration

polyforge ships as a Claude Code plugin. Codex (codex-cli) is largely compatible, but a few
tool names and one install step differ. When running under Codex, apply this mapping.

## MCP server registration (one-time, required)

Codex does NOT register an MCP server from the plugin manifest. Register the polyforge MCP
server explicitly (this writes `~/.codex/config.toml [mcp_servers.polyforge]`):

```bash
codex mcp add polyforge -- "$CLAUDE_PLUGIN_ROOT/bin/polyforge-mcp.sh"
# CLAUDE_PLUGIN_ROOT is set by Codex for installed plugins. If unset, pass the absolute path
# to <plugin-root>/bin/polyforge-mcp.sh instead.
```

Verify with `codex mcp list` or the in-session `/mcp` command. The tools then surface as
`mcp__polyforge__pf_*`.

## Tool-name mapping (Claude Code -> Codex)

| Skill text references (Claude Code) | Codex equivalent |
|---|---|
| `Skill` (invoke a `/pf-*` skill) | No `Skill` tool. Skills load natively — type `$pf-work` (etc.), use `/skills`, or let Codex select by description; then follow the skill's instructions. |
| `Task` (dispatch a subagent) | `spawn_agent` (requires `~/.codex/config.toml [features] multi_agent = true`) |
| parallel `Task` | multiple `spawn_agent`, then `wait_agent` / `close_agent` |
| `TodoWrite` | `update_plan` |
| `Read` / `Write` / `Edit` | native file tools (`apply_patch` for edits) |
| `Bash` | native shell tool |
| MCP tools `mcp__plugin_polyforge_polyforge__pf_*` | `mcp__polyforge__pf_*` (server registered as `polyforge`) |

## Skill-router note

Under Claude Code a PreToolUse(`Skill`) hook injects the step body for `/pf-spec`, `/pf-plan`,
and `/pf-execute`. Codex has no `Skill` tool, so that hook does not run. Under Codex these
skills load their step body from each `SKILL.md` directly (the stub's fallback pointer routes
to `engine.native.md` + `_common/*`). The wi lifecycle (claim / step / artifact / commit / PR /
wrap) is unchanged — it runs through the `mcp__polyforge__pf_*` tools.
