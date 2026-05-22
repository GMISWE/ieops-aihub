# SC-04 — pf-retro extracts learnings after wrap

Tests that pf-retro reads the full event timeline, compares planned vs actual,
and saves learnings to team memory.

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
  1. pf_read_events(work_item_id=WI_ID, limit="50") — full timeline
  2. Compare: planned steps (from phase.yaml wi_type steps) vs actual events
  3. Extract learnings: what went well, what took longer, pitfalls
  4. pf_remember(type="experience.retro", visibility="project", content="<learnings>")
     OR pf_remember(type="rule.process", visibility="team", content="<rule>")
  5. Confirm: "Retro complete. N memories saved."

ASSERT MCP CALLS:
  - pf_read_events called with work_item_id=WI_ID
  - pf_remember called at least once with type starting with "experience." or "rule."

ASSERT STATE:
  - New memory created in project with content referencing the wi
  - pf_recall(project="marketplace", query=wi.goal) returns the new memory

NOTE: pf_remember requires type, visibility, and content. visibility must be one
of: "personal", "project", "team". Do not use "team" unless the learning is
broadly applicable; prefer "project" for wi-specific observations.

## PASS criteria
pf-retro reads events; extracts at least one learning; saves to memory.
