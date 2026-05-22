# SR-04 — Sprint planning: create backlog, assign priorities, parallel execution

Tests realistic sprint flow: PM (Admin) creates 5 wi's with priorities and
dependencies; two agents (Alice, Bob) execute in parallel respecting dep order.

## Users
- ADMIN (PM/Orchestrator)
  API key: baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
- ALICE (Agent 1, takes high-priority tasks)
  API key: pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V
- BOB (Agent 2, takes normal-priority tasks)
  API key: pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR

## Scenario

### Phase 1: PM creates sprint backlog
ADMIN creates 5 wi's (all requires_human_session=false):

  WI_DB: pf_create_work_item(goal="chore: migrate DB schema",
    wi_type="chore", priority="high",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/db-sr04"}])

  WI_API: pf_create_work_item(goal="fix: API returns wrong status code",
    wi_type="fix_bug", priority="high",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/api-sr04"}])

  WI_UI: pf_create_work_item(goal="chore: update UI components",
    wi_type="chore", priority="normal",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/ui-sr04"}])

  WI_TEST: pf_create_work_item(goal="chore: add integration tests",
    wi_type="chore", priority="normal",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/test-sr04"}])

  WI_DOCS: pf_create_work_item(goal="chore: update API docs",
    wi_type="chore", priority="low",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/docs-sr04"}])

ADMIN creates dependencies (after wi creation):
  pf_create_dependency(blocked_work_item_id=WI_API, blocking_work_item_id=WI_DB)
  pf_create_dependency(blocked_work_item_id=WI_TEST, blocking_work_item_id=WI_API)

SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT:
  - items[]: WI_DB (high), WI_UI (normal), WI_DOCS (low) — ordered by priority
  - WI_API, WI_TEST NOT in items[] (blocked by dependency)

### Phase 2: Alice takes highest-priority WI_DB
SKILL_INVOKE (as ALICE): polyforge:pf-work WI_DB
ASSERT:
  - WI_DB claimed by Alice, status=running

Bob takes WI_UI concurrently:
SKILL_INVOKE (as BOB): polyforge:pf-work WI_UI
ASSERT:
  - WI_UI claimed by Bob, status=running
  - Alice and Bob have separate worktrees for separate repos (no lock conflict:
    WI_DB and WI_UI use different task_branches)

### Phase 3: Alice wraps WI_DB (unblocks WI_API)
SKILL_INVOKE (as ALICE): polyforge:pf-stop --wrap
  (Alice may skip code_change/commit for speed in this test)

SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT: WI_API now in items[] (unblocked by WI_DB wrap)

### Phase 4: Alice picks up WI_API (next high-priority)
SKILL_INVOKE (as ALICE): polyforge:pf-work WI_API
ASSERT:
  - WI_API claimed by Alice
  - WI_UI still running (Bob still working on it — no interference)

### Phase 5: Bob wraps WI_UI
SKILL_INVOKE (as BOB): polyforge:pf-stop --wrap

### Phase 6: Alice wraps WI_API (unblocks WI_TEST)
SKILL_INVOKE (as ALICE): polyforge:pf-stop --wrap

SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT: WI_TEST now in items[] (unblocked by WI_API wrap)

### Phase 7: Final status
SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT:
  - WI_DB, WI_API, WI_UI: wrapped (not in active segments)
  - WI_TEST, WI_DOCS: in items[] (ready for next sprint or agents)

## Cleanup
CLEANUP (as ADMIN):
  - pf_complete_attempt(WI_TEST, status="cancelled")
  - pf_complete_attempt(WI_DOCS, status="cancelled")

## PASS criteria
Dependency ordering enforced; high-priority tasks picked first; parallel agents
don't conflict on separate branches; unblocking cascade works correctly.
