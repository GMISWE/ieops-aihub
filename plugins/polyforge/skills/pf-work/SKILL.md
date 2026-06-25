---
name: pf-work
description: >
  Use when the user wants to begin working on something: start a new work item, claim
  a queued one, resume a paused one, or take over a stalled or idle one from another
  agent.
---

# pf-work — Work Item Lifecycle Entry

## Usage

**Purpose**: Enter the wi lifecycle — create a new wi, claim a queued one, resume a paused one, or force-takeover a stalled one.

**Pattern**: `/pf-work [<slug>] [--resume | --force]`

**Required**: none (no-arg = create-new dialog; with `<slug>` = claim/resume/takeover)

**Flags**:
- `--resume` — resume a paused wi (Mode C)
- `--force` — force-takeover an idle/expired wi (Mode D); destructive against current claimer, requires `reason`
- `--silent` / silent-mode trigger — Mode A: create + queue without prompting to claim (NL trigger, not a literal CLI flag)

## When to use

Any time the user wants to begin working on something — new task, picking up a queued
item, resuming yesterday's work, or taking over a stalled wi from another agent.

## Architecture rules

pf-work is the **only creation entry point** for the wi lifecycle. Whether human or AI
(including a problem discovered mid-step during execution), creating a wi must go through
this skill.

Invocation modes:
- **dialog mode** (default): a human/AI creates a wi during session discussion -> after creation, ask whether to claim
- **silent mode**: an AI creates a wi mid-step during execution -> state "use silent mode" or "silent create" when invoking -> only create and put on the queue, do not ask

## Mechanic

### Post-claim routing

See `## Post-claim Next steps Routing` in `using-polyforge/SKILL.md` — that section is the
**single source of truth** for what to suggest in "Next steps" after any claim, and applies
to **all** skills that emit three-segment output (not just `pf-work`). `using-polyforge` is
auto-loaded into every session's context, so this backreference resolves reliably
(unlike an in-file anchor jump, which the LLM does not consistently follow at
generation time — see `mem_5obNUSSR`).

### Mode A — New wi (default, triggered by intent to start something new)

1. **Memory-First** (using-polyforge handles this at session start; surface results).

2. **Resolve wi_type from scenario repo**:

   Read the project's scenario clone from `.repo/` (cloned by `polyforge init`):
   ```bash
   scenario_url  = project.scenario  // from .polyforge.yaml
   scenario_name = <last path segment of URL, strip .git>
                   // "git@github.com:GMISWE/polyforge-coding.git" → "polyforge-coding"
   scenario_path = <workspace_root>/.repo/<scenario_name>/

   // Scenario not cloned yet?
   if scenario_path does not exist:
       STOP: "⚠️ Scenario repo not cloned yet; please run polyforge init first."

   // Infer valid wi_type from .md file names
   // List all *.md files under scenario_path, extract the {wi_type} prefix (before the first .)
   // Exclude "default"
   available_wi_types = [
       f.split(".")[0]
       for f in os.listdir(scenario_path)
       if f.endswith(".md") and not f.startswith("default")
   ]

   // Validation (when creating a wi):
   // Check that at least one of {wi_type}.{project}.md or {wi_type}.md exists
   // has_step_sections: the file contains at least one ^## Step: line
   def has_step_sections(filepath):
       with open(filepath) as f:
           return any(re.match(r"^## Step: \w+", line) for line in f)

   def validate_wi_type(wi_type, project, scenario_path):
       specific = f"{wi_type}.{project}.md"
       generic  = f"{wi_type}.md"
       for path, tag in [(specific, "ok"), (generic, "warn")]:
           full = f"{scenario_path}/{path}"
           if os.path.exists(full):
               if not has_step_sections(full):
                   return "error", None  # file exists but has no ## Step: sections
               return tag, path
       return "error", None      # reject creation

   // requires_human_session: read from the .md file frontmatter
   // project-specific file takes priority; fallback to the generic file; default true if neither exists
   def get_rhs(wi_type, project, scenario_path):
       for path in [f"{wi_type}.{project}.md", f"{wi_type}.md"]:
           full = f"{scenario_path}/{path}"
           if os.path.exists(full):
               fm = parse_frontmatter(full)
               return fm.get("requires_human_session", True)
       return True  # default
   ```

   AI infers wi_type from goal description + complexity, **matching against available_wi_types**:
   - Bug, root cause clear, small change → `fix_bug`
   - Bug, large impact or root cause unknown → `critical_bug`
   - Feature needing design decisions → `feature`
   - Simple maintenance, no design needed → `chore`
   - …other wi_types defined by .md files in the project's scenario repo

   **If no project scenario configured** OR **validate_wi_type returns "error"**:
   → Fall back to built-in `default` wi_type (`requires_human_session=true`, steps=[]).
   Notify user: "⚠️ Could not match wi_type, using default (requires human session)."

   **If validate_wi_type returns "warn"**:
   → Proceed with the generic .md flow; notify user:
   "⚠️ {wi_type}.{project}.md not found, will use the generic flow {wi_type}.md."

