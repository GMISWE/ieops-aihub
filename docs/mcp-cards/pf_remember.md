# pf_remember — contract card

```json
{
  "tool": "pf_remember",
  "description_sha256": "bec195df7750ddb2b3b4714765c5ded5e0a03f968e184a781d347c9e3f553fc7",
  "input_schema_sha256": "a3e5cd83578aa67d7bfaa21a221a997af5c7b9dcd25eaea86d2913007eedc428",
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
      "required": true,
      "enum": [
        "experience.approach",
        "experience.code",
        "experience.debug",
        "experience.pitfall",
        "fact.architecture",
        "fact.constraint",
        "fact.note",
        "fact.reference",
        "rule.coding",
        "rule.convention",
        "rule.process",
        "rule.scheduling",
        "rule.work"
      ]
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

Thirteen parameters, four required. `type` is the one real enum, drawn from the
domain list so the published set and the accepted set are one value.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | project name |
| `type` | enum | yes | full name, e.g. `experience.debug`; `methodology.*` refused here |
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
  recallable — which is why §6.2 T2-6's ruling is to add the DB CHECK for the four
  prefixes: the DB is the only layer that can make an unrecallable row impossible.
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
- **§6.2 T2-6** — keep the leniency, **stop calling the 13-value list an enum in a
  schema the SDK will not enforce**, and add the DB CHECK for the four prefixes.
- **§6.2 T2-19** — `pf_recall`'s `min_strength` must be put on the same scale, after
  T1-3 lands.

## Open

- **§6.4 item 4** — **T2-6's migration is unsized.** Adding a CHECK to a populated
  table fails on any existing off-prefix row, and the live distinct `type` set was
  never read. The ruling is accepted; the migration's cost is not known.
- **`aihub#459` is open and is the residual of T1-3.** The column is `SMALLINT` while
  both Go and the published schema say `number`, so an in-range FRACTIONAL value still
  cannot be stored as stated. Deliberately out of scope of the range fix.
