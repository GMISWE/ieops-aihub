## NL Routing

This table **indexes** non-obvious intent -> operation mappings, not an exhaustive keyword
classifier. Infer intent and route to the nearest match; if nothing fits or it's ambiguous,
**ask rather than guess**. Each `/pf-*` skill's own `NL Triggers` section is authoritative
(this index may lag). NL Routing decides *what operation*; **Repo Routing** (below) decides
*which repo*.

| intent | operation |
|---|---|
| what's ready today / dispatch work / ready queue | `pf_get_ready_queue` |
| what needs my decision / needs attention | `pf_get_ready_queue` -> `needs_human_session[]` |
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

Some words route by context - is there **(a)** a slug, **(b)** a running/claimed wi this
session, **(c)** a skill flow mid-step?

- **continue / go on / next** - mid-flow or mid-step -> proceed to the next step,
  **not** resume. Only "resume `<slug>`" on a **paused** wi routes to
  `/pf-work <slug> --resume`.
- **done / finished** - mid-step -> continue the flow; whole-wi ("wrap up" / "finished" /
  "wrap") -> `/pf-stop --wrap`.
- **begin / start** - with a slug -> claim it (Mode B); without -> new wi (Mode A).
- **status** - inside a claimed wi -> that wi's detail; otherwise -> project ready queue.

When still ambiguous, state your interpretation and confirm before acting.
