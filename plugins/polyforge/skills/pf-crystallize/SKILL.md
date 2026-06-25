---
name: pf-crystallize
description: >
  Use when a wi has just been wrapped via /pf-stop --wrap and its operation flow should
  be captured as a reusable scenario workflow for future wis of the same type. Dispatched
  automatically by pf-stop --wrap, not invoked directly by the user.
---

# pf-crystallize — Workflow Crystallization

## Usage

**Purpose**: Crystallize a just-wrapped wi's workflow into a reusable scenario `{wi_type}.{project}.md` file in the scenario repo.

**Pattern**: `/pf-crystallize <source_wi_id> <wi_type_name> [--project <name>]`

**Required**: `<source_wi_id>` (the wrapped wi) and `<wi_type_name>` (snake_case `\w+`)

**Flags**:
- `--project <name>` — produce project-scoped `{wi_type}.{project}.md`; omit (or pass `generic`) for project-agnostic `{wi_type}.md`
- Not user-invokable directly — dispatched by `/pf-stop --wrap` after the user opts in

## When to use

Triggered automatically by `/pf-stop --wrap` after wrap completes. Input arguments:
- `source_wi_id`: the wi ID that was just wrapped
- `wi_type_name`: the new wi_type name entered by the user

Not for direct user invocation.

## Mechanic

### Step 1: Guiding questions

Collect the following information:
- `wi_type`: already obtained from the trigger input (only `\w+` characters, e.g. `deploy`, `data_migration`)
- `project` (optional):
  - User enters a project name → produce `{wi_type}.{project}.md`
  - Press Enter to skip / enter "generic" → produce `{wi_type}.md` (no project suffix)
- `requires_human_session` (default false): ask the user whether human intervention is required

**Early-exit guard**: if the user decides not to crystallize at this point (enters "skip" / "no" / presses Enter), immediately output "Crystallization skipped." and end without performing the subsequent steps.

### Step 2: Create and claim the crystallize chore wi (IR1 compliant)

All file write operations must happen inside a claimed worktree.

Call `/pf-work` in silent mode to create the chore wi, then claim it immediately:
```
Use silent mode:
goal: "crystallize {wi_type}[.{project}] from {source_slug}"
wi_type: chore
project: <source_wi.project>
```

pf-work silent mode only creates the wi and puts it in the queue without asking. After creation, immediately call `/pf-work <slug>` to claim the wi, and perform all subsequent file operations inside the corresponding worktree (`pf.<project>-N/polyforge-coding/`).

### Step 3: Extract the step sequence

Combine two information sources:

**3a. pf_read_events (structured timeline)**
```
events = pf_read_events(
  work_item_id=source_wi_id,
  limit=100
)
```
Extract step names and artifact_summary from `step_completed` events.

**3b. AI in-context window**
If invoked within the same session (operation details are remembered in-session), supplement the details missing from pf_read_events.

> ⚠️ When invoked across sessions, the in-context window is unavailable; rely on pf_read_events only, and output quality depends on how rich the events are.

Combining both, extract an ordered step list:
- Each step `## Step: <id>` (underscore naming, e.g. `prepare_context`, `deploy_staging`)
- Ordered by the sequence in which the operations occurred

### Step 4: Common skill extraction

For each step, scan the `.repo/polyforge-coding/common/` directory and use LLM judgment to assess the match:

**a. Matches an existing common/ skill (>80% overlap)**
→ Replace with `@include: common/<name>/SKILL.md`
→ If applicable, append a `level:` parameter (e.g. `level: quick` for review)

**b. New reusable logic**
→ Ask the user:
```
Step "<name>" looks like it could be extracted into a common skill. Write it to common/<name>/SKILL.md?
```
→ User confirms → generate `common/<name>/SKILL.md` and change the step to `@include:`

**c. Dedicated logic**
→ Write it inline inside the `## Step:` content

### Step 5: Generate the draft and present it

Generate the complete workflow file (format must be compatible with pf-execute):

```markdown
---
requires_human_session: <true|false>
---

## Step: <step_id>
<@include: or inline content>
```

> **Format constraints (pf-execute compatible)**:
> - Frontmatter contains only `requires_human_session` (bool)
> - Step heading strict format: `## Step: <word>` (line start, `\w+` match, no extra spaces)
> - `@include:` may carry a `level:` parameter, which must be on the immediately following line
> - A `## Step:` line inside a code fence is not recognized as a step by pf-execute

Present it to the user:
```
--- Draft preview ---
{generated .md content}

Confirm write (Enter) / Revise (enter revision notes) / Cancel (skip):
```

### Step 6: Write + commit + push (inside the worktree)

After the user confirms:

1. Write `{wi_type}[.{project}].md` (and any new `common/` files) into the polyforge-coding worktree
2. ```bash
   git add .
   git commit -m "feat(scenario): crystallize {wi_type}[.{project}] from {source_slug}"
   git push origin main
   ```
3. Output: "✅ Crystallized and pushed `{filename}`; takes effect on other machines after `polyforge init`."
4. Call `/pf-stop --wrap` to finish the crystallize chore wi

### Step 7: User cancel path (cancel at the draft stage)

If the user enters "cancel" / "skip" at Step 5:
- Do not write any files
- Call `/pf-stop --fail` to terminate the already-claimed crystallize wi (reason: "user cancelled crystallization")
- Output: "Crystallization skipped."

## NL Triggers

- Invoked automatically by `pf-stop --wrap`, passing `source_wi_id` and `wi_type_name`
