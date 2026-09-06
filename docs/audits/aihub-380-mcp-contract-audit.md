# aihub#380 — MCP parameter / response contract audit

Read-only audit of the MCP tool contract: for `pf_list_work_items`, every published
parameter across all four hops to developer grade; for the other 49 tools, a
mechanical static set-difference only. No production code was changed and nothing
found here was fixed — three of the findings are positive controls planted by the
owner, and correcting a control mid-audit destroys it.

- **Audited commit:** `965df70ebac6dda5b160e9c1f258813777b3c4af` (origin/main at audit
  time). Every `file:line` below is against that commit.
- **Labels.** *Measured* = a command or `file:line` anyone can re-run or re-read.
  *Unverified* = inferred; the command that would settle it is named each time.
- **Live probes** ran on 2026-09-06 against the aihub server configured in
  `~/.polyforge/config.toml`, both through the MCP tool and through direct
  `GET /v1/work_items` with `curl`. The build running there cannot be read from
  `/version` (`server_version` is a constant), but
  `git diff 90ce786..965df70 -- internal/domain/work_items.go internal/server/router.go`
  touches none of the cursor / scan / filter code probed below, so both candidate
  builds run identical code for everything that was probed (Measured).

---

## 1. Positive controls — reported before any count

The wi's gate: if any control is not detected, no count may be reported for that
direction. All three are detected.

| control | direction | detected | evidence (Measured) |
|---|---|---|---|
| **A** — `OwnerDisplay` / `WatcherUserID` / `ReporterDisplay`: SQL at hop 4, no hop 2/3 | request | **yes** | SQL: `internal/domain/work_items.go:1174-1179` (WatcherUserID `EXISTS`), `:1226` (`wi.reporter_display ILIKE`), `:1232-1237` (OwnerDisplay via `run_attempts` join). Filter fields: `work_items.go:898-909`. Absent from all three forwarding tables `internal/mcp/tools_lifecycle.go:40-63`. Not read anywhere in `handleListWorkItems` (`internal/server/router.go:248-536`; the scalar table at `:398-413` binds priority, milestone, scenario, label, user_id, source only). Their only writers are the UI: `internal/server/ui_handlers_wi.go:776-778` (ReporterDisplay), `:785-787` (OwnerDisplay), `:793-796` (WatcherUserID). Classification: **BROKEN CHAIN in the reverse sense** — reachable from `/ui`, unreachable from MCP/REST. |
| **B** — `recall_slim.go` keep-list strips response fields | response | **yes** | `internal/mcp/recall_slim.go:60-71` keeps 11 item keys; `:86-87` rewrites `attrs` to `{structured_payload}` only; `:90-127` rewrites `commits` to body/by/replies. Set difference computed in §5.5: **19 of 32** item fields the domain returns never reach the MCP caller. The file's own INVARIANT note (`:52-59`) records the three historical swallows (#249 `total`, #269 truncation pair, #289). |
| **C** — `user_id`: chain intact, meaning changed | request (semantics + description) | **yes** — classified **LOGIC BREAK / RENAMED + DESCRIPTION MISMATCH** | Full four-hop derivation in §2.10. hop 1 `"Filter by user ID"` (`tools_lifecycle.go:136`) → hop 2 forwarded verbatim (`listWorkItemsStringParams`, `:40-45`) → hop 3 `{"user_id", &filter.UserID}` (`router.go:406`), field comment `// reporter user_id exact match (legacy)` (`work_items.go:898`) → hop 4 `wi.reporter_user_id = $n` (`work_items.go:1221`). `user_id → UserID → reporter_user_id`; promised set (work items related to a user) ⊋ implemented set (work items that user filed). |

---

## 2. `pf_list_work_items` — 21 published params, four assertions each

