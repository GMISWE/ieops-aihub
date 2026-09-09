# pf_remember — contract card

```json
{
  "tool": "pf_remember",
  "description_sha256": "bec195df7750ddb2b3b4714765c5ded5e0a03f968e184a781d347c9e3f553fc7",
  "input_schema_sha256": "3fd891dcc03b4c9d2e44114fda3f92df709e272b774679404d6a8ae352e55e00",
  "params": {
    "attrs": {
      "type": "object",
      "required": false
    },
    "base_strength": {
      "type": "number",
      "required": false
    },
    "content": {
      "type": "string",
      "required": true
    },
    "context_snippet": {
      "type": "string",
      "required": false
    },
    "dedup_mode": {
      "type": "string",
      "required": false
    },
    "expires_at": {
      "type": "string",
      "required": false
    },
    "project": {
      "type": "string",
      "required": true
    },
    "related_memory_ids": {
      "type": "array",
      "required": false
    },
    "supersedes_memory_id": {
      "type": "string",
      "required": false
    },
    "tags": {
      "type": "array",
      "required": false
    },
    "type": {
      "type": "string",
      "required": true
    },
    "visibility": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "activation_count",
    "base_strength",
    "created_at",
    "id",
    "is_new",
    "memory_id",
    "project",
    "stability_days",
    "type",
    "visibility"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Thirteen parameters, four required. **None of them is an enum**, and `type` stopped
being one in `aihub#445`: what the server accepts is a prefix rule, so the published
set and the accepted set are one set only if nothing closed is published.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | project name |
| `type` | string | yes | full name, e.g. `experience.debug`; must start with `experience.` / `fact.` / `rule.` and carry no `\|`; `methodology.*` refused here; 13 curated names are published as suggestions |
| `content` | string | yes | memory content |
| `visibility` | string | yes | one of `admin\|private\|project\|public\|team`, built from `domain.MemoryVisibilityList()`; `public` is the anonymous-share tier and the description says what it costs |
| `work_item_id` | string | no | associated work item |
| `base_strength` | number | no | "Initial strength, integer 1-5 (default 3). A fractional value is refused" |
| `attrs` | object | no | additional attributes; a non-object — including a JSON-encoded string of one — is a 400 |
| `expires_at` | string | no | RFC3339 |
| `dedup_mode` | string | no | deduplication mode |
| `related_memory_ids` | array | no | related memory ids |
| `context_snippet` | string | no | context snippet for embedding |
| `supersedes_memory_id` | string | no | memory this supersedes |
| `tags` | array | no | stored with the memory; `fields="brief"` drops them |

**`base_strength` published the wrong range until `aihub#433` (merged as `c069570`),
and the cost was measured rather than argued.** It said "(0-1)" while
`memories.base_strength` is CHECK-constrained to 1-5 and the Go default was 3.0 —
three disjoint answers — and `Remember` validated the type but never the value, so a
number taken straight from the published range reached the CHECK and came back 500
with the driver's constraint text. The `aihub#412` corpus records **13 `pf_remember`
calls carrying `base_strength`, every value inside the published range and outside the
enforced one, against 13 `pf_remember` INTERNAL_ERRORs naming
`memories_base_strength_check`.** That is §6.1 T1-3's owner ruling, now landed.

**The range was only half of it, and `aihub#459` closed the other half on
2026-09-09: the value must also be a WHOLE NUMBER.** The published type is `number`
and the column is `SMALLINT`, so `2.5` used to satisfy every guard, get truncated
toward zero by pgx's int2 codec client-side — no error, so Postgres never saw the
fraction — and be stored as `2` under a 200. `aihub#475` measured that and made the
response report the row's own value rather than Go's arithmetic, which made the
answer honest without making it what the caller asked for. The owner's ruling picked
refusal over rounding and over widening the column, so the value is now a 400. The
type stays `number` — that is what JSON carries — which is exactly why the
description had to say `integer`: with the type unchanged, the published text is the
only place a caller can learn that an in-range `number` is refused.

