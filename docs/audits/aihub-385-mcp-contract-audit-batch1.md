# aihub#385 — MCP contract audit, batch 1: six high-traffic tools

Read-only audit of the MCP tool contract for `pf_get_work_item`, `pf_get_step`,
`pf_update_step`, `pf_claim_work_item`, `pf_get_ready_queue` and
`pf_create_work_item`, every published parameter across all four hops to developer
grade, using the method proven on `pf_list_work_items` in aihub#380. No production code
was changed and nothing found here was fixed: several findings are positive controls
planted by the owner (aihub#387 / #388 / #389), and correcting a control mid-audit
destroys it. Findings are filed as work items (§13).

- **Audited commit:** `81c2333` (origin/main at audit start; re-fetched when this report
  was written and still origin/main). Every citation below is against that commit
  unless a parent-tree sha is named, and names the file plus the declaration (for a
  document: the heading or table row) enclosing the cited text, with a quoted code
  literal where that declaration is large.
- **Labels.** *Measured* = a command or a file/declaration citation anyone can re-run or re-read.
  *Unverified* = inferred; the command that would settle it is named each time (§11).
- **Live probes** ran on 2026-09-07 against the aihub server configured in
  `~/.polyforge/config.toml`, through the MCP tool and through direct `curl` GETs.
  Read-only: no probe wrote state.
- **Concurrency.** aihub#390 (attempt `ra_j62rh3yN`) was running on
  `internal/server/routes_step.go` / `internal/mcp/tools_step.go` while this audit ran.
  Everything here is read from `81c2333`, not from #390's branch; where #390 covers a
  finding it is named rather than re-filed.
- **Method.** Each tool was audited by a separate clean-context subagent in two passes:
  first **blind** on the archived parent tree of each ALREADY_FIXED control (the fix
  commit's parent sha, extracted with `git archive`; the agent was not told what the
  control was), then on HEAD. I then re-read every `file:line` the agents cited on
  HEAD and applied the decision rule myself; where my verdict differs from an agent's,
  §8 lists the override and the reason.

---

## 0. Decision rule and reporting rules

**Decision rule (from aihub#380, applied unchanged).** *LOGIC BREAK* iff the set hop 1
promises for an input the description **admits** ≠ the set hop 4 returns (or does) for
it. A loud rejection (4xx naming the parameter) is not a set mismatch. Silent behaviour
on inputs the description does *not* admit is recorded separately as a *policy
deviation*. A **500 on an admitted input is a break** (the caller gets neither the
promised result nor a usable reason).

**Hops.** hop 0 = harness/SDK; hop 1 = the InputSchema text (the only thing an LLM
caller sees); hop 2 = what the MCP handler forwards and in what shape; hop 3 = which
server field/struct binds it; hop 4 = what the domain does / returns; hop 5 = domain
response → MCP projection.

**Positive-control rules (aihub#385 `usage_rules`, applied literally).**
1. An ALREADY_FIXED control is run on the fix commit's **parent** tree, never on HEAD.
   Not detected there ⇒ the detector is blind for that tool and no count is reported.
2. Discriminative power is established **per tool**; controls are never OR-ed across
   tools.
3. A weak control (5c, 3c) cannot be a tool's only control.
4. `pf_get_work_item` has **zero** controls; its "no defects" is labelled
   *no positive control, conclusion strength downgraded* and its numbers are not merged
   with the other five.
5. aihub#389 (`objectSchema` omits `additionalProperties:false`) is a hop-0 property of
   every tool, not a per-tool control (§6).

**No combined total is reported anywhere in this document.**

---

## 1. Positive controls — per tool, reported before any count

Fix → parent mapping (Measured, `git rev-parse <fix>^`): `7cb982fc^ = 58e67b3b`,
`b2a1adec^ = e5c42760`, `225a8193^ = 43517dc9`, `10c8ecad^ = 5198f37b`,
`0583d5cf^ = 103c978e`, `6fc2d006^ = cbbcb48a`, `7c49bba5^ = 25d009ab`.

| tool | control | tree | detected blind | evidence (Measured) |
|---|---|---|---|---|
| pf_get_ready_queue | **5a** `non_conflicting` published, never read at hop 3 (STILL_PRESENT, = aihub#387) | HEAD | **yes** | hop 2 forwards it (the `pf_get_ready_queue` handler in `registerLifecycleTools`, `internal/mcp/tools_lifecycle.go`); `handleGetReadyQueue` (`internal/server/router.go`) never calls `QueryParam("non_conflicting")`; live: `non_conflicting=true` response byte-identical to the omitted form (§10 probe log) |
| pf_get_ready_queue | **5b** `max` sent as a JSON number dropped by `strArg` | parent `58e67b3b` | **yes** | `strArg` (`internal/mcp/helpers.go` of that tree), called as `setIfNonempty(params, "max", strArg(args, "max"))` in the `pf_get_ready_queue` handler in `tools_lifecycle.go`, returns `""` for a non-string, so `max` was never forwarded |
| pf_get_ready_queue | **5c** (weak) `max>200` silently clamped | HEAD | **yes** | the `max > 200` clamp in `GetReadyQueue` (`internal/domain/work_items.go`); not the only control for this tool (rule 3) |
| pf_update_step | **3a** `expected_version` published, unbound at hop 3 | parent `e5c42760` | **yes** | `UpdateStepRequest` (`internal/server/routes_step.go` of that tree) has no `expected_version` tag; echo `c.Bind` drops unbound keys silently |
| pf_update_step | **3b** `artifact_summary` / `error_type` / `escalated` published, unbound | parent `43517dc9` | **yes** | the same struct (`UpdateStepRequest`, `routes_step.go` of that tree) lacks all three tags |
| pf_update_step | **3c** (weak) `escalated` with `status=completed` neither persisted nor rejected | HEAD | **yes** | only the failed-path `INSERT INTO wi_step_completions` and the escalation block `if req.Escalated && u != nil`, both in `handleUpdateStep` (`internal/server/routes_step.go`), read it; not the only control (rule 3) |
| pf_claim_work_item | **4a** `intent:"read"` still takes a lock | parent `5198f37b` | **yes** (by both the claim agent and the create agent, independently) | lock derivation in that tree was `resourceToLock` (`internal/domain/conflicts.go` of `5198f37b`), which reads no `Intent` |
| pf_claim_work_item | **4b** `requested_locks` published with no item schema → `500` SQLSTATE 23514 | parent `103c978e` | **yes** | the `"requested_locks"` entry of the `pf_claim_work_item` schema plus `prop()` itself, both in `tools_lifecycle.go` of that tree; CHECK violation routed through `dbErrCause` (`internal/domain/pgx_err.go`) to `ErrInternalError` |
| pf_claim_work_item | **4c** MCP keep-list drops `requires_human_session` / `wi_type` / `step_recovery_hint` (STILL_PRESENT, = aihub#388) | HEAD | **yes** | keep-list `safeResult` in the `pf_claim_work_item` handler (`registerLifecycleTools`, `tools_lifecycle.go`); the fields exist on `ClaimResponse` (`internal/domain/run_attempts.go`; fields `StepRecoveryHint`, `RequiresHumanSession`, `WIType`) |
| pf_get_step | **2a** Description promised "step graph, progress, previous steps" | parent `cbbcb48a` | **yes** | the tree's `tools_step.go` Description vs the handler returning `StepState` only |
| pf_get_step | **2b** slug used verbatim in the step-state query | parent `25d009ab` | **yes** | `handleGetStep` (`routes_step.go` of that tree) queries `wi_step_state` with the unresolved path segment |
| pf_get_step (effect) / pf_update_step (cause) | **aihub#390 class** STILL_PRESENT on HEAD | HEAD + live | **yes**, mechanism settled (§4.11) | `wi_step_completions` INSERT inside `SAVEPOINT bp`, failure swallowed (`handleUpdateStep`, `routes_step.go`); live `wi_Yu24OkD4` prepare_context `artifact_summary` = 4243 chars > CHECK 4096 (`CREATE TABLE wi_step_completions`, `internal/db/migrations/0005_step_state.sql`) |
| pf_create_work_item | **6a** `declared_resources` published as a bare array (no item shape) | parent `103c978e` | **yes** | `"declared_resources": prop("array", "Declared resource locks")` in `tools_lifecycle.go` of that tree; stored verbatim, no lock derived, no warning |
| pf_create_work_item | **6b** = 4a (shared `intent` semantics) | parent `5198f37b` | **yes** | as 4a |
| pf_get_work_item | **none** | — | — | **no positive control — conclusion strength downgraded** (rule 4) |

Every control present on a tool's parent tree or on HEAD was detected blind. Per rule 2
this establishes discriminative power for five tools individually; it establishes
nothing for `pf_get_work_item`.

---

## 2. `pf_get_work_item` — 2 params · **no positive control**

**Hops.** hop 1 the `pf_get_work_item` schema in `registerLifecycleTools` (`internal/mcp/tools_lifecycle.go`); hop 2 its handler closure
→ `Client.GetWorkItem` (`pkg/client/client.go`, `GET /v1/work_items/{id}`); hop 3
the `/work_items/:id` route in `NewRouter` (`internal/server/router.go`) → `handleGetWorkItem`; hop 4
`GetWorkItem` (`internal/domain/work_items.go`).

### 2.1 `work_item_id`
- **h1** (`pf_get_work_item` schema): `"Work item ID or slug"` → set: the one work item whose id **or**
  slug equals the value, within visible projects.
- **h2**: forwarded verbatim as the path segment (`Client.GetWorkItem`).
- **h3**: `handleGetWorkItem` passes the segment to the domain unchanged (`handleGetWorkItem`, `internal/server/router.go`).
- **h4** (`GetWorkItem`, `work_items.go`): `if strings.HasPrefix(idOrSlug, "wi_") { WHERE id = $1 } else { WHERE slug = $1 }` — a single-column dispatch on the prefix. Every sibling resolver uses the union: `id = $1 OR slug = $1` (`resolveBlockedByRef`, `ResolveVisibleWorkItemRef`), `wi.id = ANY OR wi.slug = ANY` (`buildListWorkItemsWhere`). The doc comment on `resolveBlockedByRef` claims the OR form for this function too (stale). `FormatIDOrSlug` (`internal/domain/ids.go` of that tree; since deleted, `aihub#402`) repeats the prefix rule. The project-name regex `^[a-z][a-z0-9_-]{0,39}$` (`projectNameRe`, `internal/domain/projects.go`) does **not** reserve the `wi_` prefix, so a project `wi_lab` would yield slugs `wi_lab#1` that this function looks up in the id column and never finds.
- **Compare**: equal for every value that exists today — live `pf_list_projects` shows 10 visible projects, none `wi_`-prefixed (Measured). Unequal for an admitted input (a slug of a `wi_`-prefixed project) that the schema and the project regex both allow.
- **Verdict: LOGIC BREAK (latent — no reachable instance with today's data).** Note, not a break: `NewRouter` (`router.go`) registers `GET /work_items/ready` ahead of `/work_items/:id`, so the literal `"ready"` is answered by the ready-queue handler (live: `aihub 400 BAD_REQUEST: project query parameter is required`); loud, and no slug can be `ready` (slugs are `<project>#<seq>`).

### 2.2 `brief`
- **h1** (`pf_get_work_item` schema): boolean; when true the response omits `content`.
- **h2**: **local** — never forwarded; the handler deletes `content` from the decoded result (the `boolArg(args, "brief")` branch of the handler).
- **h3 / h4**: none (no server involvement).
- **Compare**: equal.
- **Verdict: consistent.**

### 2.3 hop 5
The result is handed back unprojected except for the `brief` deletion. No domain field
is stripped. **0 response breaks.**

### 2.4 Summary for `pf_get_work_item`

| result | count | params |
|---|---|---|
| consistent | 1 | brief |
| LOGIC BREAK | 1 (latent) | work_item_id — `wi_`-prefix dispatch vs "ID or slug" |
| response breaks | 0 | |

**No positive control, conclusion strength downgraded.** These numbers are not comparable
with the five tools below and are not added to them. Filed: aihub#402.

---

## 3. `pf_get_step` — 1 param, 18 response promises

**Hops.** hop 1 the `pf_get_step` schema in `registerStepTools` (`internal/mcp/tools_step.go`); hop 2 `GET` with the work-item
path segment; hop 3 the step-state handler in `internal/server/routes_step.go`
(`StepState` struct, `completedStepsLimit = 200`,
`completedStepsQuery`); hop 4 `wi_step_state` + `wi_step_completions`.

### 3.1 `work_item_id`
- **h1**: id or slug → the step state of that work item.
- **h2**: verbatim path segment. **h3**: resolved id-or-slug before the state query
  (control 2b fixed at `7c49bba5`). **h4**: `wi_step_state WHERE work_item_id = $1`.
- **Compare**: equal. **Verdict: consistent.**

### 3.2 Response promises (the `pf_get_step` Description vs `StepState` and the queries)
18 decidable promises were enumerated (field presence, ordering, limit, semantics of
`completed_steps`, and the three sentences of advice). Four fail:

| # | hop 1 promise | hop 4 / struct | verdict |
|---|---|---|---|
| 2 | "Returns current_step / current_step_status / version" | `CurrentStep` is `json:"current_step,omitempty"` (`StepState.CurrentStep`); the contract test allowlists its absence (`getStepAdvertisedMayBeAbsent`, `internal/mcp/tools_step_contract_test.go`) | **break** — absent vs null is exactly the distinction the same Description insists on for `completed_steps` |
| 8 | each `completed_steps[].step_id` is the step the agent completed | the row's `step_id` is `derefStr(currentStep)` read before the update (`handleUpdateStep`, `routes_step.go`: the `SELECT current_step FROM wi_step_state` read, passed as `derefStr(currentStep)` to the completion `INSERT`), not the request's `step_id` (§4.3) | **break** (cause is in `pf_update_step`; effect visible here) |
| 11 | "treat every step_id in completed_steps as done" | `completedStepsQuery` has no status filter; `fnForceTerminateStep` writes rows with `status='failed', error_type='force_terminate'` (`internal/domain/run_attempts.go`) | **break** — the data carries `status`; the sentence says to ignore it |
| 17 | "pf_recall / pf_read_events … return nothing for a slug" | both resolve a slug now: `handleListEvents` (`internal/server/routes_memory.go`), `Recall` (`internal/domain/memory.go`) | **break** (stale advice, harmless direction) |

Also Measured: the `pf_get_step` row of `docs/mcp-tools.md` still says "Current step graph, status, progress,
and previous steps" — the promise aihub#265 removed from the schema (control 2a's
sentence survives in the docs row). `pf_steps` occurs only in comments; there is no
live reader (grep).

**#390 class (STILL_PRESENT on HEAD).** `completed_steps` omits any step whose
completion INSERT failed inside the swallowed savepoint; mechanism and live instance in
§4.11. Counted as a `pf_update_step` break (that is where the row is lost); listed here
because this is the tool through which a resuming agent is told the step never ran.

### 3.3 Summary for `pf_get_step`

| result | count |
|---|---|
| params consistent | 1 (work_item_id) |
| params LOGIC BREAK | 0 |
| response promises checked / broken | 18 / **4** (#2, #8, #11, #17) |
| controls | 2a, 2b detected blind on their parent trees; #390 class detected on HEAD |

Filed: aihub#400 (the four sentences + docs row).

---

## 4. `pf_update_step` — 10 params

**Hops.** hop 1 the `pf_update_step` schema in `registerStepTools` (`internal/mcp/tools_step.go`); hop 2 `updateStepBody`
(heartbeat branch `if boolArg(args, "heartbeat")` in the handler); hop 3 `handleUpdateStep` in
`internal/server/routes_step.go` (`UpdateStepRequest`; heartbeat `if req.Heartbeat`;
`currentStep` read `SELECT current_step FROM wi_step_state`; completed `UPDATE` (`current_step = $2, current_step_status = 'idle'`); completion `INSERT`
gate `if req.StepAttemptID != nil`, `SAVEPOINT bp` swallow; event `"step_completed"`; failed `UPDATE`
(`SET current_step_status = 'idle', current_step_attempt = NULL`), `INSERT` (`'failed', $6, $7, $8`); escalation `if req.Escalated && u != nil`; `startStep` with
the only idle predicate `WHERE wi_step_state.current_step_status = 'idle'`; `handleRenewLease` 410); hop 4 the
`wi_step_state` / `wi_step_completions` tables (`CREATE TABLE wi_step_completions` in `internal/db/migrations/0005_step_state.sql`:
`status CHECK IN ('completed','failed')`, `error_type TEXT`,
`CHECK (length(artifact_summary) <= 4096)`; and, as its own statement in that file, `CREATE UNIQUE INDEX idx_wsc_attempt`).

**Description-level promise (`pf_update_step` Description, verbatim):** `"There is no version/CAS argument:
concurrency is guarded server-side by the idle-step predicate, so no pf_get_step is
needed before this call."` The only predicate on `current_step_status` is in
`startStep` (`WHERE wi_step_state.current_step_status = 'idle'`), i.e. it guards the
**in_progress** transition. The `completed` and `failed` `UPDATE`s (the `case "completed"` and
`case "failed"` statements of `handleUpdateStep`) are `WHERE work_item_id = $1` with no status predicate and no
`current_step = $step` predicate.

### 4.1 `work_item_id`
- h1 id or slug → this work item. h2 path segment. h3 resolved. h4 all statements keyed on the resolved id.
- **Verdict: consistent.**

### 4.2 `status`
- **h1**: `in_progress | completed | failed` (heartbeat is a separate flag) → the step moves to that state, guarded per the Description.
- **h2**: forwarded verbatim (`updateStepBody`); dropped in the heartbeat branch (`if boolArg(args, "heartbeat")`).
- **h3**: switch in `handleUpdateStep`; `in_progress` → `startStep` (predicate `WHERE wi_step_state.current_step_status = 'idle'`); `completed` → the `UPDATE wi_step_state` of `case "completed"`; `failed` → that of `case "failed"`.
- **h4**: `in_progress` refused unless idle (loud); `completed` / `failed` succeed from **any** state, including idle and including a step currently in progress under another step attempt.
- **Compare**: h1 (guarded transitions) ≠ h4 for two of the three admitted values.
- **Verdict: LOGIC BREAK.**

### 4.3 `step_id`
- **h1**: the step being reported on.
- **h2**: forwarded (`updateStepBody`); dropped for heartbeat.
- **h3**: `completed` path writes the completion row with `step_id = derefStr(currentStep)`, the value read by `SELECT current_step FROM wi_step_state` **before** the update; then sets `current_step` to the request's value. `failed` path analogous (its own `INSERT INTO wi_step_completions`).
- **h4**: a completion row for whatever step the server had stored, then `current_step := req.Step`.
- **Compare**: for `step_id == stored current_step` equal; for `step_id != stored current_step` (admitted — nothing in h1 says they must match) the history records the wrong step and the state jumps to the caller's.
- **Verdict: LOGIC BREAK.**

### 4.4 `step_attempt_id`
- **h1** (verbatim): `"Step attempt ID of the step being completed/failed (required for completed/failed)"`.
- **h2**: forwarded when present.
- **h3**: the completion `INSERT` is gated `if req.StepAttemptID != nil` (`case "completed"` in `handleUpdateStep`); with nil the state `UPDATE` still runs and the call answers 200; no 400.
- **h4**: no history row, no error.
- **Compare**: "required" promises a rejection when absent; implemented = silent success without the row.
- **Verdict: LOGIC BREAK.** (aihub#403, filed by #390's agent, covers this half; aihub#399 covers the class incl. §4.11.)

### 4.5 `artifact_summary`
- **h1**: free-text summary recorded with the completion (no limit published).
- **h2**: forwarded verbatim. **h3**: bound; written by the `INSERT INTO wi_step_completions` of `case "completed"` / `case "failed"` in `handleUpdateStep`.
- **h4**: `CHECK (length(artifact_summary) <= 4096)` (`wi_step_completions`, `0005_step_state.sql`); the `INSERT` runs inside `SAVEPOINT bp`; on error → `ROLLBACK TO SAVEPOINT`, continue, 200 (`ROLLBACK TO SAVEPOINT bp` after the completion `INSERT` in `handleUpdateStep`). No Go-side truncation anywhere (`grep -rn 4096 internal pkg` hits only `maxIdempotencyEntries = 4096` in `idempotency.go`).
- **Compare**: for an admitted input of 4097+ characters the promised record is silently not made.
- **Verdict: LOGIC BREAK** — this is the aihub#390 mechanism (§4.11).

### 4.6 `error_type`
- **h1**: classification of a failure. **h3/h4**: read only by the failed-path `INSERT INTO wi_step_completions` (`handleUpdateStep`); with `status=completed` it is neither stored nor rejected; the column has no CHECK (`error_type TEXT` in `CREATE TABLE wi_step_completions`, `0005_step_state.sql`).
- **Compare**: equal for the admitted case (`failed`); silent no-op for the non-admitted case.
- **Verdict: consistent · policy deviation** (silently ignored input).

### 4.7 `escalated`
- **h1**: mark the failure as escalated (blocks the wi). **h3/h4**: read only by the escalation block (`if req.Escalated && u != nil`, `handleUpdateStep`) on the failed path.
- **Compare**: equal for `failed`; silent no-op for `completed` (control 3c).
- **Verdict: consistent · policy deviation.**

### 4.8 `heartbeat`
- **h1** (verbatim, the `heartbeat` prop of the `pf_update_step` schema): `"Send a heartbeat ping to keep the lease alive (resets step_started_at)"`.
- **h2**: the heartbeat branch (`if boolArg(args, "heartbeat")`, `registerStepTools`) sends only the flag, discarding `step_id` / `status` if also given.
- **h3**: the `if req.Heartbeat` branch of `handleUpdateStep` updates `step_started_at` and returns.
- **h4**: there is no lease to keep alive: `handleRenewLease` answers 410 "claim is permanent ownership" (`routes_step.go`) and `expires_at` left with migration 0004. Only the parenthetical is true.
- **Compare**: the promised effect (lease kept alive) does not exist; the implemented effect (timestamp reset) is the parenthetical.
- **Verdict: LOGIC BREAK** (description promises a non-existent mechanism; the silent discard of `step_id`/`status` is a policy deviation on top).

### 4.9 `next_step` / 4.10 `next_step_attempt_id`
- **h1**: fused start of the next step after a completion. **h3/h4**: `startStep` with its idle predicate (`WHERE wi_step_state.current_step_status = 'idle'`); a non-idle state is a loud error.
- **Verdict: consistent** (both).

### 4.11 aihub#390 mechanism — settled read-only (Measured)
- `GET /v1/events?work_item_id=wi_Yu24OkD4&types=step_completed` returns 7 events. `artifact_summary` lengths: prepare_context (`evt_t07fUne2`) **4243** characters; the other six 446–1971. prepare_context is exactly the step missing from `pf_get_step(...).completed_steps` for aihub#383, and `completed_steps_truncated` is false.
- Chain: `INSERT INTO wi_step_completions` inside `SAVEPOINT bp` (`handleUpdateStep`, `routes_step.go`) → CHECK 23514 (`wi_step_completions`, `0005_step_state.sql`) → rollback to savepoint, error discarded → `step_completed` event still emitted with the full payload (the `"step_completed"` event in `handleUpdateStep`) → 200. The event log kept what the history table dropped, which is how the length could be measured after the fact.
- The same swallow covers a duplicate `step_attempt_id` (`CREATE UNIQUE INDEX idx_wsc_attempt`, `0005_step_state.sql`) and an empty `run_attempt_id` FK (`req.AttemptID` is a non-pointer string, passed to the completion `INSERT` in `handleUpdateStep`). aihub#390 fixes the instance; aihub#399 is filed for the class.

### 4.12 hop 5
- The 200 body echoes the **request's** `status` rather than the resulting state (`handleUpdateStep`, `routes_step.go`: `resp := map[string]any{"status": req.Status}`), so a caller cannot see from the response that a `completed` landed on an idle step. **break.**
- The escalation side-effect (`if req.Escalated && u != nil`, the wi becomes blocked) is not reported in the response body. **break.**

### 4.13 Summary for `pf_update_step`

| result | count | params |
|---|---|---|
| consistent | **5** | work_item_id, next_step, next_step_attempt_id, error_type (policy deviation), escalated (policy deviation) |
| **LOGIC BREAK** | **5** | status (completed/failed unguarded vs Description), step_id (row filed under stored current_step), step_attempt_id ("required" but nil accepted silently), artifact_summary (>4096 silently drops the row = #390 class), heartbeat (no lease exists) |
| response breaks | **2** | echoes request status; escalation not reported |
| controls | 3a, 3b detected blind on parent trees; 3c (weak) detected on HEAD |

Filed: aihub#398 (status/step_id/heartbeat, rhs=true — behaviour vs description),
aihub#399 (step_attempt_id + swallowed INSERT class; cross-refs #390 and #403). Both
touch files under aihub#390's write locks; neither should start before #390 lands.

---

## 5. `pf_claim_work_item` — 6 params

**Hops.** hop 1 the `pf_claim_work_item` schema in `registerLifecycleTools` (`internal/mcp/tools_lifecycle.go`; Description verbatim:
`"Claim a work item — creates a new run_attempt with typed locks. Writes state file with
credentials."`); hop 2 its handler closure (secret minted `generateSessionSecret()`; partial state file
`partial := &config.StateFile{`; body assembled incl. `task_branches` from `s.claimTaskBranches`; final state file `sf := &config.StateFile{`;
keep-list `safeResult`); hop 3 `POST /v1/work_items/{id}/claim` →
`domain.ClaimWorkItemRequest`; hop 4 `FnClaimWorkItem` in `internal/domain/run_attempts.go` (replay
`SELECT id, claim_epoch FROM run_attempts WHERE work_item_id=$1 AND idempotency_key=$2`; `isTakeover` (`if wi.Status == "running" && wi.CurrentAttemptID != nil`); conflict probe `if len(req.RequestedLocks) > 0 && !isTakeover`; prior locks released
`if isTakeover && priorAttemptID != ""` → `releaseLocks`; upsert loop `for _, l := range req.RequestedLocks` → `acquireLockUpsert` (`internal/domain/resource_events.go`);
`wi_step_state` upsert `INSERT INTO wi_step_state`; `verifyAttemptCredential` (a separate top-level func in the same file);
`ClaimResponse`, a separate top-level type in the same file).

### 5.1 `work_item_id`
- h1 id or slug. h2 path. h3 resolved. h4 row locked `FOR UPDATE`, attempt inserted.
- **Verdict: consistent.**

### 5.2 `idempotency_key`
- **h1** (`pf_claim_work_item` schema): `"Idempotency key for DB dedup"`; with the Description's `"Writes state file with credentials"` → set: the same claim, and a state file whose credentials work for it.
- **h2**: forwarded verbatim; **before** the request the handler mints a new secret (`generateSessionSecret()`) and writes it to the partial state file (`partial := &config.StateFile{`); after the response it writes the final state file with that new secret (`sf := &config.StateFile{`).
- **h3**: bound as `IdempotencyKey`.
- **h4**: replay branch (`FnClaimWorkItem`) `SELECT id, claim_epoch FROM run_attempts WHERE work_item_id=$1 AND idempotency_key=$2` → returns the existing attempt; **never writes `session_secret_hash`** (the only writers are the `INSERT INTO run_attempts` statements in `FnClaimWorkItem` and `FnForceTakeover`). `verifyAttemptCredential` (`run_attempts.go`) then rejects the new secret: `UNAUTHORIZED invalid session_secret`.
- **Compare**: replay is admitted (that is what the key is for); promised = a working claim; implemented = the old attempt plus a state file whose secret no later call can use. The handler's own error string already says so (the handler's `NOT A NO-OP` error: "replaying the same key returns this attempt without registering a new secret, leaving every later call unauthorized") — but only on the state-write failure path.
- **Verdict: LOGIC BREAK.** End-to-end 401 not exercised (Unverified, §11). Filed: aihub#392.

### 5.3 `mode`
- **h1** (the `mode` prop of the `pf_claim_work_item` schema): `"fresh|resume (default: fresh)"`; `plugins/polyforge/skills/pf-work/SKILL.md` (§ `Mode C — Resume paused wi`) says resume "Restores: prepared workspace + step state from the previous attempt".
- **h2**: forwarded; not used locally (the handler's `Deliberately NOT keyed on args["mode"]` comment, since aihub#322).
- **h3**: bound as `Mode`.
- **h4**: `FnClaimWorkItem` defaults `""` → `"fresh"` (`if req.Mode == ""`); the only other reads are the audit fields `"is_resume": req.Mode == "resume"` (the `claimOp` extras and the `attempt_started` event payload). No validation; the `wi_step_state` upsert (`INSERT INTO wi_step_state`) is identical for both values (resets status/attempt/`step_started_at`, keeps `current_step`).
- **Compare**: two admitted values promise two behaviours; one behaviour exists; a third value is accepted silently.
- **Verdict: LOGIC BREAK** (inert enum + unvalidated). Filed: aihub#394 (rhs=true).

### 5.4 `force_takeover`
- **h1** (the `force_takeover` prop): `"Force takeover if already claimed"` → scoped to this work item's current attempt.
- **h2/h3**: forwarded, bound.
- **h4**: sets `isTakeover` (`FnClaimWorkItem`); the cross-work-item conflict probe is skipped: `if len(req.RequestedLocks) > 0 && !isTakeover`. The prior attempt's own locks are released first (`if isTakeover && priorAttemptID != ""` → `releaseLocks`), so every remaining collision belongs to **another** work item; `acquireLockUpsert` (`resource_events.go`, `ON CONFLICT DO UPDATE`) displaces it, emits `lock_released cause=owner_replaced`, and succeeds. The displaced agent gets no error on this call.
- **Compare**: promised = take over this wi; implemented = take over this wi **and** any foreign lock on the same keys, silently.
- **Verdict: LOGIC BREAK.** Not exercised live (Unverified, §11). Filed: aihub#393 (rhs=true).

### 5.5 `requested_locks`
- h1 typed lock list with an item schema (control 4b fixed at `0583d5cf`). h2 forwarded. h3 bound. h4 probe + upsert.
- **Verdict: consistent** on HEAD. On parent `103c978e` the blind run found the missing item schema and the resulting 500 (control 4b).

### 5.6 `scenario_ref`
- h1 pin the scenario sha; omit to keep the original (COALESCE). h4 as promised.
- **Verdict: consistent.**

### 5.7 `declared_resources` promises redeemed at claim time (shared schema, cross-tool)
The lock semantics published on `pf_create_work_item` / `pf_update_work_item` /
`pf_predict_conflicts` (`declaredResourcesProp`, `internal/mcp/tools_lifecycle.go`) are redeemed here by
`derivedLock` (`internal/domain/conflicts.go`) and `resourceToLock`
(same file). 11 promises checked, **3 broken**:

| promise (h1) | h4 | verdict |
|---|---|---|
| `intent:"read"` — `"read" (takes no write lock, and path overlaps report as info instead of soft_block)` (the `intent` prop of `declaredResourcesProp`), unqualified | `derivedLock` drops the lock only when `lockType == "file_scope" && res.Intent == "read"` (`conflicts.go`); `repo` / `service` entries with `intent:read` still take `git_branch` / `deploy_env` locks | **break** (control 4a fixed for file_scope only) |
| `base_branch` — `"Base branch (repo entries only)"` (the `base_branch` prop) | non-test readers: the struct field (`DeclaredResourceItem.BaseBranch`, `conflicts.go`) and the decoder (`decodeDeclaredResources`); no lock key, rule, query or worktree base reads it | **break** (published, inert) |
| `type:"external_ref"` under a field described as `"Declared resource locks"` (`declared_resources` in `workItemFieldProps`) | `resourceToLock` returns `("","")` (`case "external_ref"` in `resourceToLock`, `conflicts.go`); exempt from the no-uri warning (`UnrecognizedDeclaredResources`, `internal/domain/declared_resources.go`) | **break** (declarable lock that locks nothing and says nothing) |
| uri scheme per type (the `uri` prop) | `ValidateDeclaredResources` checks non-empty only (`declared_resources.go`); `TrimPrefix` (the `repo` and `service` cases of `resourceToLock`, `conflicts.go`) is a no-op on a wrong prefix → silently wrong lock key | consistent for admitted (well-formed) input · **policy deviation** |

These three are counted **once**, in §7b.2 (the schema they are published on), not in
the 6-param tally below. Filed: aihub#395.

### 5.8 hop 5
- Keep-list `safeResult` (`pf_claim_work_item` handler) = `expires_at, acquired_locks, current_attempt_epoch, slug, project, unrecognized_resources` (+ `worktrees`, `worktree_problems`). Dropped: `requires_human_session`, `wi_type`, `step_recovery_hint` (`ClaimResponse.StepRecoveryHint` / `.RequiresHumanSession` / `.WIType`) — **3 breaks = aihub#388 (control 4c)**. `plugins/polyforge/skills/using-polyforge/fragments/post-claim-routing.md` (§ `Source of wi_type (CRITICAL)`) tells the agent to read "the `wi_type` field in the `pf_claim_work_item` response (preferred — just returned)", i.e. the routing doc depends on a field the projection removes; note under #388, not a new wi.
- `expires_at` is on the keep-list but not on `ClaimResponse` (`run_attempts.go`): a dead key (nothing to keep).
- Unpublished forwarded body key `task_branches` (from `s.claimTaskBranches` in the handler): accepted by the server, not in the InputSchema (reverse direction, recorded).

### 5.9 Blind runs on the parent trees
T1 `103c978e`: 2 consistent / 4 break (requested_locks no item schema = 4b; empty-key
lock; unrecognised type silent; keep-list). T2 `5198f37b`: 3 / 3 (intent:read on all
types = 4a). Both controls detected before disclosure.

### 5.10 Summary for `pf_claim_work_item`

| result | count | params |
|---|---|---|
| consistent | **3** | work_item_id, requested_locks, scenario_ref |
| **LOGIC BREAK** | **3** | idempotency_key (replay + fresh secret → later calls UNAUTHORIZED), mode (inert, unvalidated), force_takeover (skips cross-wi probe, displaces foreign locks) |
| response breaks | **3** (= #388) + 1 dead key (`expires_at`) | |
| cross-tool declared_resources promises | 11 checked / 3 broken (counted in §7b.2) | |
| controls | 4a, 4b detected blind on parent trees; 4c detected on HEAD |

Filed: aihub#392, #393, #394; #395 (shared).

---

## 6. Hop 0 — the harness and the SDK (global, not per tool)

- **aihub#389 is global.** `objectSchema` (`internal/mcp/tools_lifecycle.go`) emits no `additionalProperties:false`, and the untyped `AddTool` form every tool uses performs no schema validation (go-sdk v1.6.0 `mcp/server.go`, `(*Server).AddTool`). Unknown keys therefore reach hop 2, where a forwarding table drops them (per-key tools) or a wholesale `s.client.X(args)` passes them on (pass-through tools), and echo v4.15.2 `c.Bind` drops keys with no struct tag silently while returning 400 on a type mismatch (`DefaultBinder.BindBody`, `bind.go` of `github.com/labstack/echo/v4@v4.15.2` in the module cache: plain `encoding/json` decoding, no `DisallowUnknownFields` anywhere in that file, `UnmarshalTypeError` / syntax error → `StatusBadRequest` in `BindBody`'s `case MIMEApplicationJSON`). Per rule 5 this is not a control for any one tool and is not counted in any per-tool table.
- **`json.RawMessage` fields bind any JSON type** (`attrs`, `declared_resources` on `CreateWorkItemRequest`, `work_items.go`), so a scalar where an object is promised is not a 400 at hop 3 (policy deviations under §7b.1).
- The harness used here coerces values to the declared type before the SDK sees them (aihub#380 §3); shapes such as "array on a string param" cannot be produced from this client.

---

## 7. `pf_get_ready_queue` — 3 params

**Hops.** hop 1 the `pf_get_ready_queue` schema in `registerLifecycleTools` (`internal/mcp/tools_lifecycle.go`); hop 2 its handler closure;
hop 3 `handleGetReadyQueue` (`internal/server/router.go`); hop 4
`internal/domain/work_items.go` (items query `buildReadyQueueItemsQuery`; in `GetReadyQueue`: the `max > 200` clamp,
the `needs_human_session` and `unclassified` queries; `ReadyQueue`;
`ReadyItem`).

### 7.1 `project`
- h1 required project. h3 access check, 400 when missing (live: the `/ready` shadow in §2.1 shows the 400 text). h4 `WHERE project = $1`.
- **Verdict: consistent.**

### 7.2 `max`
- **h1** (`pf_get_ready_queue` schema, verbatim): `"Max items in ready section (default 10). A JSON number is also accepted…"` → set: at most `max` items in `items[]`, the other sections unaffected, any value honoured.
- **h2**: the handler stringifies a number (`scalarArg(args, "max")`) (control 5b fixed at `7cb982fc`).
- **h3**: `queryInt(c, "max")` (`handleGetReadyQueue`, `router.go`), 400 on a non-integer (live: `max=abc` → 400).
- **h4**: `LIMIT $2` on **three** sections — `items` (`buildReadyQueueItemsQuery`), `needs_human_session` and `unclassified` (the `GetReadyQueue` queries filtered on `wi.requires_human_session = true` / `IS NULL`); clamp (`GetReadyQueue`): `<=0 → 10`, `>200 → 200`, with a comment admitting the adjustment is not disclosed. Live: `max=1` cut `items` 3→1 **and** `needs_human_session` 3→1; `max=5000` byte-identical to `max=200` (non-discriminative for the ceiling: fewer than 200 items were ready).
- **Compare**: "ready section" ≠ three sections; "any value" ≠ silent 200 ceiling. the `pf_list_work_items` row of `docs/mcp-tools.md` publishes a `request_adjusted` convention for exactly this case; this endpoint does not apply it.
- **Verdict: LOGIC BREAK** (scope) + undisclosed ceiling (control 5c, weak).

### 7.3 `non_conflicting`
- h1 boolean filter (also listed as a query param of the endpoint by the `query:` line of the `Ready Queue (Layer 3)` block in `docs/design/polyforge-v1-design.md`). h2 forwarded (`boolArg(args, "non_conflicting")` in the handler). h3 **never read** (`handleGetReadyQueue`, `router.go`). h4 nothing.
- Live: `non_conflicting=true` response byte-identical to the omitted form (`cmp` on the two `curl -o` bodies).
- **Verdict: LOGIC BREAK = aihub#387 (control 5a, STILL_PRESENT).** Not re-filed.

### 7.4 hop 5 / response promises — **4 breaks + 1 note**
| # | promise | implementation | |
|---|---|---|---|
| R1 | Description "LCRS (6-section) ready queue" | `ReadyQueue` (`work_items.go`) has **seven** keys; the seventh, `stale_running`, is `omitempty`, so a caller sees six until it is non-empty | break |
| R2 | `ReadyItem.UnblockedAt` (`work_items.go`), promised on `items[]` by the `items:` line of the `Ready Queue (Layer 3)` block in `docs/design/polyforge-v1-design.md` | no writer anywhere; live: `unblocked_at` absent | break (dead field) |
| R3 | `created_at` on ready items | selected (`wi.created_at`) by the `needs_human_session` and `unclassified` queries in `GetReadyQueue` but not by `buildReadyQueueItemsQuery`; live: `items[]` keys `[goal,id,priority,slug,wi_type]`, `needs_human_session[]` adds `created_at`. The design doc draws it the same way (its `items:` line vs its `needs_human_session:` / `unclassified:` lines), so this is as designed | **note, not counted** |
| R4 | design doc `docs/design/polyforge-v1-design.md`, `Ready Queue (Layer 3)` block | its `-- v1.20` comment says "六段视图" (six-section view) and the response literal lists six keys; the struct has seven (`stale_running`, which the same doc's `ownership-only` bullet (§ `0. 核心设计哲学`) calls a reminder section); `running[]` is drawn with `expires_at` (its `running:` line) although the `v1.21` Changelog entry records its removal; its `query:` line lists `non_conflicting` as a query param that hop 3 never reads (§7.3) | break (doc) |
| R5 | `request_adjusted` convention in the `pf_list_work_items` row of `docs/mcp-tools.md` | not applied to the `max` clamp | break (convention) |

### 7.5 Summary for `pf_get_ready_queue`

| result | count | params |
|---|---|---|
| consistent | **1** | project |
| **LOGIC BREAK** | **2** | max (three sections + undisclosed 200 ceiling), non_conflicting (dead at hop 3 = #387) |
| response breaks | **4** (+1 note) | R1, R2, R4, R5 (R3 is as designed) |
| controls | 5a detected on HEAD; 5b detected blind on parent `58e67b3b`; 5c (weak) detected on HEAD |

Filed: aihub#401 (everything except `non_conflicting`, which is #387).

---

## 7b. `pf_create_work_item` — 16 top-level params + 6 nested (shared) item fields

**Hops.** hop 1 the `pf_create_work_item` registration in `registerLifecycleTools` (`internal/mcp/tools_lifecycle.go`) via `createWorkItemSchema`
/ `workItemFieldProps` (`blockedByPropDescription`,
`contentPropDescription`, `declaredResourcesProp`;
all in that file); hop 2 wholesale `s.client.CreateWorkItem(args)` (16 published = 16
struct tags, aihub#380 §5.3); hop 3 `handleCreateWorkItem` (`internal/server/router.go`) →
`domain.CreateWorkItemRequest` (`internal/domain/work_items.go`); hop 4
`CreateWorkItem` (same file: its `// Defaults` block; the `ValidateDeclaredResources`
call; scenario gate `if req.Scenario != "coding"`; dedup / force `if !req.ForceCreate`; `INSERT INTO work_items`;
`dbErrCause(err, "failed to insert work_item")`).

**Shared hop-4 fact (Measured):** there is no Go-side validation of `priority`,
`source`, `labels` count, `content` length or `parent_work_item_id`; each is enforced
only by a DB constraint (`CREATE TABLE work_items` in `internal/db/migrations/0002_work_items.sql`: the `source` CHECK,
the `priority` CHECK, `cardinality(labels) <= 20`, the `parent_work_item_id` FK; the Up `ALTER TABLE work_items` in `0011_wi_content.sql`,
content ≤ 20000 chars) whose violation reaches the caller as **500 INTERNAL_ERROR**
through `dbErrCause` (`internal/domain/pgx_err.go`). Under the decision rule a
500 on an admitted input is a break; a 500 on a non-admitted input is a policy
deviation.

### 7b.1 top-level params
| param | h1 (`workItemFieldProps` entry) | h4 | verdict |
|---|---|---|---|
| project | required | access-checked, 400/403 loud | consistent |
| goal | required, ≤ 500 | stored; length checked | consistent |
| wi_type | free string | stored (free TEXT, no CHECK) | consistent |
| priority | `"low\|normal\|high\|urgent"` (`workItemFieldProps`), **no enum** though `propEnum` exists (same file) | CHECK on `priority` (`0002_work_items.sql`) → 500 on any other value | consistent for the four admitted values · **policy deviation** (500 not 400 for a non-admitted value) |
| requires_human_session | `"Whether this wi requires a human session"` (boolean, `workItemFieldProps`) | omitted → NULL → the wi lands in the ready queue's `unclassified[]` (`GetReadyQueue`, `work_items.go`: `wi.requires_human_session IS NULL`) and is never auto-dispatched; the `pf_list_work_items` row of `docs/mcp-tools.md` calls this a third state, the schema does not | **LOGIC BREAK** (a two-state promise with an undisclosed third state that changes dispatch) |
| labels | `{"type":"array"}` with no cap (`workItemFieldProps`) | `cardinality(labels) <= 20` (`0002_work_items.sql`) → 500 | **LOGIC BREAK** (admitted input → 500) |
| milestone | free string | stored | consistent |
| source | `"Source reference"` (`workItemFieldProps`) — reads free-form | CHECK of 7 literals on `source` (`0002_work_items.sql`) → 500 | **LOGIC BREAK** (admitted free text → 500; the #390 agent hit exactly this filing #403, Measured in its report) |
| scenario | coding only | `scenario != "coding"` → `ErrNotImplemented` (`CreateWorkItem`), loud | consistent |
| parent_work_item_id | "parent work item" | passed through (`CreateWorkItemRequest.ParentWorkItemID` → `req.ParentWorkItemID` in the `INSERT INTO work_items`); no resolution, unlike `blocked_by` (`resolveBlockedByRef`, id-or-slug, scoped 404); unknown id or a slug → FK 23503 → 500 | **LOGIC BREAK** (admitted slug → 500) |
| blocked_by | array of ids/slugs (`blockedByPropDescription`), no item type | resolved, 404 on miss | consistent · policy deviation (no item schema) |
| declared_resources | array of typed items (`declaredResourcesProp`) | validated (`ValidateDeclaredResources` in `CreateWorkItem`), stored; locks derived at claim (§5.7) | consistent (container) |
| attrs | object | `json.RawMessage` binds any JSON type | consistent · policy deviation (scalar accepted silently) |
| content | `"max 20000 chars"` (`contentPropDescription`) | CHECK only (the Up `ALTER TABLE work_items` in `0011_wi_content.sql`) → 500 | consistent for admitted input · policy deviation (over-limit → 500 not 400) |
| force_create | boolean | skips `checkDedup` entirely (`if !req.ForceCreate` in `CreateWorkItem`) | consistent |
| force_reason | `"Reason for force create"` (`workItemFieldProps`) | required `len >= 10` **bytes** when `force_create` (`len(req.ForceReason) < 10` in `CreateWorkItem`), then **never persisted** (non-test refs: `CreateWorkItemRequest.ForceReason`, that check, MCP defaulter `applyForceReasonDefault` in `tools_lifecycle.go`; absent from the `INSERT INTO work_items` and the `work_item_filed` payload) | **LOGIC BREAK** (an unpublished minimum enforced on a value that is then discarded) |

### 7b.2 nested `declared_resources[]` item fields (shared schema, `declaredResourcesProp`)
| field | verdict | where redeemed |
|---|---|---|
| type | **LOGIC BREAK** — `external_ref` is offered as a lock type and yields no lock and no warning (§5.7) | claim |
| uri | consistent · policy deviation — scheme unvalidated, wrong prefix → silently wrong lock key (§5.7) | claim |
| intent | **LOGIC BREAK** — `read` honoured for `file_scope` only (§5.7; control 4a residue) | claim |
| repo | consistent | claim |
| task_branch | consistent | claim |
| base_branch | **LOGIC BREAK** — read by nothing (§5.7) | nowhere |

### 7b.3 hop 5 — **3 breaks + 1 doc drift**
- 409 `CONFLICT_DUPLICATE` `details.existing.status` is the literal `"active"` (`checkDedup`, `work_items.go`) although `checkDedup` admits queued/running/paused/blocked rows. **break.**
- Every HTTP ≥ 400 is flattened to text by the client and truncated at `DetailsRenderLimit = 500` bytes (`pkg/client/client.go`, `formatDetails`), so a long 409/500 detail is cut mid-JSON. **break.**
- the `pf_create_work_item` row of `docs/mcp-tools.md` says `force_create` bypasses "on a soft-conflict"; `if !req.ForceCreate` in `CreateWorkItem` skips `checkDedup` outright, so the ≥ 0.90 hard duplicate is bypassed too. **break (doc).**
- The docs row is silent on the `content → content_len` projection the schema commits to (`contentPropDescription`); `suppressContentEcho` (`internal/mcp/wi_echo_slim.go`, called from the `pf_create_work_item` handler in `tools_lifecycle.go`). doc drift.

### 7b.4 Blind runs on the parent trees
T1 `103c978e`: 5 consistent / 11 break (control 6a detected: `declared_resources` with
no shape, stored, no lock, silent; dedup constant +0.2 also seen). T2 `5198f37b`: 8 /
13 (control 6b = 4a detected). Both before disclosure.

### 7b.5 Summary for `pf_create_work_item`

| result | count | params |
|---|---|---|
| top-level consistent | **11** | project, goal, wi_type, priority (pd), milestone, scenario, blocked_by (pd), declared_resources, attrs (pd), content (pd), force_create |
| top-level **LOGIC BREAK** | **5** | requires_human_session, labels, source, parent_work_item_id, force_reason |
| nested (shared) consistent | **3** | uri (pd), repo, task_branch |
| nested (shared) **LOGIC BREAK** | **3** | type (external_ref), intent, base_branch |
| response breaks | **3** + 1 doc drift | |
| controls | 6a detected blind on parent `103c978e`; 6b (= 4a) detected blind on parent `5198f37b` |

(pd) = policy deviation recorded alongside a consistent verdict. Filed: aihub#396
(500s + parent resolution), aihub#397 (force_reason / rhs third state /
`existing.status` / docs), aihub#395 (nested shared fields).

---

## 8. Agent verdicts I overrode, and why

Each per-tool agent reported its own verdicts; I re-read the cited lines on HEAD and
applied the §0 rule. Differences:

| tool | param | agent | final | reason |
|---|---|---|---|---|
| pf_create_work_item | scenario | break | consistent | rejection is loud (`ErrNotImplemented`, `CreateWorkItem`); rule: loud ≠ break |
| pf_create_work_item | priority, content | break | consistent · pd | the value that fails is outside the admitted set; 500-instead-of-400 is a policy deviation, not a set mismatch |
| pf_create_work_item | attrs, blocked_by | break | consistent · pd | shape looseness on non-admitted input |
| pf_create_work_item | uri | break | consistent · pd | admitted (well-formed) input behaves as promised; malformed scheme is non-admitted |
| pf_create_work_item | intent | consistent | **break** | the description's promise is unqualified and `repo`/`service` entries with `intent:read` still lock (control 4a residue, `derivedLock` in `conflicts.go`) |
| pf_create_work_item | type | consistent | **break** | `external_ref` is admitted by the enum and produces no lock; same shape as `intent` |
| pf_update_step | error_type, escalated | break | consistent · pd | ignored only for the non-admitted combination (`status=completed`) |
| pf_update_step | heartbeat | consistent | **break** | the promised mechanism ("lease") does not exist on HEAD (`handleRenewLease`, `routes_step.go`) |

Agent raw tally for reference: create 10 C / 12 B → final 14 C / 8 B over the same
22 params (+6 B→C, −2 C→B, reconciles). The update_step agent's raw tally is not
reproduced here — the three overrides above are the ones I recorded, and the final
5 C / 5 B is derived from the per-param verdicts in §4.1–4.10, not from the agent's
count.

---

## 9. Schema-budget tension (stated precisely)

- `listWorkItemsSchemaBudget = 5400` (`internal/mcp/tools_list_wi_schema_size_test.go`);
  `TestListWorkItemsSchemaStaysWithinItsWireBudget` logs `5371 B of a 5400 B budget`
  — 29 B of headroom (Measured, `go test -run TestListWorkItemsSchemaStaysWithinItsWireBudget -v ./internal/mcp`).
- That test is the **only** schema-budget test under `internal/mcp/*_test.go`
  (Measured: `grep -rn SchemaBudget internal/mcp/` hits only
  `tools_list_wi_schema_size_test.go`), and it measures `listWorkItemsSchema()`
  (`tools_lifecycle.go`) only. **None of the six audited tools' schemas is inside
  it**: `workItemFieldProps` feeds create (`createWorkItemSchema`) and batch (`batchWorkItemsProp`);
  `declaredResourcesProp` feeds create (`workItemFieldProps`), update (the `pf_update_work_item` schema in `registerLifecycleTools`) and predict
  (`registerConflictTools`, `internal/mcp/tools_conflicts.go`); `contentPropDescription` feeds create
  (`workItemFieldProps`) and update (the `pf_update_work_item` schema). So the 5400 B test does **not** mechanically block
  any description fix implied by §§2–7b.
- What does bind: every description byte is resident in every session's tool list, so
  each fix in aihub#394–#402 adds a per-request cost that has no test today. The
  right response is to measure the six tools' serialized schema sizes before and after
  (the `dump-mcp-schemas` path from #380 §5.1 gives bytes per tool), not to shorten
  wording into something less honest. **Never propose shorter, less-honest wording** to
  fit a budget: aihub#380's `user_id` is what that produces.

---

## 10. Live probe log (all 2026-09-07, read-only)

| via | request | result |
|---|---|---|
| curl | `GET /v1/work_items/ready?project=aihub` (plain) vs `…&non_conflicting=true` | bodies byte-identical (`cmp` on the two `-o` files) |
| curl | `…&max=1` | `items` 3→1, `needs_human_session` 3→1 (three-section limit) |
| curl | `…&max=abc` | HTTP 400 `max must be an integer` |
| curl | `…&max=5000` vs `…&max=200` | byte-identical (< 200 ready; ceiling not discriminated) |
| MCP | `pf_get_work_item(work_item_id="ready")` | `aihub 400 BAD_REQUEST: project query parameter is required` (route shadow) |
| MCP | `pf_list_projects` | 10 visible projects, none `wi_`-prefixed |
| curl | `GET /v1/events?work_item_id=wi_Yu24OkD4&types=step_completed` | 7 events; `artifact_summary` of prepare_context (`evt_t07fUne2`) = 4243 chars; others 446–1971 |
| MCP | `pf_batch_create_work_items` (this wi's filings) | 11 created / 0 failed — a write, but the wi's own follow-ups, not a probe |
| blind | six agents on `.audit-scratch/trees/{58e67b3b,e5c42760,43517dc9,5198f37b,103c978e,cbbcb48a,25d009ab}` | every planted control reported before disclosure (§1); the scratch trees are deleted before commit and are reproducible with `git archive <sha> \| tar -x -C <dir>` |

---

## 11. Unverified — what a single command would settle

| claim | settle with |
|---|---|
| claim replay → later calls 401 end-to-end (§5.2) | claim a scratch wi; `pf_claim_work_item` again with the same `idempotency_key`; then `pf_update_step` — expect `UNAUTHORIZED invalid session_secret` |
| force_takeover displaces a foreign lock live (§5.4) | DB test: wi A running with path p; wi B declares p; claim B with `force_takeover=true`; `SELECT owner_attempt_id FROM resource_locks WHERE resource_key='<project>:p'` |
| `FnAcquireLocks` still skips `intent:read` for all types on HEAD (§5.7) | `grep -n 'Intent == "read"' internal/domain/run_attempts.go internal/domain/conflicts.go` |
| create 500 status codes live (§7b.1) | `curl -w '%{http_code}' -X POST /v1/work_items` with `priority=P1`, `source=jira`, 21 labels, 20001-char content, `parent_work_item_id=<slug>` |
| HTTP status the server maps `ErrNotImplemented` to (§7b.1 scenario) | `grep -n ErrNotImplemented internal/server/*.go` |
| ready-queue 200 ceiling observed (§7.2) | DB test seeding 201 queued items, or accept the `max > 200` clamp in `GetReadyQueue` (`internal/domain/work_items.go`) as authoritative |
| `completed` on an idle step succeeds live (§4.2) | needs a write on a scratch wi: `pf_update_step(status="completed")` twice |
| aihub#390's fix state at the time of this report | its agent reported `code_review` in progress at 02:39Z; not on `81c2333` — `pf_get_step(wi for #390)` |

---

## 12. What was deliberately not done

- No `.go` file was modified; no description, keep-list or projection was corrected
  (`non_conflicting`, the claim keep-list and `objectSchema` are aihub#387/#388/#389,
  the controls).
- No pull request; no `pf_ship` / `pf_push` (both force-push). This report is committed
  to the wi's branch with a native, non-force `git push -u origin HEAD`.
- No combined total across tools; `pf_get_work_item`'s numbers are not merged with the
  other five.
- No live write probes (the four items in §11 that need one are left to the follow-ups).
- Nothing was filed for #387/#388/#389/#390; `post-claim-routing.md` (§ `Source of wi_type (CRITICAL)`) is noted under
  #388 (§5.8) rather than as a new wi.

## 13. Follow-ups filed (silent mode, `pf_batch_create_work_items`, 11 created / 0 failed)

| wi | tool | wi_type · priority · rhs | section |
|---|---|---|---|
| aihub#392 | pf_claim_work_item | fix_bug · high · false | §5.2 idempotency_key replay + fresh secret |
| aihub#393 | pf_claim_work_item | fix_bug · high · **true** | §5.4 force_takeover skips cross-wi probe |
| aihub#394 | pf_claim_work_item | chore · normal · **true** | §5.3 `mode` inert |
| aihub#395 | shared declared_resources | chore · normal · false | §5.7 / §7b.2 intent, base_branch, external_ref, uri scheme |
| aihub#396 | pf_create_work_item | fix_bug · normal · false | §7b.1 DB-only constraints → 500; parent resolution |
| aihub#397 | pf_create_work_item | chore · low · false | §7b.1/7b.3 force_reason, rhs third state, `existing.status`, docs `pf_create_work_item` row |
| aihub#398 | pf_update_step | fix_bug · high · **true** | §4.2/4.3/4.8 unguarded completed/failed, stored current_step, heartbeat |
| aihub#399 | pf_update_step | fix_bug · high · false | §4.4/4.5/4.11 nil step_attempt_id + swallowed INSERT class (cross-refs #390; overlaps #403 on the nil half — #403 was filed concurrently by #390's agent; #399 is the class) |
| aihub#400 | pf_get_step | chore · low · false | §3.2 four sentences + docs `pf_get_step` row |
| aihub#401 | pf_get_ready_queue | chore · low · false | §7.2/7.4 (excl. `non_conflicting` = #387) |
| aihub#402 | pf_get_work_item | chore · low · false | §2.1 `wi_` prefix dispatch, stale comment, `/ready` shadow |
