## Platform adaptation (Codex / Copilot / non-Claude runtimes)

polyforge ships as a Claude Code plugin and also runs under Codex (codex-cli) and GitHub
Copilot CLI. Tool names and skill invocation differ slightly by runtime:

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

For the full tool-name mappings and per-runtime install / MCP-registration steps, see
`references/codex-tools.md` and `references/copilot-tools.md` in this skill.
