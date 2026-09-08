# pf_batch_create_work_items — contract card

```json
{
  "tool": "pf_batch_create_work_items",
  "description_sha256": "7ffd5d5890da0ab75a79538d59e05366153977d30a99188993ec9fe8cde224e0",
  "input_schema_sha256": "18d8a1df8a560c79eef21a2f1ace42d0f9a1687ce0d77411c5d22439a4776f3a",
  "params": {
    "items": {
      "type": "array",
      "required": true
    },
    "project": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "created",
    "created_count",
    "failed",
    "failed_count",
    "ok"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two top-level parameters; every per-item field is the shared set from
`internal/mcp/tools_lifecycle.go` (`workItemFieldProps`), plus a per-item `project`
override.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | default project for every item; an item may override it |
| `items` | array | yes | 1-50 objects, each taking the same fields as `pf_create_work_item` |

The `items` entry schema is published — via
`internal/mcp/tools_lifecycle.go` (`batchWorkItemsProp`) — rather than left as a bare
array, following the `aihub#238` rule that an array whose element shape is
undocumented is a contract the caller has to guess at.

Because the entry schema IS `workItemFieldProps`, every per-item field carries that
function's published description verbatim — including `requires_human_session`,
whose three states (`true` / `false` / omitted-means-`NULL`) `aihub#447` added
there. A batch item that omits it reaches the same `unclassified[]` segment as a
single create that omits it; there is no batch-specific default.

**Why a separate tool rather than an `items` array on `pf_create_work_item`:**
`project` and `goal` sit in that tool's flat `required` list, and `objectSchema`
cannot express "required unless `items` is set". Overloading it would leave the
published schema misdescribing its own contract. The same reasoning produced
`pf_ship` rather than `pf_commit(push=true)`.

## hop 2-3 — what leaves this process, and what binds it

There is **no batch endpoint**. The handler loops and makes one
`POST /v1/work_items` per item through `pkg/client/client.go` (`CreateWorkItem`),
bound by `internal/server/router.go` (`handleCreateWorkItem`) — the same hop-3 as
`pf_create_work_item`, N times.

Each item is cloned before defaulting, so filling in `project` or `force_reason`
does not edit the caller's own array.

## hop 4 — what it actually does

- **Items are created INDEPENDENTLY and one failure does not stop the rest.** The
  response reports `created` and `failed` separately and each failure carries the
  item's `index`, so a retry can resend exactly the ones that did not land. Aborting
  would leave the caller knowing only that "the batch failed", and re-sending the
  whole batch would then trip dedup on the ones that did.
- **Duplicate detection still runs per item**, so a 409 DUPLICATE/CANDIDATES on one
  item is a normal per-item outcome.
- **The batch is capped at 50 and a larger array is REFUSED, not truncated.** Each
  item is a sequential HTTP call inside one tool call with no partial flush and
  nothing visible until it returns, so an unbounded array turns one MCP call into an
  arbitrarily long silent stall. Silently creating a prefix of what was asked for is
  worse than creating nothing.
- **An item with no `goal` fails locally** and is reported with its index rather than
  sent.

## hop 5 — what comes back

`{ok, created_count, failed_count, created, failed}`. `created` carries one whole
record per item, which is why the content echo is suppressed **against that item's
own arguments** rather than against the batch's — a ten-item batch would otherwise
echo back up to ten bodies the caller sent in the very same call.

`ok` is `len(failed) == 0`, so a partially successful batch answers `ok: false`
while having created real work items. Read `created_count`, not `ok`.

## Policy

- **§6.1 T1-5** — the per-item content suppression is a delete, applied per item.
- **§6.1 T1-4** — the same CHECK-versus-schema gap `pf_create_work_item` has
  (`scenario`) applies to every item here, since hop 3 is the same handler.

## Open

- Nothing this card can settle. The `ok: false` on a partial success is stated
  behaviour, and the per-item `index` is the field that makes it recoverable.
