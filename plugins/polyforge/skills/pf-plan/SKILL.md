---
name: pf-plan
description: >
  Use when a claimed wi has a spec and needs an implementation plan broken into ordered
  steps before coding, or the user says plan, break this down, or create subtasks.
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

## Usage

**Purpose**: Write the plan artifact for the current wi, breaking implementation into ordered steps.

**Pattern**: `/pf-plan`

**Required**: a currently-claimed wi with a completed spec step.

**Flags**: none

## NL Triggers

- "plan" / "make a plan" / "plan this out"
- "write a plan" / "break this down" / "create subtasks"
- "how should we approach this" / "next steps"
