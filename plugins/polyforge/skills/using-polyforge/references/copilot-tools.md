# Copilot CLI tool mapping & installation

polyforge ships as a Claude Code plugin; GitHub Copilot CLI (verified on 1.0.68) has a
near-identical first-class plugin system, so polyforge installs as a native Copilot plugin.
A few tool names and the install path differ. When running under Copilot, apply this mapping.

## Installation

The marketplace flow is the durable one (direct local-path install still works in 1.0.68 but
prints a deprecation warning):

```bash
# durable: register the aihub repo root as a local marketplace, then install by name@marketplace
copilot plugin marketplace add <path-to-aihub-repo-root>
copilot plugin install polyforge@ieops-aihub

# quick/local (deprecated, prints a warning): install straight from the plugin dir
copilot plugin install <path-to>/plugins/polyforge
```

The plugin's `.mcp.json` auto-registers the `polyforge` MCP server on install (STDIO, reusing
`bin/polyforge-mcp.sh`) — no manual `copilot mcp add` needed. Verify with `copilot plugin list`,
`copilot mcp list`, and `copilot skill list`.

Manual MCP fallback (when not installing as a plugin):

```bash
copilot mcp add polyforge -- "$CLAUDE_PLUGIN_ROOT/bin/polyforge-mcp.sh"
# writes ~/.copilot/mcp-config.json; --transport stdio and --tools "*" are the defaults.
```

## Tool-name mapping (Claude Code -> Copilot CLI)

| Skill text references (Claude Code) | Copilot equivalent |
|---|---|
| `Skill` (invoke a `/pf-*` skill) | No `Skill` tool. Skills load natively — use `/skills`, or let Copilot select by description; then follow the skill's instructions. |
| `Task` (dispatch a subagent) | Copilot's own subagent mechanism; parallel implementation still splits into child work items. |
| `Read` / `Write` / `Edit` | native file tools — a single `apply_patch` tool handles create / edit / delete |
| `Bash` | native shell tool (`bash`) |
| MCP tools `mcp__plugin_polyforge_polyforge__pf_*` | `polyforge(pf_*)` in `--allow-tool` / `--deny-tool` permission syntax; `polyforge-pf_*` as the `toolName` seen by hooks |

The MCP tools have three coexisting representations under Copilot: parenthetical
`polyforge(pf_commit)` (permission flags), hyphen `polyforge-pf_commit` (hook `toolName`
matching), and slash `polyforge/pf_commit` (internal namespaced name). Do not conflate them.

## Bootstrap (sessionStart)

`copilot-hooks.json` runs `hooks/pf-session-start` on `sessionStart`; its stdout
`additionalContext` is injected into the session. The script emits both the top-level
`additionalContext` (read by Copilot) and the nested `hookSpecificOutput` shape (read by
Claude Code) in one object, so a single script serves every harness. superpowers is detected
from `~/.copilot/settings.json` `enabledPlugins`.

## Guard hooks

`copilot-hooks.json` wires the same guards as the other harnesses, using Copilot's camelCase
hook schema with `polyforge-pf_*` / `apply_patch` matchers:

- `preToolUse` -> `pf-commit-guard` (attribution guard on commit/PR text plus the
  protected-branch / worktree git guards). A block is emitted as a top-level
  `{"permissionDecision":"deny", "permissionDecisionReason":"..."}` — the only form Copilot
  honors (the nested shape and a bare exit code 2 are ignored).
- `postToolUse` -> `pf-chain-hook.cjs` (lifecycle chain visualization) and
  `pf-superpowers-bridge` (mirrors a superpowers spec/plan doc into aihub; it reads the target
  path from the `apply_patch` patch envelope, since Copilot has no `file_path` field).

## Caveats

- Copilot iterates fast (facts verified on 1.0.68); tool naming and hook schema may shift —
  check `copilot plugin --help`, `copilot mcp --help`, and the hooks reference if behavior
  drifts.
- `${CLAUDE_PLUGIN_ROOT}` works unchanged under Copilot: it also exports `COPILOT_PLUGIN_ROOT`
  and `PLUGIN_ROOT` with the same value, so manifests and hook commands need no env-var rewrite.
- Hooks and MCP servers DO fire under `-p` (non-interactive) — verified on 1.0.68.
- The `PreToolUse(Skill)` skill-router does not run under Copilot (no `Skill` tool). `/pf-*`
  skills load their step body from each `SKILL.md` fallback pointer, same as Codex. The wi
  lifecycle (claim / step / artifact / commit / PR / wrap) is unchanged, via `polyforge(pf_*)`.
