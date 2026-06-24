## Memory: unified to polyforge (local .md deprecated)

In a polyforge workspace, **all memory lives in aihub** — write via `pf_remember`
(facts/rules/experience) or `pf_save_artifact` (spec/plan/review/…), recall via `pf_recall`.
The harness's local Claude memory (`~/.claude/projects/.../memory/*.md` + `MEMORY.md`,
`[[wiki-link]]` syntax) is **deprecated here: do NOT create or maintain local `.md` memory
files in a polyforge workspace.** (Non-polyforge projects may still use local memory; this
override is scoped to polyforge workspaces.)

Conventions:

- **Cross-memory links** → aihub-native `related` (the `related_memory_ids` param on
  `pf_remember`, stored in `memory_relations`), NOT `[[name]]` wiki-links. aihub renders
  `related` as clickable links.
- **Owning wi** → set `work_item_id` on the memory; the aihub UI renders it as a clickable
  link. Do NOT hand-write "belongs to wi X" in the body — that metadata is surfaced by the UI.
- **Memory type** → an aihub enum type (`experience.*` / `fact.*` / `rule.*` /
  `methodology.*`), not the local `user/feedback/project/reference` vocabulary.
- **Cross-system link discipline** (keep separate): aihub artifacts/memories link via aihub
  `related` + owning-wi; repo docs/PRs (e.g. ieops-doc) link via GitHub relative paths.
  Never put an aihub `mem_…` ref in a repo doc, nor a repo path inside an aihub memory.

This takes precedence over the harness's default local-memory instruction within a polyforge
workspace. Caveat: the harness may still auto-recall a pre-existing local `MEMORY.md` at
session start until those local files are retired; retiring the migrated local files and any
global-config change are tracked as a separate follow-up (the data already lives in aihub via
aihub#74 Stream C).
