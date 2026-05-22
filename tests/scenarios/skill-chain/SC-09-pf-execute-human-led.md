# SC-09 — pf-execute for requires_human_session=true wi (Session 2/3)

Tests pf-execute when the wi is a feature wi (requires_human_session=true). Unlike
auto wi's (SC-05), pf-execute runs spec and plan INLINE in the human session — Alice
participates directly in the discussion. Only code steps (code_change, commit_and_pr)
are dispatched as subagents. The human reviews the PR before wrap.

NOTE: requires_human_session=true wi's are NOT auto-claimed by Session 1 agents.
The human claims them explicitly (pf-work Mode B) and then drives execution in
Session 2/3 via pf-execute.

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace
- WI_ID: feature wi, already claimed by human in this session
  - wi goal: "feat: add rate limiting per API key to prevent abuse"
  - wi_type: feature, requires_human_session=true
  - Steps per phase.yaml: ["spec", "plan", "code_change", "commit_and_pr", "review"]
    (5 steps: start_step=spec, then plan, code_change, commit_and_pr, review)
  - Status: running
  - State file at WORKSPACE_ROOT/.polyforge/state/WI_ID.json
  - Worktree at WT_PATH = WORKSPACE_ROOT/pf.<shortid>/marketplace/
  - ATTEMPT_ID, CLAIM_EPOCH from state file

## Scenario

### Step 1: Human invokes pf-execute (Session 2/3 entry)
SKILL_INVOKE: polyforge:pf-execute
USER_INTENT: "execute" / "implement this feature"

EXPECTED SKILL BEHAVIOR — Setup phase:
  1. Load wi info:
     pf_list_work_items(ids=[WI_ID], include_step_state=true)
     → requires_human_session=true, current_step="start_step" (spec), phase_mode="step"

  2. Memory-First:
     pf_recall(project="marketplace", query=wi.goal,
               type=["experience.*","rule.*","methodology.spec"], top_k=5)
     pf_activate_memory(id) for each relevant result

ASSERT MCP CALLS (setup):
  - pf_list_work_items called with ids=[WI_ID], include_step_state=true
  - requires_human_session=true detected
  - pf_recall called before any pf_update_step (Memory-First)

---

### Step 2: pf-execute runs spec INLINE (not dispatched as subagent)
EXPECTED SKILL BEHAVIOR (from pf-execute SKILL.md):
  Because wi.requires_human_session=true AND start_step.action="pf-spec":
  → INLINE execution (do NOT dispatch subagent; Alice is the Wi Agent here)

  Inline spec execution:
    a. pf_get_step(work_item_id=WI_ID) → current version V1
    b. pf_update_step(work_item_id=WI_ID, step_id="start_step",
                      status="in_progress", expected_version=V1)
       → returns step_attempt_id=SA_SPEC
    c. Heartbeat during discussion:
       pf_update_step(work_item_id=WI_ID, step_id="start_step", heartbeat=true)
       [Called every 5 minutes while human is writing the spec with Alice]
    d. Guide human through spec: What/Why, Non-goals, Acceptance criteria,
       Rate limiting strategy (token bucket vs fixed window), storage backend (Redis).
    e. pf_save_artifact(type="methodology.spec", work_item_id=WI_ID,
                         content=<markdown spec>,
                         structured_payload={
                           decisions: ["token bucket algo", "Redis backend", "per-key limits"],
                           acceptance_criteria: [
                             "429 returned when limit exceeded",
                             "Rate limit headers in response",
                             "Config per API key tier"
                           ]
                         },
                         visibility="project")
       → returns mem_SPEC_ID
    f. pf_emit_event(work_item_id=WI_ID, event_type="note",
                      payload={text: "spec saved: mem_SPEC_ID"})
    g. pf_update_step(work_item_id=WI_ID, step_id="start_step", status="completed",
                      step_attempt_id=SA_SPEC,
                      artifact_summary="spec saved: mem_SPEC_ID — rate limiting via token bucket + Redis")

ASSERT MCP CALLS (spec inline):
  - pf_update_step(step_id="start_step", status="in_progress") called
  - pf_update_step(heartbeat=true) called at least once (if discussion takes >5min)
  - pf_save_artifact called with type="methodology.spec" (spec DOES save artifact)
  - pf_emit_event called with note
  - pf_update_step(step_id="start_step", status="completed") called with artifact_summary

