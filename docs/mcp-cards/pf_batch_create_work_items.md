# pf_batch_create_work_items — contract card

```json
{
  "tool": "pf_batch_create_work_items",
  "description_sha256": "7ffd5d5890da0ab75a79538d59e05366153977d30a99188993ec9fe8cde224e0",
  "input_schema_sha256": "84d1c28a23eefa51ba63e05b08eb1fe7e9e5b4bcc9ec8b95ca1492345cd61360",
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
override — `internal/mcp/create_work_item_field_set_test.go`
(`TestBothCreateToolsPublishOneWorkItemFieldSet`) compares the two tools' live schemas
both ways and requires `project` to be the only field whose description differs, and
`TestBatchItemsPublishesItsOwnEntryShape` holds this tool at exactly two top-level
properties.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | default project for every item; an item may override it |
| `items` | array | yes | 1-50 objects, each taking the same fields as `pf_create_work_item` |

The `items` entry schema is published — via
`internal/mcp/tools_lifecycle.go` (`batchWorkItemsProp`) — rather than left as a bare
array, following the `aihub#238` rule that an array whose element shape is
undocumented is a contract the caller has to guess at, and
`internal/mcp/create_work_item_field_set_test.go`
(`TestBatchItemsPublishesItsOwnEntryShape`) fails outright when that entry schema is
missing rather than comparing an empty field set.

Because the entry schema IS `workItemFieldProps`, every per-item field carries that
function's published description verbatim — a byte-for-byte comparison in
`internal/mcp/create_work_item_field_set_test.go`
(`TestBothCreateToolsPublishOneWorkItemFieldSet`) — including
`requires_human_session`, whose three states (`true` / `false` /
omitted-means-`NULL`) `aihub#447` added there and
`internal/mcp/rhs_third_state_publication_test.go`
(`TestRequiresHumanSessionPublishesItsThirdState`) requires of this tool's nested copy
as well as of the two flat ones. A batch item that omits it reaches the same
`unclassified[]` segment as a single create that omits it and there is no
batch-specific default, which
`internal/mcp/batch_create_work_items_wire_shape_test.go`
(`TestBatchAppliesNoRequiresHumanSessionDefaultOfItsOwn`) drives both ways against an
explicit `false` in the same batch. The segmentation itself is held only from the
`items[]` side: `internal/domain/ready_queue_items_db_test.go`
(`TestGetReadyQueue_ItemsExcludesHumanSessionAndBlocked`) requires a work item whose
`requires_human_session` is SQL NULL to be absent from `items[]` — its own comment
says such a row belongs to `unclassified[]` — and nothing in the tree asserts that it
arrives THERE, so the destination is the queue's documented behaviour rather than a
checked one.

**Why a separate tool rather than an `items` array on `pf_create_work_item`:**
`project` and `goal` sit in that tool's flat `required` list while an item requires
only `goal` — both lists asserted by
`internal/mcp/create_work_item_field_set_test.go`
(`TestBatchItemsPublishesItsOwnEntryShape`) — and `objectSchema`
cannot express "required unless `items` is set". Overloading it would leave the
published schema misdescribing its own contract. The same reasoning produced
`pf_ship` rather than `pf_commit(push=true)`.

## hop 2-3 — what leaves this process, and what binds it

There is **no batch endpoint**. The handler loops and makes one
`POST /v1/work_items` per item through `pkg/client/client.go` (`CreateWorkItem`),
bound by `internal/server/router.go` (`handleCreateWorkItem`) — the same hop-3 as
`pf_create_work_item`, N times, which
`internal/mcp/batch_create_work_items_wire_shape_test.go`
(`TestBatchPostsEveryItemToTheSingleCreateRoute`) measures by driving both tools and
comparing the routes they actually posted to.

Each item is cloned before defaulting, so filling in `project` or `force_reason`
does not edit the caller's own array — held in both halves by
`internal/mcp/batch_item_clone_test.go` (`TestBatchItemDefaultingRunsOnACopy`): that
the copy is a copy, and that the loop makes it BEFORE the first write, since a
perfect copier called too late protects nothing.

## hop 4 — what it actually does

