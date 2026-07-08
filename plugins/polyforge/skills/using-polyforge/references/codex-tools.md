# Codex tool mapping & MCP registration

polyforge ships as a Claude Code plugin. Codex (codex-cli) is largely compatible, but a few
tool names and one install step differ. When running under Codex, apply this mapping.

## MCP server registration (automatic)

The plugin manifest auto-registers the polyforge MCP server on install, as a **local stdio
command** — no manual step. This works via `.codex-plugin/plugin.json`'s
`"mcpServers": "./.codex-plugin/mcp.json"` pointer (Codex's manifest MCP form is a path to a
direct server-map file, not Claude's inline `mcpServers` object). Verify with
`codex mcp list` or the in-session `/mcp` command; tools surface as `mcp__polyforge__pf_*`.

**"Auth: Unsupported" is expected and harmless.** Codex only reports an auth status for
OAuth / streamable-HTTP servers; every *local stdio* server shows "Unsupported" in that
column. It does NOT block tool calls (openai/codex#15609). polyforge authenticates to the
backend internally through `~/.polyforge/config.toml` ([auth] api_key + [server] url), never
via MCP transport auth — so there is nothing for Codex to "support" here.

**Fallback (only if auto-registration did not happen).** Register by hand, but with an
ABSOLUTE path: in an interactive shell `$CLAUDE_PLUGIN_ROOT` is empty, so the
`$CLAUDE_PLUGIN_ROOT/...` form records a truncated path and the server fails to start.

```bash
codex mcp add polyforge -- /absolute/path/to/<plugin-cache>/bin/polyforge-mcp.sh
```

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

Under Claude Code a PreToolUse(`Skill`) hook injects the step body for `/pf-execute` only
(its stub's fallback pointer routes to `engine.native.md` + `_common/*`). `/pf-spec` and
`/pf-plan` are self-sufficient `SKILL.md` files with no router involvement. Codex has no
`Skill` tool, so the router hook does not run at all under Codex — `/pf-execute` loads its
step body from `SKILL.md`'s fallback pointer directly, same as `/pf-spec` and `/pf-plan`.
The wi lifecycle (claim / step / artifact / commit / PR / wrap) is unchanged — it runs
through the `mcp__polyforge__pf_*` tools.
