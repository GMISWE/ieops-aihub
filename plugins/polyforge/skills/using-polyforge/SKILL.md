---
name: using-polyforge
description: >
  Use at the start of any polyforge session and before any /pf-* skill, work-item, or
  memory operation; loaded automatically at session start.
---

<!--
  using-polyforge is ASSEMBLED at SessionStart by hooks/pf-session-start from the
  fragments listed below, IN ORDER. Each `@include: <path>` pulls a fragment into the
  RESIDENT tier; each `@ondemand: <path>` declares a fragment that ships as a file and
  is NEVER injected. Directives may be followed by an attribute block (see MANIFEST
  SCHEMA below); `when: <cond>` makes an `@include` conditional (no `when:` = always).

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
  MANIFEST SCHEMA — kind / gate / authority (aihub#295)
  ==========================================================================
  A bare `@include:` list can say WHICH fragments and IN WHAT ORDER. It cannot say
  whether a fragment is information or a rule, nor which mechanism enforces a rule.
  That gap is not cosmetic: it is the difference between two failure modes.

    info  read on demand — the cost of not reading it is one extra tool call.
                           BOUNDED and OBSERVABLE.
    rule  read on demand — "should have read it and didn't" is externally
                           IDENTICAL to "read it and ignored it". Both silent.

  So every directive carries an attribute block. Keys, one per line, immediately
  after the directive, no blank line inside the block:

    kind:             REQUIRED. `info` or `rule`.
    gate:             REQUIRED iff kind is rule; FORBIDDEN otherwise. Comma-separated
                      mechanism names, or the literal `none`. FAIL-CLOSED: if even one
                      rule inside the fragment is unenforced, the whole fragment is
                      `none`. Names are checked against the mechanisms that actually
                      exist on disk (hooks/* and tests/*.test.*), so an invented gate
                      name is a lint failure, not a comforting label.
    gate-partial:     OPTIONAL, only with `gate: none`. Records mechanisms that cover
                      SOME of the fragment's rules, without weakening the tier rule.
    authority:        REQUIRED. `self` if this file is the maintained copy; otherwise
                      a pointer to where the maintained copy lives. (This is the
                      question that found redundancies "is this needed every request?"
                      could not — aihub#296.)
    resident-because: REQUIRED iff an `@include:` entry has a non-`self` authority;
                      FORBIDDEN otherwise. If the authoritative copy is elsewhere, say
                      why this one still spends resident budget.
    when:             OPTIONAL, `@include:` only. MUST be the FIRST attribute line —
                      see BACKWARD COMPATIBILITY below.

  THE TIER RULE:  kind: rule + gate: none  =>  must NOT leave the resident tier.
  An unenforced rule that is not in context has no observable failure mode at all.
  tests/using-polyforge-manifest.test.sh enforces this, with a documented baseline of
  pre-existing violations that may only shrink.

  WHERE THE CURRENT `kind` VALUES CAME FROM — so they can be challenged, not inherited
  aihub#295 measured the rules this skill ships: SIX of them, of which exactly ONE has
  an enforcing mechanism — IR1, via hooks/pf-commit-guard's worktree and attribution
  gates. That is why iron-rules.md is `gate: none` with `gate-partial: pf-commit-guard`:
  partial coverage is fail-closed, because IR2 and IR3 are in the same file and nothing
  catches either. The other four ungated rules are the three-segment output format, NL
  routing, and post-claim Next-steps routing.
  Those six rules span four fragments, so those four are `kind: rule` and the remaining
  eight are `kind: info`. That line was NOT re-derived here. Four fragments sit close to
  it and a later measurement may well move them: memory-first.md ("pf_recall before
  every substantive action"), memory-conventions.md ("never put a mem_… id in a repo
  doc"), bootstrap.md (the session startup scan) and repo-routing.md all prescribe
  behaviour whose violation is silent. Reclassifying any of them ENLARGES the resident
  lower bound below — which is the honest direction, and the reason to do it with a
  measurement rather than in passing while moving a fragment.

  BACKWARD COMPATIBILITY (why `@ondemand:` and not `tier: on-demand`)
  The plugin and the polyforge binary update through independent channels, so a
  manifest can meet a parser older than itself. The pre-aihub#295 parser skips any
  line that does not start with `@include:`, and looks for `when:` ONLY on the line
  immediately after one. Therefore:
    - attribute lines are invisible to it — new keys can be added freely;
    - an on-demand entry MUST NOT be spelled as an `@include:` with a tier attribute,
      or an old parser would inject it and silently blow the size budget. It gets its
      own directive verb, which an old parser drops entirely — the safe direction.
    - `when:` must stay adjacent to its directive, or an old parser loses the
      condition and unconditionally injects a conditional fragment. Linted.
  Never start a line inside this comment with a bare directive verb: the parser
  strips each line before matching, so an indented example would be parsed for real.

  WHY THE ATTRIBUTES LIVE HERE AND NOT IN EACH FRAGMENT'S OWN HEADER
  Per-fragment headers were measured, not argued about: the assembler appends each
  fragment WHOLE, so a four-line YAML header on the seven resident fragments added 322
  characters and took the payload from 9,772 to 10,094 — past the 10,000 hard limit,
  into exactly the silent truncation aihub#285 just removed. A header-stripping
  assembler could avoid that, but only for readers that go through the assembler, and
  fragments/on-demand-index.md tells the model to `Read` these files directly. The
  attributes are also manifest-scoped by nature: whether a fragment is resident is a
  property of THIS list, not of the file.

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

  ON-DEMAND TIER (declared with the on-demand verb, never injected — see also
  fragments/on-demand-index.md). Rationale per fragment:
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
kind: rule
gate: none
gate-partial: pf-commit-guard
authority: self

@include: fragments/output-format.md
kind: rule
gate: none
authority: self

@include: fragments/memory-first.md
kind: info
authority: self

@include: fragments/bootstrap.md
kind: info
authority: self

@include: fragments/nl-routing.md
kind: rule
gate: none
authority: each /pf-* skill's own NL Triggers section
resident-because: it is the cross-skill index — a single skill's NL Triggers cannot tell you WHICH skill to open, and that is the decision made before any skill is read.

@include: fragments/repo-routing.md
kind: info
authority: self

@include: fragments/on-demand-index.md
kind: info
authority: self

@ondemand: fragments/post-claim-routing.md
kind: rule
gate: none
authority: self

@ondemand: fragments/memory-conventions.md
kind: info
authority: self

@ondemand: fragments/diagram-convention.md
kind: info
authority: self

@ondemand: fragments/platform-adaptation.md
kind: info
authority: references/codex-tools.md + references/copilot-tools.md

@ondemand: fragments/repo-detail.md
kind: info
authority: self
