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

NOTE: critical_bug steps per phase.yaml: ["prepare_context", "spec", "code_change", "commit_and_pr"]
     (4 steps: prepare_context, spec, code_change, commit_and_pr)

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
      Alice's orchestrator loop checks items[] only — WI_CRITICAL is invisible to her.
ASSERT: Alice does NOT attempt to call pf_claim_work_item for WI_CRITICAL

### Phase 3: Bob opens human-led session, claims WI_CRITICAL
SKILL_INVOKE (as BOB): polyforge:pf-work WI_CRITICAL
ASSERT:
  - pf_claim called successfully (human session, Bob's key)
  - WI_CRITICAL status=running
  - Worktree created for Bob at WORKSPACE_ROOT/pf.<shortid>/marketplace/
  - Save WT_PATH

### Phase 4: Bob runs prepare_context (step 1 of critical_bug)
SKILL_INVOKE (as BOB): polyforge-coding:prepare_context

NOTE: critical_bug step order is: prepare_context FIRST (start_step), THEN spec.
prepare_context must be completed before pf-spec is invoked.

EXPECTED SKILL BEHAVIOR:
  1. pf_list_work_items(ids=[WI_CRITICAL], include_step_state=true)
  2. pf_recall(project="marketplace", query=wi.goal, type=["experience.*","rule.*"])
  3. pf_get_step(work_item_id=WI_CRITICAL) — get version
  4. pf_update_step(work_item_id=WI_CRITICAL, step_id="prepare_context", status="in_progress", expected_version=<version>)
  5. Analyze codebase in WT_PATH — find the admin middleware vulnerability
  6. pf_update_step(work_item_id=WI_CRITICAL, step_id="prepare_context", status="completed",
       step_attempt_id=<from 4>,
       artifact_summary=<initial_context JSON: {goal_analysis, relevant_files, suggested_approach}>)

ASSERT:
  - pf_update_step(prepare_context, completed) called
  - pf_get_step(WI_CRITICAL) → current_step="spec"

### Phase 5: Bob runs debug spec (step 2 of critical_bug)
SKILL_INVOKE (as BOB): polyforge:pf-spec
USER_INTENT: "debug variant: analyze why auth bypass is possible"

EXPECTED SKILL BEHAVIOR:
  1. pf_recall(project="marketplace", query=wi.goal, type="methodology.spec|fact.*|rule.*", top_k=3)
  2. pf_get_step(work_item_id=WI_CRITICAL) — get current step and version
  3. pf_update_step(work_item_id=WI_CRITICAL, step_id="spec", status="in_progress", expected_version=<version>)
     → returns step_attempt_id
  4. Heartbeat during analysis: pf_update_step(heartbeat=true) if taking >5min
  5. Root cause analysis format:
     - Symptoms, reproduction steps, impact, proposed fix
  6. pf_save_artifact(type="methodology.spec", work_item_id=WI_CRITICAL,
       content="## Root Cause Analysis\n...\n## Proposed Fix\nAdd jwt.Verify() before /admin/* route handler\n...",
       structured_payload={acceptance_criteria:[...], non_goals:[...]},
       visibility="project")
  7. pf_emit_event(work_item_id=WI_CRITICAL, event_type="note", payload={text: "spec saved: mem_XXX"})
  8. pf_update_step(work_item_id=WI_CRITICAL, step_id="spec", status="completed",
       step_attempt_id=<from 3>,
       artifact_summary="root cause: missing JWT validation in admin middleware")

ASSERT:
  - pf_save_artifact called with type="methodology.spec"
  - spec content contains Root Cause Analysis section (debug-format)
  - pf_update_step(spec, completed) called with artifact_summary
  - pf_get_step(WI_CRITICAL) → current_step="code_change"

### Phase 6: Bob fixes and ships
SKILL_INVOKE (as BOB): polyforge-coding:code_change
EXPECTED:
  - Reads initial_context from prepare_context step + spec artifact
  - Edits WT_PATH file (adds JWT validation to /admin/* middleware)
  - pf_update_step(code_change, completed, artifact_summary=JSON({files_changed, tests_status}))
  - pf_save_artifact NOT called (code_change writes to step artifact_summary only)

SKILL_INVOKE (as BOB): polyforge-coding:commit_and_pr
EXPECTED:
  - pf_diff(workspace_root=WORKSPACE_ROOT, work_item_id=WI_CRITICAL, repo="marketplace", vs_base=true)
  - pf_commit(workspace_root=WORKSPACE_ROOT, work_item_id=WI_CRITICAL, repo="marketplace",
      message="fix(auth): add JWT validation to admin middleware\n\n...\n\nwi: marketplace#<seq>")
  - pf_push(workspace_root=WORKSPACE_ROOT, work_item_id=WI_CRITICAL, repo="marketplace")
  - pf_pr(workspace_root=WORKSPACE_ROOT, work_item_id=WI_CRITICAL, repo="marketplace",
      title="fix(auth): add JWT validation to admin middleware", body="...")
  - pf_update_step(commit_and_pr, completed, artifact_summary="PR #N: <url>")

SKILL_INVOKE (as BOB): polyforge:pf-stop --wrap

EXPECTED SKILL BEHAVIOR (coding scenario — pf_wrap, not pf_complete_attempt):
  - pf_wrap(workspace_root=WORKSPACE_ROOT, work_item_id=WI_CRITICAL, repo="marketplace")
  - pf_emit_event(event_type="note", payload={text: "wrapped: auth bypass fixed, PR opened"})

ASSERT:
  - pf_wrap called (NOT pf_complete_attempt directly)
  - WI_CRITICAL status=="wrapped"

### Phase 7: Admin verifies resolution
SKILL_INVOKE (as ADMIN): polyforge:pf-status
ASSERT:
  - WI_CRITICAL no longer in needs_human_session[] (wrapped)
  - pf_get_work_item(WI_CRITICAL) → status="wrapped"

## PASS criteria
Critical wi stays in needs_human_session[]; Alice skips it; Bob claims;
prepare_context runs FIRST (step 1), then pf-spec (step 2) — correct critical_bug order;
debug spec artifact saved; pf_wrap used for coding scenario; wi wrapped with PR.
