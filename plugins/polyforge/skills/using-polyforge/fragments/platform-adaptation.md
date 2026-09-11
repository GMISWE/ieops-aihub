## Platform adaptation (Codex / Copilot / pi / non-Claude runtimes)

polyforge ships as a Claude Code plugin and also runs under Codex (codex-cli), GitHub
Copilot CLI, and the pi coding agent. Tool names and skill invocation differ slightly by
runtime:

- **Claude Code**: invoke `/pf-*` skills via the `Skill` tool; the polyforge MCP tools are
  `mcp__plugin_polyforge_polyforge__pf_*`.
- **Codex**: there is no `Skill` tool — invoke a skill by typing `$pf-work` (etc.), via
  `/skills`, or let Codex select it by description, then follow its instructions. The polyforge
  MCP tools are `mcp__polyforge__pf_*`; the MCP server auto-registers from the plugin manifest
  (`.codex-plugin/plugin.json` -> `.codex-plugin/mcp.json`) as a local stdio command. The
  "Auth: Unsupported" label Codex shows for it is cosmetic (local servers have no transport
  auth) and does not block calls — see references/codex-tools.md.
- **Copilot CLI**: installs as a native plugin
  (`copilot plugin install polyforge@<marketplace>`), which auto-registers the MCP server from
  the plugin's `.mcp.json`. There is no `Skill` tool — skills load natively (`/skills` or by
  description). The polyforge MCP tools are addressed as `polyforge(pf_*)` in permission flags
  and appear as `polyforge-pf_*` to hooks.

- **pi coding agent**: installs with `plugins/polyforge/pi/install.sh [<project>]`. There is
  no `Skill` tool and no plugin manifest — pi discovers skills by directory, and the installer
  writes them to two. `~/.pi/agent/skills/` is user scope and is always read; it is the copy
  that makes the skills available. `<project>/.agents/skills/` is read **only once the project
  is trusted** — pi puts every ancestor `.agents/skills` behind its `/trust` gate, which is
  default-off, so that copy on its own leaves all of them silently unavailable (aihub#606).
  The polyforge skills work verbatim in either, no edits. MCP tools come through
  `pi-mcp-adapter`, which registers them individually as `polyforge_pf_*`. That is a FOURTH
  tool-name spelling: an underscore separator and no `__` at all, so any prefix handling
  written for the other three misses it. Hooks are bridged by a pi extension
  (`pi/extensions/polyforge/`) that converts pi events into the Claude Code-shaped payloads
  the existing hook scripts read; registration lives in `pi-hooks.json` alongside the other
  runtimes' files.

  🔴 Two `.mcp.json` settings are **security configuration, not tuning**: `scriptMode: false`
  and `disableProxyTool: true`. The `mcpScript` and `mcp` tools both take the real MCP tool
  name inside an argument, so a gate keyed on tool name cannot see a `pf_commit` underneath
  either of them. `directTools: true` matters for the same reason — without it all 45 tools
  collapse into the one proxy tool. Do not "optimise" any of the three back;
  `tests/pi-runtime.test.sh` asserts them. They are also **not sufficient on their own**: on
  a cold metadata cache the adapter registers the proxy tool anyway, so the bridge refuses
  calls to `mcp`/`mcpScript` outright as well.

  Pin pi to an exact version (`@earendil-works/pi-coding-agent@0.85.1`), not a caret range —
  it ships breaking changes in patch releases.

For the full tool-name mappings and per-runtime install / MCP-registration steps, see
`references/codex-tools.md` and `references/copilot-tools.md` in this skill.