`tags` reached the endpoint unpublished until `aihub#425`: this handler forwards its
whole argument map, so the value was on the wire and reachable only by guessing a
name no schema mentioned. Until then the only published way to tag a memory was to
create it and then call `pf_update_memory`.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`validatePfRememberArgs`) checks the four required
fields and refuses any `methodology.` prefix, then the handler passes the **argument
map verbatim** to `pkg/client/client.go` (`Remember`) → `POST /v1/memories`, bound by
`internal/server/routes_memory.go` (`handleRemember`).

Because the map is forwarded wholesale there is no forwarding table to drift from —
every published property is on the wire by construction, and the guard over
`internal/mcp/tools_memory.go` (`rememberSchema`) states that identity rather than
assuming it. The risk on this tool is at hops 1 and 4.

## hop 4 — what it actually does

- **`methodology.*` is refused client-side**, before the HTTP call, because those are
  work-item-bound credentialed artifacts that must go through `pf_save_artifact`.
  The two tools hit the **same endpoint**; the difference is that one injects attempt
  credentials and the other does not.
- **Only some type prefixes are embedded.** `experience.`, `fact.` and `rule.` get an
  `emb_vector`; the rest never do, and the vector recall path's WHERE requires one.
  So the type chosen here decides whether the memory is ever semantically
  recallable — which is why §6.2 T2-6's ruling was to add the DB CHECK for the four
  prefixes: the DB is the only layer that can make an unrecallable row impossible.
  `aihub#445` added it (`internal/db/migrations/0034_memories_type_check.sql`), so a
  row whose type carries no legal prefix, or carries a `|`, can no longer be created
  at all — not by this tool, not by `pf_save_artifact`, not by a hand-written INSERT.
- **The prefix is the contract; the 13 names are not.** An off-list type carrying a
  legal prefix — `experience.whatever` — is accepted, stored and recalled, and that
  is the DECIDED behaviour rather than a gap. `internal/domain/memory.go`
  (`MemoryTypePrefixes`) is the enforced vocabulary; `internal/domain/memory.go`
  (`MemoryTypeEnum`) is a 19-name select list for the `/ui` type dropdown
  (`internal/server/ui_handlers_memory.go`), and `internal/domain/memory.go`
  (`PfRememberTypeEnum`) is that list minus the six `methodology.*` entries, which
  is where the 13 come from. Both are derived from one another rather than typed
  twice, and NEITHER refuses anything. The consequence a caller should know: a type
  outside the 19 stores fine and the UI dropdown will never offer it back.
- **`visibility` published four of the column's five values until `aihub#495`
  (2026-09-09), and the missing one was `public`.** Migration
  `internal/db/migrations/0023_memories_visibility_public.sql` added it for
  unauthenticated artifact sharing and `aihub#434` mirrored the CHECK into Go —
  "Mirrored EXACTLY, 'public' included", `internal/domain/memory.go`
  (`memoryVisibilities`) — so `internal/domain/memory.go`
  (`validateMemoryVisibility`) has accepted it on this path ever since, and this
  tool's description said it did not exist. It is not a fifth rung on a ladder:
  `public` is the tier `internal/server/router.go`'s `GET /share/:id` gates on, and
  `internal/server/routes_artifacts.go` (`handleSharedArtifact`) is
  **unauthenticated**. That handler's own header already recorded the reachability
  — "`public` is settable by a project writer straight from `POST /v1/memories` …
  so it is not by itself a deliberate publication" — a fact about THIS tool,
  written on the read side, and published nowhere its caller could see it. The
  description now names the value AND the consequence, and the string is built from
  `internal/domain/memory.go` (`MemoryVisibilityList`), which is also what
  `internal/domain/work_item_fields.go` (`vocabularyErr`) renders into the 400 — so the set a
  caller is shown and the set the refusal names are one value in one order.
  ⚠️ **What a public memory written through THIS tool is actually reachable at is a
  narrower question than the tier suggests**, and the description stops short of
  answering it on purpose. `handleSharedArtifact` gates on `public` **and**
  `internal/server/routes_artifacts.go` (`hasRenderableBody`), which needs either a
  stored `rendered_html` — this tool publishes no `html` parameter, so it never
  writes one — or a type in the render set, which
  `internal/domain/memory.go` (`defaultRenderTypes`) makes the `methodology.*`
  names this tool refuses. On a default deployment the two conditions therefore
  cannot both hold for a `pf_remember` row. They are not a promise: the render set
  is configurable at startup (`internal/domain/memory.go` (`InitRenderTypes`)), so
  the conjunct is where an honest published claim stops.
