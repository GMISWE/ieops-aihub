# Memory Recall Ranking Implementation Plan

> # 🗄️ ARCHIVED — DO NOT EXECUTE
>
> **This plan was delivered. aihub#236 is `wrapped` (closed 2026-06-25).** It is
> kept for the reasoning, not as work to do. Executing it would re-apply changes
> that are already in `main` and would revert two of them (see below).
>
> The instruction that used to sit here told an agent to implement this plan
> task-by-task with `superpowers:subagent-driven-development`. That is why this
> banner is loud: the file's own opening line was an active hazard, not merely
> stale, and nothing in CI would have gone red.
>
> **Shipped as:** `8a3deb2` (reference-time test) · `5a9a3e2` (rank by
> `GREATEST`, drop the `NULLS LAST` tier) · `e75e224` (share the reference time
> with the GC sweep and vector recall) · `b41c60f` (clear activation state after
> `Bind`) · `34aff04` · `1e74314` (carry activation state on **every** supersede
> path; make the recall cursor a compound keyset).
>
> **Where the implementation diverged from this plan — the code is the authority:**
>
> 1. **`json:"-"` was not sufficient.** Task 3 rests on it. echo's
>    `DefaultBinder` routes `application/xml` and `text/xml` to `encoding/xml`,
>    which ignores json tags and falls back to the Go field name, so
>    `<ActivationCount>9999</ActivationCount>` bound straight through. The fix
>    zeroes the trio after `Bind` (`b41c60f`); see
>    `internal/server/routes_memory.go` (`handleRemember`).
> 2. **The cursor became a compound keyset**, not the single RFC3339Nano
>    timestamp Task 2 Step 4 specifies — see `formatRecallCursor` in
>    `internal/domain/memory.go` (`1e74314`).
> 3. **Carry-over covers every supersede path**, not only `UpdateMemory`.
> 4. **Reference time also reached the GC sweep and the vector path**
>    (`internal/domain/gc.go`, `internal/domain/memory_vector.go`) — not in this
>    plan at all.
>
> **All line numbers have been removed** (aihub#352). They were measured wrong:
> of the 23 line-number citations across this file and the spec, 18 pointed at
> the wrong line and 3 more were off by one or two. File-plus-symbol anchors
> replace them and are checked by `scripts/pf_docs_contract_check.py`.

**Goal:** Stop `domain.Recall` from exiling never-activated memories below every ever-activated one, and stop `pf_update_memory` from resetting a lineage's activation state on each edit.

**Architecture:** Define the "reference time" concept exactly once — the most recent of `last_activated_at` and `created_at` — as a Go helper (`memoryRefTime`) and a SQL constant (`memRefTimeSQL = GREATEST(last_activated_at, created_at)`), then make all four sites that currently spell it out inconsistently use it. Separately, carry `LastActivatedAt`/`LastActivatedBy`/`ActivationCount` across versions in `UpdateMemory`, with `json:"-"` so the fields cannot be set from an HTTP body. No DB migration.

**Tech Stack:** Go 1.26.3, PostgreSQL (pgx/v5, goose migrations), echo v4, testify.

## Global Constraints

- Go toolchain is **1.26.3** — `golangci-lint` must be built with an explicitly pinned matching toolchain or it refuses to run (see Task 5).
- **No DB migration.** This change is Go-only. Do not add files under `internal/db/migrations/`.
- DB-backed tests in `internal/domain` are gated on the `AIHUB_TEST_DB` env var and must `t.Skip` when it is unset, so plain `go test ./...` stays green without a database. Follow the existing pattern in `internal/domain/memory_latest_test.go`
(`setupLatestTestDB`).
- **PostgreSQL `GREATEST` ignores NULL arguments** (returns NULL only if every argument is NULL). This differs from Oracle and MySQL. `memories.created_at` is `NOT NULL` (`internal/db/migrations/0006_events_memories.sql`), so `GREATEST(last_activated_at, created_at)` is total. This property is load-bearing and is pinned by a test.
- Never use `COALESCE(last_activated_at, created_at)` for reference time. It picks the *stale* activation timestamp over a fresher `created_at`, which is precisely the regression Task 4 would otherwise introduce.
- The `/v1` and `/share` artifact output is frozen byte-identical (the aihub#160 boundary); d2 rendering is `/ui`-only. Do not "fix" raw d2 fences appearing in `/v1` output.
- Do not raise the `/ui/memories` 50-row limit or add pagination. Declared non-goals.

## File Structure

| file | responsibility | change |
|---|---|---|
| `internal/domain/memory.go` | memory domain: struct, Remember, Recall, UpdateMemory, strength math | modify — add `memoryRefTime` + `memRefTimeSQL`, apply at 4 sites, thread activation fields through `Remember`, carry them in `UpdateMemory` |
| `internal/domain/memory_reftime_test.go` | pure unit tests for the reference-time helper and strength math (no DB) | create |
| `internal/domain/memory_ranking_test.go` | DB-backed tests for ranking order, cursor coverage, and version carry-over | create |

Everything lands in one production file because the four inconsistent sites and both write paths already live there; splitting them would separate code that must change together. Tests are split into a DB-free file and a DB-gated file so the fast tests always run.

```d2
direction: right

concept: "reference time\n(defined ONCE)" {
  go: "memoryRefTime()\nGo helper"
  sql: "memRefTimeSQL\nGREATEST(last_act, created)"
}

sites: "four sites that must agree" {
  s1: "ORDER BY  :1238\nwas NULLS LAST -> tiers"
  s2: "min_strength WHERE  :1185\nwas COALESCE"
  s3: "lexical 2nd sort  :1226\nwas COALESCE"
  s4: "MemoryStrength  :243\nwas if-nil fallback"
}

writes: "write paths" {
  rr: "RememberRequest\n+3 fields, json:\"-\""
  ins: "Remember INSERT\nactivation_count was literal 0"
  upd: "UpdateMemory\nwas dropping activation trio"
}

out: "outcome" {
  rank: "never-activated ranks by created_at\nno tier cliff"
  keep: "edit preserves activation state"
  cur: "cursor = one comparison\ntier-skip gone"
}

concept.go -> sites.s4: "Task 1"
concept.sql -> sites.s1: "Task 2"
concept.sql -> sites.s2: "Task 2"
concept.sql -> sites.s3: "Task 2"
concept.go -> writes.upd: "Task 4"

writes.rr -> writes.ins: "Task 3"
writes.ins -> writes.upd: "Task 4"

sites.s1 -> out.rank
sites.s1 -> out.cur
writes.upd -> out.keep
sites.s2 -> out.keep: "prevents stale-decay\nregression"
```

---

### Task 1: Reference-time helper and strength math

**Files:**
- Modify: `internal/domain/memory.go` (`MemoryStrength`)
- Test: `internal/domain/memory_reftime_test.go` (create)

**Touched files:** `internal/domain/memory.go` (write), `internal/domain/memory_reftime_test.go` (write)

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `func memoryRefTime(lastActivatedAt *time.Time, createdAt time.Time) time.Time` — package-private, returns the later of the two. Task 2 uses it for the pagination cursor value; Task 4 depends on its stale-activation behaviour.

- [ ] **Step 1: Write the failing test**

Create `internal/domain/memory_reftime_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

// A memory that was never activated has no last_activated_at; its reference
// time is simply when it was created.
func TestMemoryRefTime_NilActivationFallsBackToCreated(t *testing.T) {
	created := time.Now().Add(-2 * time.Hour)
	if got := memoryRefTime(nil, created); !got.Equal(created) {
		t.Fatalf("nil activation: got %v, want %v", got, created)
	}
}

// A memory activated after it was created is as fresh as its activation.
func TestMemoryRefTime_PrefersLaterActivation(t *testing.T) {
	created := time.Now().Add(-48 * time.Hour)
	act := time.Now().Add(-1 * time.Hour)
	if got := memoryRefTime(&act, created); !got.Equal(act) {
		t.Fatalf("later activation: got %v, want %v", got, act)
	}
}

// The case aihub#236 turns on: UpdateMemory carries a STALE last_activated_at
// onto a brand-new head. Reference time must be the new created_at, or the
// fresh head is treated as heavily decayed.
func TestMemoryRefTime_StaleActivationLosesToFreshCreated(t *testing.T) {
	act := time.Now().Add(-200 * 24 * time.Hour)
	created := time.Now()
	if got := memoryRefTime(&act, created); !got.Equal(created) {
		t.Fatalf("stale activation: got %v, want %v", got, created)
	}
}

// Behavioural consequence of the above for the decay curve: a fact.* memory
// (stability 180d) freshly re-created while carrying a 200-day-old activation
// must still read at ~full strength. Under the old "activation wins if set"
// rule this returned 3*exp(-200/180) ~= 0.99 and could fall below the default
// min_strength of 0.3 after further decay.
func TestMemoryStrength_StaleActivationDoesNotDecayFreshHead(t *testing.T) {
	act := time.Now().Add(-200 * 24 * time.Hour)
	got := MemoryStrength(3, 180, &act, time.Now())
	if got < 2.99 {
		t.Fatalf("fresh head carrying stale activation: got %v, want ~3.0", got)
	}
}

// Guard the existing contract: stability_days <= 0 yields 0, not NaN/Inf.
func TestMemoryStrength_ZeroStabilityIsZero(t *testing.T) {
	if got := MemoryStrength(3, 0, nil, time.Now()); got != 0 {
		t.Fatalf("zero stability: got %v, want 0", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run 'TestMemoryRefTime|TestMemoryStrength' -v`
Expected: FAIL — compile error `undefined: memoryRefTime`.

- [ ] **Step 3: Add the helper and rewrite MemoryStrength**

In `internal/domain/memory.go`, replace the `MemoryStrength` function with:

```go
// memoryRefTime returns the reference timestamp used for BOTH decay and
// ranking: the most recent of last_activated_at and created_at. It is the Go
// mirror of memRefTimeSQL and the two MUST stay in agreement — recall filters
// rows in SQL and reports effective_strength from Go, so a divergence makes the
// score shown to clients disagree with the order rows come back in (aihub#236).
//
// Deliberately NOT "activation if set, else created": UpdateMemory carries a
// lineage's last_activated_at onto each new version, so a freshly created head
// can hold an old activation timestamp. Taking the later of the two keeps that
// head as fresh as it actually is.
func memoryRefTime(lastActivatedAt *time.Time, createdAt time.Time) time.Time {
	if lastActivatedAt != nil && lastActivatedAt.After(createdAt) {
		return *lastActivatedAt
	}
	return createdAt
}

// MemoryStrength calculates effective_strength (raw) per §7.2.
// Formula: base_strength × exp(-days_since / stability_days)
// days_since is measured from memoryRefTime (M8, revised by aihub#236).
func MemoryStrength(baseStrength, stabilityDays float64, lastActivatedAt *time.Time, createdAt time.Time) float64 {
	if stabilityDays <= 0 {
		return 0
	}
	daysSince := time.Since(memoryRefTime(lastActivatedAt, createdAt)).Hours() / 24
	return baseStrength * math.Exp(-daysSince/stabilityDays)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/ -run 'TestMemoryRefTime|TestMemoryStrength' -v`
Expected: PASS — 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/memory.go internal/domain/memory_reftime_test.go
git commit -m "fix(memory): reference time is the later of last_activated_at and created_at

MemoryStrength used last_activated_at unconditionally when set, so a version
carrying a stale activation timestamp reads as decayed. Take the later of the
two instead (aihub#236)."
```

---

### Task 2: One reference time in SQL — ranking and cursor

**Files:**
- Modify: `internal/domain/memory.go` (`Recall`) — the min_strength filter, the cursor predicate, the lexical secondary sort, the default ORDER BY, and the `nextCursor` computation
- Test: `internal/domain/memory_ranking_test.go` (create)

**Touched files:** `internal/domain/memory.go` (write), `internal/domain/memory_ranking_test.go` (write)

**Interfaces:**
- Consumes: `memoryRefTime` from Task 1.
- Produces: `const memRefTimeSQL string` — the SQL reference-time expression, referenced by Tasks 3 and 4's tests. Also `seedRankedMemory(t, pool, project, userID, id, memType string, createdAt time.Time, lastActivatedAt *time.Time, activationCount int)` in the new test file, reused by Task 4.

- [ ] **Step 1: Write the failing test**

Create `internal/domain/memory_ranking_test.go`:

```go
package domain

// DB-backed ranking tests for aihub#236. Gated on AIHUB_TEST_DB like
// memory_latest_test.go, so plain `go test ./...` skips them:
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestRecallRanking -v -count=1

import (
	"context"
	"testing"
	"time"
)

// seedRankedMemory inserts one memory with explicit created_at / activation
// state so ranking order can be constructed deterministically. Note the
// BEFORE INSERT trigger trg_mem_immortal overrides stability_days by type
// prefix, so pass memType deliberately (fact.* -> 180d, methodology.* -> 36500).
func seedRankedMemory(t *testing.T, pool *pgxpool.Pool, project, userID, id, memType string,
	createdAt time.Time, lastActivatedAt *time.Time, activationCount int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, type, content, author_user_id, author_display,
			visibility, status, tags, attrs, base_strength,
			activation_count, last_activated_at, created_at, latest_id)
		VALUES ($1,$2,$3,$1,$4,$4,'project','active','{}','{}',3,$5,$6,$7,$1)`,
		id, project, memType, userID, activationCount, lastActivatedAt, createdAt)
	if err != nil {
		t.Fatalf("seedRankedMemory(%s): %v", id, err)
	}
}

func recallAll(t *testing.T, pool *pgxpool.Pool, project, userID string, topK int) []MemoryWithStrength {
	t.Helper()
	resp, aerr := Recall(context.Background(), pool, &RecallRequest{
		Project:      project,
		MinStrength:  0.3,
		TopK:         topK,
		CallerUserID: userID,
		CallerRole:   "writer",
	})
	if aerr != nil {
		t.Fatalf("Recall: %v", aerr)
	}
	return resp.Items
}

func rankOf(items []MemoryWithStrength, id string) int {
	for i := range items {
		if items[i].Memory.ID == id {
			return i
		}
	}
	return -1
}

// THE REPORTED BUG. An older memory activated once must NOT outrank a newer
// never-activated one. Under `ORDER BY last_activated_at DESC NULLS LAST`
// every activated row formed a tier above every never-activated row, so the
// fresh memory sorted last regardless of age.
func TestRecallRanking_FreshNeverActivatedBeatsOlderActivated(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	oldActivation := time.Now().Add(-13 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_rank_activated", "fact.note",
		time.Now().Add(-14*24*time.Hour), &oldActivation, 1)
	seedRankedMemory(t, pool, proj, uid, "mem_rank_fresh", "fact.note",
		time.Now().Add(-1*time.Hour), nil, 0)

	items := recallAll(t, pool, proj, uid, 10)
	fresh, activated := rankOf(items, "mem_rank_fresh"), rankOf(items, "mem_rank_activated")
	if fresh < 0 || activated < 0 {
		t.Fatalf("both memories must be recalled: fresh=%d activated=%d", fresh, activated)
	}
	if fresh > activated {
		t.Errorf("fresh never-activated memory ranked #%d, below activated-13d-ago at #%d "+
			"— NULLS LAST tier still present", fresh, activated)
	}
}

// Pins the load-bearing, dialect-specific property: PostgreSQL GREATEST
// IGNORES NULL arguments. On Oracle/MySQL this returns NULL and the whole
// ranking silently collapses.
func TestRecallRanking_GreatestIgnoresNullArgument(t *testing.T) {
	pool := setupLatestTestDB(t)
	var equal bool
	err := pool.QueryRow(context.Background(),
		`SELECT GREATEST(NULL::timestamptz, now()) = now()`).Scan(&equal)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !equal {
		t.Fatal("GREATEST(NULL, now()) != now() — this PostgreSQL ignores-NULL " +
			"behaviour is required by memRefTimeSQL")
	}
}

// Paging must not skip rows. The old cursor was a single timestamp compared
// against a two-branch predicate, so crossing from the activated tier into the
// NULL tier permanently skipped any never-activated row created after the last
// activated row's activation time.
func TestRecallRanking_CursorSkipsNoRows(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// Interleave activated and never-activated rows so any tier boundary is
	// crossed mid-pagination.
	for i := 0; i < 6; i++ {
		created := time.Now().Add(-time.Duration(i) * time.Hour)
		var act *time.Time
		count := 0
		if i%2 == 0 {
			a := time.Now().Add(-time.Duration(i)*time.Hour - 30*time.Minute)
			act, count = &a, 1
		}
		seedRankedMemory(t, pool, proj, uid,
			fmt.Sprintf("mem_cur_%d", i), "fact.note", created, act, count)
	}

	seen := map[string]int{}
	cursor := ""
	for page := 0; page < 10; page++ {
		resp, aerr := Recall(context.Background(), pool, &RecallRequest{
			Project: proj, MinStrength: 0.3, TopK: 2,
			CallerUserID: uid, CallerRole: "writer", Cursor: cursor,
		})
		if aerr != nil {
			t.Fatalf("Recall page %d: %v", page, aerr)
		}
		for i := range resp.Items {
			seen[resp.Items[i].Memory.ID]++
		}
		if resp.NextCursor == nil {
			break
		}
		cursor = *resp.NextCursor
	}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("mem_cur_%d", i)
		if seen[id] != 1 {
			t.Errorf("%s seen %d times across pages, want exactly 1", id, seen[id])
		}
	}
}
```

Add `"fmt"` and `"github.com/jackc/pgx/v5/pgxpool"` to that file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
  go test ./internal/domain/ -run TestRecallRanking -v -count=1
```
Expected: `TestRecallRanking_FreshNeverActivatedBeatsOlderActivated` FAILs with "NULLS LAST tier still present"; `TestRecallRanking_CursorSkipsNoRows` FAILs with a `seen 0 times` line; `TestRecallRanking_GreatestIgnoresNullArgument` PASSes (it asserts a database property, not our code).

If `AIHUB_TEST_DB` is unset the run reports SKIP — that is not a pass. Start a Postgres with the migrations applied (`make migrate-up DATABASE_URL=...`) before continuing.

- [ ] **Step 3: Add the SQL constant**

In `internal/domain/memory.go`, immediately after the `memoryRefTime` function added in Task 1:

```go
// memRefTimeSQL is the SQL mirror of memoryRefTime. PostgreSQL's GREATEST
// IGNORES NULL arguments (returning NULL only when every argument is NULL) and
// memories.created_at is NOT NULL, so this expression is total: it can never
// yield NULL, and therefore can never produce a NULLS-ordering tier.
//
// Do NOT rewrite this as COALESCE(last_activated_at, created_at). COALESCE
// prefers a stale activation timestamp over a fresher created_at, which
// reintroduces aihub#236 in the min_strength filter — a freshly edited fact.*
// memory would be decayed against its old activation and filtered out.
const memRefTimeSQL = `GREATEST(last_activated_at, created_at)`
```

- [ ] **Step 4: Apply it at the three SQL sites and the cursor**

In `Recall`, replace the `min_strength` filter:

```go
	// H9: min_strength filter in SQL (not Go-side post-LIMIT) using inline Ebbinghaus formula.
	// immortal memories bypass the filter.
	// Formula: base_strength * exp(-days_since / stability_days) >= min_strength
	where += fmt.Sprintf(` AND (is_immortal = true OR (stability_days > 0 AND
		base_strength * exp(
			-extract(epoch from (clock_timestamp() - `+memRefTimeSQL+`))/86400.0
			/ stability_days
		) >= $%d))`, idx)
	args = append(args, req.MinStrength)
	idx++
```

Replace the cursor predicate with:

```go
	// Cursor-based pagination. ORDER BY is a single total expression
	// (memRefTimeSQL), so the cursor is one comparison against that same
	// expression — no NULL branch. The previous two-branch form could not
	// express "the next row after this one" once ordering crossed the
	// activated/never-activated boundary, and silently skipped rows (aihub#236).
	// Cursor value is an RFC3339Nano timestamp of the last item's sort key.
	if req.Cursor != "" {
		where += fmt.Sprintf(` AND `+memRefTimeSQL+` < $%d::timestamptz`, idx)
		args = append(args, req.Cursor)
		idx++
	}
```

In the lexical branch, replace the secondary sort expression:

```go
			ORDER BY ts_rank(content_tsv, replace(plainto_tsquery('english', $%d)::text, ' & ', ' | ')::tsquery) DESC,
				tanh(base_strength * exp(
					-extract(epoch from (clock_timestamp() - `+memRefTimeSQL+`))/86400.0
					/ NULLIF(stability_days, 0))) DESC
```

In the default branch, replace the ORDER BY:

```go
			ORDER BY `+memRefTimeSQL+` DESC, id DESC
```

`id DESC` is a deterministic tiebreaker for rows sharing a reference timestamp; without it equal-timestamp ordering is arbitrary between queries, which makes paging unstable. (Rows whose reference time is exactly equal to a cursor value are still excluded by the strict `<`; that pre-existing edge is unchanged and out of scope.)

Finally replace the `nextCursor` computation so it uses the same definition as the ORDER BY:

```go
	var nextCursor *string
	if len(items) > req.TopK {
		items = items[:req.TopK]
		last := items[len(items)-1]
		// Cursor is the sort-key timestamp, computed by the same rule as
		// memRefTimeSQL so the next page resumes exactly where this one ended.
		cursorVal := memoryRefTime(last.LastActivatedAt, last.CreatedAt).Format(time.RFC3339Nano)
		nextCursor = &cursorVal
	}
```

Delete the now-stale comment block at the head of the text/tag recall path that claims the text path "orders strictly by last_activated_at DESC NULLS LAST" — it documents the removed behaviour. Keep the sentence noting `RecencyWeight` is a reserved no-op:

```go
	// NOTE: RecencyWeight is currently a reserved-but-unused knob. The text/tag
	// recall path orders by memRefTimeSQL (see ORDER BY below) and does not blend
	// a separate recency score. The default is intentionally not set here so the
	// field stays an explicit no-op rather than a misleading "applied" value.
```

- [ ] **Step 5: Run tests to verify they pass**

Run:
```bash
AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
  go test ./internal/domain/ -run TestRecallRanking -v -count=1
```
Expected: PASS — 3 tests.

Then confirm nothing else regressed:

Run: `go test ./internal/... 2>&1 | tail -20`
Expected: all packages `ok` or `no test files`.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/memory.go internal/domain/memory_ranking_test.go
git commit -m "fix(recall): rank by GREATEST(last_activated_at, created_at), not a NULLS LAST tier

ORDER BY last_activated_at DESC NULLS LAST made 'never activated' a sorting
tier rather than a value: every ever-activated memory outranked every
never-activated one regardless of age, so freshly written docs sorted past the
50-row UI cap and looked deleted. Use one total expression for reference time
at all three SQL sites, which also collapses the two-branch cursor predicate
that was skipping rows across the tier boundary (aihub#236)."
```

---

### Task 3: Make activation state carryable, but not client-settable

**Files:**
- Modify: `internal/domain/memory.go` — `RememberRequest`, the `stabilityDays`
  computation, and the `Remember` INSERT
- Test: `internal/domain/memory_reftime_test.go` (append)

**Touched files:** `internal/domain/memory.go` (write), `internal/domain/memory_reftime_test.go` (write), `internal/server/routes_memory.go` (read)

**Interfaces:**
- Consumes: nothing from Tasks 1-2 (independent), but shares the file.
- Produces: `RememberRequest.LastActivatedAt *time.Time`, `.LastActivatedBy *string`, `.ActivationCount int` — all `json:"-"`. Task 4 sets these three fields.

- [ ] **Step 1: Write the failing test**

Append to `internal/domain/memory_reftime_test.go` (add `"encoding/json"` to its imports):

```go
// Activation state is server-derived and MUST NOT be settable by a client.
// handleRemember binds the HTTP body straight into domain.RememberRequest
// (internal/server/routes_memory.go, handleRemember) with no DTO, so an
// exported field with a JSON name would let any project writer pin a memory to
// the top of every recall. Regression guard: this fails if the json:"-" tags
// are ever dropped.
//
// Note this asserts encoding/json behaviour, which is what echo's Bind uses for
// JSON request bodies; it does not exercise echo itself.
func TestRememberRequest_ActivationFieldsAreNotBindable(t *testing.T) {
	body := `{
		"project": "p", "type": "fact.note", "content": "x",
		"activation_count": 9999,
		"last_activated_at": "2030-01-01T00:00:00Z",
		"last_activated_by": "u_attacker",
		"LastActivatedAt": "2030-01-01T00:00:00Z",
		"ActivationCount": 4242
	}`
	var req RememberRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ActivationCount != 0 {
		t.Errorf("ActivationCount settable from body: got %d, want 0", req.ActivationCount)
	}
	if req.LastActivatedAt != nil {
		t.Errorf("LastActivatedAt settable from body: got %v, want nil", req.LastActivatedAt)
	}
	if req.LastActivatedBy != nil {
		t.Errorf("LastActivatedBy settable from body: got %v, want nil", req.LastActivatedBy)
	}
	// Sanity: normal fields still bind, so the test is not vacuous.
	if req.Project != "p" || req.Type != "fact.note" {
		t.Errorf("ordinary fields must still bind: project=%q type=%q", req.Project, req.Type)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run TestRememberRequest_ActivationFieldsAreNotBindable -v`
Expected: FAIL — compile error `req.ActivationCount undefined`.

- [ ] **Step 3: Add the three fields**

In `internal/domain/memory.go`, inside `type RememberRequest struct`, after the `SessionSecret` / credential fields at the end of the struct:

```go
	// aihub#236: activation state carried forward by UpdateMemory so that
	// editing a memory does not reset its lineage's activation history (which
	// previously dropped every new version into the NULLS-LAST ranking tier).
	//
	// json:"-" is REQUIRED, not stylistic. handleRemember binds the request body
	// directly into this struct (internal/server/routes_memory.go,
	// handleRemember) with no
	// intermediate DTO, so a JSON-named field here would let any project writer
	// POST /v1/memories with activation_count=9999 and pin their memory to the
	// top of every recall in the project. Only UpdateMemory sets these.
	LastActivatedAt *time.Time `json:"-"`
	LastActivatedBy *string    `json:"-"`
	ActivationCount int        `json:"-"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/ -run TestRememberRequest_ActivationFieldsAreNotBindable -v`
Expected: PASS.

- [ ] **Step 5: Thread the fields through the INSERT**

⚠️ **Column-drift hazard (team memory `mem_i9I2g8Hv`).** The `Remember` INSERT's column list, its `VALUES` placeholder numbering, its `RETURNING` list and the `Scan` targets must stay in lock-step. pgx misalignment produces **no compile error and no panic** — just wrong values in neighbouring fields. Change all of them in one edit and verify by reading values back (Task 4 Step 5 does this).

Only the column list and `VALUES` change here. `RETURNING`/`Scan` already include these columns and stay untouched.

First, where `stabilityDays` is computed, use the carried count so stability
reflects accrued activations:

```go
	stabilityDays := computeStabilityDays(req.Type, req.ActivationCount)
```

Then replace the INSERT's column list and `VALUES` block with:

```go
	err := q.QueryRow(ctx, `
		INSERT INTO memories (
			id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			activation_count, last_activated_at, last_activated_by, expires_at, tags, source_artifact_id,
			emb_model, emb_dims, emb_vector,
			status, attrs, rendered_html, supersedes_id, latest_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17,
			$18, $19, $20::vector,
			'active', $21, $22, $23, $1, clock_timestamp(), clock_timestamp()
		)
		RETURNING id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, emb_model, emb_dims, status, attrs,
			rendered_html, commits, latest_id, created_at, updated_at`,
		newID, req.Project, req.Type, req.Content, req.CallerUserID, req.CallerDisplay,
		req.WorkItemID, req.Visibility, immortal, baseStrength, stabilityDays,
		req.ActivationCount, req.LastActivatedAt, req.LastActivatedBy, // $12, $13, $14
		req.ExpiresAt, req.Tags, nil, // $15, $16, $17 — source_artifact_id = nil
		embModel, embDims, embVecLit, // $18, $19, $20 — emb_model/dims/vector
		req.Attrs, renderedHTML, req.SupersedesMemID, // $21, $22, $23
	).Scan(
```

Leave the `.Scan(...)` argument list exactly as it is — 26 targets matching the unchanged `RETURNING` list.

Verify the arithmetic before running: 27 columns, 27 value slots (`$1`-`$20` plus `'active'`, `$21`, `$22`, `$23`, `$1`, and two `clock_timestamp()`), 23 bound args.

- [ ] **Step 6: Run the full domain suite**

Run: `go test ./internal/domain/ -v 2>&1 | tail -25`
Expected: PASS. A misnumbered placeholder shows up here as a pgx error such as `expected 23 arguments, got 20`, or as existing `Remember` tests returning wrong field values.

- [ ] **Step 7: Commit**

```bash
git add internal/domain/memory.go internal/domain/memory_reftime_test.go
git commit -m "feat(memory): allow Remember to accept carried activation state

Adds LastActivatedAt/LastActivatedBy/ActivationCount to RememberRequest, all
json:\"-\" because handleRemember binds the HTTP body straight into this struct
and activation state must stay server-derived. The INSERT previously hardcoded
activation_count to 0 and omitted last_activated_at entirely. Prep for
aihub#236 version carry-over."
```

---

### Task 4: Carry activation state across versions

**Files:**
- Modify: `internal/domain/memory.go` (`UpdateMemory`)
- Test: `internal/domain/memory_ranking_test.go` (append)

**Touched files:** `internal/domain/memory.go` (write), `internal/domain/memory_ranking_test.go` (write)

**Interfaces:**
- Consumes: `RememberRequest.LastActivatedAt/.LastActivatedBy/.ActivationCount` (Task 3), `memRefTimeSQL` behaviour (Task 2), `seedRankedMemory` (Task 2).
- Produces: nothing new; final behaviour change.

- [ ] **Step 1: Write the failing test**

Append to `internal/domain/memory_ranking_test.go`:

```go
// An edit must not discard the lineage's activation history. Before aihub#236
// UpdateMemory rebuilt RememberRequest from the head without the activation
// trio, so every new version started at activation_count=0 with a NULL
// last_activated_at — which, under the old ranking, demoted it.
func TestUpdateMemory_CarriesActivationState(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	act := time.Now().Add(-3 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_carry_head", "fact.note",
		time.Now().Add(-4*24*time.Hour), &act, 2)
	mustExec(t, pool, `UPDATE memories SET last_activated_by='`+uid+
		`' WHERE id='mem_carry_head'`)

	newHead, err := UpdateMemory(ctx, pool, "mem_carry_head", &UpdateMemoryRequest{
		Content:       strp("edited content"),
		CallerUserID:  uid,
		CallerDisplay: uid,
	})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}

	if newHead.ActivationCount != 2 {
		t.Errorf("activation_count: got %d, want 2 (carried from head)", newHead.ActivationCount)
	}
	if newHead.LastActivatedAt == nil {
		t.Fatal("last_activated_at: got nil, want the head's timestamp")
	}
	if d := newHead.LastActivatedAt.Sub(act); d > time.Second || d < -time.Second {
		t.Errorf("last_activated_at: got %v, want ~%v", *newHead.LastActivatedAt, act)
	}
	if newHead.LastActivatedBy == nil || *newHead.LastActivatedBy != uid {
		t.Errorf("last_activated_by: got %v, want %q", newHead.LastActivatedBy, uid)
	}
	if newHead.Content != "edited content" {
		t.Errorf("content not applied: %q", newHead.Content)
	}
}

// The regression Task 2's min_strength change exists to prevent: a fact.*
// memory (stability 180d) whose carried last_activated_at is ~200 days old is
// brand new as a row, and must NOT be decay-filtered out of recall.
func TestUpdateMemory_FreshEditWithStaleActivationSurvivesMinStrength(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	stale := time.Now().Add(-200 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_stale_head", "fact.note",
		time.Now().Add(-201*24*time.Hour), &stale, 1)

	newHead, err := UpdateMemory(ctx, pool, "mem_stale_head", &UpdateMemoryRequest{
		Content:       strp("re-edited long-dormant note"),
		CallerUserID:  uid,
		CallerDisplay: uid,
	})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}

	items := recallAll(t, pool, proj, uid, 10)
	if rankOf(items, newHead.ID) < 0 {
		t.Errorf("freshly edited head %s was filtered out of recall at min_strength=0.3 "+
			"— reference time is decaying against the carried stale activation "+
			"instead of the new created_at", newHead.ID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
  go test ./internal/domain/ -run TestUpdateMemory_ -v -count=1
```
Expected: `TestUpdateMemory_CarriesActivationState` FAILs with `activation_count: got 0, want 2`. The existing `TestUpdateMemory` in `internal/domain/memory_latest_test.go`
must still PASS.

- [ ] **Step 3: Carry the three fields**

In `UpdateMemory`, add to the `rr := &RememberRequest{...}` literal, after `SupersedesMemID`:

```go
		// aihub#236: a new version inherits the lineage's activation history.
		// Without this each edit reset the head to activation_count=0 /
		// last_activated_at=NULL, stranding the history on the archived row.
		LastActivatedAt: head.LastActivatedAt,
		LastActivatedBy: head.LastActivatedBy,
		ActivationCount: head.ActivationCount,
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
  go test ./internal/domain/ -run 'TestUpdateMemory|TestRecallRanking' -v -count=1
```
Expected: PASS — including the pre-existing `TestUpdateMemory`, `TestConcurrentUpdateSingleHead`, and `TestSupersedeAdvancesCursor`.

- [ ] **Step 5: Verify no column drift**

The one failure mode that passes tests silently is a misaligned `Scan`. Confirm a round-trip reads back every field correctly:

Run:
```bash
AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
  go test ./internal/domain/ -run 'TestLatestIDRoundTrip|TestGetLatestByID|TestUpdateMemory_CarriesActivationState' -v -count=1
```
Expected: PASS. These assert `content`, `type`, `visibility`, `latest_id` and the activation fields together — neighbouring columns in the `RETURNING` list — so a one-position shift breaks at least one of them.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/memory.go internal/domain/memory_ranking_test.go
git commit -m "fix(memory): carry activation state onto new versions

UpdateMemory rebuilt RememberRequest from the lineage head but omitted
last_activated_at / last_activated_by / activation_count, so every edit reset
the head's activation history and stranded it on the archived row. Combined
with the ranking fix this stops repeated edits from demoting a document
(aihub#236)."
```

---

### Task 5: Verification gate

**Files:**
- Test: no new files — this task runs the gates and verifies against the reported data.

**Touched files:** none (read-only verification)

**Interfaces:**
- Consumes: all prior tasks.
- Produces: evidence for the spec's acceptance criteria.

- [ ] **Step 1: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 2: Full test suite without a database**

Run: `go test ./... 2>&1 | grep -v "no test files" | tail -30`
Expected: every line `ok`. DB-gated tests report `SKIP`, which is correct here.

- [ ] **Step 3: Full test suite with a database**

Run:
```bash
AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
  go test ./internal/domain/ -count=1 2>&1 | tail -20
```
Expected: `ok github.com/GMISWE/ieops-aihub/internal/domain`.

- [ ] **Step 4: Lint**

The stock `golangci-lint` binary refuses to run on this module — a binary built with Go ≤1.25 reports *"the Go language version used to build golangci-lint is lower than the targeted Go version (1.26.3)"*, and `GOTOOLCHAIN=local` does not fix it because golangci-lint's own `go.mod` toolchain directive wins. Pin the toolchain explicitly (team memory `mem_6xCNhQJu`):

```bash
TMP=$(mktemp -d)
GOTOOLCHAIN=go1.26.3 GOBIN=$TMP go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
GOLANGCI_LINT_CACHE=$TMP/cache $TMP/golangci-lint run ./internal/... --timeout=5m
```
Expected: `0 issues`.

- [ ] **Step 5: Verify the reported memories against a real server**

The spec's acceptance criterion 1 names live data. The `/ui/memories` list injects `recallMemoriesFn`, so a unit test there would mock out the very ranking under test — this criterion is verified against a running server instead, not by a Go test.

Against a server running this build:

```bash
pf_recall project=ieops top_k=30      # expect mem_7sHjIJkp and mem_mfuPOw2V present
```

Expected: both IDs appear within the first 10 results (they ranked #54 and #61 before). Then open `/ui/memories?project=ieops` in a browser as the reporter and confirm both are on the default page.

- [ ] **Step 6: Commit any fixes and report**

```bash
git add -A
git commit -m "chore(aihub#236): verification gate — build, vet, tests, lint clean"
```

If the reported memories still do not surface, do **not** widen the fix. Re-open the spec's ruled-out section: something outside ranking is filtering them, and that is a different defect.

---

## Self-Review

**Spec coverage.** Every spec design item maps to a task: reference-time Go site → Task 1; the three SQL sites plus cursor and `nextCursor` → Task 2; `RememberRequest` fields, `json:"-"`, INSERT threading and `computeStabilityDays` → Task 3; `UpdateMemory` carry-over → Task 4. Spec test-plan rows 1-6 map to concrete tests (rows 1/2/6 in Task 2, row 3 in Task 3, rows 4/5 in Task 4). The spec's non-goals are restated as Global Constraints so no task drifts into UI pagination, a cursor rewrite, `pf_whoami`, or the `trg_mem_immortal` migration.

**One deliberate deviation.** Spec test-plan row 7 ("UI list shows a row shaped like the reported pair") is *not* implemented as a Go test. `handleUIMemories` takes its results from the injectable `recallMemoriesFn`, so a handler test asserts the mock, not the ranking. It is covered by Task 5 Step 5 as live verification against a running server. Flagged rather than silently dropped.

**Placeholder scan.** No TBD/TODO, no "add error handling", no "similar to Task N". Every code step carries the actual code; every run step carries the actual command and expected output, including the failure text expected at each red step.

**Type consistency.** `memoryRefTime(*time.Time, time.Time) time.Time` is defined in Task 1 and used in Task 2 Step 4 (`nextCursor`) with that exact signature. `memRefTimeSQL` is declared in Task 2 Step 3 and referenced in Steps 4 of the same task. The three `RememberRequest` field names (`LastActivatedAt`, `LastActivatedBy`, `ActivationCount`) are declared in Task 3 and set in Task 4 with identical spelling and types, and match the `Memory` struct field names in `internal/domain/memory.go`. Test helpers `seedRankedMemory`, `recallAll` and `rankOf` are defined in Task 2's new file and reused in Task 4; `setupLatestTestDB`, `testUser`, `testProject`, `mustExec` and `strp` are pre-existing in package `domain` and are not redefined.

**Ordering note.** Task 3 must land before Task 4 (Task 4 sets fields Task 3 declares). Task 2 is independent of Tasks 3-4 and could run in parallel, but Task 4's `min_strength` test depends on Task 2's change, so sequential execution in the listed order is the safe path.
