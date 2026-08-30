# using-polyforge — Session Bootstrap & NL Router

## Usage

**Purpose**: Session bootstrap meta-skill — establishes Iron Rules (IR1-IR3), routes natural-language intent to the correct `/pf-*` skill, enforces Memory-First, and defines the mandatory three-segment output format.

**Pattern**: (auto-loaded at session start; not invoked by the user)

**Required**: none — runs automatically before the first user message

**Flags**: none

## Session Startup Scan

On session start, before responding to any user message:

**Step 0 — Repo map first.** Your context already includes CLAUDE.md's `## Workspace`
managed block (auto-loaded by the harness). Consult it *before* anything else as your
**repo map**: it lists every repo with a one-line `positioning`, which is what you route
on — see [Repo Routing](#repo-routing-task--repo) below. Do **not** re-read the file; it's
already in context.

The per-repo detail (`tech_stack` / `main_modules` / `change_scenarios`) is deliberately
**not** in the block — it would sit at context position 0 on every request, while it is
only needed at the moment you route. Three cases:

- **`.polyforge/repo-map/<project>.md` exists** → read that file, on demand, for the one
  project you routed to.
- **It doesn't, and the block still shows `  - stack:` / `  - modules:` / `  - changes:`
  bullets under each repo** → this workspace hasn't re-run `polyforge init` since the
  split, so those fields may still be inline in the block (older format); use them
  straight from context.
- **Neither** → say "repo map missing for `<project>` — run `polyforge init`" and fall
  back to live tools (`codegraph_*`, `Grep`, a directory listing in the worktree). Do
  **not** guess a repo's internals from its one-line positioning. `polyforge doctor`
  reports this same state as a `claude_md` warning.

Only if the `## Workspace` block is absent entirely (a subagent without injection, or a
member who hasn't run `polyforge init`) → `Read` the workspace `CLAUDE.md`, or run
`/pf-init` to generate it.

Then run the state/wi scan:

1. Scan `~/.polyforge/state/*.json` for active state files.
2. For each file, read `wi_id` + `attempt_id`.
3. Call `pf_get_work_item(wi_id)` to get full wi detail including content.
4. If attempt is still `running` → surface:
   ```
   ⚠️ You have an in-progress wi: [slug] — resume?
   Goal: [goal]
   Background: [content[:200]]...   ← only if content is non-null
   ```
5. If attempt is `superseded` or `lost_lease` → prompt: "Stale state file found for [wi_id]. Delete it?"
6. Call `pf_recall(work_item_id=<wi_id>, project=<wi.project>, top_k=10)` to surface wi-linked memories.
7. Call `pf_get_ready_queue(project)` to show the current ready queue summary.