- **`work_item_id` is validated against `project`.** A work item in another project
  is refused rather than silently stored.
- `dedup_mode` and `supersedes_memory_id` change what happens to an existing similar
  memory; neither is enumerated.
- **`attrs` must be a JSON object, and only when the CALLER sent it** (`aihub#465`).
  A JSON-encoded string of an object used to be stored verbatim under a 200; it is
  now a 400 naming the type, the byte length and — through
  `details.string_decodes_to` — whether the quoted text was itself valid JSON.
  Nothing is coerced. The provenance qualifier is load-bearing rather than
  decorative: `domain.UpdateMemory` re-enters this same write path carrying the
  attrs it just READ from the lineage head, and that inherited value is exempt, or
  editing a memory whose attrs is already a string would fail — which is the only
  way such a row can be repaired.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above spans 346 calls at a 4.91%
error rate. `id` and `memory_id` are both present, which is why callers in this repo
use either.

## Policy

- **§6.1 T1-3 (owner ruling) — LANDED** (`aihub#433`). Publish 1-5, the DB's scale,
  and validate it in Go at this entry point. `internal/domain/memory.go`
  (`MinBaseStrength`) / (`MaxBaseStrength`) / (`DefaultBaseStrength`) are taken from
  the column's own DDL, and `internal/domain/memory.go` (`validateBaseStrength`)
  rejects an out-of-range caller-stated value with a 400 that opens by naming the
  field. The guard sits above the first query in `internal/domain/memory.go`
  (`Remember`), so it covers `pf_remember`, `pf_save_artifact` and `pf_update_memory`
  alike — `internal/domain/memory.go` (`UpdateMemory`) builds a `RememberRequest` and
  goes through the same function.
- **§6.1 T1-3 residual (owner ruling 2026-09-09) — LANDED** (`aihub#459`). Refuse a
  non-integral `base_strength`; do not round it in Go, and do not widen the column.
  `internal/domain/memory.go` (`ValidateIntegralStrength`) is the rule, called by
  (`validateBaseStrength`) after the range check and by
  `internal/server/routes_memory.go` (`handleReinforceMemory`) on `strength_delta`,
  so one column keeps one answer about what may be in it. The order of the two
  checks is deliberate and pinned by a test: a value that is out of range AND
  fractional — which is every `base_strength` the `aihub#412` corpus carried — still
  takes the RANGE error, because that is the constraint it violated first.
  `internal/mcp/tools_memory_test.go`
  (`TestPublishedBaseStrengthRangeIsTheEnforcedOne`) now gates the word `integer` in
  the description against the behaviour of `internal/domain/memory.go` (`Remember`)
  itself, driven with a nil pool, so a guard that exists and is never called reads
  as red rather than as green.
