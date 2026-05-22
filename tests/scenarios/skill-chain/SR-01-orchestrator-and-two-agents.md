# SR-01 — Orchestrator + two agents concurrent (realistic Session 1)

Tests real-world scenario: Admin acts as Orchestrator, creates sprint backlog,
Alice and Bob are auto-agents picking up tasks from the ready queue concurrently.

## Users
- ADMIN (Orchestrator, Session 2): creates wi's, monitors
  API key: baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
- ALICE (Agent, Session 1): picks up fix_bug tasks automatically
  API key: pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V
- BOB (Agent, Session 1): picks up chore tasks automatically
  API key: pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR

## Scenario

### Phase 1: Orchestrator creates sprint backlog
SKILL_INVOKE (as ADMIN): polyforge:pf-status
→ Verify queue is empty (or note existing wi count)

ADMIN creates 3 wi's via MCP (simulating /pf-work --no-claim pattern):
  WI_FIX1: pf_create_work_item(project="marketplace", goal="fix: null check in auth",
    wi_type="fix_bug", requires_human_session=false, priority="high",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/fix1-test-sr01"}])
  WI_FIX2: pf_create_work_item(project="marketplace", goal="fix: timeout in search endpoint",
    wi_type="fix_bug", requires_human_session=false, priority="high",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/fix2-test-sr01"}])
  WI_CHORE: pf_create_work_item(project="marketplace", goal="chore: update dependencies",
    wi_type="chore", requires_human_session=false, priority="normal",
    declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
      "task_branch":"polyforge/chore-test-sr01"}])

NOTE: All three wi's have different task_branches to avoid lock contention.

SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT: all 3 wi's in items[] segment (priority-ordered: WI_FIX1, WI_FIX2, WI_CHORE)

### Phase 2: Alice takes WI_FIX1
SKILL_INVOKE (as ALICE): polyforge:pf-work WI_FIX1
ASSERT:
  - pf_claim called (Alice's key), WI_FIX1 status=running
  - Worktree created for ALICE at WORKSPACE_ROOT/pf.<seq>.<ulid8>/marketplace/

### Phase 3: Bob takes WI_CHORE (concurrent with Alice)
SKILL_INVOKE (as BOB): polyforge:pf-work WI_CHORE
ASSERT:
  - pf_claim called (Bob's key), WI_CHORE status=running
  - Worktree created for BOB at a DIFFERENT path from Alice's
  - Alice and Bob hold DIFFERENT wi's (no conflict)
  - WI_FIX2 remains in items[] (nobody claimed it)

### Phase 4: Admin checks status mid-execution
SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT rendered output:
  - WI_FIX1 in running[] owned by Alice (Test Agent Alice)
  - WI_CHORE in running[] owned by Bob (Test Writer Bob)
  - WI_FIX2 still in items[] (available for next agent)

### Phase 5: Alice wraps WI_FIX1
SKILL_INVOKE (as ALICE): polyforge:pf-stop --wrap
  (Alice may skip code_change/commit for this test — just wrap immediately)

ASSERT:
  - WI_FIX1 status=="wrapped"
  - Alice's worktree cleaned up

### Phase 6: Bob wraps WI_CHORE
SKILL_INVOKE (as BOB): polyforge:pf-stop --wrap

ASSERT:
  - WI_CHORE status=="wrapped"
  - Bob's worktree cleaned up

### Phase 7: Admin final status check
SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT:
  - WI_FIX1 and WI_CHORE no longer in active segments (wrapped)
  - WI_FIX2 still in items[] (available for next agent)

## Cleanup
CLEANUP: pf_complete_attempt(WI_FIX2, status="cancelled") via Admin key

## PASS criteria
Two agents work concurrently without interfering; Orchestrator sees correct state
at each phase; both wi's wrapped cleanly.
