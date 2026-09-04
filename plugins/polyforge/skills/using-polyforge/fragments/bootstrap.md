## Session Startup Scan

On session start, before responding to any user message:

**Step 0 — Repo map first.** CLAUDE.md's `## Workspace` managed block is already in your
context. Consult it *before* anything else — it is the repo map Repo Routing below uses —
and do **not** re-read the file. Absent (a subagent without injection, or a member who
hasn't run `polyforge init`) → `Read` the workspace `CLAUDE.md`, or `/pf-init` to write it.

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
6. Call `pf_get_ready_queue(project)` to show the current ready queue summary.