ASSERT STATE after spec:
  - pf_get_step(WI_ID) → current_step="plan"
  - Artifact type="methodology.spec" retrievable via pf_recall

---

### Step 3: pf-execute runs plan INLINE
EXPECTED SKILL BEHAVIOR:
  Because wi.requires_human_session=true AND next step action="pf-plan":
  → INLINE execution (not dispatched as subagent)

  Inline plan execution:
    a. pf_recall(project="marketplace", query=wi.goal,
                  type=["methodology.plan","experience.*"], top_k=3)
    b. pf_recall(work_item_id=WI_ID, type="methodology.spec", top_k=1) — read spec
    c. pf_get_step(work_item_id=WI_ID) → version V2
    d. pf_update_step(work_item_id=WI_ID, step_id="plan",
                      status="in_progress", expected_version=V2)
       → returns step_attempt_id=SA_PLAN
    e. Break into implementation steps with human input
    f. pf_save_artifact(type="methodology.plan", work_item_id=WI_ID,
                         content=<markdown plan>,
                         structured_payload={
                           steps: [
                             {id: "1", title: "Implement token bucket in Redis", effort_hint: "4h"},
                             {id: "2", title: "Middleware: extract API key + check rate", effort_hint: "3h"},
                             {id: "3", title: "429 response + Retry-After header", effort_hint: "1h"},
                             {id: "4", title: "Config per API key tier", effort_hint: "2h"}
                           ]
                         },
                         visibility="project")
       → returns mem_PLAN_ID
    g. pf_update_step(work_item_id=WI_ID, step_id="plan", status="completed",
                      step_attempt_id=SA_PLAN,
                      artifact_summary="plan: 4 impl steps, ~10h, spec mem_SPEC_ID")

ASSERT MCP CALLS (plan inline):
  - pf_recall called before pf_update_step (Memory-First)
  - pf_save_artifact called with type="methodology.plan"
  - pf_update_step(step_id="plan", status="completed") called with artifact_summary
  - Artifact retrievable via pf_recall(type="methodology.plan", work_item_id=WI_ID)

ASSERT STATE after plan:
  - pf_get_step(WI_ID) → current_step="code_change"

---

### Step 4: pf-execute dispatches code_change as subagent
EXPECTED SKILL BEHAVIOR:
  Because code_change is NOT a pf-spec/pf-plan action (it is a coding step):
  → DISPATCH subagent (same as auto wi flow)

  pf_recall(project="marketplace", query="code_change rate limiting",
             type=["experience.*","rule.*"], top_k=3)

  Dispatch subagent with:
    action: code_change
    skill: polyforge-coding:code_change
    previous_context: spec (mem_SPEC_ID), plan (mem_PLAN_ID),
                      start_step.artifact_summary

  Subagent executes (per code_change skill):
    a. pf_get_step(work_item_id=WI_ID) → version V3
    b. pf_update_step(work_item_id=WI_ID, step_id="code_change",
                      status="in_progress", expected_version=V3)
       → returns step_attempt_id=SA_CODE
    c. Read spec and plan from previous_steps and recall
    d. Implement: token bucket in Redis, rate-limit middleware, 429 handler
    e. pf_update_step(work_item_id=WI_ID, step_id="code_change",
                      status="completed", step_attempt_id=SA_CODE,
                      artifact_summary=JSON({summary, files_changed, tests_status}))

ASSERT MCP CALLS (code_change subagent):
  - pf_recall called by Wi Agent before dispatching subagent (Memory-First)
  - Subagent dispatched (not inline execution)
  - pf_update_step(step_id="code_change", in_progress then completed)
  - File edits present in WT_PATH

ASSERT STATE after code_change:
  - pf_get_step(WI_ID) → current_step="commit_and_pr"

---

