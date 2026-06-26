## Platform adaptation (Codex / non-Claude runtimes)

polyforge ships as a Claude Code plugin and also runs under Codex (codex-cli). Tool names and
skill invocation differ slightly by runtime:

- **Claude Code**: invoke `/pf-*` skills via the `Skill` tool; the polyforge MCP tools are
  `mcp__plugin_polyforge_polyforge__pf_*`.
- **Codex**: there is no `Skill` tool — invoke a skill by typing `$pf-work` (etc.), via
  `/skills`, or let Codex select it by description, then follow its instructions. The polyforge
  MCP tools are `mcp__polyforge__pf_*`, and the MCP server must be registered once with
  `codex mcp add polyforge -- "$CLAUDE_PLUGIN_ROOT/bin/polyforge-mcp.sh"`.

For the full Claude Code -> Codex tool-name mapping and the MCP registration step, see
`references/codex-tools.md` in this skill.
