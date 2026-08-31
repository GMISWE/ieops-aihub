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
                      `none`. Names are checked against a CURATED list of mechanisms
                      that genuinely make a violation observable (today: exactly one,
                      hooks/pf-commit-guard) — not against what exists on disk. Merely
                      existing is not enforcing: an assembler, an injector or a test
                      that reads repo content cannot notice an agent ignoring a rule.
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
  tests/using-polyforge-manifest.test.sh checks this, with a documented baseline of
  pre-existing violations that may only shrink. ⚠️ That suite is NOT in CI yet
  (aihub#293) — so today it is a check someone must run by hand, not an enforcement.
  By this manifest's own argument a lint nobody runs is itself an ungated rule; do not
  describe it as a guarantee until #293 lands.

  WHERE THE CURRENT `kind` VALUES CAME FROM — so they can be challenged, not inherited
  aihub#295 measured the rules this skill ships: SIX of them, of which exactly ONE has
  an enforcing mechanism — IR1, via hooks/pf-commit-guard's worktree and attribution
  gates. That is why iron-rules.md is `gate: none` with `gate-partial: pf-commit-guard`:
  partial coverage is fail-closed, because IR2 and IR3 are in the same file and nothing
  catches either. The other four ungated rules are the three-segment output format, NL
  routing, and post-claim Next-steps routing.
  Those six rules span four fragments. memory-conventions.md is a FIFTH: it did not
  appear in that table, but three places in this tree call its content a rule —
  on-demand-index.md (resident, injected every session) says "the hard rule that a
  `mem_…` id never goes in a repo doc"; the on-demand rationale below concedes that the
  link-discipline rule "live[s] only in the fragment"; and the fragment itself says
  "Never put an aihub `mem_…` ref in a repo doc". Marking it `info` would have
  contradicted all three, so it is `rule`, and it is the second BASELINE entry.
  The remaining seven are `kind: info`. That line is still not settled: an independent
  review of aihub#295 applied the wi's own discriminator — is the violation observable?
  — and also made memory-first.md ("pf_recall before every substantive action"),
  bootstrap.md (the session startup scan) and repo-routing.md rules, which would take
  the lower bound below from 14,175 to 17,685. Reclassifying only ever ENLARGES that
  bound, which is the honest direction; aihub#296 owns settling it with a measurement
  rather than deciding it in passing while moving a fragment.
  (Both figures were 11,489 / 14,993 when aihub#295 wrote them. aihub#294 grew
  memory-conventions.md from 2,155 to 4,841 chars — the 11-row memory-type table, then a
  correction to it — and that fragment is already `kind: rule`, so the bound moved with
  it. Neither number is resident: both fragments are on-demand, so the payload is
  unaffected.
  ⚠️ DO NOT hand-adjust these. Re-run the suite and copy what it prints:
      bash tests/using-polyforge-manifest.test.sh | grep 'SUM(kind:rule'
  This block has now been wrong twice for two different reasons. First 17,127, from
  adding three BYTE sizes to a CHARACTER total — every figure here is characters, like
  the budget, and these fragments carry CJK and em-dashes, so `wc -c` reads high
  (memory-first is 891 chars but 909 bytes). Then 13,563, because the commit that fixed
  the units added 612 more characters to memory-conventions.md and did not recompute.
  A derived number copied by hand goes stale on the next edit to its inputs, including
  the edit that is fixing it.)

  ==========================================================================
  WHICH CHANNEL OWNS A RULE (aihub#294)
  ==========================================================================
  THREE channels put rule text in front of a model — not two. They differ on two axes
  that trade off against each other, reach and repairability:

    1. fragments/*.md     plugin-versioned, injected every session, hard 10,000-char
       (this skill)       budget. Reaches EVERY install; corrected by the next release.
    2. .polyforge/usage.md  generated once by internal/cli/init.go's writeUsageMd, which
                          returns early if the file exists. Reaches every workspace that
                          ran init; NO size cap; and never regenerated, so a wrong rule
                          here cannot be corrected in the field.
    3. hand-written prose  everything in the workspace CLAUDE.md OUTSIDE the
       in workspace       `polyforge:managed` markers. No cap, edit any time, effective
       CLAUDE.md          immediately — and reaches EXACTLY ONE MACHINE, forever.

  Channel 3 is neither a bug nor a feature. It is the residue of the same guard that
  freezes channel 2: init owns the managed block and usage.md, and everything else in
  that file belongs to the user — which also means it can never propagate. Verified on
  this workspace: the `@import` is line 1, the managed block spans lines 3-453, and two
  hand-written rule sections sit at 455 and 474, outside it.

  Note what that costs the reader: channels 2 and 3 arrive in the SAME file at the same
  context position, and the model cannot tell them apart. One is generated and frozen,
  one is hand-written and live. Identical appearance, opposite lifecycles, and no marker
  distinguishes them.

  Channels 1 and 2 both carried Iron Rules, NL Routing and the memory-type table. So the
  copy that was maintained was not the copy that could be fixed where it runs, and they
  drifted: IR1's worktree path read `pf.<shortid>` in one and `pf.<project>-<seq>` in the
  other for three months, silently, because nothing had ever compared the two.

  RULES LIVE HERE. Not because this channel is versioned — that is the weaker half of
  the argument — but because it is UNCONDITIONALLY PRESENT and NOT USER-OWNED. usage.md
  needs a .polyforge workspace, a CLAUDE.md, an intact `@import` line and a user who has
  not edited it; the guard's own comment ("don't overwrite user edits") is a promise that
  the file belongs to someone else. A rule with four delivery preconditions, kept in a
  document the product has promised not to touch, is a default, not a rule.

  And the obvious counter — "usage.md has no size cap, put the rules where there is room"
  — is backwards. The 10,000-char cap is not a property of this channel, it is a property
  of the model's context, which both channels spend. usage.md is not uncapped, it is
  UNMEASURED. Moving text there buys no budget; it discards the only instrument that
  measures the spend, and that instrument is what found this bug. Choosing a channel
  because it has no gauge is how the payload reached 18,286 characters unnoticed.

  WHAT HAPPENS TO CHANNEL 3. Nothing here, deliberately — the workspace CLAUDE.md is a
  machine-local file in no repo, so this change cannot and must not touch it. But the
  same criterion applies and gives a clean test: a line belongs on channel 3 only if it
  is true of THAT MACHINE ONLY. Anything true of polyforge generally is mis-filed there,
  because channel 3's reach is one.
  That is not hypothetical either. The two hand-written sections on this workspace are
  general agent discipline with measured backing — how to route read-only subtasks, and
  that waiting costs tool CALLS not minutes. Neither is machine-specific; both currently
  reach one machine. They belong on channel 1. What stops them is exactly what stopped
  everything else: the resident tier has 22 characters free, and the tier rule (an
  unenforced rule may not leave the resident tier) blocks parking them on-demand without
  a BASELINE entry. So channel 3 is functioning as an overflow valve for a full budget —
  which makes aihub#296's slimming the prerequisite, not a nice-to-have. Until then,
  treat channel 3 as a LOCAL OVERRIDE LAYER and know that nothing on it propagates.

  Nothing in this change rewrites ANY user-owned file. writeUsageMd keeps its existence
  guard; the usage_md doctor check only reads; ensureClaudeMdRef is additive — it prepends
  the missing `@import` line and deletes nothing. The guard's original reason (do not
  overwrite user edits) is still intact; the fix was to stop putting rules behind it.

  Enforced by internal/cli/usage_channel_test.go — a Go test, so `go test ./...` in CI
  runs it, unlike the suites in this directory (aihub#293). It asserts that no rule
  section is delivered twice, that a section dropped from usage.md still has a home in a
  fragment (moved, not deleted), and that no delivered surface spells the legacy worktree
  path. Existing workspaces keep their frozen copy — a template edit cannot reach them —
  so `polyforge doctor`'s usage_md check REPORTS it and a human deletes it.
  Reporting, not removing, and that was learned the hard way: this change first removed
  the sections under `--fix`, deciding each one's extent from markdown structure. Review
  found six input classes where that destroyed content the user owned, three of them
  leaving an unterminated fence or HTML comment — which swallows the rest of the document.
  The live run against a real frozen workspace caught none of them, because a pristine
  generated template is the one input that cannot exhibit any of them. Deleting a span
  only when it is byte-identical to a known template version is the right primitive; it
  needs the historical template bodies and is left to a follow-up. Detection is what this
  work item actually required — it is what stops the duplicate being silent — and removal
  was a convenience that had bought a data-loss path.

  ONE OF THE THREE WAS DEMOTED, NOT JUST MOVED. Iron Rules and NL Routing were already
  resident here, so dropping the usage.md copy loses no reach. The memory-type table was
  not: it now lives in memory-conventions.md, which is `@ondemand:` and never injected, so
  it went from "in every session" (usage.md rode the CLAUDE.md @import unconditionally) to
  "read it if you think to". That is the right trade at 22 chars of free budget — a type
  vocabulary is consulted when writing a memory, which is exactly an on-demand trigger —
  but it is a real reduction in reach and is recorded as one. What makes it survivable is
  on-demand-index.md naming the file, and TestOnDemandRuleSectionsAreIndexed now requires
  that: without it, "moved" and "moved somewhere nothing points at" would pass the same
  test.

  WHAT WAS NOT CARRIED OVER. Every removed SECTION has a home here, and a Go test asserts
  it. Two row-level phrasings were deliberately not carried, because on each the surviving
  copy is the better one — recorded so this reads as a decision, not an oversight:
    - usage.md's IR3 named `/reload-plugins` as the remedy; iron-rules.md names `pf doctor`.
      The fragment is right: `/reload-plugins` exists only under Claude Code, and this skill
      ships a platform-adaptation fragment precisely because Codex/Copilot are supported.
    - usage.md's NL Routing appended "+ fan-out subagents" to the ready-queue row. The ROW
      survives here; that PHRASE does not, and it survives nowhere else in the plugin — so
      unlike the item above this is a deletion, not a better surviving wording. Deliberate:
      it is an orchestration pattern rather than an intent → operation mapping, which is
      all that table claims to index, and re-adding it costs 20 of the 22 free resident
      characters.

  KNOWN RESIDUAL: usage.md still carries "Wi creation rules", which is a rule with no
  fragment copy. It is not part of this defect (one copy cannot diverge from itself) and
  moving it costs resident budget that does not exist — 22 chars free at 9,778/9,800.
  aihub#296 owns the slimming that would make room.

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

       Since aihub#293 that suite IS wired into CI (.github/workflows/ci.yml, step
       "aihub#293 using-polyforge payload budget gate"), so busting the budget now
       fails the PR rather than shipping silently. Run it locally anyway after
       editing anything under fragments/ — it is faster than a CI round trip.

       Second line of defence: hooks/pf-session-start checks its own assembled
       length at runtime. Over 10,000 characters it drops trailing fragments,
       leads with a banner naming them and warns on stderr, instead of letting
       the harness swap the payload for a ~2,000-character preview in silence.
       That is a safety net for sessions on an older plugin copy, NOT a licence
       to exceed the budget — the CI gate above treats degradation as a failure.

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
    fragments/memory-conventions.md    4,258 B — its load-bearing rule ("all memory
                                       lives in aihub, local .md memory is deprecated
                                       here") is already stated — in fact stated more
                                       strictly — in memory-first.md, which stays in the
                                       always-injected tier.
                                       ⚠️ This fragment used to be justified partly by
                                       .polyforge/usage.md carrying the memory-type table
                                       (11 rows there vs 4 here) as a fallback. That
                                       fallback is GONE as of aihub#294: leaning on it was
                                       the bug, not the mitigation. usage.md is written
                                       once by internal/cli/init.go and never regenerated,
                                       so the "fallback" was a copy nobody could correct —
                                       and it had already drifted. The full 11-row table
                                       now lives HERE and only here, which costs nothing
                                       resident because this fragment is on-demand.
                                       on-demand-index.md names the type vocabulary, the
                                       link-discipline and work_item_id rules in its
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
kind: rule
gate: none
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
