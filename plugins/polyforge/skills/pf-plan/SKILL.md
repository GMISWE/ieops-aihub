---
name: pf-plan
description: >
  Layer 2 step — write the plan artifact for the current wi. Reads the spec, breaks
  implementation into ordered steps, optionally spawns child wi's for parallel subtasks
  with blocked_by dependencies.
---

# pf-plan — Plan & Child Wi Creation

> **Stub.** The real body of this step is injected at call time by the `PreToolUse(Skill)`
> router (`hooks/pf-skill-router`): with superpowers enabled it points planning at
> `superpowers:writing-plans`; without it, it injects this folder's `engine.native.md`.
> In both cases it also injects `../_common/{memory,storage,lifecycle}.md` (Memory-First
> recall, save_artifact, step reporting and wrap — all owned by polyforge).
>
> ⚠️ **Fallback:** if you are reading this line and did NOT receive an injected step body
> (the router did not fire), read this folder's `engine.native.md` plus
> `../_common/{memory,storage,lifecycle}.md` and follow those, or run `/pf-doctor`. When an
> injected body is present, it takes precedence over this stub.

**Pattern**: `/pf-plan` · **Required**: a currently-claimed wi with a completed spec step.

## NL Triggers

- "计划" / "plan" / "plan this out"
- "写 plan" / "break this down" / "create subtasks"
- "how should we approach this" / "next steps"