- **§6.2 T2-6 (owner ruling) — LANDED** (`aihub#445`). Keep the leniency, stop
  calling the 13-value list an enum in a schema the SDK will not enforce, and add
  the DB CHECK for the four prefixes. Both halves shipped together, because either
  alone leaves the pair inconsistent in the other direction:
  - `type` is now a plain `string` whose description states the ENFORCED rule
    (prefix plus the `\|` ban) and then offers the 13 as an explicitly non-closed
    suggestion — `internal/mcp/tools_memory.go` (`memoryTypeParamDesc`), built from
    `internal/domain/memory.go` (`MemoryTypePrefixes`) and (`PfRememberTypeEnum`) so
    the text cannot drift from either. This is `aihub#238`'s rule a second time (a
    value the server does not validate is not published as a closed enum, as
    `internal/mcp/resource_schema_test.go`
    (`TestDeclaredResourcesProp_DescribesURIAndIntent`) already requires of
    `intent`), and the exact inverse of `aihub#463`, where the vocabulary really was
    closed so the repair was to make the server enforce it.
  - `memories.type` now carries `memories_type_check`, term-for-term equal to the Go
    predicate (`starts_with` is `strings.HasPrefix`, `strpos(type,'|') = 0` is
    `!strings.Contains`). `internal/domain/memory_type_check_test.go` parses the
    migration and fails if the two stop naming the same set, in the direction that
    matters: a CHECK stricter than Go would turn a 400 naming the field into a 500
    carrying the driver's constraint text, which is the `aihub#433` failure mode in
    a new place.
- **§6.1 T1-4 — the caller-facing half, applied to `visibility` by `aihub#495`
  (2026-09-09).** T1-4 says a vocabulary a DB CHECK enforces must also be checked in
  Go and answered with a 400 naming the field, because "the CHECK is the last line
  of defence and never the one facing the caller". `aihub#434` did that half. What
  it left is the half above it: the vocabulary the caller is SHOWN. Published as
  four of five values, `visibility` had a Go guard that would refuse nothing a
  caller sent — the caller simply never sent the fifth, because hop 1 said it did
  not exist. `internal/mcp/tools_memory.go` (`rememberVisibilityParamDesc`) builds
  the string from `internal/domain/memory.go` (`MemoryVisibilityList`), and
  `internal/mcp/visibility_vocab_publication_test.go` asserts the SET both ways — a
  legal value hop 1 hides fails, and an offered value the column refuses fails too,
  which is the direction a shortening back to a literal would take.
  ⚠️ **Scoped to this tool, and the arm says so rather than quietly measuring less
  than its name.** `pf_save_artifact`'s `visibility` still carries the same
  four-value literal and `pf_update_memory`'s names no values at all; both write
  this column, and both are the file scope of other work items in this batch. The
  fix there is one entry each in that gate's `visibilityVocabTools`.
- **§6.2 T2-19** — `pf_recall`'s `min_strength` must be put on the same scale, after
  T1-3 lands.

## Open

- **§6.4 item 4 — CLOSED by `aihub#445`, by removing the dependency rather than by
  taking the measurement.** The live distinct `type` set is *still* unread from
  outside the deployment — production Postgres sits inside the compose network
  (`docs/deployment.md`) — so the item was closed the only honest way available: the
  migration no longer cares. It adds the constraint `NOT VALID`, which enforces every
  INSERT and UPDATE immediately and withholds only the scan of rows already there,
  then counts the outliers itself and runs `VALIDATE CONSTRAINT` when there are none.
  A clean database therefore ends byte-identical to a plain validated CHECK; a dirty
  one succeeds anyway, with every offending type and its row count written to the
  constraint's `COMMENT` — which is where the measurement now lives, because a
  `RAISE WARNING` does not survive the deploy: measured on goose v3.28.0, a `goose up`
  over a table holding four outlier rows logged the migration `OK` and discarded the
  warning. Read it with `obj_description` on `pg_constraint`. What remains open is a
  fact about the data, not about this ruling: whether production holds any such rows
  is unknown until the migration runs there, and if it does, `convalidated` stays
  false until someone settles them and runs the one-line `VALIDATE`. Re-checked
  2026-09-09: `aihub#445` is `wrapped` (closed 2026-09-08), so the migration is
  merged and this is now a question about a deployment rather than about work in
  flight — read `obj_description` on `pg_constraint` wherever it has run.