### Step 5: pf-execute dispatches commit_and_pr as subagent
EXPECTED SKILL BEHAVIOR (dispatch — same as auto wi):
  pf_recall(project="marketplace", query="commit_and_pr rate limiting",
             type=["experience.*"], top_k=3)

  Dispatch subagent with:
    action: commit_and_pr
    skill: polyforge-coding:commit_and_pr

  Subagent executes:
    a. pf_update_step(step_id="ship", status="in_progress", ...)
    b. pf_diff(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
               repo="marketplace", vs_base=true)
    c. pf_commit(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
                 repo="marketplace",
                 message="feat(api): add per-key rate limiting via token bucket\n\n...\n\nwi: marketplace#<seq>")
    d. pf_push(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
               repo="marketplace")
    e. pf_pr(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
             repo="marketplace",
             title="feat(api): add per-key rate limiting via token bucket",
             body="...")
       → returns PR_URL
    f. pf_update_step(step_id="ship", status="completed",
                      artifact_summary="PR #N: PR_URL")

ASSERT MCP CALLS (commit_and_pr subagent):
  - pf_commit called with conventional commit message
  - pf_pr called → PR_URL in response
  - pf_get_step(WI_ID) → current_step="review" (5th and final step)

---

### Step 6: pf-execute handles review step INLINE (human interactive)
NOTE: "review" is the 5th step of feature wi type. Because requires_human_session=true
AND it is a human-interactive review step, pf-execute handles it INLINE (not dispatched).

EXPECTED SKILL BEHAVIOR:
  pf_get_step(work_item_id=WI_ID) → current_step="review"

  Inline review execution:
    a. pf_update_step(work_item_id=WI_ID, step_id="review",
                      status="in_progress", expected_version=<version>)
       → step_attempt_id=SA_REVIEW
    b. Present PR diff + acceptance criteria from spec to human:
       - "429 returned when limit exceeded" — check
       - "Rate limit headers in response" — check
       - "Config per API key tier" — check
    c. Human reviews PR_URL and confirms acceptance criteria met.
    d. pf_update_step(work_item_id=WI_ID, step_id="review", status="completed",
                      step_attempt_id=SA_REVIEW,
                      artifact_summary="review: all 3 acceptance criteria met; PR approved")

ASSERT MCP CALLS (review inline):
  - pf_update_step(step_id="review", in_progress then completed)
  - Review done interactively (not dispatched as subagent)

ASSERT STATE after review:
  - pf_get_step(WI_ID) → current_step=null (all 5 steps done — loop exits)

---

### Step 7: pf-execute dispatches automatic retro then calls pf_wrap
EXPECTED SKILL BEHAVIOR (same as SC-05 Steps 5-6):

  Retro subagent:
    pf_read_events(work_item_id=WI_ID, limit=50,
                   types=["commit","push","pr_opened","step_completed"])
    pf_recall(query=wi.goal, type="experience.*", top_k=3)
    pf_save_artifact(type="methodology.wrap_summary", work_item_id=WI_ID,
                     content="<1-para summary>")
    pf_remember(body=<learning>, type="experience.feature", visibility="team")

  Wrap (coding scenario):
    pf_wrap(workspace_root=WORKSPACE_ROOT, work_item_id=WI_ID,
            attempt_id=ATTEMPT_ID, claim_epoch=CLAIM_EPOCH,
            session_secret=<from state file>)

ASSERT MCP CALLS (retro + wrap):
  - pf_read_events called
  - pf_save_artifact(type="methodology.wrap_summary") called
  - pf_wrap called (NOT pf_complete_attempt)

ASSERT FINAL STATE:
  - WI_ID status="wrapped"
  - State file deleted
  - PR PR_URL is open for human merge

---

### Summary: inline vs dispatch decision table

| Step           | Action      | requires_human_session=true? | Execution |
|----------------|-------------|------------------------------|-----------|
| start_step     | pf-spec     | yes                          | INLINE    |
| plan           | pf-plan     | yes                          | INLINE    |
| code_change    | code_change | yes (coding step)            | SUBAGENT  |
| commit_and_pr  | ship        | yes (coding step)            | SUBAGENT  |
| review         | review      | yes (human interactive)      | INLINE    |

NOTE: For requires_human_session=false wi's (SC-05), ALL steps are dispatched as
subagents — even spec and plan if they exist in the phase.yaml step graph.

## PASS criteria
pf-execute detects requires_human_session=true; spec and plan executed inline
(not dispatched as subagents); heartbeat called during long inline discussions;
code_change and commit_and_pr dispatched as subagents; review step executed inline;
all 5 steps complete in sequence; automatic retro dispatched; pf_wrap called
(not pf_complete_attempt); state file deleted after wrap.
