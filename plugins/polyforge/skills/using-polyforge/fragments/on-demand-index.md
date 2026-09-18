## On-demand sections (NOT in context - `Read` the file before you rely on it)

Hard session-start size budget: these ship as files, not context.
`fragments/<name>` under the `using-polyforge` skill dir.

- **`post-claim-routing.md`** - **mandatory** before emitting a three-segment "Next steps"
  for a `requires_human_session=true` wi: the single source of truth for that list and its
  ordering - do not improvise it. Two more sections apply either way. Pinned DB workflow
  wi (`steps_version > 0`)? Also **`workflow-v2.md`**.
- **`memory-conventions.md`** - writing a memory: types, `related`, `work_item_id`,
  update-vs-reinforce; **and the hard rule that a `mem_…` id never goes in a repo doc nor a
  repo path in a memory.**
- **`repo-detail.md`** - a repo's modules / changes / stack (the block has only a pointer).
- **`diagram-convention.md`** - authoring an artifact with a diagram (aihub renders d2 only).
- **`platform-adaptation.md`** - under Codex / Copilot CLI: **no `Skill` tool there**, and
  MCP tool names differ.
