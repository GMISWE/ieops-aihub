# aihub#385 — MCP contract audit, batch 1: six high-traffic tools

Read-only audit of the MCP tool contract for `pf_get_work_item`, `pf_get_step`,
`pf_update_step`, `pf_claim_work_item`, `pf_get_ready_queue` and
`pf_create_work_item`, every published parameter across all four hops to developer
grade, using the method proven on `pf_list_work_items` in aihub#380. No production code
was changed and nothing found here was fixed: several findings are positive controls
planted by the owner (aihub#387 / #388 / #389), and correcting a control mid-audit
destroys it. Findings are filed as work items (§13).

- **Audited commit:** `81c2333` (origin/main at audit start; re-fetched when this report
  was written and still origin/main). Every `file:line` below is against that commit
  unless a parent-tree sha is named.
- **Labels.** *Measured* = a command or `file:line` anyone can re-run or re-read.
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
| pf_get_ready_queue | **5a** `non_conflicting` published, never read at hop 3 (STILL_PRESENT, = aihub#387) | HEAD | **yes** | hop 2 forwards it (`internal/mcp/tools_lifecycle.go:1331-1350`); `handleGetReadyQueue` (`internal/server/router.go:796-823`) never calls `QueryParam("non_conflicting")`; live: `non_conflicting=true` response byte-identical to the omitted form (§10 probe log) |
| pf_get_ready_queue | **5b** `max` sent as a JSON number dropped by `strArg` | parent `58e67b3b` | **yes** | `strArg` at `tools_lifecycle.go:1064` of that tree returns `""` for a non-string, so `max` was never forwarded |
| pf_get_ready_queue | **5c** (weak) `max>200` silently clamped | HEAD | **yes** | `internal/domain/work_items.go:2684-2702`; not the only control for this tool (rule 3) |
| pf_update_step | **3a** `expected_version` published, unbound at hop 3 | parent `e5c42760` | **yes** | `UpdateStepRequest` `internal/server/routes_step.go:27-39` of that tree has no `expected_version` tag; echo `c.Bind` drops unbound keys silently |
| pf_update_step | **3b** `artifact_summary` / `error_type` / `escalated` published, unbound | parent `43517dc9` | **yes** | same struct at `:27-36` of that tree lacks all three tags |
| pf_update_step | **3c** (weak) `escalated` with `status=completed` neither persisted nor rejected | HEAD | **yes** | only the failed-path INSERT `:503-513` and the escalation block `:534-557` read it; not the only control (rule 3) |
| pf_claim_work_item | **4a** `intent:"read"` still takes a lock | parent `5198f37b` | **yes** (by both the claim agent and the create agent, independently) | lock derivation in that tree was `resourceToLock` (`internal/domain/conflicts.go:360` of `5198f37b`), which reads no `Intent` |
| pf_claim_work_item | **4b** `requested_locks` published with no item schema → `500` SQLSTATE 23514 | parent `103c978e` | **yes** | `tools_lifecycle.go:264` + `prop()` `:830-835` of that tree; CHECK violation routed through `dbErrCause` (`internal/domain/pgx_err.go:83-97`) to `ErrInternalError` |
| pf_claim_work_item | **4c** MCP keep-list drops `requires_human_session` / `wi_type` / `step_recovery_hint` (STILL_PRESENT, = aihub#388) | HEAD | **yes** | keep-list `tools_lifecycle.go:1063-1088` (`:1075`); the fields exist on `ClaimResponse` `internal/domain/run_attempts.go:161-186` (`:166`, `:173`, `:174`) |
| pf_get_step | **2a** Description promised "step graph, progress, previous steps" | parent `cbbcb48a` | **yes** | the tree's `tools_step.go` Description vs the handler returning `StepState` only |
| pf_get_step | **2b** slug used verbatim in the step-state query | parent `25d009ab` | **yes** | `routes_step.go:67` of that tree queries `wi_step_state` with the unresolved path segment |
| pf_get_step (effect) / pf_update_step (cause) | **aihub#390 class** STILL_PRESENT on HEAD | HEAD + live | **yes**, mechanism settled (§4.11) | `wi_step_completions` INSERT inside `SAVEPOINT bp`, failure swallowed (`routes_step.go:445-458`); live `wi_Yu24OkD4` prepare_context `artifact_summary` = 4243 chars > CHECK 4096 (`internal/db/migrations/0005_step_state.sql:78`) |
| pf_create_work_item | **6a** `declared_resources` published as a bare array (no item shape) | parent `103c978e` | **yes** | `tools_lifecycle.go:109` of that tree; stored verbatim, no lock derived, no warning |
| pf_create_work_item | **6b** = 4a (shared `intent` semantics) | parent `5198f37b` | **yes** | as 4a |
| pf_get_work_item | **none** | — | — | **no positive control — conclusion strength downgraded** (rule 4) |

Every control present on a tool's parent tree or on HEAD was detected blind. Per rule 2
this establishes discriminative power for five tools individually; it establishes
nothing for `pf_get_work_item`.

---

## 2. `pf_get_work_item` — 2 params · **no positive control**

**Hops.** hop 1 `internal/mcp/tools_lifecycle.go:639-645`; hop 2 handler `:646-665`
→ `pkg/client/client.go:212-215` (`GET /v1/work_items/{id}`); hop 3
`internal/server/router.go:58` → `handleGetWorkItem` `:561-579`; hop 4
`internal/domain/work_items.go:865-908`.

### 2.1 `work_item_id`
- **h1** (`:639-645`): `"Work item ID or slug"` → set: the one work item whose id **or**
  slug equals the value, within visible projects.
- **h2**: forwarded verbatim as the path segment (`client.go:212-215`).
- **h3**: `handleGetWorkItem` passes the segment to the domain unchanged (`:561-579`).
- **h4** (`work_items.go:865-908`): `if strings.HasPrefix(idOrSlug, "wi_") { WHERE id = $1 } else { WHERE slug = $1 }` — a single-column dispatch on the prefix. Every sibling resolver uses the union: `id = $1 OR slug = $1` (`:260`, `:947`), `wi.id = ANY OR wi.slug = ANY` (`:1313`). The comment at `:237` claims the OR form for this function too (stale). `FormatIDOrSlug` (`internal/domain/ids.go:53-58`) repeats the prefix rule. The project-name regex `^[a-z][a-z0-9_-]{0,39}$` (`internal/domain/projects.go:22`) does **not** reserve the `wi_` prefix, so a project `wi_lab` would yield slugs `wi_lab#1` that this function looks up in the id column and never finds.
- **Compare**: equal for every value that exists today — live `pf_list_projects` shows 10 visible projects, none `wi_`-prefixed (Measured). Unequal for an admitted input (a slug of a `wi_`-prefixed project) that the schema and the project regex both allow.
- **Verdict: LOGIC BREAK (latent — no reachable instance with today's data).** Note, not a break: `router.go:57` registers `GET /work_items/ready` ahead of `/work_items/:id`, so the literal `"ready"` is answered by the ready-queue handler (live: `aihub 400 BAD_REQUEST: project query parameter is required`); loud, and no slug can be `ready` (slugs are `<project>#<seq>`).

### 2.2 `brief`
- **h1** (`:639-645`): boolean; when true the response omits `content`.
- **h2**: **local** — never forwarded; the handler deletes `content` from the decoded result (`:661-663`).
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

**Hops.** hop 1 `internal/mcp/tools_step.go:34-46`; hop 2 `GET` with the work-item
path segment; hop 3 the step-state handler in `internal/server/routes_step.go`
(`StepState` struct `:63-93`, `completedStepsLimit = 200` `:103`,
`completedStepsQuery` `:177-188`); hop 4 `wi_step_state` + `wi_step_completions`.

### 3.1 `work_item_id`
- **h1**: id or slug → the step state of that work item.
- **h2**: verbatim path segment. **h3**: resolved id-or-slug before the state query
  (control 2b fixed at `7c49bba5`). **h4**: `wi_step_state WHERE work_item_id = $1`.
- **Compare**: equal. **Verdict: consistent.**

### 3.2 Response promises (Description `:34-46` vs `StepState` `:63-93` and the queries)
18 decidable promises were enumerated (field presence, ordering, limit, semantics of
`completed_steps`, and the three sentences of advice). Four fail:

| # | hop 1 promise | hop 4 / struct | verdict |
|---|---|---|---|
| 2 | "Returns current_step / current_step_status / version" | `CurrentStep` is `json:"current_step,omitempty"` (`:66`); the contract test allowlists its absence (`internal/mcp/tools_step_contract_test.go:329-332`) | **break** — absent vs null is exactly the distinction the same Description insists on for `completed_steps` |
| 8 | each `completed_steps[].step_id` is the step the agent completed | the row's `step_id` is `derefStr(currentStep)` read before the update (`routes_step.go:405-406`, used `:454`), not the request's `step_id` (§4.3) | **break** (cause is in `pf_update_step`; effect visible here) |
| 11 | "treat every step_id in completed_steps as done" | `completedStepsQuery` (`:177-188`) has no status filter; `fnForceTerminateStep` writes rows with `status='failed', error_type='force_terminate'` (`internal/domain/run_attempts.go:1050-1072`) | **break** — the data carries `status`; the sentence says to ignore it |
| 17 | "pf_recall / pf_read_events … return nothing for a slug" | both resolve a slug now: `internal/server/routes_memory.go:847`, `internal/domain/memory.go:1982-1988` | **break** (stale advice, harmless direction) |

Also Measured: `docs/mcp-tools.md:86` still says "Current step graph, status, progress,
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

**Hops.** hop 1 `internal/mcp/tools_step.go:78-98`; hop 2 `updateStepBody` `:250-276`
(heartbeat branch `:125-141`); hop 3 `handleUpdateStep` in
`internal/server/routes_step.go` (`UpdateStepRequest`; heartbeat `:387-390`;
`currentStep` read `:405-406`; completed `UPDATE` `:437-442`; completion `INSERT`
gate `:445`, `SAVEPOINT bp` swallow `:450-458`; event `:460-464`; failed `UPDATE`
`:496-500`, `INSERT` `:503-513`; escalation `:534-557`; `startStep` `:665-683` with
the only idle predicate at `:677`; `handleRenewLease` 410 `:685-690`); hop 4 the
`wi_step_state` / `wi_step_completions` tables (`internal/db/migrations/0005_step_state.sql`:
`status CHECK IN ('completed','failed')` `:70`, `error_type TEXT` `:74`,
`CHECK (length(artifact_summary) <= 4096)` `:78`, `UNIQUE idx_wsc_attempt` `:82`).

**Description-level promise (`:78-98`, verbatim):** `"There is no version/CAS argument:
concurrency is guarded server-side by the idle-step predicate, so no pf_get_step is
needed before this call."` The only predicate on `current_step_status` is in
`startStep` (`:677`, `WHERE current_step_status = 'idle'`), i.e. it guards the
**in_progress** transition. The `completed` and `failed` `UPDATE`s (`:437-442`,
`:496-500`) are `WHERE work_item_id = $1` with no status predicate and no
`current_step = $step` predicate.

### 4.1 `work_item_id`
- h1 id or slug → this work item. h2 path segment. h3 resolved. h4 all statements keyed on the resolved id.
- **Verdict: consistent.**

### 4.2 `status`
- **h1**: `in_progress | completed | failed` (heartbeat is a separate flag) → the step moves to that state, guarded per the Description.
- **h2**: forwarded verbatim (`:250-276`); dropped in the heartbeat branch (`:125-141`).
- **h3**: switch in `handleUpdateStep`; `in_progress` → `startStep` (`:665-683`, predicate `:677`); `completed` → `:437-442`; `failed` → `:496-500`.
- **h4**: `in_progress` refused unless idle (loud); `completed` / `failed` succeed from **any** state, including idle and including a step currently in progress under another step attempt.
- **Compare**: h1 (guarded transitions) ≠ h4 for two of the three admitted values.
- **Verdict: LOGIC BREAK.**

### 4.3 `step_id`
- **h1**: the step being reported on.
- **h2**: forwarded (`:250-276`); dropped for heartbeat.
- **h3**: `completed` path writes the completion row with `step_id = derefStr(currentStep)` (`:454`), the value read at `:405-406` **before** the update; then sets `current_step` to the request's value. `failed` path analogous (`:503-513`).
- **h4**: a completion row for whatever step the server had stored, then `current_step := req.Step`.
- **Compare**: for `step_id == stored current_step` equal; for `step_id != stored current_step` (admitted — nothing in h1 says they must match) the history records the wrong step and the state jumps to the caller's.
- **Verdict: LOGIC BREAK.**

### 4.4 `step_attempt_id`
- **h1** (verbatim): `"Step attempt ID of the step being completed/failed (required for completed/failed)"`.
- **h2**: forwarded when present.
- **h3**: the completion `INSERT` is gated `if req.StepAttemptID != nil` (`:445`); with nil the state `UPDATE` still runs and the call answers 200; no 400.
- **h4**: no history row, no error.
- **Compare**: "required" promises a rejection when absent; implemented = silent success without the row.
- **Verdict: LOGIC BREAK.** (aihub#403, filed by #390's agent, covers this half; aihub#399 covers the class incl. §4.11.)

### 4.5 `artifact_summary`
- **h1**: free-text summary recorded with the completion (no limit published).
- **h2**: forwarded verbatim. **h3**: bound; written by the `INSERT` at `:445-458` / `:503-513`.
- **h4**: `CHECK (length(artifact_summary) <= 4096)` (`0005_step_state.sql:78`); the `INSERT` runs inside `SAVEPOINT bp`; on error → `ROLLBACK TO SAVEPOINT`, continue, 200 (`:450-458`). No Go-side truncation anywhere (`grep -rn 4096 internal pkg` hits only `idempotency.go:29`).
- **Compare**: for an admitted input of 4097+ characters the promised record is silently not made.
- **Verdict: LOGIC BREAK** — this is the aihub#390 mechanism (§4.11).

### 4.6 `error_type`
- **h1**: classification of a failure. **h3/h4**: read only by the failed-path `INSERT` (`:503-513`); with `status=completed` it is neither stored nor rejected; the column has no CHECK (`:74`).
- **Compare**: equal for the admitted case (`failed`); silent no-op for the non-admitted case.
- **Verdict: consistent · policy deviation** (silently ignored input).

### 4.7 `escalated`
- **h1**: mark the failure as escalated (blocks the wi). **h3/h4**: read only by the escalation block `:534-557` on the failed path.
- **Compare**: equal for `failed`; silent no-op for `completed` (control 3c).
- **Verdict: consistent · policy deviation.**

### 4.8 `heartbeat`
- **h1** (verbatim, `:90`): `"Send a heartbeat ping to keep the lease alive (resets step_started_at)"`.
- **h2**: the heartbeat branch (`:125-141`) sends only the flag, discarding `step_id` / `status` if also given.
- **h3**: `:387-390` updates `step_started_at` and returns.
- **h4**: there is no lease to keep alive: `handleRenewLease` answers 410 "claim is permanent ownership" (`:685-690`) and `expires_at` left with migration 0004. Only the parenthetical is true.
- **Compare**: the promised effect (lease kept alive) does not exist; the implemented effect (timestamp reset) is the parenthetical.
- **Verdict: LOGIC BREAK** (description promises a non-existent mechanism; the silent discard of `step_id`/`status` is a policy deviation on top).

### 4.9 `next_step` / 4.10 `next_step_attempt_id`
- **h1**: fused start of the next step after a completion. **h3/h4**: `startStep` (`:665-683`) with the idle predicate (`:677`); a non-idle state is a loud error.
- **Verdict: consistent** (both).

### 4.11 aihub#390 mechanism — settled read-only (Measured)
- `GET /v1/events?work_item_id=wi_Yu24OkD4&types=step_completed` returns 7 events. `artifact_summary` lengths: prepare_context (`evt_t07fUne2`) **4243** characters; the other six 446–1971. prepare_context is exactly the step missing from `pf_get_step(...).completed_steps` for aihub#383, and `completed_steps_truncated` is false.
- Chain: `INSERT INTO wi_step_completions` inside `SAVEPOINT bp` (`routes_step.go:445-458`) → CHECK 23514 (`0005_step_state.sql:78`) → rollback to savepoint, error discarded → `step_completed` event still emitted with the full payload (`:460-464`) → 200. The event log kept what the history table dropped, which is how the length could be measured after the fact.
- The same swallow covers a duplicate `step_attempt_id` (`UNIQUE` `:82`) and an empty `run_attempt_id` FK (`req.AttemptID` is a non-pointer string, `:454`). aihub#390 fixes the instance; aihub#399 is filed for the class.

### 4.12 hop 5
- The 200 body echoes the **request's** `status` rather than the resulting state (`routes_step.go:594`, `resp` built from `req.Status`), so a caller cannot see from the response that a `completed` landed on an idle step. **break.**
- The escalation side-effect (`:534-557`, the wi becomes blocked) is not reported in the response body. **break.**

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

**Hops.** hop 1 `internal/mcp/tools_lifecycle.go:774-784` (Description verbatim:
`"Claim a work item — creates a new run_attempt with typed locks. Writes state file with
credentials."`); hop 2 the handler `:785-` (secret minted `:800`; partial state file
`:806-813`; body assembled incl. `task_branches` `:844-851`; final state file `:866-872`;
keep-list `:1063-1088`); hop 3 `POST /v1/work_items/{id}/claim` →
`domain.ClaimWorkItemRequest`; hop 4 `internal/domain/run_attempts.go` (replay
`:438-512`; `isTakeover` `:519-540`; conflict probe `:615`; prior locks released
`:655-672`; upsert loop `:717-740` → `acquireLockUpsert` `internal/domain/resource_events.go:561`;
`wi_step_state` upsert `:763-771`; `verifyAttemptCredential` `:1445-1474`;
`ClaimResponse` `:161-186`).

### 5.1 `work_item_id`
- h1 id or slug. h2 path. h3 resolved. h4 row locked `FOR UPDATE`, attempt inserted.
- **Verdict: consistent.**

### 5.2 `idempotency_key`
- **h1** (`:774-784`): `"Idempotency key for DB dedup"`; with the Description's `"Writes state file with credentials"` → set: the same claim, and a state file whose credentials work for it.
- **h2**: forwarded verbatim; **before** the request the handler mints a new secret (`:800`) and writes it to the partial state file (`:806-813`); after the response it writes the final state file with that new secret (`:866-872`).
- **h3**: bound as `IdempotencyKey`.
- **h4**: replay branch (`:438-512`) `SELECT id, claim_epoch FROM run_attempts WHERE work_item_id=$1 AND idempotency_key=$2` → returns the existing attempt; **never writes `session_secret_hash`** (the only writers are the `INSERT`s at `:685` and `:1349`). `verifyAttemptCredential` (`:1449-1474`) then rejects the new secret: `UNAUTHORIZED invalid session_secret`.
- **Compare**: replay is admitted (that is what the key is for); promised = a working claim; implemented = the old attempt plus a state file whose secret no later call can use. The handler's own error string already says so (`:930-940`: "replaying the same key returns this attempt without registering a new secret, leaving every later call unauthorized") — but only on the state-write failure path.
- **Verdict: LOGIC BREAK.** End-to-end 401 not exercised (Unverified, §11). Filed: aihub#392.

### 5.3 `mode`
- **h1** (`:780`): `"fresh|resume (default: fresh)"`; `plugins/polyforge/skills/pf-work/SKILL.md:286-292` says resume "Restores: prepared workspace + step state from the previous attempt".
- **h2**: forwarded; not used locally (`:985-987`, deliberately since aihub#322).
- **h3**: bound as `Mode`.
- **h4**: `:388-389` defaults `""` → `"fresh"`; the only other reads are the audit fields `"is_resume": req.Mode == "resume"` (`:725`, `:819`). No validation; the `wi_step_state` upsert (`:763-771`) is identical for both values (resets status/attempt/`step_started_at`, keeps `current_step`).
- **Compare**: two admitted values promise two behaviours; one behaviour exists; a third value is accepted silently.
- **Verdict: LOGIC BREAK** (inert enum + unvalidated). Filed: aihub#394 (rhs=true).

### 5.4 `force_takeover`
- **h1** (`:782`): `"Force takeover if already claimed"` → scoped to this work item's current attempt.
- **h2/h3**: forwarded, bound.
- **h4**: sets `isTakeover` (`:519-540`); the cross-work-item conflict probe is skipped: `if len(req.RequestedLocks) > 0 && !isTakeover` (`:615`). The prior attempt's own locks are released first (`:655-672`), so every remaining collision belongs to **another** work item; `acquireLockUpsert` (`resource_events.go:561`, `ON CONFLICT DO UPDATE`) displaces it, emits `lock_released cause=owner_replaced`, and succeeds. The displaced agent gets no error on this call.
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
`pf_predict_conflicts` (`declaredResourcesProp` `:1818-1855`) are redeemed here by
`derivedLock` (`internal/domain/conflicts.go:452-458`) and `resourceToLock`
(`:692-712`). 11 promises checked, **3 broken**:

| promise (h1) | h4 | verdict |
|---|---|---|
| `intent:"read"` — `"read" (takes no write lock, and path overlaps report as info instead of soft_block)` (`:1835`), unqualified | `derivedLock` drops the lock only when `lockType == "file_scope" && res.Intent == "read"` (`conflicts.go:452-458`); `repo` / `service` entries with `intent:read` still take `git_branch` / `deploy_env` locks | **break** (control 4a fixed for file_scope only) |
| `base_branch` — `"Base branch (repo entries only)"` (`:1849`) | non-test readers: the struct field (`conflicts.go:65`) and the decoder (`:606`); no lock key, rule, query or worktree base reads it | **break** (published, inert) |
| `type:"external_ref"` under a field described as `"Declared resource locks"` (`:1727`) | `resourceToLock` returns `("","")` (`conflicts.go:706-707`); exempt from the no-uri warning (`internal/domain/declared_resources.go:165`) | **break** (declarable lock that locks nothing and says nothing) |
| uri scheme per type (`:1825-1826`) | `ValidateDeclaredResources` checks non-empty only (`declared_resources.go:115-124`); `TrimPrefix` (`conflicts.go:695`, `:704`) is a no-op on a wrong prefix → silently wrong lock key | consistent for admitted (well-formed) input · **policy deviation** |

These three are counted **once**, in §7b.2 (the schema they are published on), not in
the 6-param tally below. Filed: aihub#395.

### 5.8 hop 5
- Keep-list `:1063-1088` = `expires_at, acquired_locks, current_attempt_epoch, slug, project, unrecognized_resources` (+ `worktrees`, `worktree_problems`). Dropped: `requires_human_session`, `wi_type`, `step_recovery_hint` (`ClaimResponse` `:166`, `:173`, `:174`) — **3 breaks = aihub#388 (control 4c)**. `plugins/polyforge/skills/using-polyforge/fragments/post-claim-routing.md:14` tells the agent to read "the `wi_type` field in the `pf_claim_work_item` response (preferred — just returned)", i.e. the routing doc depends on a field the projection removes; note under #388, not a new wi.
- `expires_at` is on the keep-list but not on `ClaimResponse` (`:161-186`): a dead key (nothing to keep).
- Unpublished forwarded body key `task_branches` (`:844-851`): accepted by the server, not in the InputSchema (reverse direction, recorded).

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

- **aihub#389 is global.** `objectSchema` (`internal/mcp/tools_lifecycle.go:1679`) emits no `additionalProperties:false`, and the untyped `AddTool` form every tool uses performs no schema validation (go-sdk v1.6.0 `server.go:238-283`). Unknown keys therefore reach hop 2, where a forwarding table drops them (per-key tools) or a wholesale `s.client.X(args)` passes them on (pass-through tools), and echo v4.15.2 `c.Bind` drops keys with no struct tag silently while returning 400 on a type mismatch (`DefaultBinder.BindBody`, `bind.go:76-130` of `github.com/labstack/echo/v4@v4.15.2` in the module cache: plain `encoding/json` decoding, no `DisallowUnknownFields` anywhere in that file, `UnmarshalTypeError` / syntax error → `StatusBadRequest` at `:93-103`). Per rule 5 this is not a control for any one tool and is not counted in any per-tool table.
- **`json.RawMessage` fields bind any JSON type** (`attrs`, `declared_resources` on `CreateWorkItemRequest`, `work_items.go:100-117`), so a scalar where an object is promised is not a 400 at hop 3 (policy deviations under §7b.1).
- The harness used here coerces values to the declared type before the SDK sees them (aihub#380 §3); shapes such as "array on a string param" cannot be produced from this client.

---

## 7. `pf_get_ready_queue` — 3 params

**Hops.** hop 1 `internal/mcp/tools_lifecycle.go:1319-1325`; hop 2 `:1331-1350`;
hop 3 `handleGetReadyQueue` `internal/server/router.go:796-823`; hop 4
`internal/domain/work_items.go` (items query `:2670-2680`, clamp `:2684-2702`,
needs_human_session `:2834-2846`, unclassified `:2861-2873`; `ReadyQueue` `:141-149`;
`ReadyItem` `:152-160`).

### 7.1 `project`
- h1 required project. h3 access check, 400 when missing (live: the `/ready` shadow in §2.1 shows the 400 text). h4 `WHERE project = $1`.
- **Verdict: consistent.**

### 7.2 `max`
- **h1** (`:1319-1325`, verbatim): `"Max items in ready section (default 10). A JSON number is also accepted…"` → set: at most `max` items in `items[]`, the other sections unaffected, any value honoured.
- **h2**: `:1331-1350` stringifies a number (control 5b fixed at `7cb982fc`).
- **h3**: `queryInt(c, "max")` (`router.go:813`), 400 on a non-integer (live: `max=abc` → 400).
- **h4**: `LIMIT $2` on **three** sections — `items` (`:2680`), `needs_human_session` (`:2834`), `unclassified` (`:2861`); clamp `:2684-2702`: `<=0 → 10`, `>200 → 200`, with a comment admitting the adjustment is not disclosed. Live: `max=1` cut `items` 3→1 **and** `needs_human_session` 3→1; `max=5000` byte-identical to `max=200` (non-discriminative for the ceiling: fewer than 200 items were ready).
- **Compare**: "ready section" ≠ three sections; "any value" ≠ silent 200 ceiling. `docs/mcp-tools.md:47` publishes a `request_adjusted` convention for exactly this case; this endpoint does not apply it.
- **Verdict: LOGIC BREAK** (scope) + undisclosed ceiling (control 5c, weak).

### 7.3 `non_conflicting`
- h1 boolean filter (also listed as a query param of the endpoint by `docs/design/polyforge-v1-design.md:1399`). h2 forwarded (`:1331-1350`). h3 **never read** (`router.go:796-823`). h4 nothing.
- Live: `non_conflicting=true` response byte-identical to the omitted form (`cmp` on the two `curl -o` bodies).
- **Verdict: LOGIC BREAK = aihub#387 (control 5a, STILL_PRESENT).** Not re-filed.

### 7.4 hop 5 / response promises — **4 breaks + 1 note**
| # | promise | implementation | |
|---|---|---|---|
| R1 | Description "LCRS (6-section) ready queue" | `ReadyQueue` (`:141-149`) has **seven** keys; the seventh, `stale_running`, is `omitempty`, so a caller sees six until it is non-empty | break |
| R2 | `ReadyItem.UnblockedAt` (`:152-160`), promised on `items[]` by `docs/design/polyforge-v1-design.md:1403` | no writer anywhere; live: `unblocked_at` absent | break (dead field) |
| R3 | `created_at` on ready items | selected for needs_human_session (`:2846`) and unclassified (`:2873`) but not by the items query (`:2670-2673`); live: `items[]` keys `[goal,id,priority,slug,wi_type]`, `needs_human_session[]` adds `created_at`. The design doc draws it the same way (`:1403` vs `:1412`/`:1415`), so this is as designed | **note, not counted** |
| R4 | design doc `docs/design/polyforge-v1-design.md:1398-1416` | `:1400` "六段视图" (six-section view) and `:1401-1416` list six keys; the struct has seven (`stale_running`, which the same doc's `:63` calls a reminder section); `running[]` is drawn with `expires_at` (`:1405`) although the v1.21 line (`:33`) records its removal; `:1399` lists `non_conflicting` as a query param that hop 3 never reads (§7.3) | break (doc) |
| R5 | `docs/mcp-tools.md:47` `request_adjusted` convention | not applied to the `max` clamp | break (convention) |

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

**Hops.** hop 1 `internal/mcp/tools_lifecycle.go:447-479` via `createWorkItemSchema`
`:1764-1768` / `workItemFieldProps` `:1718-1736` (`blockedByPropDescription`
`:1746-1749`, `contentPropDescription` `:1759-1760`, `declaredResourcesProp`
`:1818-1855`); hop 2 wholesale `s.client.CreateWorkItem(args)` (16 published = 16
struct tags, aihub#380 §5.3); hop 3 `internal/server/router.go:210-235` →
`domain.CreateWorkItemRequest` `internal/domain/work_items.go:100-117`; hop 4
`CreateWorkItem` `:288-535` (defaults `:305-316`; `ValidateDeclaredResources`
`:324-326`; scenario gate `:331-334`; dedup / force `:393-399`; `INSERT` `:435-438`;
`dbErrCause` `:441`).

**Shared hop-4 fact (Measured):** there is no Go-side validation of `priority`,
`source`, `labels` count, `content` length or `parent_work_item_id`; each is enforced
only by a DB constraint (`internal/db/migrations/0002_work_items.sql:26-28` source,
`:34-35` priority, `:44-45` labels ≤ 20, `:58` parent FK; `0011_wi_content.sql:4`
content ≤ 20000 chars) whose violation reaches the caller as **500 INTERNAL_ERROR**
through `dbErrCause` → `internal/domain/pgx_err.go:83-97`. Under the decision rule a
500 on an admitted input is a break; a 500 on a non-admitted input is a policy
deviation.

### 7b.1 top-level params
| param | h1 (`workItemFieldProps` line) | h4 | verdict |
|---|---|---|---|
| project | required | access-checked, 400/403 loud | consistent |
| goal | required, ≤ 500 | stored; length checked | consistent |
| wi_type | free string | stored (free TEXT, no CHECK) | consistent |
| priority | `"low\|normal\|high\|urgent"` (`:1722`), **no enum** though `propEnum` exists (`:1700`) | CHECK `:34-35` → 500 on any other value | consistent for the four admitted values · **policy deviation** (500 not 400 for a non-admitted value) |
| requires_human_session | `"Whether this wi requires a human session"` (boolean, `:1724`) | omitted → NULL → the wi lands in the ready queue's `unclassified[]` (`work_items.go:2852` `requires_human_session IS NULL`) and is never auto-dispatched; `docs/mcp-tools.md:47` calls this a third state, the schema does not | **LOGIC BREAK** (a two-state promise with an undisclosed third state that changes dispatch) |
| labels | `{"type":"array"}` with no cap (`:1726`) | `cardinality(labels) <= 20` (`:44-45`) → 500 | **LOGIC BREAK** (admitted input → 500) |
| milestone | free string | stored | consistent |
| source | `"Source reference"` (`:1729`) — reads free-form | CHECK of 7 literals (`:26-28`) → 500 | **LOGIC BREAK** (admitted free text → 500; the #390 agent hit exactly this filing #403, Measured in its report) |
| scenario | coding only | `scenario != "coding"` → `ErrNotImplemented` (`:331-334`), loud | consistent |
| parent_work_item_id | "parent work item" | passed through (`:110`, `:437`); no resolution, unlike `blocked_by` (`resolveBlockedByRef` `:245-275`, id-or-slug, scoped 404); unknown id or a slug → FK 23503 → 500 | **LOGIC BREAK** (admitted slug → 500) |
| blocked_by | array of ids/slugs (`:1746-1749`), no item type | resolved, 404 on miss | consistent · policy deviation (no item schema) |
| declared_resources | array of typed items (`:1818-1855`) | validated (`:324-326`), stored; locks derived at claim (§5.7) | consistent (container) |
| attrs | object | `json.RawMessage` binds any JSON type | consistent · policy deviation (scalar accepted silently) |
| content | `"max 20000 chars"` (`:1759`) | CHECK only (`0011:4`) → 500 | consistent for admitted input · policy deviation (over-limit → 500 not 400) |
| force_create | boolean | skips `checkDedup` entirely (`:393-396`) | consistent |
| force_reason | `"Reason for force create"` (`:1734`) | required `len >= 10` **bytes** when `force_create` (`:397`), then **never persisted** (non-test refs: `:116`, `:397`, MCP defaulter `tools_lifecycle.go:1789`; absent from the `INSERT` `:435-438` and the `work_item_filed` payload) | **LOGIC BREAK** (an unpublished minimum enforced on a value that is then discarded) |

### 7b.2 nested `declared_resources[]` item fields (shared schema, `:1818-1855`)
| field | verdict | where redeemed |
|---|---|---|
| type | **LOGIC BREAK** — `external_ref` is offered as a lock type and yields no lock and no warning (§5.7) | claim |
| uri | consistent · policy deviation — scheme unvalidated, wrong prefix → silently wrong lock key (§5.7) | claim |
| intent | **LOGIC BREAK** — `read` honoured for `file_scope` only (§5.7; control 4a residue) | claim |
| repo | consistent | claim |
| task_branch | consistent | claim |
| base_branch | **LOGIC BREAK** — read by nothing (§5.7) | nowhere |

### 7b.3 hop 5 — **3 breaks + 1 doc drift**
- 409 `CONFLICT_DUPLICATE` `details.existing.status` is the literal `"active"` (`work_items.go:829`) although `checkDedup` admits queued/running/paused/blocked rows. **break.**
- Every HTTP ≥ 400 is flattened to text by the client and truncated at `DetailsRenderLimit = 500` bytes (`pkg/client/client.go:34`, `formatDetails`), so a long 409/500 detail is cut mid-JSON. **break.**
- `docs/mcp-tools.md:45` says `force_create` bypasses "on a soft-conflict"; `:393-396` skips `checkDedup` outright, so the ≥ 0.90 hard duplicate is bypassed too. **break (doc).**
- The docs row is silent on the `content → content_len` projection the schema commits to (`:1759-1760`); `suppressContentEcho` (`tools_lifecycle.go:470`). doc drift.

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
| pf_create_work_item | scenario | break | consistent | rejection is loud (`ErrNotImplemented`, `:331-334`); rule: loud ≠ break |
| pf_create_work_item | priority, content | break | consistent · pd | the value that fails is outside the admitted set; 500-instead-of-400 is a policy deviation, not a set mismatch |
| pf_create_work_item | attrs, blocked_by | break | consistent · pd | shape looseness on non-admitted input |
| pf_create_work_item | uri | break | consistent · pd | admitted (well-formed) input behaves as promised; malformed scheme is non-admitted |
| pf_create_work_item | intent | consistent | **break** | the description's promise is unqualified and `repo`/`service` entries with `intent:read` still lock (control 4a residue, `conflicts.go:452-458`) |
| pf_create_work_item | type | consistent | **break** | `external_ref` is admitted by the enum and produces no lock; same shape as `intent` |
| pf_update_step | error_type, escalated | break | consistent · pd | ignored only for the non-admitted combination (`status=completed`) |
| pf_update_step | heartbeat | consistent | **break** | the promised mechanism ("lease") does not exist on HEAD (`:685-690`) |

Agent raw tally for reference: create 10 C / 12 B → final 14 C / 8 B over the same
22 params (+6 B→C, −2 C→B, reconciles). The update_step agent's raw tally is not
reproduced here — the three overrides above are the ones I recorded, and the final
5 C / 5 B is derived from the per-param verdicts in §4.1–4.10, not from the agent's
count.

---

## 9. Schema-budget tension (stated precisely)

- `listWorkItemsSchemaBudget = 5400` (`internal/mcp/tools_list_wi_schema_size_test.go:28`);
  `TestListWorkItemsSchemaStaysWithinItsWireBudget` logs `5371 B of a 5400 B budget`
  — 29 B of headroom (Measured, `go test -run TestListWorkItemsSchemaStaysWithinItsWireBudget -v ./internal/mcp`).
- That test is the **only** schema-budget test under `internal/mcp/*_test.go`
  (Measured: `grep -rn SchemaBudget internal/mcp/` hits only
  `tools_list_wi_schema_size_test.go`), and it measures `listWorkItemsSchema()`
  (`tools_lifecycle.go:108`) only. **None of the six audited tools' schemas is inside
  it**: `workItemFieldProps` (`:1718`) feeds create (`:1765`) and batch (`:1774`);
  `declaredResourcesProp` feeds create (`:1727`), update (`:694`) and predict
  (`internal/mcp/tools_conflicts.go:18`); `contentPropDescription` feeds create
  (`:1732`) and update (`:709`). So the 5400 B test does **not** mechanically block
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
| ready-queue 200 ceiling observed (§7.2) | DB test seeding 201 queued items, or accept `:2684-2702` as authoritative |
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
- Nothing was filed for #387/#388/#389/#390; `post-claim-routing.md:14` is noted under
  #388 (§5.8) rather than as a new wi.

## 13. Follow-ups filed (silent mode, `pf_batch_create_work_items`, 11 created / 0 failed)

| wi | tool | wi_type · priority · rhs | section |
|---|---|---|---|
| aihub#392 | pf_claim_work_item | fix_bug · high · false | §5.2 idempotency_key replay + fresh secret |
| aihub#393 | pf_claim_work_item | fix_bug · high · **true** | §5.4 force_takeover skips cross-wi probe |
| aihub#394 | pf_claim_work_item | chore · normal · **true** | §5.3 `mode` inert |
| aihub#395 | shared declared_resources | chore · normal · false | §5.7 / §7b.2 intent, base_branch, external_ref, uri scheme |
| aihub#396 | pf_create_work_item | fix_bug · normal · false | §7b.1 DB-only constraints → 500; parent resolution |
| aihub#397 | pf_create_work_item | chore · low · false | §7b.1/7b.3 force_reason, rhs third state, `existing.status`, docs:45 |
| aihub#398 | pf_update_step | fix_bug · high · **true** | §4.2/4.3/4.8 unguarded completed/failed, stored current_step, heartbeat |
| aihub#399 | pf_update_step | fix_bug · high · false | §4.4/4.5/4.11 nil step_attempt_id + swallowed INSERT class (cross-refs #390; overlaps #403 on the nil half — #403 was filed concurrently by #390's agent; #399 is the class) |
| aihub#400 | pf_get_step | chore · low · false | §3.2 four sentences + docs:86 |
| aihub#401 | pf_get_ready_queue | chore · low · false | §7.2/7.4 (excl. `non_conflicting` = #387) |
| aihub#402 | pf_get_work_item | chore · low · false | §2.1 `wi_` prefix dispatch, stale comment, `/ready` shadow |