- **Items are created INDEPENDENTLY and one failure does not stop the rest.** The
  response reports `created` and `failed` separately and each failure carries the
  item's `index`, so a retry can resend exactly the ones that did not land —
  `internal/mcp/tools_fusion_test.go` (`TestBatchCreateContinuesPastAFailedItem`)
  drives a three-item batch whose middle item the server refuses and reads the index,
  the code and the two counts back off the response. Aborting
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
  sent, which `internal/mcp/batch_create_work_items_wire_shape_test.go`
  (`TestBatchReportsALocalFailureAgainstItsOwnIndex`) puts in the MIDDLE of a batch on
  purpose — an index reported as 0 is indistinguishable from a correct answer when the
  failing item is first — alongside `internal/mcp/tools_fusion_test.go`
  (`TestBatchCreateRejectsMalformedItemsWithoutSendingThem`) for the counts.
- **A per-item `attrs` that is not a JSON object is a per-item 400** (`aihub#465`,
  published here by `aihub#486`, recorded on this card by `aihub#495`), and
  `internal/mcp/batch_create_work_items_wire_shape_test.go`
  (`TestBatchPerItemRejectionCostsARoundTripAndTakesOneItemDown`) drives one such item
  through a three-item batch. The guard is
  `internal/domain/work_items.go` (`validateJSONObjectParam`), which runs on the
  server inside `CreateWorkItem` — so unlike the missing-`goal` check above it costs
  a round trip, and unlike a whole-batch refusal it takes exactly one item down: the
  400 lands in `failed` with that item's `index`, and every well-formed sibling is
  still created — both contrasts asserted by
  `TestBatchPerItemRejectionCostsARoundTripAndTakesOneItemDown` as OBSERVED REQUESTS,
  because a local pre-screen and a server refusal produce the same `failed` entry to a
  reader of the response. The rejection names the type and the byte length, and for a
  string reports through `details.string_decodes_to` whether the quoted text was
  itself valid JSON; nothing is coerced, which
  `internal/domain/json_object_params_test.go` (`TestStringifiedObjectParamIsRejected`)
  anchors at both ends of the message across every guarded site.
  ⚠️ **The failure this matters for is the one a batch makes likelier.** 18 of the 19
  stringified values in the `aihub#412` corpus
  are a model hand-writing escaped JSON that came out malformed, and a batch is
  where a model hand-writes N of them in one message — so N items can carry the same
  serialisation mistake and the response reports it N times, once per index, rather
  than as one failed call.
  <!-- prose-only: because=measurement --> Size is never the reason: `attrs` has no length cap,
  which is `internal/domain/work_items.go` (`jsonObjectParamSizeNote`)'s entry for
  it and is asserted against real behaviour rather than restated by
  `internal/domain/json_object_params_test.go`
  (`TestJSONObjectParamNoLengthCapPreserved`), with the PUBLISHED half bound to that
  same table by `internal/mcp/jsonb_size_claim_publication_test.go`
  (`TestPublishedNoLengthCapClaimIsBoundToTheDomainTable`).

## hop 5 — what comes back

`{ok, created_count, failed_count, created, failed}`. `created` carries one whole
record per item, which is why the content echo is suppressed **against that item's
own arguments** rather than against the batch's — a ten-item batch would otherwise
echo back up to ten bodies the caller sent in the very same call, and
`internal/mcp/wi_echo_test.go` (`TestBatchCreateSuppressesEachItemsOwnContent`) puts
an item that sent a body beside one that did not so the per-item judgement is what is
measured.

`ok` is `len(failed) == 0`, so a partially successful batch answers `ok: false`
while having created real work items — `internal/mcp/tools_fusion_test.go`
(`TestBatchCreateContinuesPastAFailedItem`) reads `ok: false` off a batch that created
two of three, and both new arms in
`internal/mcp/batch_create_work_items_wire_shape_test.go` re-assert the pair. Read
`created_count`, not `ok`.

## Policy

- **§6.1 T1-5** — the per-item content suppression is a delete, applied per item.
- **§6.1 T1-4** — the same CHECK-versus-schema gap `pf_create_work_item` has
  (`scenario`) applies to every item here, since hop 3 is the same handler: the
  registry side is `internal/domain/db_check_policy_test.go`
  (`TestDBCheckRegistry_AccountsForEveryCheck`), the guard itself
  `internal/domain/create_work_item_scenario_and_attrs_test.go`
  (`TestCreateAcceptsOneOfTheScenariosTheCheckPermits`), and the shared hop
  `internal/mcp/batch_create_work_items_wire_shape_test.go`
  (`TestBatchPostsEveryItemToTheSingleCreateRoute`), without which nothing says the
  gap is inherited at all.

## Open

- Nothing this card can settle. The `ok: false` on a partial success is stated
  behaviour, and the per-item `index` is the field that makes it recoverable.
  <!-- prose-only: because=judgement -->
