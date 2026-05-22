# SC-03 — pf-status shows LCRS six-segment ready queue

Tests that pf-status correctly renders the six-segment view with wi's
in different states: items, running, stalled, paused, needs_human_session, unclassified.

## Setup
Create wi's in different states before running pf-status.

## Scenario

### Setup: Create wi's in all segments
AS ADMIN (via MCP tools, not skills):
  1. Create WI_AUTO (fix_bug, requires_human_session=false) → queued → items[]
  2. Create WI_HUMAN (critical_bug, requires_human_session=true) → needs_human_session[]
  3. Create WI_RUNNING (chore) → claim it → running[]
  4. Create WI_PAUSED (chore) → claim → pause → paused[]
  5. Create WI_BLOCKED_DEP (chore) → create dependency on WI_RUNNING → blocked

NOTE: For MCP claims (steps 3-4), use pf_claim_work_item with mode="fresh":
```bash
MY_SECRET=$(python3 -c "import secrets; print(secrets.token_hex(32))")
# WI_RUNNING: claim with Alice's key, do not wrap
# WI_PAUSED: claim with Bob's key, then pf_complete_attempt(status="paused")
```

### Invoke pf-status (skill layer)
SKILL_INVOKE: polyforge:pf-status
USER_INTENT: "show me the current status of all work items"

EXPECTED SKILL BEHAVIOR:
  1. pf_get_ready_queue(project="marketplace")
  2. Render six segments: items[], running[], stalled[], paused[], needs_human_session[], unclassified[]
  3. Show wi summaries with owner, priority, slug

ASSERT MCP CALLS:
  - pf_get_ready_queue called with project parameter

ASSERT RENDERED OUTPUT CONTAINS:
  - WI_AUTO in items[] section
  - WI_HUMAN in needs_human_session[] section
  - WI_RUNNING in running[] section
  - WI_PAUSED in paused[] section
  - WI_BLOCKED_DEP NOT in items[] (blocked by WI_RUNNING)

## Cleanup
CLEANUP:
  - Cancel WI_AUTO, WI_HUMAN, WI_BLOCKED_DEP (pf_complete_attempt or DELETE)
  - Wrap WI_RUNNING, WI_PAUSED (must re-claim WI_PAUSED first if paused)

## PASS criteria
pf-status renders all six segments correctly; wi's appear in correct segments.
