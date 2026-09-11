# MCP tool reference

The polyforge MCP server (the `polyforge` binary in MCP mode, see
[`../README.md`](../README.md)) exposes **45 `pf_*` tools**. Every tool maps to
an HTTP endpoint through the Go SDK in one path:

```
pf_* tool  ->  pkg/client  ->  HTTP handler  ->  internal/domain
```

There is no separate logic in the tool layer - it parses arguments and calls
the client. Mutating tools (`pf_commit`, `pf_emit_event`, lifecycle writes)
inject the per-work-item attempt credential from the local state file under
`<workspace>/.polyforge/state/`. That file is keyed by **either** the canonical
id (`wi_xxx.json`) **or** the slug (`project#seq.json`), and both shapes exist
on disk at once during a claim, so it must be read through
`config.ResolveStateFile` and never by filename: a slug-addressed
`ReadStateFile` finds the pre-claim stub, sends an empty `attempt_id`, and the
server answers 409 `CONFLICT_EPOCH_MISMATCH` — which reads as "someone stole my
attempt" when the real cause is reading the wrong local file (aihub#141,
aihub#149, aihub#319). Authority: `internal/config/state.go`
(`ResolveStateFile`).

This page is a curated index. The **authoritative, always-current schema** for
every tool (argument names, types, required fields) is emitted by:

```bash
polyforge dump-mcp-schemas        # full JSON schema for all tools (CI contract)
```

Do not copy argument lists into this page. The tool count in the heading above
and the per-section counts below are checked against that dump on every run of
the `Contract Lint` job (`scripts/pf_docs_contract_check.py`, check C3), by set
equality on the tool names rather than by count alone — so a tool added,
removed or renamed without touching this file turns the build red.

The tools are registered in `internal/mcp/tools_*.go`; the grouping below
follows those files.

