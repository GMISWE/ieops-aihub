---
name: pf-execute
description: >
  Use when a claimed wi is ready to be carried out step by step against its scenario
  step graph, e.g. the user says execute, run it, or go.
---

# pf-execute — Wi Agent Main Loop

> **Stub.** The real body of this step is injected at call time by the `PreToolUse(Skill)`
> router (`hooks/pf-skill-router`): with superpowers enabled it tells you to delegate each
> step's implementation to `superpowers:subagent-driven-development` and
> **stop before finishing-a-development-branch**; without it, it injects this folder's
> `engine.native.md` (the native main loop). In both cases it also injects
> `../_common/{memory,storage,lifecycle}.md` (step reporting; commit/PR/wrap/CI owned by
> polyforge; `.pf_*` hygiene; wrap + cleanup).
>
> ⚠️ **Fallback:** if you are reading this line and did NOT receive an injected step body
> (the router did not fire), read this folder's `engine.native.md` plus
> `../_common/{memory,storage,lifecycle}.md` and follow those, or run `/pf-doctor`. When an
> injected body is present, it takes precedence over this stub.

## Usage

**Purpose**: Run the wi's scenario step graph to completion (the main execution loop).

**Pattern**: `/pf-execute`

**Required**: a currently-claimed wi (state file at `<workspace>/.polyforge/state/<wi_id>.json`).

**Flags**: Mode is derived from the wi's `requires_human_session` (`false` -> auto dispatch per step; `true` -> interactive step-by-step).

## NL Triggers

- "execute" / "run it" / "start executing" / "go"
