# pf_remember — contract card

```json
{
  "tool": "pf_remember",
  "description_sha256": "bec195df7750ddb2b3b4714765c5ded5e0a03f968e184a781d347c9e3f553fc7",
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
| `base_strength` | number | no | **"Initial strength (0-1)"** |
| `attrs` | object | no | additional attributes |
| `expires_at` | string | no | RFC3339 |
| `dedup_mode` | string | no | deduplication mode |
| `related_memory_ids` | array | no | related memory ids |
| `context_snippet` | string | no | context snippet for embedding |
| `supersedes_memory_id` | string | no | memory this supersedes |
| `tags` | array | no | stored with the memory; `fields="brief"` drops them |

🔴 **`base_strength`'s published range is wrong.** The description says 0-1; the DB
scale is **1-5**, and §6.1 T1-3 is the owner's ruling to publish 1-5 and validate it
in Go at the entry point, reusing the reinforce clamp's bounds. A caller following
the schema sends a value the storage layer treats as far weaker than intended.

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

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above spans 346 calls at a 4.91%
error rate. `id` and `memory_id` are both present, which is why callers in this repo
use either.

## Policy

- **§6.1 T1-3 (owner ruling)** — publish **1-5**, the DB's scale, and validate it in
  Go at this entry point reusing the reinforce clamp's bounds. The `(0-1)` above is
  the live defect that ruling names.
- **§6.2 T2-6** — keep the leniency, **stop calling the 13-value list an enum in a
  schema the SDK will not enforce**, and add the DB CHECK for the four prefixes.
- **§6.2 T2-19** — `pf_recall`'s `min_strength` must be put on the same scale, after
  T1-3 lands.

## Open

- **§6.4 item 4** — **T2-6's migration is unsized.** Adding a CHECK to a populated
  table fails on any existing off-prefix row, and the live distinct `type` set was
  never read. The ruling is accepted; the migration's cost is not known.
- T1-3 is filed (`aihub#433`) and not landed, so the range published above is still
  the wrong one at the time this card was written.
