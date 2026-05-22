# SC-08 — pf-work dedup detection and conflict resolution

Tests the 409 DUPLICATE / 409 CANDIDATES dedup flow in pf-work Mode A. When the
user tries to create a wi with a goal similar to an existing wi, pf-work must
detect the conflict, surface the existing wi, and offer three choices:
"Continue new / Claim existing / Cancel". This scenario tests the user selecting
"Claim existing" (Mode B claim).

## Setup
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3
- Project: marketplace
- Pre-existing wi: EXISTING_WI_ID
  - goal: "fix: auth bypass allows unauthenticated API access"
  - wi_type: fix_bug, status: queued (not yet claimed)
  - labels: ["security","auth"]

## Scenario

### Step 1: User creates a wi that duplicates an existing one
SKILL_INVOKE: polyforge:pf-work
USER_INTENT: "fix the auth bypass" (vague — similar to EXISTING_WI_ID goal)

EXPECTED SKILL BEHAVIOR (Mode A — dedup check):
  1. Memory-First:
     pf_recall(project="marketplace", query="auth bypass",
               type=["experience.*","rule.*","fact.*"])

  2. Infer wi_type=fix_bug (security bug, root cause likely known given prior recall)

  3. Dedup check via create:
     pf_create_work_item(
       project="marketplace",
       goal="fix the auth bypass",
       wi_type="fix_bug",
       requires_human_session=false,
       priority="urgent",
       labels=["security"]
     )
     → Server returns 409 CONFLICT_DUPLICATE
       {
         "conflict_type": "CONFLICT_DUPLICATE",
         "existing_wi": {
           "id": "EXISTING_WI_ID",
           "goal": "fix: auth bypass allows unauthenticated API access",
           "status": "queued",
           "wi_type": "fix_bug",
           "priority": "urgent",
           "labels": ["security","auth"]
         }
       }

ASSERT MCP CALLS:
  - pf_recall called before pf_create_work_item (Memory-First)
  - pf_create_work_item called with goal="fix the auth bypass"
  - pf_create_work_item returns 409 CONFLICT_DUPLICATE

ASSERT OUTPUT to user:
  Skill surfaces the conflict and presents three choices:
  ```
  Duplicate detected! An existing wi matches your goal:

  EXISTING_WI_ID | fix_bug | queued | urgent
  "fix: auth bypass allows unauthenticated API access"

  Options:
  A) Continue new — create a separate wi anyway
  B) Claim existing — claim EXISTING_WI_ID and start work (recommended)
  C) Cancel — do nothing
  ```
  No new wi created yet; no claim yet.

---

### Step 1b: 409 CANDIDATES variant (multiple fuzzy matches)
NOTE: If the server returns 409 CANDIDATES instead of 409 DUPLICATE, the behavior
is slightly different — multiple candidate wi's are shown and the user must choose.

  Server response (CANDIDATES variant):
    {
      "conflict_type": "CONFLICT_CANDIDATES",
      "candidates": [
        {"id": "WI_A", "goal": "fix: auth bypass allows unauthenticated API access", "similarity": 0.93},
        {"id": "WI_B", "goal": "fix: missing auth check on /api/admin endpoint",    "similarity": 0.71}
      ]
    }

  EXPECTED SKILL BEHAVIOR:
    Show candidate list ranked by similarity; ask user which (if any) to claim.

  ASSERT OUTPUT (CANDIDATES variant):
    - Both WI_A and WI_B displayed with their similarity scores
    - User prompted: "Which wi matches your intent? (A/B/Create new/Cancel)"

---

### Step 2: User chooses option B — Claim existing
USER_RESPONSE: "B" (or "claim existing" / "claim EXISTING_WI_ID")

EXPECTED SKILL BEHAVIOR (transition to Mode B):
  1. Conflict preview:
     pf_predict_conflicts(wi_id=EXISTING_WI_ID, dry_run=true)
     Show impact: locks to acquire, downstream wi's that will unblock.

  2. Claim existing wi:
     pf_claim_work_item(
       work_item_id=EXISTING_WI_ID,
       idempotency_key=<client ULID>,
       session_info={machine_id: <hostname>},
       requested_locks=[{resource_type: "git_branch",
                         resource_key: "marketplace/polyforge/<slug>"}],
       mode="fresh"
     )
     → returns {attempt_id, claim_epoch, expires_at}

  3. State file written at WORKSPACE_ROOT/.polyforge/state/EXISTING_WI_ID.json

  4. Worktree created at WORKSPACE_ROOT/pf.<shortid>/marketplace/
     Save WT_PATH

  5. Start background lease renewer: pf_renew_lease(attempt_id) every 20 seconds.

  6. Output three-segment format:
     ## 结果
     Claimed existing wi EXISTING_WI_ID.
     fix: auth bypass allows unauthenticated API access

     ## 状态
     | wi      | marketplace#<seq>     |
     | status  | running               |
     | step    | 1/3 prepare_context   |
     | expires | 60min                 |

     ## 下一步
     - Run `/pf-execute` to start work
     - Or `polyforge-coding:prepare_context` to begin the first step

ASSERT MCP CALLS (Mode B claim):
  - pf_predict_conflicts(wi_id=EXISTING_WI_ID, dry_run=true) called
  - pf_claim_work_item(work_item_id=EXISTING_WI_ID, mode="fresh") called
  - pf_create_work_item NOT called again (existing wi claimed, not new one)

ASSERT STATE after claim:
  - EXISTING_WI_ID status="running"
  - State file present at WORKSPACE_ROOT/.polyforge/state/EXISTING_WI_ID.json
  - Worktree present at WT_PATH
  - No duplicate wi created in marketplace project

---

### Step 3: User chooses option A — Continue new (alternative path)
NOTE: This step is an ALTERNATIVE to Step 2. Test either Step 2 OR Step 3, not both.

USER_RESPONSE: "A" (or "create new anyway" / "continue new")

EXPECTED SKILL BEHAVIOR:
  Force-create despite duplicate detection:
  pf_create_work_item(
    project="marketplace",
    goal="fix the auth bypass",
    wi_type="fix_bug",
    requires_human_session=false,
    priority="urgent",
    force_create=true   // or server dedup override header
  )
  → Returns NEW_WI_ID with status="queued"

  Then claim NEW_WI_ID (Mode A standard claim flow — see SC-01 Step 1 for details).

ASSERT MCP CALLS (continue new):
  - pf_create_work_item called with force_create=true (or equivalent)
  - Returns NEW_WI_ID (different from EXISTING_WI_ID)
  - pf_claim_work_item(work_item_id=NEW_WI_ID, mode="fresh") called

---

### Step 4: User chooses option C — Cancel (alternative path)
NOTE: Another alternative to Steps 2 and 3.

USER_RESPONSE: "C" (or "cancel" / "never mind")

EXPECTED SKILL BEHAVIOR:
  No MCP calls. Output:
  ## 结果
  Cancelled. No wi created.

  ## 状态
  No active wi.

  ## 下一步
  - Claim EXISTING_WI_ID with `/pf-work EXISTING_WI_ID`

ASSERT MCP CALLS (cancel):
  - pf_claim_work_item NOT called
  - pf_create_work_item NOT called again
  - No state file created

## PASS criteria (Mode B path — Step 2):
pf_create_work_item returns 409 CONFLICT_DUPLICATE; skill surfaces existing wi and
prompts user with three choices; user picks B; pf_predict_conflicts called before
claim; pf_claim_work_item(work_item_id=EXISTING_WI_ID, mode="fresh") called; new
duplicate wi NOT created; state file for EXISTING_WI_ID written.