**Per-tool depth lives in [`mcp-cards/`](mcp-cards/)** (aihub#458): one contract
card per published tool, walking every parameter and response field across hops
0-5 and citing the adjudicated policy that binds it. This page stays the index —
what exists and where it is registered; a card answers what a parameter actually
does. The cards are gated against the live registry by
`internal/mcp/contract_cards_gate_test.go`, so a tool added, renamed or
reschematised without its card turns the build red.

## An argument no tool publishes is reported, not rejected (aihub#389)

Every tool goes through one registration wrapper (`(*Server).addTool`) that diffs
the caller's top-level argument names against that tool's own published schema.
Extras are **not** rejected and **not** silently dropped: they come back in the
response under the existing `request_adjusted` convention, as an entry whose
`param` is the literal `unknown_params` and whose `requested` lists the names.
They are also logged to stderr.

```json
"request_adjusted":[{"param":"unknown_params","requested":["expected_version"],"applied":[]}]
```

Read that as *"nothing you sent under these names was applied"* — whatever the rest
of the response says. The mechanism differs by family (aihub#570, 2026-09-10): for
the wholesale-forwarding memory writers (`pf_remember`, `pf_save_artifact`,
`pf_update_memory`) the named keys are **stripped at the MCP boundary** before the
request is built (`internal/mcp/wire_strip.go`, aihub#586), so nothing is forwarded;
for every other tool they either never leave the process (handlers that build typed
bodies) or ride the request to the server and are dropped at its JSON binding (the
wholesale forwarders `internal/mcp/wire_strip.go` records). An earlier version of
this paragraph said the names "reached nothing", which was false for that second
group — the wording is pinned by `internal/mcp/unknown_params_wording_test.go`.
It is appended to the list, so a `request_adjusted` the server
itself produced (a clamped `limit`, say) is still there alongside it.

Reporting rather than rejecting is deliberate and temporary. `additionalProperties:false`
in `objectSchema` would make the SDK refuse these before the handler runs, and it
is the intended end state — but measured over 21 days of transcripts, 301 of
12,133 `pf_*` calls carried an unpublished argument, 202 of them
`pf_update_step.expected_version` alone (18% of that tool's calls), so flipping it
today would fail roughly one `pf_update_step` in five. The flip is a separate work
item, gated on a re-measure below 0.1%.

## Work item lifecycle (13) - `internal/mcp/tools_lifecycle.go`

| tool | purpose |
|---|---|
| `pf_whoami` | Caller identity plus accessible projects and roles. |
| `pf_create_work_item` | Create a work item. Runs F3 dedup; `force_create` bypasses on a soft-conflict. **`requires_human_session` has THREE states, not two (aihub#411 T2-9):** `true`, `false`, and **omitted**. Omitting it stores `NULL`, which is not a default of `false` — the wi lands in the ready queue's `unclassified[]` segment rather than `items[]`, so it is never offered for unattended dispatch and `pf_list_work_items`' `ready_only` filter does not return it. `NULL` is **not permanent**: the FIRST `pf_claim_work_item` on such a wi resolves it to `true` from a server default, writes that back and records a `wi_classification_resolved` event — so a wi that has ever been claimed cannot still be unclassified. Send `false` explicitly for a wi an agent may take unattended. |
| `pf_batch_create_work_items` | Create SEVERAL work items in one round-trip. Items are independent: the response splits `created` / `failed`, each failure carrying its `index` so a retry resends only those. Dedup still runs per item. Each entry takes `pf_create_work_item`'s fields, including the three-state `requires_human_session` described in that row. |
| `pf_list_work_items` | List work items with filters (status, kind, label, milestone, user, source, ...). `sort=created_at\|closed_at` + `order=desc\|asc` control the ordering; `sort=closed_at` returns only closed items. **Response is projected (aihub#278), by one rule: an item key is omitted only when its value is null, so an absent key means null.** The keys this can remove are `content` (this endpoint never serves bodies — read one with `pf_get_work_item`), `milestone`, `parent_work_item_id`, `closed_at`, `current_attempt_id`, `external_share_type` and `external_share_key`. Nothing else is ever removed. In particular `requires_human_session` is always present even when null, because there null is a third state (*unclassified*) rather than "none"; and `seq` and `scenario` are kept despite being reconstructible, because removing a key that holds a real value makes a reader silently render `null` in its place, whereas removing a null key is invisible in `jq` and a loud `KeyError` in Python. **`request_adjusted` (aihub#314):** present only when the server changed a parameter you sent — a list of `{param, requested, applied}`. Today the one case is `limit`, which is served as 200 when it arrives above the 200 ceiling and as 50 when it arrives non-positive (aihub#267); an absent key means nothing was adjusted. A `limit` that is not an integer at all is a 400, not an adjustment (aihub#340). **Semantic retrieval (aihub#273/#276/#277):** two mutually exclusive query-vector sources, both similarity-ordered and neither combinable with `sort`/`order`/`cursor`. `query=<text>` embeds the text you pass (ILIKE fallback when the server has no provider). `similar_to=<id\|slug>` reuses another work item's **stored** `goal+content` vector — an order of magnitude more precise, since a one-line query is a poor stand-in for a whole document (measured: a work item searched with its own goal text verbatim ranked *itself* 81st, while its stored vector ranks it 1st). `similar_to` makes `project` optional the same way `ids` does, so omitting `project` gives cross-project neighbour search bounded by the projects you can see; the source is scoped identically, so one outside that scope is a 404, and a source with no vector yet is a 412. A vector-served page carries a `semantic` block (`mode`, `source_work_item_id`, `emb_model`, `ranked_candidates`, `min_similarity`, `similarity_scale`); its **absence** means the ILIKE text path answered and no item carries a `similarity`. Within that block `source_work_item_id` follows the same absent-means-null rule as the item keys above — it is omitted on the `query=` path, where there is no source. It is also the only unconditional confirmation of which row `similar_to` used: the source is an ordinary row, so it appears in its own results at similarity 1.0 *unless another filter excludes it*. 🔴 **`similarity` is comparable only WITHIN one result set — never across queries, and never against an absolute value.** There is no relevance filter and there will not be a default one: measured over 20 queries against a 351-work-item corpus, fourteen candidate statistics — nine derived from the similarity values (top1, gaps, z-score over the full corpus, min-max normalisation) and five from lexical or IDF-weighted query-term coverage — *all* overlap between deliberate garbage and real queries, frequently inverted — the absolute scale is set by the query's own length and register, not by match quality. Read `semantic.ranked_candidates` instead: you received the top `len(items)` of that many ranked candidates, so a full page means nothing on its own. `min_similarity` is an opt-in floor, default 0, rejected with 400 (rather than silently ignored) on a request the text path would serve. |
| `pf_get_work_item` | Fetch one work item by id or slug. |
| `pf_update_work_item` | Patch goal, wi_type, priority, labels, declared_resources, content, attrs. **Editability is one matrix over three field tiers (aihub#440), and the "queued or paused" this row used to claim was never the rule for anything but goal and wi_type.** `goal`/`wi_type` need `queued`/`paused`/`blocked` *and* reporter, project maintainer or admin; `content`/`labels`/`priority`/`milestone`/`requires_human_session`/`declared_resources` need any non-terminal status; `attrs`/`attrs_patch`/`attrs_unset` are writable in **every** status, including on a wrapped work item, which is deliberate and load-bearing for post-wrap records. The strictest tier a patch touches governs the whole patch. Refusals name the KIND, not the field: 409 `CONFLICT_WI_ALREADY_CLAIMED` / `CONFLICT_TERMINAL_STATE` for a wrong state, 403 `FORBIDDEN` for a wrong caller. See the contract card for the table. **Since aihub#495 the tool description carries that table too** — the matrix was enforced from aihub#440 and stated only here, in the card and in a source comment, none of which a tool caller can read; it now names the three tiers, the two 409s, the 403, the mixed-patch rule, and the five fields whose terminal-state refusal is the one cell that moved. **`requires_human_session` reaches only two of its three states (aihub#447):** there is no way back to `NULL` (*unclassified*) through this tool, because omitting the field and sending an explicit `null` are the same no-op. Measured, not inferred: a `pf_update_work_item` that carries only `work_item_id` and `attrs_patch` leaves the column exactly as it was, on a live-era build and on `origin/main` alike — the `NULL` to `true` transition aihub#411 T2-9 attributed to such a call was written by the CLAIM path. The reply carries the field either way, so do not read its presence as evidence this call set it. |
| `pf_claim_work_item` | Claim a queued/paused wi -> new run attempt + resource locks. No resume flag: step state lives in `wi_step_state` per work item, so every re-claim sees it (aihub#394 withdrew `mode`). |
| `pf_complete_attempt` | End the current attempt: `wrapped` (success), `failed`, or `paused`. `note` records the closing note in the same call, before the state file is deleted. |
| `pf_force_takeover` | Take a wi from another agent (same-user, or maintainer/admin). |
| `pf_get_ready_queue` | LCRS **seven**-segment ready queue for a project: `items`, `running`, `stalled`, `paused`, `needs_human_session`, `unclassified`, `stale_running`. **Every segment is always present** — an empty one is `[]`, never a missing key. It was six-until-non-empty before aihub#449 removed `stale_running`'s `omitempty`, so a caller on an older server sees the key only when something is stale, and cannot tell that case from an empty one. `max` is a **per-segment** page size and reaches only three of the seven: `items`, `needs_human_session` and `unclassified` each take it as their own `LIMIT`, while `running`, `stalled`, `paused` and `stale_running` are unbounded — so it is not a budget over the response, and one section being full says nothing about the others. Items in `items[]` carry `id`, `slug`, `wi_type`, `priority`, `goal`; the other two item segments add `created_at`, and that asymmetry is as designed rather than a gap. |
| `pf_cancel_work_item` | Cancel a work item. |
| `pf_pause_attempt` | Pause: release `file_scope` locks, retain every other lock type for resume — since aihub#416 that set is normally empty, because `file_scope` is the only lock the server derives. |
| `pf_acquire_locks` | Acquire declared `file_scope` locks mid-attempt (blocks on conflict, never steals). |

## Memory and artifacts (9) - `internal/mcp/tools_memory.go`

| tool | purpose |
|---|---|
| `pf_remember` | Store a memory (type, visibility, strength, expiry). Rejects `methodology.*` types - use `pf_save_artifact`. **`visibility` is one of `admin`/`private`/`project`/`team`/`public`** — the whole `memories_visibility_check` set, published at hop 1 since aihub#495 and built from `domain.MemoryVisibilityList()` rather than retyped. `public` had been legal since migration 0023 and accepted in Go since aihub#434 while the tool description listed only the other four. It is not merely the widest tier: it is the value the **unauthenticated** `GET /share/:id` gates on, so send `project` unless you mean to publish. |
| `pf_recall` | Recall memories with filters. Item `content` is truncated to 800 runes; such items carry `content_truncated: true` and `content_full_len` (full rune length) — read the rest with `pf_get_memory` (aihub#269). **`request_adjusted` (aihub#314):** present only when the server changed a parameter you sent — a list of `{param, requested, applied}`. Today the one case is `top_k`, which is capped at 200 (and replaced with the default 20 when negative); an absent key means nothing was adjusted. **Ranking (aihub#192, shipped):** pgvector semantic recall is live, not pending. `Recall` is a router — it takes the vector path when an embedding provider is configured, `query` is non-empty, no `work_item_id` filter is set (a wi-scoped recall is deterministic, not semantic) and the type filter is empty or names at least one embeddable type; the text/tag path otherwise, with both halves merged when a request spans them (aihub#270). **The sort key differs per path** and is not the reference time except on one: cosine similarity bucketed to 0.01 on the vector path (aihub#311 — cosine must dominate, because the embedding model packs a result set into a ~0.04-wide band where any strength weight can flip a near-tie), `ts_rank` on the lexical text branch, and `GREATEST(last_activated_at, created_at) DESC, id DESC` only on the text branch with no query. What every path *does* share is `GREATEST(last_activated_at, created_at)` as the **reference time inside the strength-decay expression** — never a `NULLS LAST` tier (aihub#236). There is **no** similarity floor by default; `similarity_threshold` sets one and gates the embeddable half only (aihub#148). Similarity values are not comparable across different queries, and the vector path does not paginate. Authority: `internal/domain/memory.go` (`recallRouted`) and `internal/domain/memory_vector.go` (`RecallWithVector`). |
| `pf_get_memory` | Fetch one memory by id with its full, untruncated content. The follow-up read for a `pf_recall` item whose `content_truncated` is true. |
| `pf_activate_memory` | Increment activation count and update stability. |
| `pf_reinforce_memory` | Add context and adjust strength (same row, no new version). |
| `pf_update_memory` | Update a memory: create a new version superseding the current head and advance the `latest_id` cursor, so an id you already hold still resolves to the latest. |
| `pf_redact_memory` | Soft-delete a memory. |
| `pf_save_artifact` | Save a methodology artifact, optionally with pre-rendered HTML. **`type` must start with `methodology.`, and that PREFIX — not a list of names — is what is enforced** (`validatePfSaveArtifactArgs`, aihub#499). `spec`/`plan`/`review`/`execute`/`retro`/`wrap_summary` are SUGGESTED; an off-list `methodology.*` name is accepted and stored, but is not pre-rendered and does not appear in the work item's artifact-links section, both of which name the six literally. The six were published as a JSON-Schema `enum` from aihub#211 until aihub#499 withdrew it: nothing had ever enforced the names, and 3 of the 1,185 `methodology.*` rows measured live on 2026-09-09 sit outside them. |
| `pf_resolve_commit` | Resolve a spec/plan annotation commit with a reply. |

⚠️ **Retired, and this is a behaviour change** (`aihub#446`, `aihub#411` T2-7):
`pf_adopt_artifact`, `pf_close_artifact` and `pf_ignore_artifact` are no longer
published. They were wrappers that emitted one `artifact_action` event with
`payload.action` in `{adopt, close, ignore}`, and nothing in the tree read that
event — the `/ui` annotation flow, which shares part of the vocabulary, runs on
`open`/`resolved` commit annotations instead (`POST
/ui/artifacts/:id/commit/:commit_id/resolve`). A client that calls one of the
three now gets a JSON-RPC `InvalidParams` (-32602) `unknown tool "..."` from the
MCP SDK, before any handler runs — a protocol error, not a tool result. The
event itself was NOT withdrawn: `pf_emit_event` still accepts
`event_type: "artifact_action"`, and existing events stay on the timeline.

## Events (2) - `internal/mcp/tools_events.go`

| tool | purpose |
|---|---|
| `pf_emit_event` | Emit an event on a work item (note, wi_reclassified, step_started, ...). |
| `pf_read_events` | Read events for a work item or a whole project. |

## Step state (2) - `internal/mcp/tools_step.go`

| tool | purpose |
|---|---|
| `pf_get_step` | The authoritative step record: `current_step` / `current_step_status` / `version`, plus `completed_steps` — the step history oldest first, retries included, each entry carrying its own `status`, so only a `completed` entry means that step is done. No step graph here: the graph is a scenario template, pinned per work item by `scenario_ref`. |
| `pf_update_step` | Update the current step (`in_progress`/`completed`/`failed`, heartbeat, artifact summary). `next_step` completes one step and starts its successor in one call. No version/CAS argument and no `pf_get_step` needed first — but **not because one predicate covers the endpoint**. `in_progress` is guarded by the idle predicate; `completed`/`failed` must name the step the server has open (a mismatch is 409 naming both); **neither checks step STATE**, so an idle step with a matching name can still be completed twice. This row said "concurrency is guarded by the server's idle-step predicate" until `aihub#493`: `aihub#398` corrected that sentence in the tool description because it over-promised — a caller reads it as "the server will stop me getting this wrong" — and corrected the description only, leaving this copy asserting the version the server never implemented. `heartbeat` refreshes `step_started_at` and nothing else; there is no lease, and the heartbeat branch returns early, so a `status`/`step_id` sent alongside it goes nowhere. |

## Dependencies (3) - `internal/mcp/tools_dependency.go`

| tool | purpose |
|---|---|
| `pf_create_dependency` | Link two work items (`blocks`/`supersedes`/`related`). |
| `pf_remove_dependency` | Remove a dependency. |
| `pf_list_dependencies` | List blocking + blocked_by (cross-project items folded if no viewer access). |

## Conflicts (1) - `internal/mcp/tools_conflicts.go`

| tool | purpose |
|---|---|
| `pf_predict_conflicts` | Predict resource-lock conflicts for a set of declared resources; also returns `will_unlock`. |

## Coding / git (6) - `internal/mcp/tools_coding.go`

Credentials are injected from the state file; these operate inside the wi
worktree.

| tool | purpose |
|---|---|
| `pf_diff` | Git diff for the worktree (vs HEAD or base). |
| `pf_commit` | Commit staged changes in the worktree. |
| `pf_push` | Push the branch, lease-protected when it already exists on origin (refuses main/master/dev/tot). |
| `pf_pr` | Create a GitHub PR for the task branch. |
| `pf_ship` | **Commit + push + PR in one call**, and the push is the same force-push as `pf_push`. Prefer it over the three separately: those cost three round-trips for three confirmations no decision depends on. On failure the response is JSON with `stage` (which of commit/push/pr failed) and `side_effects` (typically an unpushed local commit). Retrying never duplicates a commit. |
| `pf_wrap` | Push + PR + `complete_attempt(wrapped)` + delete state file. Idempotent only when a PR already covers local HEAD; see `pr_action` in the response. `note` records the closing note in the same call. |

## Projects (4) - `internal/mcp/tools_projects.go`

| tool | purpose |
|---|---|
| `pf_list_projects` | List projects visible to the caller. |
| `pf_create_project` | Create a project (repos + scenario). |
| `pf_update_project` | Update repos, members, description, scenario, visibility. |
| `pf_rotate_identifier` | Rotate the project access identifier (returned once). |

## Users and API keys (5) - `internal/mcp/tools_users.go`

Admin only.

| tool | purpose |
|---|---|
| `pf_list_users` | List the 100 newest users by creation time; no cursor, no total. |
| `pf_create_user` | Create a user (human or machine). |
| `pf_update_user` | Update a user's display name or role. |
| `pf_create_api_key` | Create an API key for a user (returned once). |
| `pf_revoke_api_key` | Revoke an API key. |
