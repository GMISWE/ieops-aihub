---
name: using-polyforge
description: >
  Use at the start of any polyforge session and before any /pf-* skill, work-item, or
  memory operation; loaded automatically at session start.
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

  ==========================================================================
  SIZE BUDGET — READ BEFORE ADDING AN @include (aihub#285)
  ==========================================================================
  The assembled payload is delivered as ONE `additionalContext` string. Claude
  Code replaces any single hook output over 10,000 CHARACTERS with a
  `<persisted-output>` wrapper: the full text is written to a file on disk and
  only a ~2,000-character preview is put in the model's context. That failure is
  SILENT — the hook still exits 0 and the skill still "looks" injected.
  (10,000 and the character-vs-byte unit are both measured, not inferred: a
  10,000-char payload arrives intact, 10,001 is truncated, and a 10,000-char /
  29,658-byte CJK payload also arrives intact.)

  This manifest was 10 unconditional fragments = 18,286 characters, so ~89% of it
  never reached any model. Two rules keep it fixed:

    1. TOTAL STAYS UNDER BUDGET. tests/using-polyforge-payload.test.sh asserts the
       assembled size against PF_PAYLOAD_MAX_CHARS. If you add content here and
       that test goes red, do not raise the limit — move something to the
       on-demand tier instead.

       ⚠️ That suite is NOT wired into CI yet (.github/workflows/ci.yml runs only
       launcher-update-check.test.sh — see its aihub#254 note). Until it is, this
       is a check someone must run by hand, not an automatic guarantee. Run it
       after editing anything under fragments/.

    2. IRON RULES STAY FIRST. fragments/iron-rules.md is @include'd first so that
       IR1-IR3 land inside the ~2,000-character preview window. If the budget is
       ever busted again, the rules survive the truncation instead of bootstrap
       boilerplate. The same test asserts this ordering property.

  ON-DEMAND TIER (deliberately NOT @include'd — see fragments/on-demand-index.md):
    fragments/post-claim-routing.md    4,813 B — only applies to a three-segment
                                       "Next steps" for requires_human_session=true;
                                       the rhs=false auto-dispatch path is explicitly
                                       unaffected by it.
    fragments/memory-conventions.md    2,173 B — its load-bearing rule ("all memory
                                       lives in aihub, local .md memory is deprecated
                                       here") is already stated — in fact stated more
                                       strictly — in memory-first.md, which stays in the
                                       always-injected tier. Its memory-type vocabulary
                                       is also carried by .polyforge/usage.md (11 rows
                                       there vs 4 here) via CLAUDE.md's @import.
                                       ⚠️ usage.md is a partial fallback ONLY: it carries
                                       the type table and nothing else (no `related`
                                       links, no work_item_id, no cross-system link
                                       discipline), and internal/cli/init.go does not
                                       regenerate it once it exists, so older workspaces
                                       may not have even that. The link-discipline and
                                       work_item_id rules therefore live only in the
                                       fragment — on-demand-index.md names them in its
                                       trigger so an agent knows when to go read it.
    fragments/platform-adaptation.md   1,431 B — its most load-bearing fact, the
                                       per-runtime MCP tool names for Claude Code /
                                       Codex / Copilot, is already spelled out in the
                                       hook's own preamble, which is always delivered.
                                       ⚠️ What does NOT survive is "there is no `Skill`
                                       tool under Codex/Copilot" — a skill-INVOCATION
                                       fact, not install detail, and the injected payload
                                       is full of `/pf-*` instructions that assume a
                                       Skill tool. on-demand-index.md's trigger says so
                                       explicitly. If this keeps biting non-Claude
                                       runtimes, move that one clause into the preamble.
    fragments/diagram-convention.md      702 B — only applies while authoring an artifact
                                       that contains a diagram (/pf-spec, /pf-plan).
    fragments/repo-detail.md           ~2,2xx B — where a repo's main_modules /
                                       change_scenarios / tech_stack live now that the
                                       `## Workspace` block carries only a one-line
                                       positioning per repo plus a per-project pointer
                                       (aihub#291). Deliberately on-demand: the pointer
                                       is emitted by the same code that writes
                                       .polyforge/repo-map/<project>.md, so the block
                                       itself routes you to the detail and no resident
                                       prose is needed to describe the layout.

  Nothing was deleted: every file above still ships and is still readable. It moved
  from a channel that silently dropped it to one that does not.
-->

@include: fragments/iron-rules.md
@include: fragments/output-format.md
@include: fragments/memory-first.md
@include: fragments/bootstrap.md
@include: fragments/nl-routing.md
@include: fragments/repo-routing.md
@include: fragments/on-demand-index.md
