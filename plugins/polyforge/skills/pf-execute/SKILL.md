---
name: pf-execute
description: >
  Wi Agent main loop — reads {wi_type}.{project}.md from the scenario repo and runs its
  ## Step: sections in order. rhs=false dispatches a subagent per step; rhs=true walks the
  user through step by step. The server only calls pf_complete_attempt.
---

# pf-execute — Wi Agent Main Loop

> **Stub.** The real body of this step is injected at call time by the `PreToolUse(Skill)`
> router (`hooks/pf-skill-router`): with superpowers enabled it tells you to delegate each
> step's implementation to `superpowers:subagent-driven-development` / `executing-plans` and
> **stop before finishing-a-development-branch**; without it, it injects this folder's
> `engine.native.md` (the native main loop). In both cases it also injects
> `../_common/{memory,storage,lifecycle}.md` (step reporting; commit/PR/wrap/CI owned by
> polyforge; `.pf_*` hygiene; wrap + cleanup).
>
> ⚠️ **Fallback:** if you are reading this line and did NOT receive an injected step body
> (the router did not fire), read this folder's `engine.native.md` plus
> `../_common/{memory,storage,lifecycle}.md` and follow those, or run `/pf-doctor`. When an
> injected body is present, it takes precedence over this stub.

**Pattern**: `/pf-execute` · **Required**: a currently-claimed wi (state file at
`<workspace>/.polyforge/state/<wi_id>.json`). **Mode**: from the wi's `requires_human_session`
(`false` → auto dispatch; `true` → interactive step-by-step).

## NL Triggers

- "执行" / "execute" / "run it" / "开始执行" / "go"
