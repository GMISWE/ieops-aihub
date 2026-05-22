# SC-04 — pf-retro extracts learnings after wrap

Tests that pf-retro reads the full event timeline, compares planned vs actual,
and saves learnings to team memory with the correct artifact and memory types.

## Scenario

### Setup: Complete a wi via skill chain
Run SC-01 first (or create and wrap any wi with full event timeline).
Save WI_ID of a recently wrapped wi.

NOTE: The wrapped wi must have at least these event types in its timeline:
  claim, step_update (in_progress + completed for each step), note, complete

### Invoke pf-retro
SKILL_INVOKE: polyforge:pf-retro
USER_INTENT: "let's do a retro on what we just shipped"

EXPECTED SKILL BEHAVIOR:

  Step 1 — Load wi context:
    pf_list_work_items(ids=[WI_ID], include_step_state=true)

  Step 2 — Read full event stream:
    pf_read_events(work_item_id=WI_ID, limit=100)
    Includes: commits, pushes, PRs, step completions, notes, decisions, errors.

  Step 3 — Recall related historical experience (Memory-First before writing):
    pf_recall(project="marketplace", query=<wi.goal>, type="experience.*", top_k=3)
    Find prior experience for comparison. Activate useful memories:
    pf_activate_memory(id) for each relevant result.

  Step 4 — LLM retrospective analysis:
    Produce: what was done, what took longer, deviations, problems resolved,
    comparison with historical experience, recommendations.

  Step 5 — Batch save learnings (recall-before-remember protocol):
    For each finding:
      pf_recall(query=finding.content, type=finding.type, top_k=3)  ← recall FIRST
      if similarity > 0.85: pf_activate_memory + pf_remember(dedup_mode="merge", body=..., supersedes_memory_id=...)
      elif similarity > 0.65: pf_remember(dedup_mode="suggest", body=..., attrs={similar_to: ...})
      else: pf_remember(dedup_mode="off", body=...)
    NOTE: pf_remember uses `body` param (NOT `content`). Example:
      pf_remember(type="experience.pitfall", body="<learning text>", project="marketplace",
                  visibility="team", dedup_mode="off")

  Step 6 — Save retro artifact:
    pf_save_artifact(
      type="retro",
      work_item_id=WI_ID,
      content=<full markdown retro>,
      structured_payload={went_well:[...], went_wrong:[...], learnings:[...], next_time:[...]}
    )

  Step 7 — Save wrap summary artifact:
    pf_save_artifact(
      type="methodology.wrap_summary",
      work_item_id=WI_ID,
      content="<1-paragraph summary of what was accomplished, key decisions, outcome>"
    )

  Step 8 — Output three-segment format.

ASSERT MCP CALLS (in order):
  - pf_list_work_items called with WI_ID
  - pf_read_events called with work_item_id=WI_ID
  - pf_recall called BEFORE any pf_remember (Memory-First protocol enforced)
  - pf_remember called at least once with:
      - `body` param (NOT `content`)
      - type starting with "experience." or "rule."
      - visibility = "project" or "team"
  - pf_save_artifact called with type="retro" and structured_payload
  - pf_save_artifact called with type="methodology.wrap_summary"

ASSERT STATE:
  - New memory created in project with content referencing the wi
  - pf_recall(project="marketplace", query=wi.goal) returns the new memory
  - Two artifacts created: type="retro" and type="methodology.wrap_summary"

NOTE: pf_remember requires type, visibility, and body (NOT content).
visibility must be one of: "personal", "project", "team".
Use "team" only for broadly applicable learnings; prefer "project" for wi-specific observations.

## PASS criteria
pf-retro reads events; calls pf_recall before pf_remember (Memory-First);
uses `body` param for pf_remember; saves retro artifact (type="retro");
saves wrap summary artifact (type="methodology.wrap_summary");
at least one memory saved/reinforced.
