# pf_remember — contract card

```json
{
  "tool": "pf_remember",
  "description_sha256": "bec195df7750ddb2b3b4714765c5ded5e0a03f968e184a781d347c9e3f553fc7",
  "input_schema_sha256": "026d81dc9e37c2b6c4f49d1aa1b9f1f25cfc59d712ce7e540846ffca6b35cb01",
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
| `visibility` | string | yes | `private\|project\|team\|admin` |
| `work_item_id` | string | no | associated work item |
| `base_strength` | number | no | "Initial strength, 1-5 (default 3)" |
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
  false until someone settles them and runs the one-line `VALIDATE`.
- **`aihub#459` is open and is the residual of T1-3.** The column is `SMALLINT` while
  both Go and the published schema say `number`, so an in-range FRACTIONAL value still
  cannot be stored as stated. Deliberately out of scope of the range fix.
