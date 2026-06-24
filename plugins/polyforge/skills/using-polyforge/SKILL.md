---
name: using-polyforge
description: >
  Meta skill — loaded automatically at session start. Establishes Iron Rules
  (IR1-IR3), routes natural-language intent to the correct /pf-* skill, enforces
  Memory-First before every action, and defines the mandatory three-segment output
  format for all polyforge skills.
---

<!--
  using-polyforge is ASSEMBLED at SessionStart by hooks/pf-session-start from the
  fragments listed below, IN ORDER. Each `@include: <path>` pulls a fragment; an
  optional immediately-following `when: <cond>` makes that fragment conditional
  (no `when:` = always included).

  Conditions:
    when: superpowers   — the superpowers plugin is ENABLED for this workspace
                          (enabledPlugins scanned across ~/.claude + cwd/.claude
                          settings.json / settings.local.json, low->high precedence).

  To edit the skill, edit a fragment. To add a conditional section, drop a fragment
  under fragments/ and add an `@include:` (+ optional `when:`) here. This mirrors the
  scenario layer's {wi_type}.{project}.md + @include common/[step] pattern.

  When invoked directly via the Skill tool (rare — this skill is auto-injected), read
  the fragments below to see the full content.
-->

@include: fragments/bootstrap.md
@include: fragments/iron-rules.md
@include: fragments/memory-first.md
@include: fragments/output-format.md
@include: fragments/post-claim-routing.md
@include: fragments/nl-routing.md
@include: fragments/memory-conventions.md
@include: fragments/repo-routing.md
@include: fragments/diagram-convention.md
@include: fragments/superpowers-bridge.md
when: superpowers
