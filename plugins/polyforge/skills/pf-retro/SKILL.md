---
name: pf-retro
description: >
  Use when a wi has just been wrapped and its outcome should be reviewed for learnings
  to save to team memory, whether run standalone or dispatched by /pf-execute. Triggers
  include retro, lessons learned, and what did we learn.
---

# pf-retro - Retrospective & Knowledge Distillation

## Usage

**Purpose**: Run a post-wrap retrospective on a recently-finished wi - extract learnings from the event timeline and batch-save them to team memory.

**Pattern**: `/pf-retro`

**Required**: a just-finished (or about-to-finish) wi accessible in context. Preferred
placement is BEFORE the terminal call (`/pf-stop --wrap` / `--fail`) so artifacts can be
saved; standalone post-wrap runs are memory-only (see When to use)

**Flags**: none

## When to use

After completing a wi. **Placement matters** (credentials, not preference):

- **Preferred - BEFORE the terminal call** (`/pf-stop --wrap` / `--fail`): the attempt's
  state file is still live, so `pf_save_artifact` works and the retro + wrap_summary
  artifacts land on the wi. `/pf-execute`'s built-in retro dispatch and `/pf-stop`'s
  suggestion both point here.
- **Standalone after wrap**: the state file is gone and the wi is TERMINAL - it cannot be
  re-claimed. Analysis and `pf_remember` still work (they need no attempt credentials),
  but artifact saves (`pf_save_artifact`) cannot run. Post-wrap retro is therefore
  **memory-only by design**, not a degraded accident. Do not re-claim a wrapped wi to
  work around this; an explicitly authorized post-wrap artifact path is tracked as a
  follow-up (docs/workflow-v2/04-unresolved-migration-references.md).

## Mechanic

### Step 1: Load wi context

```
wi_info = pf_list_work_items(
  ids=[<current_wi_id>],
  include_step_state=true
)
```

> Since aihub#280 this call actually works. Before it, `ids` was in no MCP
> forwarding table and this site sends no `project`, so the outgoing request was
> a bare `GET /v1/work_items` - a hard **400 `project query parameter is
> required`**. This step had essentially never succeeded. Three things changed:
> `ids` is published and forwarded, `project` is now optional when `ids` is given
> (an id already names one wi; the query is bounded to the projects you can see),
> and `include_step_state=true` really attaches `step_state`. Do not "fix" this
> by adding `project=` or by dropping either param.

### Step 2: Read full event stream

```
events = pf_read_events(
  work_item_id=<current>,
  limit=100
)
```

Includes: commits, pushes, PRs, step completions, notes, decisions, errors.

### Step 3: Recall related historical experience

```
pf_recall(
  project=<current>,
  query=<wi.goal>,
  type="experience.*",
  top_k=3
)
```

Find prior experience relevant to this wi for comparison.

No `fields="brief"` here (aihub#313). Step 4 has to write "This confirms…" /
"This contradicts…" against these entries, and you cannot tell confirmation from
contradiction by reading a title.

### Step 4: LLM retrospective analysis

Produce a structured analysis:

- **What was done**: High-level summary of accomplishments
- **What took longer than expected**: Steps or issues that caused delays
- **What was skipped or changed**: Deviations from the original plan
- **Problems encountered and how they were resolved**: Bug patterns, workarounds
- **Comparison with historical experience**: New findings vs. prior knowledge
  - "This confirms..." / "This contradicts..." / "New discovery: ..."
- **Recommendations for next time**: Concrete suggestions

### Step 5: Batch save learnings (recall-before-remember protocol)

The dedup recall below must NOT pass `fields="brief"` (aihub#313), for two reasons.
"if finding has new details" is a comparison against the candidate's TEXT, so briefing it
would make that branch guess. And `similarity` is what the 0.85 / 0.65 thresholds read:
brief rounds it to 4 decimals - harmless at that precision, but do not lower the rounding.

For each finding:
```python
for finding in findings:
    candidates = pf_recall(
        query=finding.content,
        type=finding.type,  // e.g., "experience.pitfall"
        top_k=3
    )
    
    max_similarity = max(c.similarity for c in candidates) or 0
    
    if max_similarity > 0.85:
        // High similarity - reinforce existing memory, don't duplicate
        pf_activate_memory(candidates[0].id)
        if finding has new details:
            pf_remember(
                project=<current>,
                type=finding.type,
                content=finding.content,
                visibility="team",
                dedup_mode="merge",
                supersedes_memory_id=candidates[0].id
            )
    elif max_similarity > 0.65:
        // Partial overlap - save with cross-reference
        pf_remember(
            type=finding.type,
            content=finding.content,
            project=<current>,
            visibility="team",
            dedup_mode="suggest",
            attrs={"similar_to": candidates[0].id}
        )
    else:
        // New knowledge - save directly
        pf_remember(
            type=finding.type,
            content=finding.content,
            project=<current>,
            visibility="team",
            dedup_mode="off"
        )
```

Memory types to use:
- `experience.debug` - bug patterns discovered
- `experience.approach` - successful solutions to problem classes
- `experience.pitfall` - gotchas to avoid next time
- `experience.code` - specific code-level findings

> **NOTE - artifact saves need live attempt credentials.** `pf_save_artifact` requires an
> active wi claim (state file present). Running retro BEFORE the terminal call satisfies
> this for free. When running standalone on an ALREADY-WRAPPED wi: skip Steps 6-7 (the
> artifact saves) - `pf_remember` (Step 5), `pf_read_events` and `pf_list_work_items` still
> work (no attempt credentials needed). Do NOT re-claim a wrapped wi to force the artifact
> saves: a terminal work item refuses claims, and teaching that path is exactly the
> contradiction this section exists to remove.

### Step 6: Save retro artifact (only while the attempt is live)

Skipped on an already-wrapped wi (see the NOTE above - no state file, no artifact saves).

```
pf_save_artifact(
  type="methodology.retro",
  work_item_id=<current>,
  content=<full markdown retro>,
  structured_payload={
    "went_well": ["..."],
    "went_wrong": ["..."],
    "learnings": ["..."],
    "next_time": ["..."]
  }
)
```

### Step 7: Save wrap summary (same condition)

```
pf_save_artifact(
  type="methodology.wrap_summary",
  work_item_id=<current>,
  content="<1-paragraph summary of what was accomplished, key decisions, outcome>"
)
```

### Step 8: Output three-segment format

"Result" lists how many memories were saved/reinforced.
"Next steps" is `_none_` (wi is complete) or suggests creating follow-up wi's for items
noted in the retro.

## Retro Markdown Format

```markdown
## Retro: <wi_slug> - <goal>
**Date**: <date>  **Duration**: <elapsed>

### What Went Well
- ...

### What Went Wrong
- ...

### Root Causes & Fixes
| Issue | Root Cause | Fix Applied |
|-------|-----------|-------------|
| ...   | ...       | ...         |

### Learnings
1. ...

### For Next Time
- ...

### Memories Saved
- `experience.pitfall` (new): "..."
- `experience.approach` (reinforced): "..."
```

## NL Triggers

- "review" / "retro" / "retrospective"
- "lessons learned" / "what did we learn"
- "save learnings" / "document what happened"
