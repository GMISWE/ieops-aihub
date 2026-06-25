---
name: pf-spec
description: >
  Use when a claimed feature or bug wi needs its scope, non-goals, design, and
  acceptance criteria defined, or when a bug needs root-cause analysis, e.g. write a
  spec, scope this out, debug, or root cause.
---

# pf-spec — Spec & Debug Analysis

> **Stub.** The real body of this step is injected at call time by the `PreToolUse(Skill)`
> router (`hooks/pf-skill-router`): with superpowers enabled it points spec authoring at
> `superpowers:brainstorming`; without it, it injects this folder's `engine.native.md`.
> In both cases it also injects `../_common/{memory,storage,lifecycle}.md` (Memory-First
> recall, save_artifact, step reporting and wrap — all owned by polyforge).
>
> ⚠️ **Fallback:** if you are reading this line and did NOT receive an injected step body
> (the router did not fire), read this folder's `engine.native.md` plus
> `../_common/{memory,storage,lifecycle}.md` and follow those, or run `/pf-doctor`. When an
> injected body is present, it takes precedence over this stub.

## Usage

**Purpose**: Write the spec artifact for the current wi (scope, non-goals, design, acceptance), or run root-cause analysis for a bug.

**Pattern**: `/pf-spec`

**Required**: a currently-claimed `feature` / `critical_bug` wi.

**Flags**: none

## NL Triggers

- "design" / "spec" / "brainstorm" / "discuss the approach"
- "write a spec" / "define requirements" / "scope this out"
- "debug" / "what's going on with this bug" / "analyze this issue"
- "root cause" / "why is this failing"