2b. **AI extracts content draft from conversation**:
    From the current session conversation, extract a content draft describing the problem:
    - Background: why does this wi exist, what triggered it
    - Context: relevant information, known constraints, related discussions
    - Do NOT include solution approach (that belongs in spec/plan)

    Show the draft to the user for confirmation/modification:
    ```
    --- content draft ---
    <extracted background and context>

    Confirm (press Enter) or modify:
    ```

    If conversation context is insufficient for meaningful content, skip (content is optional).
    Pass the confirmed draft as `content=<draft>` to pf_create_work_item.

3. **Conflict preview** (before creating):
   ```
   pf_predict_conflicts(declared_resources=<new wi's resources>, dry_run=true)
   ```
   Show impact. If hard conflict → stop and explain.

4. **Create** (do NOT claim yet):
   ```
   pf_create_work_item(
     project=<from .polyforge.yaml>,
     goal=<user_goal>,
     wi_type=<inferred>,
     requires_human_session=<from get_rhs(wi_type, project, scenario_path)>,
     priority=<inferred: urgent/high/normal/low>,
     labels=[...],
     content=<confirmed draft>
   )
   ```
   - `400 PROJECT_NOT_FOUND` → prompt to create project first
   - `409 DUPLICATE` → show existing wi, ask: "Continue new / Claim existing / Cancel"
   - `409 CANDIDATES` → show candidate list, ask user to choose

5. **Interactive confirmation** (dialog mode) / **Silent** (silent mode):

   **dialog mode** (default):
   Output: "Created <slug> (<goal[:40]>). Claim and start working on it now?"
   
   → human says "yes" / "do it" / "claim" → claim directly (skip predict_conflicts, the wi was just created and has no locks):
     ```
     pf_claim_work_item(
       work_item_id=<wi_id>,
       idempotency_key=<client ULID>,
       mode="fresh"
     )
     ```
     then recall wi-linked memories:
     ```
     pf_recall(project=<wi.project>, work_item_id=<wi_id>, top_k=10)
     ```
     **rhs routing** (wi.requires_human_session):
     - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
     - `true`  → emit three-segment output ("Next steps" decided per `using-polyforge`'s `## Post-claim Next steps Routing`), wait for human session.
   
   → human says "no" / "not now" / "leave it" → emit three-segment output, wi stays on the queue.

   **silent mode** (state "use silent mode" or "silent create" when invoking):
   emit three-segment output directly, do not ask, do not claim, wi stays on the queue.

6. Output three-segment format.

---

### Mode B — Claim existing queued wi (`/pf-work <slug>`)

1. `pf_predict_conflicts(work_item_id=<slug>, dry_run=true)` → conflict preview
2. `pf_claim_work_item(work_item_id=<slug>, mode="fresh", ...)`
3. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
4. **rhs routing** (wi.requires_human_session):
   - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
   - `true`  → emit three-segment output ("Next steps" decided per `using-polyforge`'s `## Post-claim Next steps Routing`), wait for human session.

---

### Mode C — Resume paused wi (`/pf-work <slug> --resume`)

1. ```
   pf_claim_work_item(
     work_item_id=<slug>,
     mode="resume",
     idempotency_key=<client ULID>
     // Do NOT pass scenario_ref — COALESCE on server preserves the original pinned SHA
   )
   ```
   Restores: prepared workspace + step state from the previous attempt.

   > ⚠️ If this wi was originally claimed on a different machine, the pinned
   > `scenario_ref` SHA may not exist in the local clone. pf-execute will auto-fetch
   > if needed, but verify local scenario clone is current: `polyforge init`.
2. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
3. Show step progress: "Resuming at step 2/4 (review)".
4. **rhs routing** (wi.requires_human_session):
   - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
   - `true`  → emit three-segment output (including step progress; "Next steps" decided per `using-polyforge`'s `## Post-claim Next steps Routing`), wait for human session.

---

### Mode D — Force takeover (`/pf-work <slug> --force`)

Permission rules:
- `writer` can take over any running wi (claim is static ownership; takeover is always explicit)
- `admin` can take over any attempt at any time (must supply `reason`)

Steps:
1. `pf_force_takeover(work_item_id=<slug>, reason=<user input>)`
2. `pf_claim_work_item(mode="fresh", ...)` — fresh claim.
4. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
5. **rhs routing** (wi.requires_human_session):
   - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
   - `true`  → emit three-segment output ("Next steps" decided per `using-polyforge`'s `## Post-claim Next steps Routing`), wait for human session.

---

### State file management

After a successful claim, `<workspace>/.polyforge/state/<wi_id>.json` contains:
```json
{
  "wi_id": "wi_xxx",
  "attempt_id": "ra_xxx",
  "claim_epoch": 1,
  "workspace_root": "/path/to/workspace",
  "repo": "repo-name",
  "task_branch": "polyforge/<slug>"
}
```
`session_secret` is stored in this file by the MCP server and is never shown in output.

## NL Triggers

- "start" / "new task" / "let's start" / "I want to work on"
- "claim [slug]" / "pick up [slug]"
- "resume [slug]" / "continue [slug]" / "pick this back up"
- "takeover [slug]" / "take over [slug]" / "force claim [slug]"
