## Iron Rules (all operations must obey)

**IR1 — Work-item-gated writes**
Every write operation (`git commit`, `git push`, `gh pr create`, `Edit`/`Write` under
`.repo/`) must occur inside a claimed work-item worktree (`pf.<shortid>/<repo>/`).
No env-var bypass.

> Engine choice ≠ bypass. Using the `superpowers` plugin's skills as the **engine** inside a
> claimed `/pf-*` step (brainstorming / writing-plans / subagent-driven-development / …)
> satisfies IR1 — those writes land in the claimed worktree and go through the wi lifecycle.
> What IR1 forbids is skipping claim / step / artifact / wrap, not which engine authors content.

**IR2 — Analyze obstacles; track blockers as work items**
When you hit an obstacle, analyze the root cause — do not route around it. If the
obstacle is a bug or cannot be resolved in the current context, create a work item to
track it (`/pf-work --goal "bug: ..."`).

**IR3 — MCP unavailable → stop**
If the polyforge MCP server is unreachable, report the error and stop. Do not fall
back to direct HTTP calls. Check connectivity with `pf doctor` then retry.
