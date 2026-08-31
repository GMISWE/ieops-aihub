# using-polyforge — Session Bootstrap & NL Router

## Session Startup Scan

On session start, before responding to any user message:

**Step 0 — Repo map first.** Your context already includes CLAUDE.md's `## Workspace`
managed block (auto-loaded by the harness). Consult it *before* anything else as your
**repo map**: each repo's one-line `positioning` tells you at a glance what every repo is.
Use it to route work — see [Repo Routing](#repo-routing-task--repo) below. Do **not** re-read the
file; it's already in context. Only if the `## Workspace` block is absent (a subagent
without injection, or a member who hasn't run `polyforge init`) → `Read` the workspace
`CLAUDE.md`, or run `/pf-init` to generate it.

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
