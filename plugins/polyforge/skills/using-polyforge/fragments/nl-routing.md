## NL Routing

This table **indexes** non-obvious intent → operation mappings; it is **not** an exhaustive
keyword classifier. Infer the user's intent and route to the nearest match — if nothing fits
or intent is ambiguous, **ask rather than guess**. Each `/pf-*` skill's own `NL Triggers`
section is authoritative for its triggers (this index may lag). NL Routing decides *what
operation*; **Repo Routing** (below) decides *which repo*.

| intent | operation |
|---|---|
| what's ready today / dispatch work / ready queue | `pf_get_ready_queue` |
| what needs my decision / needs attention | `pf_get_ready_queue` → `needs_human_session[]` |
| begin / new task / new / start | `/pf-work` (Mode A) |
| claim + slug | `/pf-work <slug>` (Mode B) |
| resume + slug | `/pf-work <slug> --resume` (Mode C) |
| takeover + slug | `/pf-work <slug> --force` (Mode D) |
| pause / done / wrap / finished / fail / abandon | `/pf-stop --pause` \| `--wrap` \| `--fail` |
| design / spec / brainstorm | `/pf-spec` |
| this bug / debug | `/pf-spec` (debug variant) |
| note / log | `pf_emit_event(event_type="note", ...)` |
| doctor / can't connect | `/pf-doctor` |
| release / cut | `/pf-release` |
| sync Jira/GitHub / push to external | `/pf-sync` |
| user management / whoami / issue key / list users | `/pf-user` |
| revise spec/plan per annotations / resolve review comments | `/pf-revise` |

### Disambiguation (context-dependent intents)

Some words route differently by context — check **(a)** is there a slug? **(b)** is there a
running/claimed wi this session? **(c)** are we mid-step inside a skill flow?

- **continue / go on / next** — if a skill flow or step is in progress → *proceed within
  it* (advance to the next step), **not** resume. Only "resume `<slug>`" for a
  **paused** wi routes to `/pf-work <slug> --resume`.
- **done / finished** — mid-step ("this step is done") → continue the flow; whole-wi
  ("wrap up / this wi is finished / wrap") → `/pf-stop --wrap`.
- **begin / start** — with a slug → claim it (Mode B); without → new wi (Mode A).
- **status** — inside a claimed wi → that wi's detail; otherwise → project ready queue.

When still ambiguous, state your interpretation and confirm before acting.
