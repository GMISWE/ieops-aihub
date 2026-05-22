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
  5. Create WI_BLOCKED_DEP (chore) → create dependency on WI_RUNNING → stalled[]
  6. Create WI_UNCLASSIFIED via raw HTTP (wi_type=NULL) → unclassified[]

NOTE: For MCP claims (steps 3-4), use pf_claim_work_item with mode="fresh":
```bash
MY_SECRET=$(python3 -c "import secrets; print(secrets.token_hex(32))")
# WI_RUNNING: claim with Alice's key, do not wrap — remains in running[]
# WI_PAUSED: claim with Bob's key, then pf_complete_attempt(status="paused")
```

NOTE on WI_UNCLASSIFIED: create a wi via the HTTP API with wi_type omitted or set to NULL.
wi's with wi_type=NULL cannot be auto-claimed and appear in the unclassified[] segment.
```
POST /api/v1/work_items  {"project":"marketplace","goal":"orphan wi for test","wi_type":null}
```

NOTE on WI_BLOCKED_DEP: create the dependency after WI_BLOCKED_DEP is created:
```
pf_create_dependency(blocked_wi_id=WI_BLOCKED_DEP, blocking_wi_id=WI_RUNNING, kind="blocks")
```
WI_BLOCKED_DEP will be moved from items[] to stalled[] automatically.

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
  - WI_HUMAN in needs_human_session[] section (NOT in items[])
  - WI_RUNNING in running[] section with owner info
  - WI_PAUSED in paused[] section
  - WI_BLOCKED_DEP in stalled[] section (NOT in items[]; blocked by WI_RUNNING)
    - stalled[] entry must show which wi is blocking: "blocked by: WI_RUNNING"
  - WI_UNCLASSIFIED in unclassified[] section (wi_type=NULL)

ASSERT SEGMENTS:
  - WI_HUMAN NOT in items[] (requires_human_session=true items go to needs_human_session, not items)
  - WI_BLOCKED_DEP NOT in items[] (blocked by dependency, must be in stalled[])
  - WI_AUTO is the only entry in items[] from this test setup

NOTE: Auto-agents (Session 1) dispatch only from items[], not from needs_human_session[]
or stalled[]. The LCRS view is critical for Admin to identify bottlenecks.

## Cleanup
CLEANUP:
  - Cancel WI_AUTO, WI_HUMAN, WI_BLOCKED_DEP, WI_UNCLASSIFIED
    (pf_complete_attempt(status="cancelled") or DELETE endpoint)
  - Wrap WI_RUNNING (pf_complete_attempt(status="wrapped") via Admin key)
  - Re-claim then wrap WI_PAUSED (pf_claim_work_item mode="resume" then pf_complete_attempt(status="wrapped"))

## PASS criteria
pf-status renders all six segments correctly; wi's appear in correct segments;
WI_BLOCKED_DEP appears in stalled[] with blocker identified;
WI_UNCLASSIFIED appears in unclassified[].
