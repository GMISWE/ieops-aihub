## On-demand sections (NOT in your context — `Read` the file before you rely on it)

Session-start has a hard size budget, so these ship as files, not context:
`fragments/<name>` under the `using-polyforge` skill dir.

- **`post-claim-routing.md`** — **mandatory** before emitting a three-segment "Next steps"
  for a `requires_human_session=true` wi: it is the single source of truth for that list
  and its ordering. Do not improvise it. (Unused on the `rhs=false` auto-dispatch path.)
- **`memory-conventions.md`** — writing a memory: types, `related`, `work_item_id`,
  update-vs-reinforce; **and the hard rule that a `mem_…` id never goes in a repo doc nor a
  repo path in a memory.**
- **`diagram-convention.md`** — authoring an artifact with a diagram (aihub renders d2 only).
- **`platform-adaptation.md`** — under Codex / Copilot CLI: **no `Skill` tool there**, and
  MCP tool names differ.
