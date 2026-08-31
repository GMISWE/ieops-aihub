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
- **Updating a memory** → use `pf_update_memory` to revise an existing memory: it creates a
  new version superseding the current head and advances the `latest_id` cursor, so any id you
  already hold still resolves to the latest. `pf_reinforce_memory` only appends context to the
  same row (no new version); artifacts still revise via `pf_save_artifact(..., supersedes_memory_id=…)`.
- **Cross-system link discipline** (keep separate): aihub artifacts/memories link via aihub
  `related` + owning-wi; repo docs/PRs (e.g. ieops-doc) link via GitHub relative paths.
  Never put an aihub `mem_…` ref in a repo doc, nor a repo path inside an aihub memory.

### Memory Type Reference

Pick the type by **consumer** — which skill needs to recall it — not by what the content is
about. `experience.*` is written automatically by `/pf-retro`; for a hand-written memory
prefer `rule.*` / `fact.*`.

| content | type | recalled by |
|---|---|---|
| init/setup experience | `experience.init` | pf-init |
| bug patterns found during execution | `experience.debug` | pf-plan, pf-execute, pf-retro |
| an approach that solved a class of problem | `experience.approach` | pf-plan, pf-execute, pf-retro |
| pitfalls to avoid | `experience.pitfall` | pf-plan, pf-execute, pf-retro |
| wi lifecycle operating rules | `rule.work` | using-polyforge, pf-spec |
| init-phase operating rules | `rule.init` | pf-init |
| scheduling rules | `rule.scheduling` | pf-init (managed block) |
| domain facts | `fact.<subtopic>` | pf-spec |
| spec output | `methodology.spec` | pf-plan, pf-execute, pf-retro |
| plan output | `methodology.plan` | pf-execute, pf-retro |
| release record | `methodology.release` | pf-release |

🔴 **Six of those rows are off the curated list, and they fail in three different ways.**
Be precise about which, because the modes are not interchangeable:

- `methodology.spec` / `methodology.plan` — **`pf_remember` refuses all `methodology.*`**
  outright (`PfRememberTypeEnum` is the curated list minus `methodology.*`, and
  `handleRemember` has a hard gate, aihub#210). Write these with `pf_save_artifact`.
- `methodology.release` — refused by `pf_remember` *and* absent from
  `MethodologyTypeEnum` (`spec|plan|review|execute|retro|wrap_summary`), so
  `pf_save_artifact` rejects it too. **This row is valid nowhere.**
- `experience.init` / `rule.init` / the `fact.<subtopic>` placeholder — **accepted by the
  server**, whose validation is a lenient four-prefix check (`experience.` / `fact.` /
  `rule.` / `methodology.`), but off the curated enum, so the tool schema does not offer
  them and contract-lint flags them. Usable, not blessed.

The curated list is `experience.approach|code|debug|pitfall`,
`fact.architecture|constraint|note|reference`,
`rule.coding|convention|process|scheduling|work`. **The tool schema is authoritative.**

That mismatch is exactly the point. This table was duplicated into `.polyforge/usage.md` by
`polyforge init`, and that file is never regenerated once it exists — so the copy there has
been wrong since 2026-05-25 and no one could fix it. aihub#294 moved the table here, to the
versioned channel, deliberately UNCHANGED plus this correction rather than quietly rewritten,
so the drift stays visible. Reconciling the rows with the enum is not yet tracked by a work
item. This is now the only copy.

This takes precedence over the harness's default local-memory instruction within a polyforge
workspace. Caveat: the harness may still auto-recall a pre-existing local `MEMORY.md` at
session start until those local files are retired; retiring the migrated local files and any
global-config change are tracked as a separate follow-up (the data already lives in aihub via
aihub#74 Stream C).
