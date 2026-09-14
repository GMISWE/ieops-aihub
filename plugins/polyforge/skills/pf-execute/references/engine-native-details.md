# pf-execute/references/engine-native-details.md - deferred engine detail (ON DEMAND)

> **Not injected.** `engine.native.md` is injected by `hooks/pf-skill-router` and shares a hard
> **10,000-character** budget with the memory, storage and lifecycle fragments (aihub#304 - past
> that limit the harness replaces the whole payload with a ~2,000-character preview, so growing
> the resident tier removes context instead of adding it).
>
> `Read` this file when you are actually running an interactive (`requires_human_session=true`)
> execute loop, or when a tool does not publish a parameter the resident fragment uses.

**Go package (aihub#654 / aihub#657).** `internal/engine` implements pieces (a)-(e) of this
document and `polyforge engine <verb>` (**§0h**) is its CLI surface: (a) §0's startup sequence
(`startup.go`), (b) the step-bracket `pf_update_step` sequencing described in
`_common/references/lifecycle-details.md` §1 (`bracket.go`), (c) review-marker parsing
(`review.go`), (d) the `role:` catalog/heuristic fallback (`role.go`), and (e) wrap-time
worktree cleanup (`wrap.go`), each with its own Go tests (`internal/engine/*_test.go`,
`internal/cli/engine_test.go`). The loops below now CALL those verbs instead of restating them.
What remains on this page is the argument shapes, the failure branches and the reasoning a
caller needs in order to CHECK an answer - deliberately not a second implementation of it, which
is what a hand-executed copy of the same logic had become.

---

## 0. Startup - one command, and what it does for you

```bash
polyforge engine startup --workspace-root='<workspace_root>' --worktree-root='<worktree_root>' \
  --scenario-url='<project.scenario>' --wi-type='<wi_type>' [--project='<project>']
```

prints `{scenario_path, legacy_fallback, sha, template_source, steps:[{id, content, expanded}]}`
and writes `<worktree_root>/.pf_meta.json`. That one call IS steps 2-6 below; only step 1 stays a
tool call. `legacy_fallback: true` means it resolved the pre-aihub#327 clone path - say so, and
recommend re-running `polyforge init`.

`internal/engine/startup.go` is the implementation and `internal/engine/startup_test.go` pins
every branch. Steps 2-6 are kept here as the SPEC of that command - what to expect in its output,
what each failure means, and how to do it by hand on a harness whose binary predates §0h.

1. Read the wi info: `wi_info = pf_list_work_items(ids=[<current_wi_id>])`.
2. **Scenario repo path**, from `.polyforge.yaml`. `owner` and `repo` are the last two
   path segments of `project.scenario` with `.git` stripped
   (`git@github.com:GMISWE/polyforge-coding.git` -> `GMISWE`, `polyforge-coding`; a URL with
   only ONE path segment has no owner and keeps the bare repo name):
   `scenario_path = <workspace_root>/.repo/<owner>__<repo>/`.

   Owner-qualified because keying on the repo name alone gave two orgs' same-named scenario
   repos ONE directory: the second was never cloned, its projects silently ran the first
   org's step graph, and nothing went red - `polyforge init` only fetch+reset an existing
   checkout without comparing its remote, and wi_type validation asks whether the template
   file exists, never which repo it came from (aihub#327).

   **Legacy fallback - and it is guarded.** A workspace whose last `polyforge init` predates
   this layout still holds the clone at `<workspace_root>/.repo/<repo>/`. It is used only when
   `git -C <workspace_root>/.repo/<repo> remote get-url origin` names the same repo as
   `project.scenario` (ignore scheme, credentials and `.git`). If it names a different repo,
   or the directory is absent, the command fails - STOP and report that `polyforge init` has not
   been run with a binary that knows this layout. An unguarded fallback re-opens the exact
   silent mix-up in every workspace that has not re-inited.
3. **SHA pinning** - `git -C <scenario_path> rev-parse HEAD`, reported as `sha` and written to
   `<worktree_root>/.pf_meta.json` as `{"scenario_sha": "<sha>", "started_at": "<ISO8601>"}`.
4. **Resolve the .md file** at that pinned SHA, with a fallback chain (`template_source` names
   the rung that won):
   ```
   1. git show <sha>:{wi_type}.{project}.md   <- project-specific ("project")
   2. git show <sha>:{wi_type}.md             <- generic fallback ("generic"; warn and continue)
   3. neither exists -> pf_complete_attempt(failed), report and list the available .md files, stop
   ```
5. **Scan `## Step:` sections** by `^## Step: (\w+)\s*$` (strict line start, ignoring any such
   heading inside a code fence), in document order -> `steps[].id` / `steps[].content`.
6. **Expand `@include` directives** into `steps[].expanded` (pair-parsing rule). `@include:` and
   `level:` are a bound pair: `level:` must be the line immediately after `@include:`, and its
   scope is only that include. Multiple includes each have their own level (or no level). Each
   include is read with `git show <sha>:<path>`; a missing one is fatal, so
   `pf_complete_attempt(failed)` and stop. Where a level is present, the line
   `Review level: <value>` is inserted before that include's expanded content.

   `level:` does NOT select a model. It is `common/review`'s review-depth argument and the loop
   passes it through as `Review level: <value>` - nothing more. See §0f.

## 0b. The auto-mode dispatch, verbatim - the call, not just its prompt

> **The wrapper below is CLAUDE CODE's; the prompt body is every harness's** (aihub#670). Only
> the outer call and the agent id change per harness - §0f's harness table has your row. The
> text between `"""` and `"""` is identical on all four and is what "COPY IT WHOLE" is about.

`engine.native.md` summarises this in prose to stay inside the payload budget. The literal
template the loop dispatches - COPY IT WHOLE. Every argument shown is REQUIRED, and
`subagent_type` is the one this template exists to force. The model is deliberately NOT an
argument: it lives in the `model:` frontmatter of the agent file the subagent_type names
(agents/step-executor.md, agents/step-reviewer.md), where the harness reads it as
configuration instead of prose a dispatcher may skim - aihub#544 measured 3/3 review
dispatches forgetting the tier when it lived only in pseudocode, and aihub#555 re-measured
1 of 2 dispatches still forgetting it after the template marked it REQUIRED. Passing a model
anyway is worse than forgetting it: an explicit per-invocation model silently OVERRIDES the
agent file (aihub#555 measured), re-creating the dead selector with the sign flipped. So:
select the agent from the step id, and let the file carry the model:

```
Agent(
  subagent_type: <REQUIRED - ROLE_AGENT[role] where role = `polyforge engine resolve-role
          --step-id='<step_id>'`.role (aihub#642/#664: five roles - executor/operator/
          explorer/reviewer/designer - not just is_review's two). Use the full
          plugin-namespaced id: the bare agent name resolves to a same-named user/project
          agent when one exists, while the namespaced id always resolves to the plugin's
          file (measured, aihub#555). NOT-CC HARNESSES: this argument name, the tool name
          and the id spelling all change together - substitute your §0f row whole
          (pi `subagent(agent=<id>, task=...)`; opencode `task(subagent_type=<id>,
          prompt=..., description=<3-5 words>)`; codex has no dispatchable agent, run the
          step in-session). Do NOT add a model argument - it would silently
          override the agent file.>,
  prompt: """
You are executing step {step_id} of wi {wi_id}.

Call pf_get_step(work_item_id={wi_id}) FIRST - it is the only authority for prior-step context.
In completed_steps, count only entries whose status is "completed" as done - a "failed" entry did
NOT finish (pausing an attempt files its in-progress step that way too), so redo that step_id
unless a later entry completes it. Read each done entry's artifact_summary. Never take step
progress from a file in the worktree; nothing writes one.

--- step instructions ---
{expanded}
--- END ---

When done, RETURN your one-line summary of this step in your output; the loop passes it straight
to pf_update_step(artifact_summary=...). Do not write it to a file.
If there are learnings worth keeping, call pf_remember to store them in aihub.
"""
)
```

`internal/cli/engine_native_dispatch_model_test.go` pins this template: it must select the
agent via `ROLE_AGENT[role]` where `role` comes from `polyforge engine resolve-role`, name
all five agents, agree with the dict `engine.native.md` declares AND with the `model:` /
`disallowedTools:` frontmatter the five agent files actually carry, and contain NO model
argument - so re-adding the argument, swapping an agent, or re-keying the choice back onto
a restated predicate all go red rather than shipping as prose drift.

## 0c. `fail_step_and_attempt` - the review-FAIL path, verbatim

`engine.native.md` abbreviates this call pair. When `polyforge engine parse-review` returns
`FAIL`, ask §0h for the step half and make what it prints:

```bash
polyforge engine bracket-plan --step-id='<step_id>' --status='failed' \
  --step-attempt-id='<sa_id>' --error-type='review_fail'
```

It prints exactly ONE `pf_update_step(status="failed")` call and never a next one - `next_step`
is not valid on a failure and the loop stops here anyway. Then, yourself:

```python
pf_complete_attempt(work_item_id=<current>, status="failed",
                    note="failed reason: review_fail at step " + step_id)
break   # stop the whole loop, skip the completed report; output the review issues
```

Both calls, in that order. `pf_update_step(failed)` alone leaves the attempt running;
`pf_complete_attempt(failed)` alone leaves the step showing `in_progress` forever.
`pf_complete_attempt` is deliberately NOT part of `bracket-plan`'s output: it is terminal
attempt state, once per wi, outside the per-step bracket that verb models.

## 0d. The startup three-segment report, with its literal values

```
## Result
Started executing <slug>, N steps total.

## Status
| wi     | <slug>     |
| steps  | N steps    |
| mode   | auto/human |
| status | running    |

## Next steps
- Running; monitor with /pf-status
- Pause with /pf-stop --pause
```

`mode` is `auto` when `requires_human_session=false` and `human` when it is true.

## 0e. The paused-attempt exit, in full (aihub#182)

`engine.native.md` carries this as a two-line branch in the auto loop. The reasoning:

A step template that hits a blocker calls `pf_emit_event(note)` + `pf_pause_attempt` and hands
the wi to a human. From that moment the server hard-rejects every call that authenticates
through `verifyAttemptCredential` - `pf_update_step`, `pf_save_artifact`, `pf_complete_attempt`,
`pf_commit`, `pf_acquire_locks`, `pf_wrap`. One tool keeps working, by design: `pf_emit_event`'s
lighter credential check reads the attempt id, the claim epoch and the secret, never the
attempt's status, so a paused attempt may still write notes to the timeline (owner ruling,
2026-09-10, aihub#585) - the reason for a pause is often best recorded after it. Pausing
therefore cannot corrupt step state; the loop simply cannot advance. What it *can* do is walk
into a cascade of surprise credential errors and retry the rejected calls, which is what this
branch exists to prevent.

Break out of the loop on either signal:

- the step's own output says it paused the attempt (it called `pf_pause_attempt`), or
- any `pf_*` call is rejected because the attempt is not running. The paused case has its own
  error code - `ErrAttemptPaused`, "attempt is paused; resume it before continuing" - which is
  deliberately distinct from a stale credential, so do not treat it as one.

On that path:

- **Do NOT retry.** Nothing can succeed until a human resumes; a retry only burns tokens.
- **Do NOT call `pf_complete_attempt`.** The attempt must STAY paused - that is the state the
  human resumes into, and a terminal call destroys it. This is the one loop exit that ends with
  no terminal call, and the server would reject it anyway.
- **Do NOT report the step completed**, and do not start the next step.
- Report three-segment, needs-you:

```
## Result
Step <step_id> paused the attempt for <slug> - a human needs to look at it.

## Status
| wi     | <slug>              |
| step   | <step_id>           |
| status | paused (needs you)  |

## Next steps
- See what it needs: /pf-status <slug>
- Resume once it is resolved: /pf-work <slug>
```

## 0f. The model tier is keyed on step KIND; `level:` cannot become one (aihub#358 / aihub#338)

**The mapping, as the auto loop implements it.** Five roles, resolved from the step id (or an
explicit `role:`) by ONE call: `polyforge engine resolve-role --step-id='<sid>'`
(`internal/engine.ResolveRole`, aihub#642/#664). Since aihub#555 the tier is carried by an
AGENT DEFINITION FILE, not by a dispatch argument: each agent file's `model:` frontmatter is
the single copy of its model name, read by the harness as configuration, and it is
deliberately NOT repeated in this table (a second hand-written copy is the aihub#294 drift
class; the gate pins the file, not prose). The loop's only job is `ROLE_AGENT[role]` - a
name -> agent-id lookup, never a predicate.

| role | tier | step ids (catalog, `internal/roles/definitions/*.yaml`) | agent |
|---|---|---|---|
| `executor` | default, write | `code_change`, `review_fix`, `verify`, `plan`, `test`, `e2e`, `live_verify`, `deploy_prod`, `prove_unchanged`, `finalize`, `commit_loop` | `polyforge:step-executor` |
| `operator` | lowest, write | `commit_and_pr`, `await_ci`, `refresh_descriptions`, `publish_stable`, `publish_plugin`, `bump_version`, `build`, `await_image` | `polyforge:step-operator` |
| `explorer` | low, **read-only** | `prepare_context`, `map_consumers`, `ground`, `check_status` | `polyforge:step-explorer` |
| `reviewer` | raised, **read-only** | `code_review`, `review`, `release_review` | `polyforge:step-reviewer` |
| `designer` | raised, write | `spec`, `direction`, `prototype` | `polyforge:step-designer` |

**The `agent` column above is CLAUDE CODE's spelling, and it is one of FOUR (aihub#670).** This
file, and `engine.native.md` with it, ships byte-identical to pi, codex and opencode: pi's
installer copies `skills/` with `cp -r` and no transform, `.codex-plugin/plugin.json` points
codex at `./skills/` in place, and opencode's installer never copies skills at all, so three of
the four harnesses have no installer stage that *could* rewrite a line, and the contract has to
carry every harness itself. You know which harness you are running in; the `polyforge` binary
does not and must never be asked for it (aihub#649, refuted twice: the thing reading this line
is the model, not the process).

| harness | agent id for role `<role>` | the dispatch call, `<id>` = that agent id |
|---|---|---|
| cc | `polyforge:step-<role>` | `Agent(subagent_type=<id>, prompt=<§0b>)` |
| pi | `pf-<role>` | `subagent(agent=<id>, task=<§0b>)` |
| opencode | `step-<role>` | `task(subagent_type=<id>, prompt=<§0b>, description=<3-5 words>)` |
| codex | `step-<role>` | **none today** - run the step yourself, see below |

`internal/roles/dispatch.go` is the single copy of that table; the four renderers in that package
take each harness's agent name from it, and
`internal/cli/engine_native_dispatch_model_test.go` pins both this table and
`engine.native.md`'s compact version against it. So renaming a generated agent file cannot leave
the documented dispatch pointing at a name nobody ships.

Per row, what actually breaks if you copy cc's line instead of yours:

- **pi** - the tool is `subagent`, not `Agent`; BOTH argument names differ (`agent` / `task`);
  and the id is `pf-<role>`. `pf-execute`'s own installer generates those files
  (`polyforge roles generate pi`), so the agents exist - only the call is wrong.
- **opencode** - the trap row, because `subagent_type` is spelled exactly as cc spells it. The
  TOOL is `task`; `description` (3-5 words) is REQUIRED and cc's call has no such argument; and
  `Agent.get("polyforge:step-...")` throws `Unknown agent type: ... is not a valid agent type`.
  Getting the tool name right and the id wrong fails just as hard as getting neither right.
- **codex** - there is no dispatchable polyforge agent at all right now, so there is nothing to
  translate the line into. `spawn_agent` exists (feature `multi_agent`, on by default) but
  resolves `agent_type` against an `[agents]` config section nothing in this repo writes, and
  what `polyforge roles generate codex` produces is `$CODEX_HOME/step-<role>.config.toml`, a
  config PROFILE for the `-p`/`--profile` process flag - codex has no agent-file auto-discovery
  in this version at all (aihub#655, live-verified codex-cli 0.154.0). **Run the step in-session
  yourself and say in the step summary that you did**; do not silently skip it, and do not
  invent an `agent_type`.

The ROLE -> role mapping in the table at the top of this section is harness-INDEPENDENT: only
the last column changes. `polyforge engine resolve-role` stays the one place the role is decided
on every harness.

Read-only above means Edit/Write/NotebookEdit are disallowed in that agent's frontmatter
(`internal/roles.CompileCapability` compiles `read_only` into that list - the actual
enforcement mechanism, not the table). Dispatching `explorer` for `prepare_context` therefore
cannot silently widen to a write-capable executor the way the pre-aihub#664 two-way
`is_review` predicate did: that predicate had no `explorer` / `operator` / `designer` branch
at all, so all three fell through to the `else` arm - the default, write-capable
`STEP_AGENT` - which is the capability-widening bug aihub#664 closes.

**The two channels that can defeat the agent files (aihub#555, measured on Claude Code
2.1.258).**

1. A per-invocation `model` argument on the Agent call beats the file: an agent file
   declaring one model, dispatched with an explicit different model, ran the explicit one.
   That is why the loop and the §0b template never pass a model and the gate goes red if the
   argument comes back - two live channels mean the file is silently dead on every dispatch
   that fills the argument.
2. Same-named agents do NOT displace these under their namespaced ids: a project
   `.claude/agents/step-reviewer.md` coexists with the plugin's file, and
   `polyforge:step-reviewer` still resolves to the plugin copy - only the BARE name
   `step-reviewer` reaches the project copy (measured; the docs' "plugin agents have lowest
   precedence" manifests as bare-name resolution, not as replacement of the namespaced id).
   The loop therefore dispatches namespaced ids only. Retuning a tier on one machine means
   editing that machine's plugin copy, not shadowing the name.

Measured against polyforge-coding@09cc434 (17 templates, 102 step occurrences, 26 distinct step
ids): the catalog's raised-tier (`reviewer`) step ids occur **9** times - `code_review` ×8
(chore.aihub, chore.tether, critical_bug.ieops, feature.aihub, feature.tether, fix_bug.aihub,
fix_bug.ieops, fix_bug.tether) and `release_review` ×1 (release.aihub). Note `review_fix` (×8)
resolves to `executor`, not `reviewer` - it applies a review's findings, which is ordinary
editing work.

**Decided by the owner (2026-09-04): task-kind -> tier.** The owner explicitly rejected keying it
on `level:`, and explicitly asked for the policy `hooks/pf-skill-router` already ships on the
superpowers branch - "sonnet for everything except review/architecture, which use opus" - rather
than a second, divergent one. So the two branches now state the same rule.

**Two limits of this mapping, recorded rather than glossed.**
1. The scenario repo produces no *architecture* step id today, so "architecture" in the owner's
   policy has no site to land on and the raised set is the review steps. When such a step
   appears, its id is what needs adding here - not a new key.
2. `plan` steps are design judgement and are NOT raised; `spec` WAS raised, by the owner's
   2026-09-13 decision 3 ("只升 spec 档不升 plan"), and the catalog carries that today - `spec`
   resolves to `designer`, which the table above lists as raised. This entry used to bracket the
   two together as undecided, and that half is now stale rather than merely cautious: a reader
   comparing it against the table would find the file contradicting itself. What remains the
   owner's call is `plan` alone. The evidence behind raising review steps is two measured catches
   by clean-context reviewers, while the same note recorded spec as "possible, undecided" until
   decision 3 settled it; the per-review cost delta was measured by aihub#544 against the real
   transcript corpus (median a few dollars per review run at the published price ratio).

**Why `level:` cannot be the key.** This is the aihub#358 defect and it is still true: the engine
spent 2.5 months claiming a tier it never applied.

1. `step_level` was defined nowhere - in any language, in any repo. It is pseudo-code a model
   executes by reading it, so a comparison is only ever as real as the value it compares.
2. **The two `level:`s are different parameters that happen to share a key name.** The one the
   scenario repo emits is `common/review/SKILL.md`'s review-DEPTH argument, enumerated
   `quick|medium|deep|challenge` (that file's frontmatter and its `structured_payload` contract
   both state it). The one the old selector wanted was a model name. Measured at
   polyforge-coding@6231732: **9** `level:` lines, every one of them the line immediately after
   `@include: common/review/SKILL.md`, values `quick`×4 / `deep`×5 - and not one occurrence of
   any model name anywhere in that repo.
3. So the two sets were disjoint and the branch was unreachable: **every step dispatched the
   default.** The tiering never fired once, silently, because a selector that never matches is
   indistinguishable from one whose condition is simply never true.

Putting a model name in that field to select a model would *simultaneously* hand that model name
to `common/review` as a review depth its enumeration does not contain. "deep review" and "raised
tier" can never both be requested through one key. Keying on the step id costs nothing there,
which is the whole reason it is the key.

**The gate.** `internal/cli/engine_native_contract_test.go`
(`TestEngineNativeLevelVocabularyContract`) fails if either engine document ever names a
`level:` value the scenario repo does not produce, and separately asserts that both documents
state how the tier IS chosen - silence is what let a reader assume a broken mechanism worked, so
neither half is satisfied by saying nothing. It carries the scenario vocabulary as a pinned set
because aihub's CI never checks out polyforge-coding, and reconciles that pinned set against the
live repo whenever a checkout is reachable - so the pin cannot rot silently on any machine that
has one.

## 0g. Identity: the A vs. B/C enforcement asymmetry (aihub#654 S1/S2)

`aihub#640` names three ways this engine can run: **A** is a future headless CLI orchestrator
with no LLM turning the crank; **B/C** is today's Claude Code / pi session, where an LLM reads
this file and the loop above is pseudocode it executes by hand. The `workflow_identity_constraint`
(aihub#640, owner-authored) requires A to contain no execution logic B/C lacks - which is why
`internal/engine` exists (see the Go package pointer above) - but the constraint is about
*execution logic*, not about *enforcement*, and those two turned out to differ:

- **S1** - does a headless CLI (`claude -p`, `codex exec`, `opencode --auto`) fire
  `pf-commit-guard` (IR1)? Measured yes on Claude Code (hooks fire identically in `-p` mode;
  `hooks/pf-skill-router`'s own in-repo comment records a blocked push from a subagent the same
  day). Inferred yes on Codex (`codex-hooks.json` wires the same guard, `exec` mode itself not
  independently measured). Confirmed gap on opencode (no hook mechanism yet; aihub#653's scope).
- **S2** - does `claude -p` receive the `SessionStart` using-polyforge payload while a
  Task-spawned subagent does not? Measured, both halves: a dispatched subagent gets
  `PreToolUse` gating but NOT `SessionStart`'s `additionalContext` (IR1-3 do not arrive); a
  headless top-level `claude -p` gets both.
- **Net conclusion.** A (once built) gets the full `SessionStart` payload AND `PreToolUse`
  gating. B/C (today, a dispatched subagent) gets the same `PreToolUse` gating but not the raw
  `SessionStart` payload - which is exactly why `hooks/pf-skill-router`'s `Skill`-matcher exists
  as B/C's substitute injection channel, and why the §0b dispatch template pastes the prior-step
  and step-instruction context directly into the subagent's prompt rather than relying on any
  ambient session context reaching it. A and B/C reach *equivalent* enforcement through
  *different* mechanisms - a real, load-bearing asymmetry, not an oversight to be closed by
  making A "reuse the same channel" B/C uses (the option aihub#640's review rejected). Whoever
  builds A's own dispatch path later must preserve B/C's inline-instruction compensation rather
  than assume ambient context will be there, because for A it will be present twice and for B/C
  it is the only copy.

## 0h. `polyforge engine <verb>` - what both loops call instead of re-deriving it (aihub#657)

`internal/engine` is the single implementation of the mechanics this file used to spell out as
pseudocode, and `polyforge engine <verb>` is its CLI surface. Every verb is local-only - no
aihub API call, no network - and prints JSON on stdout, so any harness with a shell can run one
and read the answer. They are a COMPUTE surface, never a side-effect one: none of them makes an
MCP call, so `pf_update_step` / `pf_complete_attempt` remain the loop's own calls, made after
reading what the verb printed.

**QUOTE every flag value, on every verb** - `--name='<value>'`, not `--name=<value>`. The rule is
spelled out under `bracket-plan` below and the reason is identical for all five;
`internal/cli/engine_bc_contract_test.go` now reads EVERY documented invocation in this plugin,
not only the `bracket-plan` blocks, so an unquoted value goes red wherever it is written.

| verb | replaces the pseudocode for | stdout |
|---|---|---|
| `startup` | §0 steps 2-6 | `{scenario_path, legacy_fallback, sha, template_source, steps:[{id,content,expanded}]}`, and writes `.pf_meta.json` |
| `resolve-role` | the entire role choice - a step's explicit `role:`, the catalog, and the `is_review` fallback (§0f's table, aihub#664) | `{role, tier, read_only, source}`, `source` one of `declared` / `catalog` / `heuristic` |
| `parse-review` | `parse_review_result` | `{"result": "PASS"}`, or `WARN` / `FAIL`: the LAST `<!-- REVIEW_RESULT: ... -->` marker in the output, and `WARN` when there is none - never `PASS`, never an auto-`FAIL` |
| `bracket-plan` | the step bracket, both its forms | an ORDERED array of the `pf_update_step` calls to make |
| `cleanup-worktrees` | wrap-time worktree removal (`_common/references/lifecycle-details.md` §0) | `{removed:[...], errors:{}}` |

### `bracket-plan` - the one the loop runs on every step

```bash
polyforge engine bracket-plan --step-id='<step_id>' --status='<completed|failed>' \
  --step-attempt-id='<sa_id>' --next-step-id='<next_step_id>' \
  --next-step-attempt-id='<next_sa_id>' --supports-next-step \
  --artifact-summary='<artifact_summary>' --error-type='<error_type>'
```

Make each printed call with exactly the arguments it lists, in the order printed. Nothing has
happened when the command returns: it is a plan.

- **QUOTE every value, as shown.** The flag parser matches the literal prefix `--name=` and
  treats anything else as an argument it does not recognise - which it DROPS, with exit 0 and
  nothing on stderr. So an unquoted `--artifact-summary=pr=x/y#1 base=main` reaches the binary
  as two words, the summary silently becomes `pr=x/y#1`, and the step is filed with a truncated
  record. `artifact_summary` is prose and routinely contains spaces, so this is the normal case,
  not an edge one.
- `--next-step-attempt-id` is a NEW ulid that YOU mint, once, before the call. No verb mints
  ids; `bracket-plan` only threads the one you give it into the right call of the plan, and the
  same value becomes the next iteration's `--step-attempt-id`.
- Omit `--next-step-id` / `--next-step-attempt-id` on the LAST step, and on `--status=failed`
  (`next_step` is rejected on a failure, not ignored).
- Omit `--supports-next-step` when `pf_update_step` does not publish `next_step` (§2). The plan
  then has TWO calls rather than one fused call, and the second carries
  `step_attempt_id=<next_sa_id>` - exactly the threading the two-call form gets wrong by hand.
- Omit `--artifact-summary` / `--error-type` where they do not apply.
- `--step-id`, `--status` and `--step-attempt-id` are REQUIRED on every call, failures included;
  the command errors without them rather than assuming anything.

`internal/cli/engine_bc_contract_test.go` reads the flags out of THIS block, renders them
**through a real shell** (which is how a session runs them, and the only way the quoting rule
above is actually exercised), runs a freshly built binary, and compares the result against
`engine.PlanStepBracket` called directly in Go - over the same step-sequence fixtures, asserting
an identical `pf_update_step` sequence. A flag renamed, dropped or unquoted here therefore goes
red rather than drifting silently, which is what makes deleting the pseudocode safe rather than
merely shorter. It gates §0c's invocation the same way.

## 1. Execute (rhs=true, interactive mode) - the loop in full

> **One-layer dispatch (aihub#644 - owner ruling 2026-09-13, decision 2, option ①).** Until
> that ruling this loop dispatched NOTHING. It printed the step body and the human executed it:
> zero-layer dispatch, which put the whole step body plus every round of human interaction into
> the main session's context, and left `reviewer` steps judged by the person who had just done
> the work. The ruling makes it **one layer**: the main session dispatches a step agent per
> step, and the human confirms at the **step boundary**.
>
> So this is now the auto loop plus exactly ONE addition - a human gate between the agent
> returning and the bracket being made. Everything else is shared deliberately: the same
> `steps[]`, `resolve-role`, `ROLE_AGENT`, §0b template, `parse-review`, `bracket-plan`, and the
> same §0c / §0e exits. A second execution path is what aihub#657 deleted and what aihub#640's
> `workflow_identity_constraint` forbids; the one licensed difference is whether a human stands
> at the step boundary.
>
> **Why this one is still deferred, when the auto loop is not.** The **budget**, not the
> mechanics: `engine.native.md` shares a hard 10,000-character payload and only one of the two
> loops can be resident; the auto loop stays because `requires_human_session=false` is the
> common case. Now that the two differ by one gate rather than by a whole execution model, the
> resident fragment can carry that difference in a sentence and this page carries its rules.

```python
# Same bracket as auto mode - the SAME `polyforge engine bracket-plan` command with the same
# flags (§0h), so both loops produce an identical pf_update_step sequence for the same step.
# No pf_get_step is needed FOR THE BRACKET (it carries no version token); it is still the
# authority for prior-step context.
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=steps[0].id, status="in_progress")

for i, (step_id, expanded) in enumerate(steps):   # steps[] as `engine startup` printed them
    role = `polyforge engine resolve-role --step-id='<step_id>'`.role   # never re-derive
    feedback = None
    halt = False      # set by the two exits that must leave BOTH loops - see below

    while True:   # ONLY `retry` re-enters this. i does not advance and sa_id does not change.
        dispatch Agent(subagent_type=ROLE_AGENT[role], prompt=§0b + retry_note(feedback))
        # ^ copy §0b's template VERBATIM - the same template the auto loop dispatches, for the
        #   same reasons. The call shown is cc's (aihub#670): on pi/opencode substitute your
        #   §0f row - tool name, argument names and agent id all change, while the prompt body
        #   and `retry_note` stay identical; on codex run the step in-session (§0f).
        #   No model argument here either (§0f, and §2's unknown-agent-id
        #   fallback is its one exception). retry_note(None) is empty; otherwise it appends
        #   `--- human feedback on the previous attempt ---`, the feedback, AND the fact that a
        #   previous attempt already ran and ITS EDITS ARE IN THE WORKTREE - pf_get_step cannot
        #   show them (this step is still in_progress, so it has no completed_steps entry), so
        #   an agent not told would read a clean record against a dirty tree.
        #   The step body goes to the AGENT, not to the human: printing {expanded} for a person
        #   to carry out is the zero-layer loop this ruling replaced.

        if a step called pf_pause_attempt (or a pf_* call is rejected "attempt is paused"):
            halt = True; break   # §0e: no retry, do NOT call pf_complete_attempt, and do NOT
                                 # bracket this step - `halt` is what carries that past the
                                 # `while` (a bare `break` leaves only the inner loop and drops
                                 # straight into the completed bracket below, which would file
                                 # the step and start the next one - the two things §0e forbids)

        verdict = None
        if role == "reviewer":
            # parse_review_result = `polyforge engine parse-review`, subagent output on STDIN
            verdict = that verb's {"result": "PASS"|"WARN"|"FAIL"}   # a RECOMMENDATION - §1c

        # ── THE GATE ── present, then WAIT. It is a gate only if nothing below it happens
        # first: no bracket-plan, no pf_update_step, and no dispatch of steps[i+1] until the
        # human has answered. A loop that runs ahead while waiting is the zero-layer loop with
        # extra steps - the human's answer can no longer change anything.
        output: f"## Step {step_id} ({role}) - agent finished\n\n{the agent's one-line summary}"
                and, when verdict is set, f"Reviewer verdict: {verdict} - recommendation, §1c"

        wait for user input:
          "continue" / "done" / "ok"  -> leave the while; make the bracket below
          "retry <feedback>"          -> feedback = <feedback>; re-enter the while. It makes NO
                                         server call, so sa_id is unchanged - do NOT mint a new
                                         step_attempt_id; this step is still open under that one.
          "skip"                      -> leave the while; COMPLETE it with a summary that says it
                                         was skipped: the same bracket-plan call as the
                                         "continue" path, only with
                                         --artifact-summary='skipped - <the user's reason>' (§1b)
          "fail"                      -> bracket-plan --step-id='<step_id>' --status='failed'
                                         --step-attempt-id='<sa_id>' (§0c's shape, without
                                         --error-type unless a review said so);
                                         make what it prints, then
                                         pf_complete_attempt(failed, note="failed reason: <user description>");
                                         halt = True; break

    if halt:
        break   # leave the FOR too. This step is already terminal (failed, §0c) or deliberately
                # left open (paused, §0e); falling through to the completed bracket below would
                # file it a SECOND time, under the same step_id and the same sa_id.

    # Only "continue/done/ok" or "skip" reaches here - including a reviewer verdict the human
    # overrode (§1c). Report this step completed AND start the next one. Same command, same flag
    # set, same quoting as §0h - including --supports-next-step, which is what keeps this loop's
    # pf_update_step sequence identical to auto mode's instead of always degrading to two calls.
    next_sa = new_ulid() if i + 1 < len(steps) else None
    run bracket-plan(--step-id='<step_id>', --status='completed', --step-attempt-id='<sa_id>',
                     --supports-next-step when pf_update_step publishes next_step (§2),
                     --next-step-id / --next-step-attempt-id='<next_sa>' only when next_sa exists,
                     --artifact-summary='<one-line summary of what this step produced>')
    make every pf_update_step call it prints, in that order
    sa_id = next_sa

# all steps done -> wrap + worktree cleanup (_common/lifecycle.md ## Once per wi, whose full
# sequence is §0 "Wrap & cleanup" of _common/references/lifecycle-details.md)
```

## 1b. `skip` completes the step - it does not leave it open (aihub#413)

This branch used to say the opposite: "continue to the next step WITHOUT calling pf_update_step
at all. The skipped step stays in_progress and stays the server's current_step; the next step you
actually complete reports itself and advances from there." That last clause was already the weak
part of it, and **aihub#398 made it false outright.**

`validateStepIdentity` (internal/server/routes_step.go) now refuses a terminal transition whose
`step_id` is not the step the server has open:

    status="completed" names step "test", but this work item's current_step is "review_fix"
    -> 409 CONFLICT_CAS_FAILED, nothing committed

So after an uncalled skip, the very next step you complete is the one that fails - and it fails
naming a step the user thought was behind them. The old text promised precisely the thing that now
errors.

**Completing the skipped step is not a workaround, it is what the record needs anyway.** Three
independent reasons, so this is not a single-purpose accommodation:

1. `current_step` stays in sync, which is the whole subject of the identity predicate.
2. It files a step-history row, so `pf_get_step`'s `completed_steps[]` shows the skip. An
   uncalled skip is invisible there, and a resuming agent reads `completed_steps[]` as the
   authority - so it would either redo the step or, worse, treat the still-open step as its own
   in-progress work.
3. The scenario templates already ask for exactly this wording - and they live in the SCENARIO
   repo (`GMISWE/polyforge-coding`), not here, so look for them there rather than in this tree.
   Both `chore.aihub.md` and `fix_bug.aihub.md` tell the `review_fix` step to write
   `artifact_summary` = `skipped - no findings` when `code_review` found nothing. The old engine
   text therefore contradicted the step graphs it was executing, not just the server.

`status="failed"` is NOT the way to skip. It is refused with `next_step` (so the loop cannot
advance in one call), and it terminates the attempt through the review-FAIL path in §0c. "The user
chose not to do this" and "this step failed" are different facts and the timeline should not
conflate them.

Under one-layer dispatch `skip` is answered AFTER the agent has run, which adds a fact this
branch did not used to have: see §1c - it changes the record, never the worktree.

## 1c. The human is the ADJUDICATOR, not the executor (aihub#644)

Zero-layer dispatch made the human the executor, so "what if the human disagrees with the
result" had no meaning - they produced it. One layer separates the two roles and the owner's
ruling assigns them: the step agent EXECUTES, the human ADJUDICATES. Three consequences, each a
rule rather than a preference.

**1. Four verbs, not three.** `retry` is new, and it is what makes the gate a gate. Without it a
human looking at an unsatisfactory step has exactly two moves: accept it, or `fail`, which takes
the whole attempt down through §0c. Neither of them is "do it again, properly" - which is the
ordinary answer, and the one a person standing at a step boundary is there to give.

| verb | what the loop does | what the record shows |
|---|---|---|
| `continue` / `done` / `ok` | bracket the step completed, start the next | a normal completed step |
| `retry <feedback>` | re-dispatch THIS step with the feedback appended; **no server call** | nothing yet - the step is still open under the same `step_attempt_id` |
| `skip` | the same bracket, with `--artifact-summary='skipped - <reason>'` (§1b) | a completed step whose summary says it was skipped |
| `fail` | §0c's call pair, then stop the whole loop | a failed step and a failed attempt |

`retry` is the only one of the four that touches no server state, and that is why `sa_id`
survives it: the step was never closed, so the id that will eventually close it has not been
spent. Minting a fresh one is the trap - `step_attempt_id` keys the step-history row
(aihub#399), and a second id for one step is how a loop ends up filing two rows for it.

**`skip` does not undo anything.** Under zero-layer dispatch nothing had happened when the human
said `skip`, so there was nothing to undo. Now the agent has already run, and skipping changes
only what the RECORD says - every edit it made is still in the worktree. To actually undo work,
`retry` with that instruction, or `fail`. Saying `skip` over an agent that edited files leaves
the tree and the timeline disagreeing, and nothing reports it.

The retried agent cannot discover those edits on its own, which is why §1's dispatch tells it.
`pf_get_step` is the only authority the §0b template gives it, and a step under `retry` is still
`in_progress` - so it has no `completed_steps` entry and the record looks CLEAN while the tree is
dirty. An agent not told that a previous attempt ran will read that gap as "nothing has happened
yet" and start over on top of its own earlier edits.

**2. A reviewer's verdict is a recommendation.** `polyforge engine parse-review` parses the
marker exactly as it does in auto mode and still returns PASS / WARN / FAIL - but here it is
shown to the human and the HUMAN's answer decides:

- verdict `FAIL`, human answers `continue` -> the step completes, and the override goes in the
  record: `--artifact-summary='<summary> - review FAIL overridden by human: <reason>'`. An
  override that is not written down is indistinguishable from a review that passed.
- verdict `PASS`, human answers `fail` -> §0c's call pair, but WITHOUT its `--error-type` and
  without its `review_fail` note: §0c's literal block hardcodes both, and no review failed here.
  Use the human's own reason, exactly as §1's `fail` branch does. The human's call wins in this
  direction too, or "adjudicator" means nothing.
- verdict `WARN` -> present it; the answer decides, exactly as for any other step.
- want a second opinion -> `retry`. It re-runs the SAME reviewer agent with a clean context, so
  it is a genuine second look rather than a continuation of the first one's reasoning.

**Never apply the verdict automatically.** Auto-applying it IS the auto loop - `FAIL` goes
straight to §0c with nobody asked. That is the behaviour option ① deliberately did not choose,
and it is the single change that would make this loop's human gate decorative.

**3. The tier is the same tier, by construction.** This loop calls the same `polyforge engine
resolve-role` and dispatches the same `ROLE_AGENT` agent files as auto mode, so every tier and
capability in §0f's table applies here unchanged - `reviewer`'s raised read-only agent and
`designer`'s raised write-capable one for `spec` included. There is no rhs-dependent tier and
nothing selects one: making the two differ would need an argument to `resolve-role` that no
ruling asks for. Recorded because "does interactive mode get the raise too" is a question this
file should answer rather than leave to inference.

The second half of the ruling's benefit falls out of the same fact. Under zero-layer dispatch a
`review` step was judged by the person who had just done the work, which is not a clean-context
review at all; both measured catches behind raising the reviewer tier (aihub#338) came from a
clean-context reviewer, and interactive mode now gets one.

## 2. Compatibility - server binary older than aihub#290

Applies to BOTH loops. Check what the tools publish; do not infer it from a version string.

- **No `next_step` on `pf_update_step`** -> drop `--supports-next-step` from `bracket-plan`
  (§0h). It then prints TWO calls instead of one: `status="completed"` for this step, and a
  separate `status="in_progress"` for the next one with `step_attempt_id` threaded onto it.
  Passing `next_step` anyway means it is silently dropped and the next step never starts.
- **No `note` on `pf_complete_attempt`** -> emit
  `pf_emit_event(event_type="note", payload={text: "failed reason: ..."})` **before** the
  terminal call. Do not pass `note` to a tool that does not publish it: it is accepted and
  ignored, and the state file is deleted immediately afterwards, so the failure reason is lost
  with no error.
- **Harness does not know the plugin agent ids** (the dispatch is REJECTED naming an unknown
  agent - an old harness, or a runtime without the generated agent definitions) -> fall back
  for THAT dispatch only, to your harness's own general-purpose agent, with an explicit model
  argument stating the tier, since no agent file can apply there. Never fall back silently, and
  never pass a model when the real ids resolve - on a harness that knows them, an explicit model
  overrides the agent file (§0f).
  **CHECK YOUR ROW BEFORE CONCLUDING THE ID IS UNKNOWN** (aihub#670): on pi, codex and opencode
  a rejection is far more likely to mean you used Claude Code's id (`polyforge:step-<role>`)
  than that your harness is old - §0f's table has the id your harness actually ships. The
  general-purpose fallback is spelled `subagent_type: "general-purpose"` on Claude Code only; on
  another harness it is that harness's own default agent, which is not necessarily called
  "general-purpose". Falling back is a REAL downgrade - it loses the role's model tier and its
  read-only tool policy - so it is the wrong answer to a wrong-id error.

See `_common/references/lifecycle-details.md` for the same rules stated from the lifecycle side,
including the `step_attempt_id` trap in the two-call fallback.

## 3. Why `pf_list_work_items(ids=[...])` omits `project`

Since aihub#280 `ids` is a real filter and an id already names exactly one wi. Before that, `ids`
reached no forwarding table and no `project` was sent, so this call was a hard 400 and never ran -
which is why older copies of this engine passed `project` defensively.
