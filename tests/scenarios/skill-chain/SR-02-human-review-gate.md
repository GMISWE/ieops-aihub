# SR-02 — Human review gate: critical bug requires Bob's session

Tests the Session 1 / Session 2 routing: a critical_bug wi waits in
needs_human_session[] until a human (Bob) explicitly claims it.
An auto-agent (Alice) cannot pick it up.

## Users
- ADMIN: creates critical_bug wi
  API key: baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
- ALICE (auto-agent, Session 1): should NOT pick up the critical wi
  API key: pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V
- BOB (human developer, Session 2): claims and resolves it
  API key: pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR

## Scenario

### Phase 1: Admin reports critical bug
ADMIN (via MCP): pf_create_work_item(
  project="marketplace",
  goal="critical: auth bypass allowing unauthenticated access to admin endpoints",
  wi_type="critical_bug",
  requires_human_session=true,
  priority="urgent",
  declared_resources=[{"type":"repo","uri":"repo:marketplace","intent":"exclusive",
    "task_branch":"polyforge/critical-sr02-test"}]
)
Save as WI_CRITICAL.

SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT: WI_CRITICAL in needs_human_session[] NOT in items[]

### Phase 2: Alice's Orchestrator view does NOT dispatch WI_CRITICAL
AS ALICE: pf_get_ready_queue(project="marketplace")
ASSERT: items[] does NOT contain WI_CRITICAL
NOTE: Auto-agents (Session 1) only pick from items[], not needs_human_session[].
      Alice's orchestrator loop checks items[] only — WI_CRITICAL is invisible.
ASSERT: Alice does NOT attempt to call pf_claim_work_item for WI_CRITICAL

### Phase 3: Bob opens human-led session, claims WI_CRITICAL
SKILL_INVOKE (as BOB): polyforge:pf-work WI_CRITICAL
ASSERT:
  - pf_claim called successfully (human session, Bob's key)
  - WI_CRITICAL status=running
  - Worktree created for Bob

### Phase 4: Bob runs debug spec
SKILL_INVOKE (as BOB): polyforge:pf-spec
USER_INTENT: "debug variant: analyze why auth bypass is possible"

EXPECTED SKILL BEHAVIOR:
  1. pf_update_step(spec, in_progress)
  2. Heartbeat during analysis: pf_update_step(heartbeat=true)
  3. Root cause analysis format:
     - Symptoms, reproduction steps, impact, proposed fix
  4. pf_save_artifact(type="methodology.spec", content="Root cause: missing JWT validation in /admin/* middleware; fix: add jwt.Verify() before route handler")
  5. pf_emit_event(event_type="note", payload={text: "spec saved: mem_XXX"})
  6. pf_update_step(step_id="spec", status="completed", artifact_summary="root cause: missing JWT validation in admin middleware")

ASSERT:
  - methodology.spec artifact saved with debug-format content (root cause section present)
  - pf_update_step(spec, completed) called with artifact_summary

### Phase 5: Bob fixes and ships
SKILL_INVOKE (as BOB): polyforge-coding:code_change
EXPECTED: reads spec artifact, edits WT_PATH file (adds JWT validation), saves plan artifact

SKILL_INVOKE (as BOB): polyforge-coding:commit_and_pr
EXPECTED: pf_commit (conventional: "fix: add JWT validation to admin middleware"),
          pf_push, pf_pr → PR URL logged

SKILL_INVOKE (as BOB): polyforge:pf-stop --wrap

### Phase 6: Admin verifies resolution
SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT:
  - WI_CRITICAL no longer in needs_human_session[] (wrapped)
  - pf_get_work_item(WI_CRITICAL) → status="wrapped"

## PASS criteria
Critical wi stays in needs_human_session[]; Alice skips it; Bob claims and resolves;
debug spec artifact saved; wi wrapped with PR.
