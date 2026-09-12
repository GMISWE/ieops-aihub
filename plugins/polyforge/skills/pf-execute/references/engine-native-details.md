# pf-execute/references/engine-native-details.md - deferred engine detail (ON DEMAND)

> **Not injected.** `engine.native.md` is injected by `hooks/pf-skill-router` and shares a hard
> **10,000-character** budget with the memory, storage and lifecycle fragments (aihub#304 - past
> that limit the harness replaces the whole payload with a ~2,000-character preview, so growing
> the resident tier removes context instead of adding it).
>
> `Read` this file when you are actually running an interactive (`requires_human_session=true`)
> execute loop, or when a tool does not publish a parameter the resident fragment uses.

---

## 0. Startup - the exact commands

1. Read the wi info: `wi_info = pf_list_work_items(ids=[<current_wi_id>])`.
2. Resolve the scenario repo path from `.polyforge.yaml`. `owner` and `repo` are the last two
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
   this layout still holds the clone at `<workspace_root>/.repo/<repo>/`. Use it only when
   `git -C <workspace_root>/.repo/<repo> remote get-url origin` names the same repo as
   `project.scenario` (ignore scheme, credentials and `.git`). If it names a different repo,
   or the directory is absent, STOP and report that `polyforge init` has not been run with a
   binary that knows this layout. An unguarded fallback re-opens the exact silent mix-up in
   every workspace that has not re-inited.
3. **SHA pinning** - write it to `.pf_meta.json` in the worktree root:
   ```bash
   sha = git -C <scenario_path> rev-parse HEAD
   # <worktree_root>/.pf_meta.json: {"scenario_sha": "<sha>", "started_at": "<ISO8601>"}
   ```
4. **Resolve the .md file** using that pinned SHA, with a fallback chain:
   ```
   1. git show <sha>:{wi_type}.{project}.md   <- project-specific
   2. git show <sha>:{wi_type}.md             <- generic fallback (warn and continue)
   3. neither exists -> pf_complete_attempt(failed), report and list the available .md files, stop
   ```
5. **Scan `## Step:` sections** (document order):
   ```
   scan by the regex ^## Step: (\w+)\s*$ (strict line start, ignore inside code fences)
   produce an ordered step list: [(step_id, content), ...]
   ```
6. **Expand `@include` directives** (pair-parsing rule). `@include:` and `level:` are a bound
   pair: `level:` must be the line immediately after `@include:`, and its scope is only that
   include. Multiple includes each have their own level (or no level).
   ```
   scan the section content line by line:
   - on @include: <path>
       -> read the next line; if it is "level: <value>" record the level, else level=null
       -> expand via git show <sha>:<path> (if missing, call pf_complete_attempt(failed) and stop)
       -> if there is a level, insert a line before the expanded content: "Review level: <value>"
   - any other line: keep as-is
   concatenate the expanded content with the remaining prose
   ```
   `level:` does NOT select a model. It is `common/review`'s review-depth argument and the loop
   passes it through as `Review level: <value>` - nothing more. See §0f.

## 0b. The auto-mode dispatch, verbatim - the Agent call, not just its prompt

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
  subagent_type: <REQUIRED - "polyforge:step-reviewer" (REVIEW_AGENT) when is_review(step_id),
          else "polyforge:step-executor" (STEP_AGENT). Use the full plugin-namespaced id: the
          bare agent name resolves to a same-named user/project agent when one exists, while
          the namespaced id always resolves to the plugin's file (measured, aihub#555).
          Do NOT add a model argument - it would silently override the agent file.>,
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
agent with an explicit `subagent_type:` keyed on `is_review`, name BOTH agents, agree with
the constants `engine.native.md` declares AND with the `model:` frontmatter the two agent
files actually carry, and contain NO model argument - so re-adding the argument, swapping
the agents, or re-keying the choice all go red rather than shipping as prose drift.

## 0c. `fail_step_and_attempt` - the review-FAIL path, verbatim

`engine.native.md` abbreviates this call pair. When `parse_review_result` returns `FAIL`:

```python
# next_step is NOT valid on a failure - the loop stops here anyway.
pf_update_step(work_item_id=<current>, step_id=step_id, status="failed",
               step_attempt_id=sa_id, error_type="review_fail")
pf_complete_attempt(work_item_id=<current>, status="failed",
                    note="failed reason: review_fail at step " + step_id)
break   # stop the whole loop, skip the completed report; output the review issues
```

Both calls, in that order. `pf_update_step(failed)` alone leaves the attempt running;
`pf_complete_attempt(failed)` alone leaves the step showing `in_progress` forever.

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

**The mapping, as the auto loop implements it.** Two tiers, selected from the step id alone.
Since aihub#555 the tier is carried by an AGENT DEFINITION FILE, not by a dispatch argument:
each agent file's `model:` frontmatter is the single copy of its model name, read by the
harness as configuration, and it is deliberately NOT repeated in this table (a second
hand-written copy is the aihub#294 drift class; the gate pins the file, not prose).

| step kind | predicate in `engine.native.md` | agent (its file carries the model) |
|---|---|---|
| review / design judgement | `sid.endswith("_review")`, or `sid` in `review` / `code_review` / `release_review` | `REVIEW_AGENT` = `polyforge:step-reviewer` (agents/step-reviewer.md, the raised tier; read-only by construction: Edit/Write/NotebookEdit are disallowed in its definition) |
| everything else | otherwise | `STEP_AGENT` = `polyforge:step-executor` (agents/step-executor.md, the default tier; write-capable) |

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
ids), that raises exactly **9 occurrences**: `code_review` ×8 (chore.aihub, chore.tether,
critical_bug.ieops, feature.aihub, feature.tether, fix_bug.aihub, fix_bug.ieops, fix_bug.tether)
and `release_review` ×1 (release.aihub). Note `review_fix` (×8) is NOT one of them - it applies a
review's findings, which is ordinary editing work, and `endswith("_review")` does not match it.

