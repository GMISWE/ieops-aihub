# Memory recall ranking: never-activated memories are exiled below activated ones

**wi**: aihub#236 (`wi_RVfgEGWu`) · **reporter**: jason.z · **date**: 2026-08-06

> # 🗄️ ARCHIVED — historical record
>
> **aihub#236 is `wrapped` (closed 2026-08-07); this design shipped.** The
> diagnosis below is the authoritative account of *why* the bug existed and why
> `GREATEST` was chosen over `COALESCE` — that reasoning is preserved verbatim
> and is the reason this file is archived rather than deleted. For what the code
> does *now*, read the code: `internal/domain/memory.go` (`recallRouted`,
> `memoryRefTime`, `memRefTimeSQL`) and `internal/domain/memory_vector.go`.
>
> **Erratum (aihub#352, 2026-09-03).** §2's *"These fields MUST be tagged
> `json:"-"`"* is **wrong as a defence**, and the shipped code says so at the
> callsite. `json:"-"` stops `encoding/json` only; echo's `DefaultBinder` sends
> `application/xml` and `text/xml` bodies to `encoding/xml`, which ignores json
> tags and falls back to the Go field name, so
> `<ActivationCount>9999</ActivationCount>` bound straight through — exactly the
> privilege escalation §2 set out to prevent. The real guard zeroes the trio
> *after* `Bind` (`b41c60f`); see `internal/server/routes_memory.go`
> (`handleRemember`). The reasoning in §2 about *why* activation state must be
> server-derived remains correct; only the mechanism it prescribes was
> insufficient.
>
> **All line numbers have been removed** (aihub#352) and replaced with
> file-plus-symbol anchors, which `scripts/pf_docs_contract_check.py` checks.
> Measured before removal: of the 23 line-number citations in this file and the
> plan, 18 pointed at the wrong line.

## Summary

`domain.Recall`'s default ordering uses `NULLS LAST`, which splits results into two hard
tiers: every memory that has *ever* been activated outranks every memory that has never been
activated, regardless of age. Because `pf_update_memory` mints each new version with
`last_activated_at = NULL`, editing a document pushes it into the lower tier. Combined with
the UI list's 50-row cap and absence of pagination, recently-written documents become
unreachable.

The fix defines a single **reference time** — the most recent of `last_activated_at` and
`created_at` — and uses it consistently for both filtering and ordering, then carries
activation state forward across versions so an edit no longer resets it.

## Reported symptom

Two `methodology.*` artifacts written 2026-08-05 could not be found in the `/ui/memories`
list, while `pf_recall` and `polyforge artifact view` both returned them intact:

- `mem_7sHjIJkp` — `methodology.spec`, `visibility=project`, `effective_strength≈3.00`
- `mem_mfuPOw2V` — `methodology.execute`, `visibility=project`, `effective_strength≈3.00`

Both are `status=active`, both are the `latest_id` head of a lineage with 4 archived prior
versions, both have `activation_count=0` on the head with activation history stranded on the
archived rows. In an unfiltered `pf_recall(project=ieops)` they ranked **#54** and **#61**,
while a 2026-07-24 `fact.reference` with `activation_count=1` ranked **#10**.

## Root cause

### The system holds two disagreeing definitions of "how fresh is this memory"

| definition | site | semantics |
|---|---|---|
| strength / filtering | `MemoryStrength`, `internal/domain/memory.go` | `last_activated_at` **if set, else** `created_at` — a *fallback* |
| ordering | `Recall`, `internal/domain/memory.go` | `last_activated_at DESC **NULLS LAST**, created_at DESC` — a *tier* |

Identical intent, incompatible implementation. The ordering treats "never activated" as a
sorting class rather than as a value, so `created_at` is only ever consulted *within* the
NULL bucket — never against an activated row. A document written ten minutes ago sorts below
a 2024 document activated once.

This is exactly the observed inversion: the 07-24 document (`activation_count=1`) sits in
tier 1 at #10; the 08-05 documents (`activation_count=0`) sit in tier 2 at #54/#61.

It also explains the apparent contradiction in the report — both lost documents show
`effective_strength≈3.00`, the maximum. Strength is computed from the *fallback* definition,
which correctly treats a fresh never-activated document as fresh. Ranking uses the *tier*
definition, which does not. The score shown to clients and the score used to sort disagree.

### Every edit pushes a document down

`Remember`'s INSERT (`internal/domain/memory.go`, `Remember`) hardcodes
`activation_count` to the literal `0` and omits `last_activated_at` from the column list entirely, so it defaults to NULL.

`UpdateMemory` (`internal/domain/memory.go`) creates each new version by rebuilding a
`RememberRequest` from the lineage head. It copies `Project`, `Type`, `WorkItemID`,
`Visibility`, `Tags`, `Content`, `Attrs`, `BaseStrength` and `SupersedesMemID` — but not the
activation trio. So each edit produces a head in tier 2 and leaves the activation history on
the archived row it superseded. Repeated editing actively demotes a document.

### Nothing can reach past the cap

`handleUIMemories` (`internal/server/ui_handlers_memory.go`) defaults
`limit=50` (max 200) and passes **no cursor** — `grep Cursor internal/server/ui_handlers_memory.go`
returns zero hits. There is no pagination in the memories UI. A row ranked past `limit` is not
on "page 2"; it is unreachable. Rank #54 under a 50-row cap is invisible.

The intermittency (「偶现」) is the tier-1 population growing: each activation anywhere in the
project moves another row above the cap, so which documents fall off the end drifts over time.

```d2
direction: right

clients: "Callers" {
  ui: "GET /ui/memories\nlimit=50, no cursor"
  mcp: "pf_recall / GET /v1/memories\ntop_k, cursor"
}

recall: "domain.Recall  (internal/domain/memory.go)" {
  reftime: "reference time\nTWO definitions disagree"
  filter: "WHERE min_strength\nCOALESCE(last_act, created)"
  order: "ORDER BY\nlast_act DESC NULLS LAST"
  cursor: "cursor predicate\ntwo-branch NULL split"

  filter -> order
  order -> cursor
}

db: "memories table" {
  t1: "tier 1 — last_activated_at IS NOT NULL\nranked by activation time"
  t2: "tier 2 — last_activated_at IS NULL\nALWAYS below tier 1"
}

write: "Write paths" {
  remember: "Remember INSERT\nactivation_count = 0 (literal)\nlast_activated_at omitted -> NULL"
  update: "UpdateMemory\ncopies 9 fields,\nDROPS activation trio"
  activate: "Activate\nUPDATE sets last_activated_at\n+ stability_days"
}

clients.ui -> recall.filter: "TopK=50"
clients.mcp -> recall.filter: "TopK, Cursor"
recall.order -> db.t1
recall.order -> db.t2: "unreachable past cap"

write.remember -> db.t2: "every new memory starts here"
write.update -> db.t2: "every EDIT lands here\n(demotes the doc)"
write.activate -> db.t1: "only path out of tier 2"

lost: "mem_7sHjIJkp #54\nmem_mfuPOw2V #61\neffective_strength 3.00" {
  style: {
    fill: "#ffe0e0"
  }
}
db.t2 -> lost: "reported as missing"
```

## Ruled out: the "Mine view / membership" hypothesis

The wi proposed that the `Mine` view diverges from MCP on `latest_id` resolution or on
`visibility=project` membership. This is not the cause:

- **There is no `Mine` view for memories.** `grep -ri '\bmine\b'` across the repo hits only
  `internal/server/ui_handlers_wi.go` and `ui_handlers_queue.go` — `Mine` is a *work-item*
  concept (an `?owner=` filter). The memories list has no author filter at all; its
  parameters are `project`, `type`, `strength_min`, `q`, `wi`, `limit`. The report's inference
  ("the reporter has only 8 memories in `ieops`, so all 8 should be visible under any
  pagination") rests on a filter that does not exist — the page lists *all* project memories,
  ranked, capped at 50.
- **`visibility=project` is not gated out.** `memoryVisibleTo`
  (`internal/server/ui_handlers_memory.go`, `memoryVisibleTo`) rejects only `private` (non-author) and `admin`; `project`
  falls through to `return true`. The page-level gate in `handleUIMemories` checks
  `u.ProjectRoles[project]`, the same field `pf_whoami` reports as `writer`.

**Separately confirmed, and genuinely a bug — but not this one:** `pf_whoami` returns two
contradictory membership answers for one user. Reproduced on `dahe.p` during this
investigation: `project_roles.aihub = "writer"` alongside
`projects[].aihub = {relation: "public", role: "viewer"}`. Filed as an observation here; not
addressed by this spec.

## Design

### 1. One reference time, defined once

> **reference time** = the most recent of `last_activated_at` and `created_at`.

- SQL: `GREATEST(last_activated_at, created_at)`
- Go: a `memoryRefTime(lastActivatedAt *time.Time, createdAt time.Time) time.Time` helper

Both are **total** — never NULL, never tiered. `created_at` is `NOT NULL`
(`internal/db/migrations/0006_events_memories.sql`), and PostgreSQL's `GREATEST` *ignores*
NULL arguments, returning NULL only when every argument is NULL. This differs from Oracle and
MySQL, where any NULL argument yields NULL, so it is pinned by an explicit test rather than
trusted.

Four sites currently spell this concept out inconsistently; all four adopt the shared form:

| site | before | after |
|---|---|---|
| `Recall` default `ORDER BY` | `last_activated_at DESC NULLS LAST, created_at DESC` | `GREATEST(last_activated_at, created_at) DESC` |
| `Recall` `min_strength` filter | `COALESCE(last_activated_at, created_at)` | `GREATEST(last_activated_at, created_at)` |
| `Recall` lexical secondary sort | `COALESCE(last_activated_at, created_at)` | `GREATEST(last_activated_at, created_at)` |
| `MemoryStrength` | `ref = *lastActivatedAt` when non-nil | later of the two |

The two `COALESCE` sites are not bugs today — they are already tier-free. They must change
anyway, because once §2 carries `last_activated_at` forward, `COALESCE` would prefer a *stale*
activation timestamp over the new head's fresh `created_at` and compute decay against it. For
`fact.*` (stability 180d) that can push a just-edited memory below the default
`min_strength=0.3` and out of results entirely — trading the reported bug for a worse one.
`GREATEST` is immune by construction. Changing three of four sites would be strictly worse
than changing none.

**The cursor becomes coherent as a side effect.** The cursor predicate and the
`nextCursor` computation both collapse onto the same single expression, so
their two-branch NULL handling disappears:

```sql
-- before: two branches, incoherent across the tier boundary
AND (last_activated_at < $N::timestamptz
     OR (last_activated_at IS NULL AND created_at < $N::timestamptz))

-- after: one key, one comparison
AND GREATEST(last_activated_at, created_at) < $N::timestamptz
```

The pre-existing defect this removes: the cursor is a single opaque timestamp with no tier
marker, so paging from tier 1 into tier 2 admitted NULL-tier rows only where
`created_at < <last tier-1 row's last_activated_at>`. Any never-activated memory created after
that instant was skipped on *every* subsequent page, permanently. Collapsing the tiers
eliminates the boundary the bug depended on. Cursor semantics are otherwise untouched.

**No migration.** Pure code change. Existing heads with NULL `last_activated_at` immediately
rank by `created_at`, so the two reported documents surface on the next request.

### 2. Carry activation state across versions

`UpdateMemory` adds the three omitted fields:

```go
rr.LastActivatedAt = head.LastActivatedAt   // *time.Time, may be nil
rr.LastActivatedBy = head.LastActivatedBy
rr.ActivationCount = head.ActivationCount
```

`Remember` threads them into the INSERT, replacing the literal `0` and adding
`last_activated_at` / `last_activated_by` to the column list. They default to `0` / `NULL`
when unset, so plain `pf_remember` behaviour is unchanged — only `UpdateMemory` populates them.

The `stabilityDays` computation in `Remember` becomes
`computeStabilityDays(req.Type, carriedCount)`.

#### These fields MUST be tagged `json:"-"`

`handleRemember` binds the request body directly into `domain.RememberRequest`
(`internal/server/routes_memory.go`, `handleRemember`) with no intermediate DTO. Untagged, any project
writer could `POST /v1/memories` with `{"activation_count": 9999, "last_activated_at":
"2030-01-01"}` and pin an arbitrary memory to the top of every recall in the project.

This is the aihub#210 lesson (team memory `mem_LpIoA2p1`) running in reverse: that incident
was fields being *silently dropped* for lack of json tags; the same binding seam means newly
added fields are *silently accepted* from untrusted input. Activation state is server-derived
and must never be settable by a client.

## Non-goals

- **UI pagination / raising the 50-row cap.** Correct ranking puts these documents in the top
  handful, so the cap stops mattering for the reported symptom. Explicitly deferred.
- **Rewriting cursor pagination.** The tier collapse in §1 repairs the boundary defect as a
  consequence; no keyset-cursor rewrite is undertaken.
- **`pf_whoami` membership reconciliation.** A real bug in a different subsystem with its own
  auth implications. Bundling it would make this change unreviewable as one unit.
- **Backfilling stranded activation history.** Recovering it means walking every lineage for
  the maximum historical activation, and it buys nothing once ranking no longer punishes a
  zero count.

## Known limitation: stability accrual is still reset per version

The `computeStabilityDays(req.Type, carriedCount)` change reaches `experience.*` only. For
`rule.*` / `fact.*` / `methodology.*`, `trg_mem_immortal`
(`internal/db/migrations/0006_events_memories.sql`) fires `BEFORE INSERT` and re-forces `stability_days` to
the type default (36500 / 180 / 36500), overwriting whatever the Go layer computed. Because
`Activate` raises stability via `UPDATE` (`internal/domain/memory.go`, `Activate`), the trigger does not fire
there and the raised value sticks — until the next version insert resets it.

Concretely: a `fact.*` memory activated once holds `180 × (1 + 1×0.5) = 270`; editing it
returns the new head to 180.

Carrying `activation_count` makes this **transient rather than permanent** — the next
`Activate` computes `180 × (1 + 2×0.5) = 360`, building on the preserved count instead of
restarting from zero. Fully preserving stability across versions requires changing the
trigger, which is a migration and a wider blast radius than this wi. Deliberately out of
scope.

## Implementation hazard

Adding columns to the `Remember` INSERT shifts every positional parameter after `$11`, and
the `RETURNING` list feeds a 26-field positional `Scan` in the same statement. The codebase already
carries scar tissue here: `scanMemoryLite` (`internal/domain/memory.go`) logs
`"possible column drift"` and **continues**, so a misalignment silently drops rows
from recall instead of failing loudly —
the same class of silent invisibility this wi is about.

Column list, parameter numbering, `RETURNING` clause and `Scan` targets change as one unit,
verified by a test that reads back every carried field. Not by inspection.

## Test plan

| # | case | asserts |
|---|---|---|
| 1 | **Rank inversion (the reported bug)** — older activated memory vs newer never-activated | newer ranks first; fails before, passes after |
| 2 | **`GREATEST` NULL semantics** | `GREATEST(NULL, created_at) = created_at`; pins the load-bearing, dialect-specific assumption |
| 3 | **Body-spoofing guard** | `POST /v1/memories` carrying `activation_count` / `last_activated_at` yields `0` / `NULL` |
| 4 | **`min_strength` interaction** | `fact.*` head carrying a ~200-day-old activation timestamp, freshly updated, still returned at default `min_strength=0.3` |
| 5 | **Carry-over round-trip** | activate → update → new head has all three fields; ranks by fresh `created_at`, not the older activation time |
| 6 | **Cursor coherence** | paging a mixed activated/never-activated set with `limit` < total returns every memory exactly once, none skipped |
| 7 | **UI list** | a row shaped like the reported pair (`methodology.*`, `activation_count=0`, `visibility=project`) appears within the default 50-row page |

Files: `internal/domain/memory_version_test.go`, `internal/domain/memory_latest_test.go`,
`tests/integration/memory_recall_test.go`, `internal/server/ui_handlers_memory_test.go`.

## Acceptance criteria

1. `pf_recall(project=ieops)` with no query returns `mem_7sHjIJkp` and `mem_mfuPOw2V` within
   the first 10 results, and both appear on the default `/ui/memories` page.
2. Ordering contains no `NULLS LAST` and no tier behaviour: a never-activated memory created
   after an activated memory's last activation ranks above it.
3. `pf_update_memory` produces a head whose `last_activated_at`, `last_activated_by` and
   `activation_count` equal the superseded head's, and which still ranks by its own
   `created_at`.
4. `activation_count` and `last_activated_at` cannot be set through any client-supplied
   request body.
5. A freshly updated `fact.*` memory is not filtered out by the default `min_strength`.
6. Cursor paging over a mixed set skips no rows.
7. `go build ./... && go vet ./... && go test ./...` clean.