**Hop definitions (aihub#280):** hop 1 = `listWorkItemsSchema()` (`tools_lifecycle.go:108-227`),
the only text an LLM caller ever sees; hop 2 = `buildListWorkItemsParams`
(`tools_lifecycle.go:75-104`) → HTTP query string; hop 3 = `handleListWorkItems`
(`router.go:248-536`) → `domain.ListWorkItemsFilter` (`work_items.go:892-953`);
hop 4 = `buildListWorkItemsWhere` (`work_items.go:1147-1303`) / `listWorkItemsPage`
(`:1409-1539`) / `listWorkItemsByVector` (`internal/domain/wi_vector.go:241-393`).

**Decision rule.** *LOGIC BREAK* iff the set hop 1 promises for an input the
description admits ≠ the set hop 4 returns for it. A loud rejection (4xx naming the
parameter) is not a set mismatch. Silent behaviour on inputs the description does
*not* admit is recorded separately as a *policy deviation* (§4), because it is a
different claim with a different fix and the owner asked for decidable
comparisons, not a blended score.

**Hop-2 facts shared by every param (Measured):**
- 17 string params go through `scalarArg` (`internal/mcp/helpers.go:112`): string / number / bool → string; array / object / null → `""` → `setIfNonempty` (`:310`) does not forward. So a non-scalar shape on any string param is a **silent drop at hop 2**. Whether a real caller ever sends one is Unverified per param; the harness used here coerces to the declared type before the SDK (§6), so this client cannot even produce that shape.
- 2 boolean params go through `parseBoolArg` (`:55`): JSON bool, `strconv.ParseBool` spellings, 0/1 → forwarded only when `true`; anything else → error naming the param. `false` is deliberately not forwarded (`tools_lifecycle.go:88-97`).
- 2 CSV params go through `csvArg` (`:173`): string, or `[]any` of strings → comma-joined; `[]any` containing a non-string → `""` → not forwarded (locked by `tools_list_wi_schema_test.go:260`).
- Every published param is in exactly one forwarding table and vice-versa (`TestListWorkItemsEveryPublishedParamHasAWireProbe`, `tools_list_wi_schema_test.go:168-207`); hop-2 scanner (§5.2) independently reports 21/21 touched.

Format per param: **h1** promised set (description verbatim) · **h2** what is carried and in what shape · **h3** filter field + declared semantics · **h4** SQL predicate = implemented set · **Compare** h1 vs h4 · **Verdict**.

### 2.1 `project`
- **h1** (`tools_lifecycle.go:117-119`): `"Project name. Optional when `ids` or `similar_to` is given (each already names a work item); required otherwise. Omitting it widens the search to every project you can see."` → set: work items whose project is exactly this one; 403 if inaccessible (the `ids` text states the 403 for `project`).
- **h2**: `scalarArg` → `project=<verbatim string>`.
- **h3**: `project := c.QueryParam("project")` (`router.go:254`) — **raw, untrimmed**, the only scalar read without `trimmedParam`; `""` with no `ids`/`similar_to` → 400 `project is required` (`:304-305`); otherwise `checkProjectAccess(c, u, project, "viewer")` → 403 (`:362`); omitted-with-ids → `filter.AccessibleProjects` scoping (`:337`, `:358`). Passed as a separate argument, not a filter field.
- **h4**: `wi.project = $n` (`work_items.go:1152`) when set; `wi.project = ANY($n)` over AccessibleProjects when empty (`:1156`).
- **Compare**: equal sets. Shape asymmetry: `" aihub"` is a different project here but `" urgent"` is trimmed to `urgent` for priority (`:398-413` use `trimmedParam`, `:243-245`). Not a set mismatch for a conforming input.
- **Verdict: consistent** (note: untrimmed read).

### 2.2 `ids`
- **h1** (`:109-115`): `"Filter to these work item IDs or slugs (array of strings; a comma-separated string is also accepted). Makes `project` optional — an id already names one work item, and the query is bounded to the projects you can see. Note the asymmetry with `project`: an inaccessible project= is a 403, whereas ids you cannot see are silently omitted, so a short result means "not visible to you" as well as "does not exist"."` → set: items with id ∈ ids OR slug ∈ ids, within visible projects.
- **h2**: `csvArg` → `ids=a,b` (array or CSV string; array with a non-string → dropped, Measured `tools_list_wi_schema_test.go:260`).
- **h3**: `ids := queryCSV(c, "ids")` trimmed, empties dropped (`queryparam.go:243-245`); `ids=,` guard at `router.go:304` (`len(ids)==0 && similarTo==""` → 400, not "every project"); `filter.IDs = ids` (`:414-416`); AccessibleProjects scoping (`:312-359`).
- **h4**: `(wi.id = ANY($n) OR wi.slug = ANY($n))` (`work_items.go:1245`), inside the project/AccessibleProjects clause.
- **Compare**: equal sets; "id or slug" and "silently omitted" are both disclosed.
- **Verdict: consistent.**

### 2.3 `status`
- **h1** (`:121-122`): `"Filter by status; comma-separated for several (e.g. "running,paused"). An array of strings is also accepted."` → set: items whose status ∈ the given set. Declared type `string`.
- **h2**: `csvArg` → `status=wrapped` / `status=running,paused`.
- **h3**: `queryEnumCSV(c, "status", domain.WorkItemStatusValues())` (`router.go:371`; helper `queryparam.go:375-397`): unknown token → 400 naming the seven legal values, deduplicated.
- **h4**: `wi.status = ANY($n)` (`work_items.go:1182`).
- **Compare**: equal sets for legal values; illegal values rejected loudly. Live: `status=bogus_status_380` → HTTP 400 `invalid status "bogus_status_380": must be one of queued, running, paused, blocked, wrapped, failed, cancelled` (Measured, direct GET).
- **Verdict: consistent.** (Description does not list the vocabulary; the 400 does.)

### 2.4 `wi_type`
- **h1** (`:123`): `"Filter by work item type (e.g. fix_bug, feature)"` → set: items of exactly this type.
- **h2**: `scalarArg` → verbatim.
- **h3**: `wiType := trimmedParam(c, "wi_type")` (`router.go:382`) → `filter.WIType`.
- **h4**: `wi.wi_type = $n` (`work_items.go:1187`), exact, case-sensitive; column is free TEXT (no CHECK).
- **Compare**: equal.
- **Verdict: consistent.**

### 2.5 `kind`
- **h1** (`:124`): `"DEPRECATED alias for `wi_type`; an explicit wi_type wins. There is no separate `kind` field."` → set: same as `wi_type`, lower precedence.
- **h2**: forwarded as `kind=` verbatim.
- **h3**: `if wiType == "" { wiType = trimmedParam(c, "kind") }` (`router.go:382-388`) → same `filter.WIType`.
- **h4**: same `wi.wi_type = $n` (`:1187`).
- **Compare**: equal; precedence disclosed and implemented.
- **Verdict: consistent.** (Contrast with `pf_update_work_item.kind`, §5.4 finding F1, where the same alias is published but dropped.)

### 2.6 `priority`
- **h1** (`:125`): `"Filter by priority (urgent|high|normal|low)"` → set: items with exactly this priority; vocabulary of four.
- **h2**: `scalarArg` → verbatim.
- **h3**: table `{"priority", &filter.Priority}` via `trimmedParam` (`router.go:398-413`); **no vocabulary check**.
- **h4**: `wi.priority = $n` (`work_items.go:1192`); column CHECK `low|normal|high|urgent` (`internal/db/migrations/0002_work_items.sql:35`).
- **Compare**: equal for the four admitted values. For a value outside the vocabulary the description admits nothing; implemented = empty 200. Live: `priority=bogus_priority_380` → HTTP 200 `{"items":[],"next_cursor":null}` (Measured, MCP and direct GET).
- **Verdict: consistent** (set) · **policy deviation P1** (§4): Rule 1 of `queryparam.go:63-75` ("a token outside a closed vocabulary → 400") is applied to `status` and not to `priority`.

### 2.7 `milestone`
- **h1** (`:126`): `"Filter by milestone"` → set: items with exactly this milestone.
- **h2**: verbatim. **h3**: table → `filter.Milestone`. **h4**: `wi.milestone = $n` (`:1197`); nullable TEXT, NULL never matches.
- **Compare**: equal.
- **Verdict: consistent** (description restates the name; open vocabulary, so nothing to enumerate).

### 2.8 `scenario`
- **h1** (`:133-134`): `"Filter by scenario. In practice every work item is 'coding': the column is constrained to coding|writing|data and creation rejects all but coding."` → set: items with this scenario; discloses that only `coding` can match.
- **h2**: verbatim. **h3**: table → `filter.Scenario`. **h4**: `wi.scenario = $n` (`:1207`); CHECK `coding|writing|data` (`internal/db/migrations/0002_work_items.sql:18`); `CreateWorkItem` rejects non-coding with `ErrNotImplemented` (`work_items.go:312-314`).
- **Compare**: equal, and the description states the cross-hop fact rather than the field name. The owner's model of a coherent description.
- **Verdict: consistent.**

### 2.9 `label`
- **h1** (`:135`): `"Filter by label"` (singular) → set: items carrying this one label.
- **h2**: `scalarArg` → verbatim single string. An array shape would be dropped silently at hop 2 (shared fact above); this client cannot send one (§6).
- **h3**: table → `filter.Label` (`*string`, one label).
- **h4**: `$n = ANY(wi.labels)` (`:1212`) — membership in the `TEXT[]` column, exact.
- **Compare**: equal for the singular promise. Live: a literal string `["mcp"]` returned empty (this wi carries label `mcp`), confirming exact membership rather than substring or JSON parsing (Measured, MCP).
- **Verdict: consistent.**

### 2.10 `user_id` — **LOGIC BREAK (Control C)**
- **h1** (`:136`): `"Filter by user ID"` → the only reading a caller has: work items related to this user (reporter, current owner, watcher — the description names no role).
- **h2**: `listWorkItemsStringParams` (`:40-45`) → `scalarArg` → `user_id=<verbatim>`. Intact.
- **h3**: `{"user_id", &filter.UserID}` (`router.go:406`) → `ListWorkItemsFilter.UserID *string` whose own comment is `// reporter user_id exact match (legacy)` (`work_items.go:898`). Intact, and the field already says the meaning has narrowed. The UI never sets `UserID` (`ui_handlers_wi.go:744-796` sets ReporterDisplay/OwnerDisplay/WatcherUserID only), so the REST/MCP `user_id` is this field's only writer (Measured grep).
- **h4**: `wi.reporter_user_id = $n` (`work_items.go:1221`) → the set of items this user **filed**.
- **Compare**: h1 ≠ h4. Reporter ⊊ "related to": a wi also has a current attempt owner (`run_attempts`, the column `OwnerDisplay` reaches at `:1232-1237`) and watchers (`:1174-1179`), neither in this predicate. The excess of the promise returns nothing and nothing signals the narrowing. Live corroboration: `user_id=u_5dFjeaMZ&limit=5` → all five `reporter_user_id == u_5dFjeaMZ` (Measured, MCP; weak alone since the auditor filed most items — the SQL is the decisive evidence).
- **Verdict: LOGIC BREAK — RENAMED (`user_id → UserID → reporter_user_id`) AND DESCRIPTION MISMATCH** (`"Filter by user ID"` vs reporter-only exact match). Not fixed here (control).

### 2.11 `source`
- **h1** (`:137`): `"Filter by source"` → set: items with exactly this source. The description restates the field name; the vocabulary is not given.
- **h2**: verbatim. **h3**: table → `filter.Source`. **h4**: `wi.source = $n` (`:1202`); CHECK of seven values (`internal/db/migrations/0002_work_items.sql:27`).
- **Compare**: equal for legal values; illegal → silent empty 200 (same shape as `priority`; not live-probed, inferred from identical code path — settle with `GET /v1/work_items?project=aihub&source=bogus`).
- **Verdict: consistent** (set) · **policy deviation P1** · description gives no vocabulary for a closed one (weakest description after `user_id`).

### 2.12 `ready_only`
- **h1** (`:138-143`): `"Only return items that are ready to claim: queued, not requiring a human session, and with no unfinished blocking dependency. Same PREDICATE as pf_get_ready_queue's items[] (one shared SQL constant), but not the same page: this defaults to limit=50 ordered by created_at desc, while the ready queue defaults to 10 ordered by priority desc. With more ready items than either limit they return different subsets."`
- **h2**: `parseBoolArg` → `ready_only=true` only when true; unreadable → error (Measured `tools_list_wi_schema_test.go:135-145`).
- **h3**: `queryBool(c, "ready_only")` (`router.go:441`; unparseable → 400) → `filter.ReadyOnly`.
- **h4**: `readyOnlyPredicate` (`work_items.go:1120-1122`): `status = 'queued' AND requires_human_session = false AND <noLiveBlockerPredicate>` — the constant `pf_get_ready_queue` shares.
- **Compare**: equal. One tri-state nuance: `requires_human_session` is nullable and `= false` excludes NULL (unclassified). "not requiring a human session" admits either reading; the shared-constant disclosure lets a caller cross-check with the ready queue. Not counted as a break.
- **Verdict: consistent.**

### 2.13 `include_step_state`
- **h1** (`:144-148`): `"Attach each item's step state as `step_state` (current_step, current_step_status, step_started_at, ...). The key is ABSENT for a work item that has never been claimed — and also if the lookup itself failed, which is best-effort and reported only on the server's stderr. Absent therefore means "no step state", not "definitely never claimed"."`
- **h2**: `parseBoolArg` → `include_step_state=true`.
- **h3**: `queryBool` (`router.go:446`) → `filter.IncludeStepState`.
- **h4**: not a WHERE predicate; `attachStepState` (`work_items.go:1551-1590`) runs on all three return paths — similar_to (`:1423-1425`), vector query (`:1474-1476`), text (`:1537-1538`); errors go to stderr only (`:1587`).
- **Compare**: equal; the best-effort absence is disclosed.
- **Verdict: consistent.**

### 2.14 `since`
- **h1** (`:149-152`): `"Only items whose CREATED_AT is at or after this RFC3339 timestamp. This is creation time, not close time: combining it with status=wrapped does NOT give "wrapped since T" — an item created before T and wrapped after it is excluded. An unparseable value is rejected rather than ignored."`
- **h2**: verbatim. **h3**: `queryRFC3339(c, "since")` (`router.go:430`; `queryparam.go:224-235`, 400 on parse failure) → `filter.Since time.Time`.
- **h4**: `wi.created_at >= $n` (`work_items.go:1250`).
- **Compare**: equal, including the `>=` and the created-not-closed caveat.
- **Verdict: consistent.**

### 2.15 `query`
- **h1** (`:190-196`): `"Semantic search over goal+content (aihub#273): embedding cosine when the server has a provider, ILIKE fallback otherwise. Similarity-ordered; not combinable with sort/order/cursor; mutually exclusive with similar_to. 🔴 `similarity` compares only WITHIN one result set — it has no absolute meaning and there is no relevance filter, so ANY input returns a full page. Judge by `semantic.ranked_candidates` (you got the top len(items) of that many) and by reading the goals. For "like this work item" use similar_to."`
- **h2**: verbatim. **h3**: `q := c.QueryParam("query")` (`router.go:296`, raw); 400 with `similar_to` (`:477-481`); 400 with sort/order/cursor (`:482-487`); `filter.Query = &q` (`:489`).
- **h4**: `listWorkItemsPage:1435-1478` — vector path when a provider is active; on vector error without a floor (`:1451-1452`, stderr only) or an empty vector result without a floor (`:1453-1470`) it falls through to the text path `(wi.goal ILIKE '%'||$n||'%' OR wi.content ILIKE ...)` (`:1259`). With a floor, a vector error is returned as the server error (`:1438-1450`) and an empty vector page is returned as the answer through the default branch (`:1471-1478`).
- **Compare**: equal for "provider present" and "no provider". The description's "otherwise" also covers two undisclosed fallback triggers (vector error; zero embedded rows, e.g. pre-backfill). The caller can tell which path answered by the presence of the `semantic` block (documented in `docs/mcp-tools.md`, not in the param text).
- **Verdict: consistent** (note: fallback trigger under-described; the difference is observable in the response).

### 2.16 `similar_to`
- **h1** (`:197-205`): `"Document→document recall (aihub#277): a work item id or slug whose STORED goal+content vector becomes the query vector — far sharper than approximating it with a one-line query=. Makes `project` optional the way `ids` does. The source is scoped like the results: 404 outside that scope, 412 if it has no embedding yet. It is an ordinary row in its own results (so at similarity 1.0, unless one of your other filters excludes it) — to confirm which row was used, read `semantic.source_work_item_id`, which is unconditional. `similarity` is still only comparable within this one result set. Excludes query=; no sort/order/cursor."`
- **h2**: verbatim (`#` percent-encoded on the wire, decoded by the server; probe `tools_list_wi_schema_test.go:103-106`).
- **h3**: `trimmedParam(c, "similar_to")` (`router.go:297`); makes project optional (`:304`); mutual exclusion and sort/order/cursor 400s as above; `filter.SimilarTo` (`:492`).
- **h4**: handled first, never falls to ILIKE, does not consult the provider (`work_items.go:1410-1427`); `semanticQuerySource` → 404 `ErrNotFound` (`wi_vector.go:224`) / 412 `ErrPreconditionFailed` (`:234`); `listWorkItemsByVector` reuses the WHERE with the same model+dims (`:287`), `ORDER BY wi.emb_vector <=> $vec` (`:338`), `LIMIT f.Limit` (`:339`), `SourceWorkItemID` always set (`:386`).
- **Compare**: equal, including the 404/412 contract and the unconditional source id.
- **Verdict: consistent.**

### 2.17 `min_similarity`
- **h1** (`:206-211`): `"Opt-in cosine floor for the vector path; a JSON number is also accepted. Must be in [0,1]. 0 is the default and means OFF, so sending 0 is always accepted and always a no-op; any value above 0 requires query= or similar_to= and is a 400 otherwise, never a silent no-op. 🔴 No globally valid value exists, so none is ever defaulted: measured, garbage and real queries overlap on every similarity-derived statistic."`
- **h2**: `scalarArg` → `min_similarity=0.8` (number stringified; probes `:114-119`).
- **h3**: `queryFloatInRange(c, "min_similarity", 0, 1)` (`router.go:509`; outside → 400) → `filter.MinSimilarity`.
- **h4**: vector path `(1 - (wi.emb_vector <=> $vec::vector)) >= $n` (`wi_vector.go:303-306`); text path with a floor → 400 (`work_items.go:1489-1494`). Live: `min_similarity=0.5` with no query → HTTP 400 `min_similarity applies only to the vector path; ...` (Measured, direct GET).
- **Compare**: equal for every 200; every divergence is a 400 naming the parameter. "requires query= or similar_to=" is necessary, not sufficient — `query=` on a server with no provider is also a 400 (`:1489-1494`, and the 400 text says so).
- **Verdict: consistent** (note: description states a necessary condition as if sufficient; the failure is loud).

### 2.18 `limit`
- **h1** (`:212-214`): `"Max items to return (default 50, ceiling 200). A JSON number is also accepted, and is what most callers send. A value above 200 is served as 200 and reported in `request_adjusted`; a value that is not an integer is rejected with 400."` Declared type `string` (disclosed).
- **h2**: `scalarArg` → `limit=50` (number → string; probes `:128-132`).
- **h3**: `queryInt(c, "limit")` (`router.go:457`; `Atoi`, 400 on non-integer) → `filter.Limit` as sent (`:462`).
- **h4**: `NormalizeListWorkItemsLimit` (`work_items.go:1377`): non-positive → 50, `> 200` → 200, adjustment reported in `request_adjusted`; text path `LIMIT f.Limit+1` (`:1331`) for cursor look-ahead, truncated to `f.Limit` (`:1527-1529`); vector path `LIMIT f.Limit` (`wi_vector.go:339`). Live: `limit=abc` → HTTP 400 `limit must be an integer, got "abc"` (Measured, direct GET).
- **Compare**: equal; every adjustment is reported.
- **Verdict: consistent.**

### 2.19 `cursor`
- **h1** (`:215-216`): `"Pagination cursor. Carries the value of the column named by `sort`, so pass it back unchanged and do not mix cursors between different sort orders."` → set: the page strictly after this position in the same ordering.
- **h2**: verbatim.
- **h3**: `if cursor := c.QueryParam("cursor"); cursor != "" { filter.Cursor = &cursor }` (`router.go:464-465`) — **raw; no parse, no validation**; 400 only when combined with query/similar_to (`:483-485`).
- **h4**: `<sortCol> <op> $n::timestamptz` (`work_items.go:1285`), strict `<`/`>` per `order` (`listWorkItemsSort`, `:1044-1053`); `next_cursor` = last row's sort column (`:1062-1072`, `:1529`).
- **Compare**: equal for a cursor the server issued. Live: `cursor=2026-09-06T12:55:21.1431Z&limit=1` (the `next_cursor` from a previous page) → HTTP 200 with exactly the next older item (Measured, direct GET).
- **Verdict: consistent** (set) · **DEFECT D1 (§4)**: a malformed cursor is not a 400; it is a **200 with an empty page**, because the text-path scan loop never checks `rows.Err()` and the failed `::timestamptz` cast is swallowed. Evidence in §4.

### 2.20 `sort`
- **h1** (`:220-223`): `"Sort column (default created_at). closed_at returns ONLY closed items — a NULL close time has no position in that ordering."`, enum `[created_at, closed_at]` from `domain.ListWorkItemsSortValues()` (locked by `TestListWorkItemsToolPublishesServerEnums`).
- **h2**: verbatim. **h3**: `domain.NormalizeListWorkItemsSort` (`router.go:520`; `work_items.go:1014-1034`: trim, lowercase, default `created_at`, 400 on unknown); 400 with query/similar_to.
- **h4**: `ORDER BY <col> <dir>` (`:1331`); `sort=closed_at` adds `wi.closed_at IS NOT NULL` (`:1277`).
- **Compare**: equal; the narrowing is disclosed.
- **Verdict: consistent.**

### 2.21 `order`
- **h1** (`:224-225`): `"Sort direction (default desc)"`, enum `[desc, asc]`.
- **h2**: verbatim. **h3**: same normalizer, 400 on unknown. **h4**: `ASC`/`DESC` plus the matching cursor operator (`:1044-1053`).
- **Compare**: equal.
- **Verdict: consistent.**

### 2.22 Summary for `pf_list_work_items`

| result | count | params |
|---|---|---|
| hop 1 == hop 4 (consistent) | **20** | project, ids, status, wi_type, kind, priority, milestone, scenario, label, source, ready_only, include_step_state, since, query, similar_to, min_similarity, limit, cursor, sort, order |
| **LOGIC BREAK** (hop 1 ≠ hop 4) | **1** | **user_id** — promised "related to user", implemented `wi.reporter_user_id = $n`; renamed at hop 3 and hop 4; description restates the field name |
| policy deviations / defects outside the set-mismatch rule | 2 classes | D1 cursor (error masking, §4); P1 priority/source (silent empty on a closed vocabulary, §4) |
| description under-specifies (no set mismatch) | 3 | source (no vocabulary), query (fallback triggers), min_similarity (necessary stated as sufficient) |

Connectivity (hop 1 → 2 → 3 → 4 all present): **21/21** (Measured: forwarding-table test + scanner + router reads + SQL cites above). This is exactly why connectivity alone would have scored `user_id` green.

---

## 3. Hop 0 — the harness and the SDK

- **Unknown parameters are dropped silently somewhere between the model and SQL** (Measured, live): `pf_list_work_items(project="aihub", ids=["wi_dvKPd9px"], nonexistent_param_380="probe")` returned the item normally, no warning. Hop 2 drops it by construction (only table keys are forwarded); whether the harness or the MCP SDK drops it *before* hop 2 is **Unverified** — indistinguishable from the caller's side; settle with SDK-level request logging.
- **The harness coerces to the declared type** (Measured, live): a value typed as `["mcp"]` for the `string` param `label` reached SQL as the literal string `["mcp"]` (empty result on a wi that carries `mcp`; a dropped filter would have returned three items). So the "array on a string param → silent hop-2 drop" shape cannot be produced from this client; other clients (raw SDK) may.
- Per-call validation: none in the SDK for the untyped `AddTool` form every aihub tool uses (recorded at `tools_list_wi_schema_test.go:77-81`; consistent with the two probes above).

---

## 4. Defects the set-mismatch rule does not count

### D1 — `listWorkItemsPage` never checks `rows.Err()`: a failing list query is a 200 with an empty page

- **Observed** (Measured, live, both paths): `cursor=garbage-not-a-timestamp&limit=1` → MCP returned `{"items":[],"next_cursor":null}`; direct `GET /v1/work_items?project=aihub&cursor=garbage-not-a-timestamp&limit=1` → **HTTP 200** `{"items":[],"next_cursor":null}`. On the same server, `limit=abc` → 400 and `status=bogus` → 400 (instrument control).
- **Mechanism** (Measured): hop 3 passes the cursor unparsed (`router.go:464-465`); hop 4 casts it `$n::timestamptz` (`work_items.go:1285`), which Postgres rejects at execute time; pgx surfaces execute-time errors only through `rows.Err()` ("Only errors encountered sending the query and initializing Rows will be returned. Err() on the returned Rows must be checked after the Rows is closed" — pgx v5 `conn.go` Query doc, lines 681-682 of the v5.5.5 copy in the module cache; go.mod pins v5.9.2, same contract). The text-path scan loop `for rows.Next() {...}` at `work_items.go:1505-1524` has **no `rows.Err()` check**, so the loop ends with zero rows and the handler returns an empty 200. The two sibling loops do check: `attachStepState` (`:1587`) and the vector path (`wi_vector.go:372`).
- **Blast radius** (inferred from the mechanism): not limited to `cursor`. Any execute-time failure of the text-path list statement — statement timeout, cancelled context mid-stream, a connection dropped after Parse — is returned as "no items", indistinguishable from an empty project. `cursor` is merely the one published parameter through which a caller can trigger it. The MCP client cannot see it either: `pkg/client/client.go:101-111` only converts HTTP ≥ 400 into errors, and this is a 200.
- **Rule 1 violation** (`queryparam.go:63-75`): a malformed value must be a 400 naming the parameter; here it is silent, and silent in the dangerous direction for pagination (empty page reads as "no more pages").
- Not fixed here (read-only wi). Tracked as a follow-up work item (§9).

### P1 — closed vocabularies without a hop-3 check: `priority`, `source` (and `scenario`)

- `status` gets `queryEnumCSV` and a 400 (`router.go:371`); `priority`, `source`, `scenario` are read by the scalar table (`:398-413`) with no vocabulary check although each column has a CHECK constraint. An unknown value is a 200 empty page. Live: `priority=bogus_priority_380` → 200 `{"items":[]}` (Measured). `source` and `scenario`: same code path, not live-probed (Unverified; the GET is one line).
- The server's own policy file names this exact shape as Rule 1 and cites `?status=notastatus` as the precedent it closed (`queryparam.go:369-372`); the closure did not extend to the other three closed vocabularies.

---

## 5. Mechanical pass — the other 49 tools

**Boundary, stated up front.** This section is static set-difference only. It says which
published names are read downstream and which returned fields are projected away. It
does **not** claim developer-grade logical review of any of these 49 tools; a tool
listed as "clean" here has connectivity, not verified semantics (§2.10 is the proof
that those differ).

### 5.1 Method (Measured, reproducible)

1. **Hop 1**: `polyforge dump-mcp-schemas` built from this commit (`internal/cli/dump_schemas.go`; `mcp.New(nil,nil)` enumerated over an in-memory transport) → **50 tools, 240 published params**.
2. **Hop 2**: a stdlib `go/ast` scanner over `internal/mcp/*.go` (non-test). For each `s.mcp.AddTool(...)` it seeds the args map from `args, err := parseArgs(req.Params.Arguments)` (45/50 handlers; the other 5 take no args) and records every key read/written/deleted via the helper decoders, direct indexing, or `delete`; resolves loop variables over package-level `[]string` vars, inline `[]string{}` literals and map-literal keys; follows same-package callees that receive the map or call `parseArgs` themselves; records **WHOLESALE** when the whole map is passed to `s.client.X(args)`, ranged over, or embedded in a literal; records projection calls (`slim*`/`suppress*`/`brief*`) and the expression handed to `jsonResult`/`jsonResultCompact`/`textResult`. Zero unresolved sites in the final run.
3. **Hop 3 for the wholesale tools**: the server-side bind struct's json tags (echo `c.Bind`); `DisallowUnknownFields` is used nowhere under `internal/`, `pkg/`, `cmd/` (Measured grep), so a forwarded key with no tag is dropped silently.
4. **Response**: the projection inventory from step 2 plus reading the three projection files.

The scanner is scratch tooling and was deleted; it is described here so the pass can be redone.

### 5.2 Hop 1 → hop 2 results

| class | tools | meaning |
|---|---|---|
| published == touched, no wholesale, no unresolved | **40** | every published key is read by name in the MCP handler and no unpublished key is read |
| wholesale `s.client.X(args)` | 5 — pf_create_work_item, pf_create_project, pf_create_user, pf_predict_conflicts, pf_remember | hop 2 filters nothing; hop 3 struct decides (§5.3) |
| wholesale `range over args` (all keys except one or two) | 4 — pf_create_api_key (`tools_users.go:103`), pf_update_project (`tools_projects.go:113`), pf_update_user (`tools_users.go:69`), pf_update_work_item (`tools_lifecycle.go:734`) | same |
| touched-but-not-published | 1 — pf_recall: `cursor`, `recall_algo` | keys the handler forwards that the schema does not offer (§5.4 F2, F3) |
| published-but-not-touched (after hop-3 check) | 1 param — `pf_update_work_item.kind` | §5.4 F1 |

`pf_list_work_items` itself: 21 published / 21 touched (agrees with `tools_list_wi_schema_test.go`).

### 5.3 Hop 2 → hop 3 for the 9 pass-through tools (Measured: struct json tags)

| tool | published (excl. path/local) | server bind struct | published − struct | struct − published |
|---|---|---|---|---|
| pf_create_work_item | 16 | `domain.CreateWorkItemRequest` (`work_items.go:100-117`) 16 | ∅ | ∅ |
| pf_predict_conflicts | 4 | `domain.PredictConflictsRequest` (`conflicts.go:34`) 4 | ∅ | ∅ |
| pf_create_project | 5 | `domain.CreateProjectRequest` (`projects.go:83`) 5 | ∅ | ∅ |
| pf_create_user | 5 | anon struct `router.go:1005` (email, display_name, user_type, role, author_aliases) | ∅ | ∅ |
| pf_create_api_key | name, project_scope (`user_id` is the path) | anon struct (name, project_scope) | ∅ | ∅ |
| pf_update_project | 7 (`name` is the path) | `domain.UpdateProjectRequest` (`projects.go:98`) 7 | ∅ | ∅ |
| pf_update_user | display_name, role (`id` is the path) | anon struct (display_name, role, author_aliases) | ∅ | **author_aliases** (accepted, unpublished on update; published on pf_create_user only) |
| pf_remember | 12 | `domain.RememberRequest` (`memory.go:563`) 18 | ∅ | attempt_id, claim_epoch, session_secret (attempt-binding fields injected by pf_save_artifact / pf_emit_event from the state file, `tools_memory.go:589-591`, `tools_events.go:32-34`); rendered_html, structured_payload (artifact-only; pf_save_artifact publishes `html`/`structured_payload`); **tags** (publishable only via pf_update_memory) |
| pf_update_work_item | 15 (`work_item_id` path, `brief` local) | `domain.UpdateWorkItemRequest` (`work_items.go:120-138`) 14 | **kind** | ∅ |

### 5.4 Findings

**F1 — `pf_update_work_item.kind`: BROKEN CHAIN at hop 3 (silent drop).** Published as `"kind": prop("string", "Updated kind")` (`tools_lifecycle.go:668`); forwarded in the JSON body by the range loop that excludes only `work_item_id` and `brief` (`:734`); the handler binds `domain.UpdateWorkItemRequest` (`router.go:566-567`), which has `wi_type` but **no `kind` tag** (`work_items.go:120-138`); no `UnmarshalJSON`, no `DisallowUnknownFields`, and no other reader of `"kind"` in `internal/server` or `internal/domain` outside the list handler (`router.go:384`) and the dependency path param (`:977`) (Measured grep). Unlike the list tool, the MCP update handler does not translate `kind` → `wi_type` (the only `"kind"` occurrences in `tools_lifecycle.go` are lines 41, 124, 668). ⇒ a caller who sets `kind` gets a 200 and an unchanged `wi_type`. Impact today: no skill under `plugins/` passes `kind` to `pf_update_work_item` (Measured grep: zero hits), so the contract lies without a current victim. Live confirmation would need a write (`pf_update_work_item(kind=<other>)` on a throwaway wi, then compare `wi_type`) — not performed in a read-only audit; **Unverified** as to live behaviour, Measured as to code.

**F2 — `pf_recall` returns `next_cursor` but publishes no `cursor`.** The handler forwards `cursor` (`recallStringParams`, `tools_memory.go:321`); the server reads it (`routes_memory.go:317`); the response keeps `next_cursor` (`recall_slim.go:134-135`); the InputSchema has no `cursor` (Measured: dump lists 11 params — fields, include_archived, min_strength, project, query, recency_weight, similarity_threshold, top_k, type, visibility, work_item_id). Given §3 (unknown params dropped before hop 2 from at least this harness), a caller following the published contract cannot page `pf_recall`. Only `pf_list_work_items` publishes a `cursor` at all (Measured over all 50 schemas).

**F3 — `pf_recall.recall_algo`: unpublished knob, deliberate.** Read at `tools_memory.go:447-450`, explicit arg wins over the env `POLYFORGE_RECALL_ALGO` (comment `:444-446`). Not published by any tool. Recorded, not judged.

**F4 — server-accepts-but-MCP-does-not-publish** (reverse direction, from §5.3): `pf_update_user.author_aliases`; `pf_remember.tags` (and the artifact/attempt fields, which are correctly unpublished on `pf_remember`). Plus the three Control-A filter fields on the list tool.

### 5.5 Response direction

Projection layers found (Measured, scanner + reading): **three**.

| layer | applied by | shape | strips |
|---|---|---|---|
| `slimListWorkItemsResult` (`list_wi_slim.go:157`) | pf_list_work_items | delete-list | `content` only when null (`:204`); `external_share_type`, `external_share_key`, `milestone`, `parent_work_item_id`, `closed_at`, `current_attempt_id` only when null (`:217`, set at `:330`). **Strips no non-null field.** |
| `slimRecallResultMode` (`recall_slim.go`) | pf_recall (`tools_memory.go:77`) | **keep-list** | see Control B below |
| `suppressContentEcho` (`wi_echo_slim.go:138`) | pf_create_work_item (`tools_lifecycle.go:470`), pf_batch_create_work_items (`:567`), pf_update_work_item (`:749`) | delete-list of one key | `content`, only when byte-equal to what the caller sent |

**Control B set difference** — `Memory` + `MemoryWithStrength` (`memory.go:504-560`) carry 32 item fields; keep-list keeps 11 (`id, type, content, effective_strength, similarity, work_item_id, tags, related, created_at, content_truncated, content_full_len`); 2 are rewritten (`attrs` → `{structured_payload}` only; `commits` → body/by/replies). **Stripped: 19** — `project, author_user_id, author_display, visibility, is_immortal, base_strength, stability_days, last_activated_at, last_activated_by, activation_count, expires_at, source_artifact_id, emb_model, emb_dims, status, rendered_html, latest_id, updated_at, backlinks`. Top level: all six `RecallResponse` fields (`items, next_cursor, total, unmatched_types, unmatched_types_error, request_adjusted`) are copied conditionally (`recall_slim.go:134, 142, 151, 157, 181`). The keep-list is opt-in, so the next field the REST response adds is stripped by default — the file's own INVARIANT note says so.

Result-expression inventory for the other 47 tools (scanner): **40** hand back `result` unprojected; **7** construct their own shape — pf_claim_work_item and pf_force_takeover (`safeResult`), pf_commit and pf_ship (`gate.report(...)`), pf_wrap (`wrapResult`), pf_push and pf_batch_create_work_items (literals), pf_diff (no JSON result). For those 7 a domain-vs-MCP field difference needs the constructed shape and the domain type side by side; **not classified here** (boundary).

### 5.6 Counts for the 49 (mechanical only)

- Request direction — published param that nothing downstream reads: **1** (`pf_update_work_item.kind`, F1). 240 − 21 = 219 params examined at hop 1→2; 40 tools clean at hop 2; 9 tools pass-through with hop 3 checked against struct tags; 0 unresolved sites.
- Request direction, reverse — read/accepted but unpublished: `pf_recall.cursor`, `pf_recall.recall_algo`, `pf_update_user.author_aliases`, `pf_remember.tags` (+ the list tool's three Control-A fields).
- Response direction — fields the domain returns that the MCP layer strips: **19** item fields on `pf_recall` (Control B); **0** non-null fields on `pf_list_work_items`; **1** conditional key on the three echo-suppressed tools; 40 tools unprojected; 7 constructed shapes unclassified.

---

## 6. Live probe log (all 2026-09-06, read-only GETs)

| via | request | result |
|---|---|---|
| MCP | `pf_list_work_items(project="aihub", ids=["wi_dvKPd9px"], nonexistent_param_380="probe")` | normal item; unknown param silently dropped |
| MCP | `pf_list_work_items(project="aihub", user_id="u_5dFjeaMZ", limit=5)` | 5 items, all `reporter_user_id == u_5dFjeaMZ` |
| MCP | `... cursor="garbage-not-a-timestamp", limit=1` | `{"items":[],"next_cursor":null}` — no error |
| MCP | `... priority="bogus_priority_380", limit=3` | `{"items":[],"next_cursor":null}` |
| MCP | `... label=["mcp"], limit=3` | `{"items":[]}` — value reached SQL as the literal string `["mcp"]` (harness coercion) |
| curl | `GET /v1/work_items?project=aihub&cursor=garbage-not-a-timestamp&limit=1` | **HTTP 200** `{"items":[],"next_cursor":null}` |
| curl | `GET ...?project=aihub&limit=abc` | HTTP 400 `limit must be an integer, got "abc"` |
| curl | `GET ...?project=aihub&priority=bogus_priority_380&limit=1` | HTTP 200 `{"items":[],"next_cursor":null}` |
| curl | `GET ...?project=aihub&status=bogus_status_380&limit=1` | HTTP 400 `invalid status "bogus_status_380": must be one of queued, running, paused, blocked, wrapped, failed, cancelled` |
| curl | `GET ...?project=aihub&cursor=2026-09-06T12:55:21.1431Z&limit=1` | HTTP 200, one item, the next older by `created_at` |
| curl | `GET ...?project=aihub&min_similarity=0.5&limit=1` | HTTP 400 `min_similarity applies only to the vector path; ...` |

---

## 7. The wi's Unverified group — what this run settled

| item | status |
|---|---|
| "hop 0 silently drops unknown params" | **Settled — yes** (Measured probe, §3); *who* drops it (harness vs SDK) still Unverified |
| "the other 48 tools have real gaps" (inference) | **Replaced by counts** (§5.6): one hop-3 silent drop (`pf_update_work_item.kind`), one unpublished pagination param (`pf_recall.cursor`); 40 tools clean at hop 2 |
| "six-hop" framing | hop 0 (harness/SDK) and hop 5 (response projection) are real and were exercised; the audit form (one assertion per hop) held for hops 1-4 and needed a different instrument for 0 and 5 |
| controls A/B/C detectable by the method | **Settled — all three detected** (§1) |
| `user_id` semantics live | corroborated (§2.10); the decisive evidence remains the SQL |
| D1 mechanism (`rows.Err()`) on the pinned pgx v5.9.2 | doc text read from the v5.5.5 copy in the module cache; **Unverified for 5.9.2 specifically** — settle with `go mod download github.com/jackc/pgx/v5@v5.9.2 && grep -n "Err() on the returned Rows" $(go env GOMODCACHE)/github.com/jackc/pgx/v5@v5.9.2/conn.go`; the live 200 does not depend on it |
| `source` / `scenario` silent-empty on unknown value | inferred from the shared code path; **Unverified live** (one GET each) |
| `pf_update_work_item.kind` dropped live | Measured statically; **Unverified live** (needs a write) |

---

## 8. What was deliberately not done

- No `.go` file under `internal/` was modified; no description, keep-list or filter field was corrected (three of them are controls).
- No pull request; no `pf_ship` / `pf_push` (both force-push). The report is committed to the wi's branch with a native, non-force `git push -u origin HEAD`.
- No developer-grade semantic review of the 49 other tools: the owner's addendum scopes that depth to one tool until the method is proven on the controls, which it now is.

## 9. Follow-ups

Tracked as work items rather than fixed here (IR2): D1 (`listWorkItemsPage` swallows execute-time errors into an empty 200; `cursor=garbage` reproduces) and F1 (`pf_update_work_item.kind` published but dropped at hop 3). Control C (`user_id`), P1 and F2 are left to the owner's decision after reading this report, per the wi's "no treatment decided" scope. Work item ids are recorded in the wi's wrap note.
