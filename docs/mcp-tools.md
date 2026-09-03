# MCP tool reference

The polyforge MCP server (the `polyforge` binary in MCP mode, see
[`../README.md`](../README.md)) exposes **50 `pf_*` tools**. Every tool maps to
an HTTP endpoint through the Go SDK in one path:

```
pf_* tool  ->  pkg/client  ->  HTTP handler  ->  internal/domain
```

There is no separate logic in the tool layer - it parses arguments and calls
the client. Mutating tools (`pf_commit`, `pf_emit_event`, lifecycle writes)
inject the per-work-item attempt credential from the local state file
(`<workspace>/.polyforge/state/<wi_id>.json`).

This page is a curated index. The **authoritative, always-current schema** for
every tool (argument names, types, required fields) is emitted by:

```bash
polyforge dump-mcp-schemas        # full JSON schema for all tools (CI contract)
```

The tools are registered in `internal/mcp/tools_*.go`; the grouping below
follows those files.

## Work item lifecycle (13) - `tools_lifecycle.go`

| tool | purpose |
|---|---|
| `pf_whoami` | Caller identity plus accessible projects and roles. |
| `pf_create_work_item` | Create a work item. Runs F3 dedup; `force_create` bypasses on a soft-conflict. |
| `pf_batch_create_work_items` | Create SEVERAL work items in one round-trip. Items are independent: the response splits `created` / `failed`, each failure carrying its `index` so a retry resends only those. Dedup still runs per item. |
| `pf_list_work_items` | List work items with filters (status, kind, label, milestone, user, source, ...). `sort=created_at\|closed_at` + `order=desc\|asc` control the ordering; `sort=closed_at` returns only closed items. **Response is projected (aihub#278), by one rule: an item key is omitted only when its value is null, so an absent key means null.** The keys this can remove are `content` (this endpoint never serves bodies — read one with `pf_get_work_item`), `milestone`, `parent_work_item_id`, `closed_at`, `current_attempt_id`, `external_share_type` and `external_share_key`. Nothing else is ever removed. In particular `requires_human_session` is always present even when null, because there null is a third state (*unclassified*) rather than "none"; and `seq` and `scenario` are kept despite being reconstructible, because removing a key that holds a real value makes a reader silently render `null` in its place, whereas removing a null key is invisible in `jq` and a loud `KeyError` in Python. **`request_adjusted` (aihub#314):** present only when the server changed a parameter you sent — a list of `{param, requested, applied}`. Today the one case is `limit`, which is served as 200 when it arrives above the 200 ceiling and as 50 when it arrives non-positive (aihub#267); an absent key means nothing was adjusted. A `limit` that is not an integer at all is a 400, not an adjustment (aihub#340). **Semantic retrieval (aihub#273/#276/#277):** two mutually exclusive query-vector sources, both similarity-ordered and neither combinable with `sort`/`order`/`cursor`. `query=<text>` embeds the text you pass (ILIKE fallback when the server has no provider). `similar_to=<id\|slug>` reuses another work item's **stored** `goal+content` vector — an order of magnitude more precise, since a one-line query is a poor stand-in for a whole document (measured: a work item searched with its own goal text verbatim ranked *itself* 81st, while its stored vector ranks it 1st). `similar_to` makes `project` optional the same way `ids` does, so omitting `project` gives cross-project neighbour search bounded by the projects you can see; the source is scoped identically, so one outside that scope is a 404, and a source with no vector yet is a 412. A vector-served page carries a `semantic` block (`mode`, `source_work_item_id`, `emb_model`, `ranked_candidates`, `min_similarity`, `similarity_scale`); its **absence** means the ILIKE text path answered and no item carries a `similarity`. Within that block `source_work_item_id` follows the same absent-means-null rule as the item keys above — it is omitted on the `query=` path, where there is no source. It is also the only unconditional confirmation of which row `similar_to` used: the source is an ordinary row, so it appears in its own results at similarity 1.0 *unless another filter excludes it*. 🔴 **`similarity` is comparable only WITHIN one result set — never across queries, and never against an absolute value.** There is no relevance filter and there will not be a default one: measured over 20 queries against a 351-work-item corpus, fourteen candidate statistics — nine derived from the similarity values (top1, gaps, z-score over the full corpus, min-max normalisation) and five from lexical or IDF-weighted query-term coverage — *all* overlap between deliberate garbage and real queries, frequently inverted — the absolute scale is set by the query's own length and register, not by match quality. Read `semantic.ranked_candidates` instead: you received the top `len(items)` of that many ranked candidates, so a full page means nothing on its own. `min_similarity` is an opt-in floor, default 0, rejected with 400 (rather than silently ignored) on a request the text path would serve. |
| `pf_get_work_item` | Fetch one work item by id or slug. |
| `pf_update_work_item` | Patch goal, wi_type, priority, labels, declared_resources, content (status must be queued or paused). |
| `pf_claim_work_item` | Claim a queued/paused wi -> new run attempt + resource locks. `mode=fresh|resume`. |
| `pf_complete_attempt` | End the current attempt: `wrapped` (success), `failed`, or `paused`. `note` records the closing note in the same call, before the state file is deleted. |
| `pf_force_takeover` | Take a wi from another agent (same-user, or maintainer/admin). |
| `pf_get_ready_queue` | LCRS six-segment ready queue for a project. |
| `pf_cancel_work_item` | Cancel a work item. |
| `pf_pause_attempt` | Pause: release `file_scope` locks, retain `git_branch`/`deploy_env` for resume. |
| `pf_acquire_locks` | Acquire declared `file_scope` locks mid-attempt (blocks on conflict, never steals). |

## Memory and artifacts (12) - `tools_memory.go`

| tool | purpose |
|---|---|
| `pf_remember` | Store a memory (type, visibility, strength, expiry). Rejects `methodology.*` types - use `pf_save_artifact`. |
| `pf_recall` | Recall memories with filters. Item `content` is truncated to 800 runes; such items carry `content_truncated: true` and `content_full_len` (full rune length) — read the rest with `pf_get_memory` (aihub#269). **`request_adjusted` (aihub#314):** present only when the server changed a parameter you sent — a list of `{param, requested, applied}`. Today the one case is `top_k`, which is capped at 200 (and replaced with the default 20 when negative); an absent key means nothing was adjusted. **Note:** ranking is recency-based today; semantic/vector recall is in flight (aihub#192). |
| `pf_get_memory` | Fetch one memory by id with its full, untruncated content. The follow-up read for a `pf_recall` item whose `content_truncated` is true. |
| `pf_activate_memory` | Increment activation count and update stability. |
| `pf_reinforce_memory` | Add context and adjust strength (same row, no new version). |
| `pf_update_memory` | Update a memory: create a new version superseding the current head and advance the `latest_id` cursor, so an id you already hold still resolves to the latest. |
| `pf_redact_memory` | Soft-delete a memory. |
| `pf_save_artifact` | Save a methodology artifact (`spec`/`plan`/`review`/`execute`/`retro`/`wrap_summary`), optionally with pre-rendered HTML. |
| `pf_adopt_artifact` | Mark an artifact adopted. |
| `pf_close_artifact` | Mark an artifact closed. |
| `pf_ignore_artifact` | Mark an artifact ignored. |
| `pf_resolve_commit` | Resolve a spec/plan annotation commit with a reply. |

## Events (2) - `tools_events.go`

| tool | purpose |
|---|---|
| `pf_emit_event` | Emit an event on a work item (note, wi_reclassified, step_started, ...). |
| `pf_read_events` | Read events for a work item or a whole project. |

## Step state (2) - `tools_step.go`

| tool | purpose |
|---|---|
| `pf_get_step` | Current step graph, status, progress, and previous steps. |
| `pf_update_step` | Update the current step (`in_progress`/`completed`/`failed`, heartbeat, artifact summary). `next_step` completes one step and starts its successor in one call. No version/CAS argument - concurrency is guarded by the server's idle-step predicate, so no `pf_get_step` is needed first. |

## Dependencies (3) - `tools_dependency.go`

| tool | purpose |
|---|---|
| `pf_create_dependency` | Link two work items (`blocks`/`supersedes`/`related`). |
| `pf_remove_dependency` | Remove a dependency. |
| `pf_list_dependencies` | List blocking + blocked_by (cross-project items folded if no viewer access). |

## Conflicts (1) - `tools_conflicts.go`

| tool | purpose |
|---|---|
| `pf_predict_conflicts` | Predict resource-lock conflicts for a set of declared resources; also returns `will_unlock`. |

## Coding / git (6) - `tools_coding.go`

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

## Projects (4) - `tools_projects.go`

| tool | purpose |
|---|---|
| `pf_list_projects` | List projects visible to the caller. |
| `pf_create_project` | Create a project (repos + scenario). |
| `pf_update_project` | Update repos, members, description, scenario, visibility. |
| `pf_rotate_identifier` | Rotate the project access identifier (returned once). |

## Release (2) - `tools_release.go`

Admin / release-manager only.

| tool | purpose |
|---|---|
| `pf_cut_alpha` | Cut the next alpha release (tag + manifest). |
| `pf_promote` | Promote an alpha channel to stable. |

## Users and API keys (5) - `tools_users.go`

Admin only.

| tool | purpose |
|---|---|
| `pf_list_users` | List all users. |
| `pf_create_user` | Create a user (human or machine). |
| `pf_update_user` | Update a user's display name or role. |
| `pf_create_api_key` | Create an API key for a user (returned once). |
| `pf_revoke_api_key` | Revoke an API key. |