**Decided by the owner (2026-09-04): task-kind -> tier.** The owner explicitly rejected keying it
on `level:`, and explicitly asked for the policy `hooks/pf-skill-router` already ships on the
superpowers branch - "sonnet for everything except review/architecture, which use opus" - rather
than a second, divergent one. So the two branches now state the same rule.

**Two limits of this mapping, recorded rather than glossed.**
1. The scenario repo produces no *architecture* step id today, so "architecture" in the owner's
   policy has no site to land on and the raised set is the review steps. When such a step
   appears, its id is what needs adding here - not a new key.
2. `spec` and `plan` steps are design judgement and are NOT raised. That is deliberate and it is
   a judgement call, not an oversight: the evidence behind raising review steps is two measured
   catches by clean-context reviewers, while the same note records spec as "possible, undecided".
   The per-review cost delta was measured by aihub#544 against the real transcript corpus
   (median a few dollars per review run at the published price ratio); raising spec/plan
   remains undecided and is the owner's call.

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

## 1. Execute (rhs=true, interactive mode) - the loop in full

> **Why this one is deferred, when the auto loop is not.** Interactive mode is a *per-step*
> mechanic, so the resident/on-demand criterion ("resident = every step, on-demand = once per
> wi") does not by itself put it here - the **budget** did. The two loops are near-identical and
> only one can be resident; the auto loop stays because `requires_human_session=false` is the
> common case. `engine.native.md` therefore carries a summary of this loop plus an explicit
> instruction to read this section before running interactively. If the budget ever frees up,
> this is the first thing that should come back.

```python
# Same bracket as auto mode: start the first step, then complete-and-advance. No pf_get_step
# is needed FOR THE BRACKET (it carries no version token); it is still the authority for
# prior-step context.
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=sections[0].step_id, status="in_progress")

for i, (step_id, content) in enumerate(sections):
    expanded = expand_includes(content, sha)

    output: f"## Step {step_id}\n\n{expanded}"

    wait for user input:
      "continue" / "done" / "ok"  -> fall through to the completed report below, then move to the
                                     next step
      "skip"                      -> COMPLETE it, with a summary that says it was skipped:
                                     pf_update_step(step_id, status="completed",
                                       step_attempt_id=sa_id,
                                       artifact_summary="skipped - <the user's reason>",
                                       next_step=..., next_step_attempt_id=...)
                                     i.e. the same complete-and-advance call as the
                                     "continue" path, differing only in the summary text.
      "fail"                      -> pf_update_step(step_id, status="failed", step_attempt_id=sa_id);
                                     pf_complete_attempt(failed, note="failed reason: <user description>");
                                     break (stop the whole loop)

    if step_id.endswith("_review") or step_id in ("review", "code_review", "release_review"):
        "PASS" / "continue"  -> fall through to the completed report below
        "WARN <desc>"        -> record the warning, ask whether to continue; if yes, report
                                completed as usual
        "FAIL <desc>"        -> pf_update_step(step_id, status="failed", step_attempt_id=sa_id,
                                error_type="review_fail");
                                pf_complete_attempt(failed, note="failed reason: <desc>"); break

    # only the "continue/done/ok" path (or review PASS / WARN-continue) reaches here:
    # report this step completed AND start the next one, in one call.
    next_sa = new_ulid() if i + 1 < len(sections) else None
    pf_update_step(work_item_id=<current>, step_id=step_id, status="completed",
                   step_attempt_id=sa_id,
                   artifact_summary=<one-line summary of what this step produced>,
                   next_step=sections[i+1].step_id if next_sa else None,
                   next_step_attempt_id=next_sa)
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

## 2. Compatibility - server binary older than aihub#290

Applies to BOTH loops. Check what the tools publish; do not infer it from a version string.

- **No `next_step` on `pf_update_step`** -> drop the two `next_*` arguments and start each step
  with its own `pf_update_step(..., status="in_progress", step_attempt_id=...)` at the top of the
  loop, as before. Passing `next_step` anyway means it is silently dropped and the next step
  never starts.
- **No `note` on `pf_complete_attempt`** -> emit
  `pf_emit_event(event_type="note", payload={text: "failed reason: ..."})` **before** the
  terminal call. Do not pass `note` to a tool that does not publish it: it is accepted and
  ignored, and the state file is deleted immediately afterwards, so the failure reason is lost
  with no error.
- **Harness does not know the plugin agent ids** (the Agent call is REJECTED naming an unknown
  subagent_type - an old harness, or a runtime without plugin agent definitions) -> fall back
  for THAT dispatch only: `subagent_type: "general-purpose"` with an explicit model argument
  stating the tier, since no agent file can apply there. Never fall back silently, and never
  pass a model when the namespaced ids resolve - on a harness that knows them, an explicit
  model overrides the agent file (§0f).

See `_common/references/lifecycle-details.md` for the same rules stated from the lifecycle side,
including the `step_attempt_id` trap in the two-call fallback.

## 3. Why `pf_list_work_items(ids=[...])` omits `project`

Since aihub#280 `ids` is a real filter and an id already names exactly one wi. Before that, `ids`
reached no forwarding table and no `project` was sent, so this call was a hard 400 and never ran -
which is why older copies of this engine passed `project` defensively.
