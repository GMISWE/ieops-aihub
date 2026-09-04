# pf-execute/references/engine-native-details.md — deferred engine detail (ON DEMAND)

> **Not injected.** `engine.native.md` is injected by `hooks/pf-skill-router` and shares a hard
> **10,000-character** budget with the memory, storage and lifecycle fragments (aihub#304 — past
> that limit the harness replaces the whole payload with a ~2,000-character preview, so growing
> the resident tier removes context instead of adding it).
>
> `Read` this file when you are actually running an interactive (`requires_human_session=true`)
> execute loop, or when a tool does not publish a parameter the resident fragment uses.

---

## 0. Startup — the exact commands

1. Read the wi info: `wi_info = pf_list_work_items(ids=[<current_wi_id>])`.
2. Resolve the scenario repo path from `.polyforge.yaml`. `owner` and `repo` are the last two
   path segments of `project.scenario` with `.git` stripped
   (`git@github.com:GMISWE/polyforge-coding.git` → `GMISWE`, `polyforge-coding`; a URL with
   only ONE path segment has no owner and keeps the bare repo name):
   `scenario_path = <workspace_root>/.repo/<owner>__<repo>/`.

   Owner-qualified because keying on the repo name alone gave two orgs' same-named scenario
   repos ONE directory: the second was never cloned, its projects silently ran the first
   org's step graph, and nothing went red — `polyforge init` only fetch+reset an existing
   checkout without comparing its remote, and wi_type validation asks whether the template
   file exists, never which repo it came from (aihub#327).

   **Legacy fallback — and it is guarded.** A workspace whose last `polyforge init` predates
   this layout still holds the clone at `<workspace_root>/.repo/<repo>/`. Use it only when
   `git -C <workspace_root>/.repo/<repo> remote get-url origin` names the same repo as
   `project.scenario` (ignore scheme, credentials and `.git`). If it names a different repo,
   or the directory is absent, STOP and report that `polyforge init` has not been run with a
   binary that knows this layout. An unguarded fallback re-opens the exact silent mix-up in
   every workspace that has not re-inited.
3. **SHA pinning** — write it to `.pf_meta.json` in the worktree root:
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
   passes it through as `Review level: <value>` — nothing more. See §0f.

## 0b. The auto-mode subagent prompt, verbatim

`engine.native.md` summarises this in prose to stay inside the payload budget. The literal
template the loop dispatches:

```
You are executing step {step_id} of wi {wi_id}.

Call pf_get_step(work_item_id={wi_id}) FIRST — it is the only authority for prior-step context.
Treat every step_id in completed_steps as already done and read their artifact_summary. Never
take step progress from a file in the worktree; nothing writes one.

--- step instructions ---
{expanded}
--- END ---

When done, RETURN your one-line summary of this step in your output; the loop passes it straight
to pf_update_step(artifact_summary=...). Do not write it to a file.
If there are learnings worth keeping, call pf_remember to store them in aihub.
```

## 0c. `fail_step_and_attempt` — the review-FAIL path, verbatim

`engine.native.md` abbreviates this call pair. When `parse_review_result` returns `FAIL`:

```python
# next_step is NOT valid on a failure — the loop stops here anyway.
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
the wi to a human. From that moment the attempt is no longer `running`, and the server's
`verifyAttemptCredential` hard-rejects EVERY credential-checked `pf_*` call — `pf_update_step`,
`pf_save_artifact`, `pf_complete_attempt`, all of them. Pausing therefore cannot corrupt step
state; the loop simply cannot advance. What it *can* do is walk into a cascade of surprise
credential errors and retry them, which is what this branch exists to prevent.

Break out of the loop on either signal:

- the step's own output says it paused the attempt (it called `pf_pause_attempt`), or
- any `pf_*` call is rejected because the attempt is not running. The paused case has its own
  error code — `ErrAttemptPaused`, "attempt is paused; resume it before continuing" — which is
  deliberately distinct from a stale credential, so do not treat it as one.

On that path:

- **Do NOT retry.** Nothing can succeed until a human resumes; a retry only burns tokens.
- **Do NOT call `pf_complete_attempt`.** The attempt must STAY paused — that is the state the
  human resumes into, and a terminal call destroys it. This is the one loop exit that ends with
  no terminal call, and the server would reject it anyway.
- **Do NOT report the step completed**, and do not start the next step.
- Report three-segment, needs-you:

```
## Result
Step <step_id> paused the attempt for <slug> — a human needs to look at it.

## Status
| wi     | <slug>              |
| step   | <step_id>           |
| status | paused (needs you)  |

## Next steps
- See what it needs: /pf-status <slug>
- Resume once it is resolved: /pf-work <slug>
```

## 0f. There is no per-step model tier, and `level:` cannot become one (aihub#358)

Every step in both loops dispatches with `default_model`. This section exists because the engine
spent 2.5 months claiming otherwise.

**What the text used to say.** `engine.native.md` compared `step_level(content)` against the
literal `"opus"` to pick the model, and this file called that comparison a "special case".
Three things were wrong with it at once:

1. `step_level` is defined nowhere — in any language, in any repo. It is pseudo-code a model
   executes by reading it, so the comparison is only ever as real as the value it compares.
2. **The two `level:`s are different parameters that happen to share a key name.** The one the
   scenario repo emits is `common/review/SKILL.md`'s review-DEPTH argument, enumerated
   `quick|medium|deep|challenge` (that file's frontmatter and its `structured_payload` contract
   both state it). The one the selector wanted was a model name. Measured at
   polyforge-coding@6231732: **9** `level:` lines, every one of them the line immediately after
   `@include: common/review/SKILL.md`, values `quick`×4 / `deep`×5 — and not one occurrence of
   `opus`, `sonnet` or `haiku` anywhere in that repo.
3. So `{quick,medium,deep,challenge} ∩ {opus} = ∅` and the branch was unreachable: **every step
   has always dispatched sonnet.** The tiering never fired once.

**Why the key name makes this unfixable in place.** Because the two parameters share `level:`,
putting a model name in that field to select a model *simultaneously* hands that model name to
`common/review` as a review depth its enumeration does not contain. "deep review" and "opus
model" cannot both be requested through this one key. Mapping a depth to a model is therefore a
change of contract, and it changes cost on every project at once — 5 step-graph entries carry
`level: deep` (`release.aihub`, `critical_bug.ieops`, `fix_bug.tether`, `feature.aihub`,
`feature.tether`) and 4 carry `level: quick`. That is the owner's call, not an implementation
detail.

**Note the superpowers branch is unaffected and already has a working policy.**
`hooks/pf-skill-router` injects, for that branch only, "sonnet for everything except
review/architecture, which use opus" — keyed on the KIND OF TASK, not on `level:`. Whatever
replaces the native tier should match that policy rather than invent a second one.

**The gate.** `internal/cli/engine_native_contract_test.go`
(`TestEngineNativeLevelVocabularyContract`) fails if either engine document ever names a
`level:` value the scenario repo does not produce. It carries the scenario vocabulary as a
pinned set because aihub's CI never checks out polyforge-coding, and reconciles that pinned set
against the live repo whenever a checkout is reachable — so the pin cannot rot silently on any
machine that has one.

## 1. Execute (rhs=true, interactive mode) — the loop in full

> ⚠️ **Why this one is deferred, when the auto loop is not.** Interactive mode is a *per-step*
> mechanic, so the resident/on-demand criterion ("resident = every step, on-demand = once per
> wi") does not by itself put it here — the **budget** did. The two loops are near-identical and
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
      "skip"                      -> note it in your own output; **do NOT report this step**;
                                     continue to the next step WITHOUT calling pf_update_step at
                                     all. The skipped step stays in_progress and stays the
                                     server's current_step; the next step you actually complete
                                     reports itself and advances from there.
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

## 2. Compatibility — server binary older than aihub#290

Applies to BOTH loops. Check what the tools publish; do not infer it from a version string.

- **No `next_step` on `pf_update_step`** → drop the two `next_*` arguments and start each step
  with its own `pf_update_step(..., status="in_progress", step_attempt_id=...)` at the top of the
  loop, as before. Passing `next_step` anyway means it is silently dropped and the next step
  never starts.
- **No `note` on `pf_complete_attempt`** → emit
  `pf_emit_event(event_type="note", payload={text: "failed reason: ..."})` **before** the
  terminal call. Do not pass `note` to a tool that does not publish it: it is accepted and
  ignored, and the state file is deleted immediately afterwards, so the failure reason is lost
  with no error.

See `_common/references/lifecycle-details.md` for the same rules stated from the lifecycle side,
including the `step_attempt_id` trap in the two-call fallback.

## 3. Why `pf_list_work_items(ids=[...])` omits `project`

Since aihub#280 `ids` is a real filter and an id already names exactly one wi. Before that, `ids`
reached no forwarding table and no `project` was sent, so this call was a hard 400 and never ran
— which is why older copies of this engine passed `project` defensively.
