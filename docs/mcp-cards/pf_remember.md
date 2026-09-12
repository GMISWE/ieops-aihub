# pf_remember — contract card

```json
{
  "tool": "pf_remember",
  "description_sha256": "17bc65c8f97840767b49cbab31ab72ba366e95bded21840f8bff0529df13ae27",
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

Thirteen parameters, four required. **None of them is an enum** — asserted over the
whole published property set by `internal/mcp/remember_published_shape_test.go`
(`TestRememberPublishesNoClosedEnumOnAnyParam`), which is wider than the `type`
arm below on purpose: `dedup_mode` and `visibility` both have real closed
spellings behind them and are standing invitations to publish one. And `type`
stopped being one in `aihub#445` — `internal/mcp/tools_memory_type_vocab_test.go`
(`TestRememberTypeIsNotPublishedAsAClosedEnum`) — because what the server accepts is
a prefix rule (`internal/domain/memory_type_check_test.go`,
`TestMemoryTypeCheckMatchesTheGoPrefixes`), so the published set and the accepted set
are one set only if nothing closed is published.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | project name |
| `type` | string | yes | full name, e.g. `experience.debug`; must start with `experience.` / `fact.` / `rule.` and carry no `\|`; `methodology.*` refused here; 13 curated names are published as suggestions — prefixes and the no-pipe rule by `TestMemoryTypeCheckMatchesTheGoPrefixes`, the methodology subtraction by `TestPfRememberTypeEnum_NoMethodology` |
| `content` | string | yes | memory content |
| `visibility` | string | yes | one of `admin\|private\|project\|public\|team`, built from `domain.MemoryVisibilityList()`; `public` is the anonymous-share tier and the description says what it costs (`TestPublishedMemoryVisibilityVocabularyIsTheEnforcedOne`) |
| `work_item_id` | string | no | associated work item |
| `base_strength` | number | no | "Initial strength, integer 1-5 (default 3). A fractional value is refused" |
| `attrs` | object | no | additional attributes; a non-object — including a JSON-encoded string of one — is a 400 (`TestStringifiedObjectParamIsRejected`) |
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
with the driver's constraint text.
<!-- prose-only: because=history -->
The `aihub#412` corpus records **13 `pf_remember`
calls carrying `base_strength`, every value inside the published range and outside the
enforced one, against 13 `pf_remember` INTERNAL_ERRORs naming
`memories_base_strength_check`.** That is §6.1 T1-3's owner ruling, now landed.

**The range was only half of it, and `aihub#459` closed the other half on
2026-09-09: the value must also be a WHOLE NUMBER.** The published type is `number`
and the column is `SMALLINT`, so `2.5` used to satisfy every guard, get truncated
toward zero by pgx's int2 codec client-side — no error, so Postgres never saw the
fraction — and be stored as `2` under a 200. `aihub#475` measured that and made the
response report the row's own value rather than Go's arithmetic, which made the
answer honest without making it what the caller asked for.
<!-- prose-only: because=history --> The owner's ruling picked
refusal over rounding and over widening the column, so the value is now a 400. The
type stays `number` — that is what JSON carries — which is exactly why the
description had to say `integer`: with the type unchanged, the published text is the
only place a caller can learn that an in-range `number` is refused, which is why
`internal/mcp/remember_published_shape_test.go`
(`TestPublishedBaseStrengthTypeStaysNumber`) holds the type and the word as a pair —
narrowing the published type to `integer` would make the word redundant, and a
redundant sentence is the next one deleted.

`tags` reached the endpoint unpublished until `aihub#425`: the handler forwarded its
whole argument map, so the value was on the wire and reachable only by guessing a
name no schema mentioned.
Until then the only published way to tag a memory was to
create it and then call `pf_update_memory`.
<!-- prose-only: because=history -->

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`validatePfRememberArgs`) checks the four required
fields — `internal/mcp/tools_memory_test.go` (`TestValidatePfRememberArgs`) — and
refuses any `methodology.` prefix, then the handler passes the argument map,
**projected to the published property set**, to `pkg/client/client.go` (`Remember`)
→ `POST /v1/memories`, bound by `internal/server/routes_memory.go`
(`handleRemember`). The verb and the route are observed on a request a fake aihub
really received, alongside the two refusals costing no request at all, by
`internal/mcp/remember_wire_shape_test.go`
(`TestRememberRefusesItsOwnContractBeforeAnyRequest`).

The projection is `aihub#586` (owner ruling 2026-09-10), and it cuts both ways:
<!-- prose-only: because=external-state -->

- **Every published property is on the wire by construction.** The projection keeps
  exactly what `internal/mcp/tools_memory.go` (`rememberSchema`) publishes, so there
  is still no forwarding table to drift from, and the guard states that identity
  rather than assuming it: `internal/mcp/memory_tools_wire_test.go`
  (`TestMemoryToolsForwardEveryPublishedPropertyByValue`) asserts each landing as a
  value and (`TestMemoryToolsEveryPublishedPropertyHasAWireProbe`) is the
  completeness half.
- **No unpublished name is.** The map used to be forwarded VERBATIM, and
  `internal/domain/memory.go` (`RememberRequest`) binds five names this schema does
  not publish — `attempt_id`, `claim_epoch`, `session_secret`, `rendered_html` and
  `structured_payload`, the census read off the struct's own tags and pinned by
  `internal/mcp/wire_strip_test.go`
  (`TestRememberUnpublishedBindableKeysAreExactlyTheCensus`) — so a caller who
  guessed a spelling got a capability hop 1 never sold, and for `rendered_html` an
  anonymously shareable one (the 🟢 paragraph in the `visibility` bullet below).
  Now `internal/mcp/server.go` (`addTool`) strips the unknown set for this tool
  before the handler runs, and the response's `request_adjusted` names every
  stripped key under `unknown_params` — the `aihub#389` echo, whose `applied: []`
  claim this tool used to falsify and now satisfies by construction. Stripped keys
  absent from the wire, published siblings byte-identical, disclosure earned rather
  than unconditional: all read off real requests by
  `internal/mcp/remember_wire_shape_test.go`
  (`TestRememberStripsUnpublishedRenderedHTMLBeforeTheWire`), and across the whole
  wholesale-forwarding family — this tool, `pf_save_artifact`, `pf_update_memory` —
  by `internal/mcp/wire_strip_family_test.go`
  (`TestWireStrippedFamilyDropsUnpublishedKeysAndDisclosesThem`), with the roster
  itself pinned by `internal/mcp/wire_strip_test.go`
  (`TestWireStrippedToolsAreExactlyTheMemoryWriteFamily`).

The remaining risk on this tool is at hops 1 and 4.

## hop 4 — what it actually does

- **`methodology.*` is refused client-side**, before the HTTP call, because those are
  work-item-bound credentialed artifacts that must go through `pf_save_artifact` —
  and "before the HTTP call" is a request COUNT rather than an error string, which is
  what `internal/mcp/remember_wire_shape_test.go`
  (`TestRememberRefusesItsOwnContractBeforeAnyRequest`) reads. The two tools hit the
  **same endpoint**; the difference is that one injects attempt credentials and the
  other does not.
- **Only some type prefixes are embedded.** `experience.`, `fact.` and `rule.` get an
  `emb_vector`; the rest never do — `internal/domain/memory_vector_test.go`
  (`TestEmbeddableType`) walks all 19 curated names plus an off-list one — and the
  vector recall path's WHERE requires one, which is what
  `internal/domain/memory_recall_hybrid_test.go`
  (`TestPartitionTypesByEmbeddableAgreesWithEmbeddableType`) holds the routing
  against.
  So the type chosen here decides whether the memory is ever semantically
  recallable — which is why §6.2 T2-6's ruling was to add the DB CHECK for the four
  prefixes: the DB is the only layer that can make an unrecallable row impossible.
  `aihub#445` added it (`internal/db/migrations/0034_memories_type_check.sql`), so a
  row whose type carries no legal prefix, or carries a `|`, can no longer be created
  at all — by this tool, by `pf_save_artifact`, or by a hand-written INSERT, the
  column and the Go predicate driven over the same type set by
  `internal/domain/memory_type_check_db_test.go`
  (`TestMemoryTypeCheckDB_ColumnAndGoAgreeOnEveryType`) and the raw-INSERT half
  refused inside
  (`TestMigration0034_StillAppliesWhenTheTableAlreadyHoldsAnOutlier`).
- **The prefix is the contract; the 13 names are not.** An off-list type carrying a
  legal prefix — `experience.whatever` — is accepted, stored and recalled, and that
  is the DECIDED behaviour rather than a gap:
  `internal/domain/memory_commit_test.go`
  (`TestMemoryTypeEnum_OffListTypePassesLenientCheck`) is the lenient-check half and
  `internal/domain/memory_type_check_db_test.go`
  (`TestMemoryTypeCheckDB_OffListTypeStaysStorableEndToEnd`) carries it through a real
  column. `internal/domain/memory.go`
  (`MemoryTypePrefixes`) is the enforced vocabulary; `internal/domain/memory.go`
  (`MemoryTypeEnum`) is a 19-name select list for the `/ui` type dropdown
  (`internal/server/ui_handlers_memory.go`), and `internal/domain/memory.go`
  (`PfRememberTypeEnum`) is that list minus the six `methodology.*` entries, which
  is where the 13 come from — the 19 by `internal/domain/memory_commit_test.go`
  (`TestMemoryTypeEnum_Count`), the subtraction by
  `internal/domain/memory_supersede_scope_test.go`
  (`TestPfRememberTypeEnum_NoMethodology`), and the fact that this tool is the one
  the subtraction is for by `internal/mcp/tools_memory_type_vocab_test.go`
  (`TestRememberTypeDescriptionIsDerivedNotRetyped`). Both are derived from one
  another rather than typed twice (`TestRememberTypeDescriptionIsDerivedNotRetyped`),
  and NEITHER refuses anything — the off-list type stays storable end to end
  (`TestMemoryTypeCheckDB_OffListTypeStaysStorableEndToEnd`). The consequence a
  caller should know: a type outside the 19 stores fine and the UI dropdown will
  never offer it back.
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
  **unauthenticated** — `internal/server/routes_artifacts_test.go`
  (`TestSharedArtifact_Public_200`) drives it with no user at all and
  (`TestSharedArtifact_NonPublic_404`) is the tier half of the same gate.
  The reachability was written down on the READ side long before hop 1 carried it:
  `internal/server/routes_artifacts_share_lazy_test.go`
  (`TestSharedArtifact_StillNotFoundWithNothingToServe`) records "`public` is
  settable by a project writer straight from `POST /v1/memories` … so it is not by
  itself a deliberate publication" and is also the arm that holds it, since a
  `public` memory with content and no stored HTML still answers 404.
  ⚠️ This card used to attribute that sentence to `handleSharedArtifact`'s own
  header, where it does not appear — that header is about `hasRenderableBody`
  standing in for a `rendered_html != nil` gate — and the same wrong attribution
  still sits in `internal/mcp/tools_memory.go` (`rememberVisibilityParamDesc`) and
  in the `aihub#495` gate's own header, both left alone here because this slice is
  scoped to the card (measured 2026-09-10).
  The description now names the value AND the consequence, and the string is built
  from `internal/domain/memory.go` (`MemoryVisibilityList`), which is also what
  `internal/domain/work_item_fields.go` (`vocabularyErr`) renders into the 400 — so
  the set a caller is shown and the set the refusal names are one value in one
  order, compared as ORDERED sequences by
  `internal/mcp/remember_visibility_scope_test.go`
  (`TestPublishedVisibilityVocabularyMatchesTheRefusalInOrder`), which the
  `aihub#495` set arm cannot do because it sorts both sides.
  ⚠️ **What a public memory written through THIS tool is actually reachable at is a
  narrower question than the tier suggests**, and the description stops short of
  answering it on purpose. `handleSharedArtifact` gates on `public` **and**
  `internal/server/routes_artifacts.go` (`hasRenderableBody`), which needs either a
  stored `rendered_html` or a type in the render set; the second branch is closed to
  this tool, because `internal/domain/memory.go` (`defaultRenderTypes`) holds only
  `methodology.*` names it refuses — censused by
  `internal/domain/render_types_reach_test.go`
  (`TestDefaultRenderTypesAreOnlyTypesPfRememberRefuses`).
  🟢 **The first branch was open until `aihub#586` closed it (owner ruling
  2026-09-10, option ②).** `rendered_html` is an unpublished name on this tool, and
  until that fix the handler forwarded its whole argument map, so a caller who sent
  it under that exact spelling put it on the wire, `internal/domain/memory.go`
  (`RememberRequest`) bound it, and `internal/domain/memory.go`
  (`resolveRenderedHTML`) stored an explicit non-empty value verbatim for ANY type.
  <!-- prose-only: because=history -->
  So `visibility: public` plus an unpublished `rendered_html` was enough to put a
  `pf_remember` row into the state `internal/server/routes_artifacts_test.go`
  (`TestSharedArtifact_Public_200`) serves with no auth — the same
  unpublished-but-reachable path `tags` took until `aihub#425`, two paragraphs
  above, with anonymous readability rather than a second round trip as the stake.
  ⚠️ This card had reasoned that combination impossible on a default deployment;
  measured on 2026-09-10, the inference was unsound at its first step, and the
  measurement is what aihub#586 was filed on.
  The closure is the hop 2 projection in this card's hop 2-3 section: the name is
  stripped before the wire and named in `request_adjusted`, held by
  `internal/mcp/remember_wire_shape_test.go`
  (`TestRememberStripsUnpublishedRenderedHTMLBeforeTheWire`); and the journey this
  card once called undriven now runs whole — one call carrying `visibility: public`
  and `rendered_html`, the row's column required NULL, an ANONYMOUS `/share/:id`
  required not to serve the payload, and a REST control on the same run proving the
  server-side channel `pf_save_artifact`'s published `html` rides is untouched — in
  `internal/mcp/remember_strip_e2e_db_test.go`
  (`TestE2ERememberStripsRenderedHTMLFromTheShareSurface`), against a real database
  and the real router. `internal/domain/memory_render_test.go`
  (`TestResolveRenderedHTML_ExplicitOverrides`) still holds the storage precedence,
  deliberately: the binder and the store are untouched, so the closure lives
  entirely at this tool's boundary.
  The closure of the second branch is no promise either: the render set is
  configurable at startup (`internal/domain/memory.go` (`InitRenderTypes`)), so a
  deployment can open that one too — `internal/domain/render_types_reach_test.go`
  (`TestInitRenderTypesAdmitsATypePfRememberAccepts`) — and the conjunct is where an
  honest published claim stops.
- **`work_item_id` is validated against `project`**
  (`TestRememberWorkItemRefIsScopedToTheRequestProject`, driven end to end by
  `TestRememberRejectsCrossProjectWorkItem`). A work item in another project
  is refused rather than silently stored — the predicate lives inside the resolving
  query, so "no such work item" and "not in this project" are one zero-row outcome
  (`internal/domain/memory_work_item_scope_test.go`,
  `TestRememberWorkItemRefIsScopedToTheRequestProject`), driven end to end across two
  real projects by `internal/server/remember_work_item_scope_db_test.go`
  (`TestRememberRejectsCrossProjectWorkItem`).
- `dedup_mode` and `supersedes_memory_id` change what happens to an existing similar
  memory and neither is enumerated — all three `dedup_mode` spellings driven against
  a real column by `internal/server/remember_work_item_scope_db_test.go`
  (`TestRememberRejectsCrossProjectWorkItem`), the supersede side by
  `internal/domain/memory_cursor_test.go`
  (`TestRemember_SupersedeInheritsActivationState`), and the enum half by the
  whole-tool census in `internal/mcp/remember_published_shape_test.go`
  (`TestRememberPublishesNoClosedEnumOnAnyParam`). Those three spellings are `off`
  annotating nothing, `suggest` recording `attrs.similar_to`, and `strict` answering
  409 `CONFLICT_SIMILAR_MEMORY` while naming the row it collided with, with a
  dissimilar control beside them so a mode that refused everything would fail
  (`TestRememberRejectsCrossProjectWorkItem`).
- **`attrs` must be a JSON object, and only when the CALLER sent it** (`aihub#465`).
  A JSON-encoded string of an object used to be stored verbatim under a 200; it is
  now a 400 naming the type, the byte length and — through
  `details.string_decodes_to` — whether the quoted text was itself valid JSON.
  Nothing is coerced. The provenance qualifier is load-bearing rather than
  decorative: `domain.UpdateMemory` re-enters this same write path carrying the
  attrs it just READ from the lineage head, and that inherited value is exempt —
  `internal/domain/json_object_params_test.go` (`TestRememberAttrsProvenanceSplit`)
  and (`TestResolveUpdateMemoryAttrsReportsProvenance`) hold the two sides of that
  split — or editing a memory whose attrs is already a string would fail, which is
  the only way such a row can be repaired.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above spans 346 calls at a 4.91%
error rate. `id` and `memory_id` are both present and carry the same value, which is
why callers in this repo use either — asserted on a live 201 by
`internal/server/remember_work_item_scope_db_test.go`
(`TestRememberRejectsCrossProjectWorkItem`), because K10 only fails on a live key the
card does NOT declare and so cannot see a key that stops being sent.

`embedded_len` (aihub#504, 2026-09-12) is a CONDITIONAL key: present iff the
stored vector embeds a strict prefix of the content — the embedding input budget
(`internal/embedding/input_budget.go`, `DefaultInputMaxRunes`) truncated it —
valued at how many leading runes the vector covers, with the presence rule in
`internal/domain/memory.go` (`finalizeEmbeddedLen`) and its two directions
calibrated by `internal/domain/embedded_len_test.go`
(`TestFinalizeEmbeddedLen_KeepsAStrictPrefix`) /
(`TestFinalizeEmbeddedLen_SuppressesFullCoverage`). It is absent from the corpus
record above because it postdates the aihub#412 corpus, whose calls also never
stored over-budget content.

## Policy

- **§6.1 T1-3 (owner ruling) — LANDED** (`aihub#433`). Publish 1-5, the DB's scale,
  and validate it in Go at this entry point. `internal/domain/memory.go`
  (`MinBaseStrength`) / (`MaxBaseStrength`) / (`DefaultBaseStrength`) are taken from
  the column's own DDL — `internal/domain/memory_base_strength_range_test.go`
  (`TestBaseStrengthBoundsAreTheColumnsOwn`) parses that DDL — and
  `internal/domain/memory.go` (`validateBaseStrength`)
  rejects an out-of-range caller-stated value with a 400 that opens by naming the
  field (`TestValidateBaseStrengthRejectsWhatTheColumnWouldRefuse`). The guard sits
  above the first query in `internal/domain/memory.go`
  (`Remember`) — `TestRememberRejectsOutOfRangeBaseStrengthBeforeThePool` proves the
  ordering with a nil pool — so it covers `pf_remember`, `pf_save_artifact` and
  `pf_update_memory` alike: `internal/domain/memory.go` (`UpdateMemory`) builds a
  `RememberRequest` and goes through the same function.
- **§6.1 T1-3 residual (owner ruling 2026-09-09) — LANDED** (`aihub#459`). Refuse a
  non-integral `base_strength`; do not round it in Go, and do not widen the column —
  the refusal wired above the first query by
  `internal/domain/memory_base_strength_range_test.go`
  (`TestRememberRefusesFractionalBaseStrengthBeforeThePool`), and the truncation the
  unwidened `SMALLINT` still performs on anything that gets past it by
  (`TestBaseStrengthIsTruncatedByThePgxInt2Codec`).
  `internal/domain/memory.go` (`ValidateIntegralStrength`) is the rule
  (`TestValidateIntegralStrengthIsTheWholeNumberRule`), called by
  (`validateBaseStrength`) after the range check and by
  `internal/server/routes_memory.go` (`handleReinforceMemory`) on `strength_delta` —
  `internal/server/routes_memory_reinforce_integral_test.go`
  (`TestReinforceRefusesFractionalStrengthDeltaBeforeThePool`) drives that second
  caller — so one column keeps one answer about what may be in it. The order of the
  two checks is deliberate and pinned by
  `internal/domain/memory_base_strength_range_test.go`
  (`TestValidateBaseStrengthRefusesFractionalInRangeValues`): a value that is out of
  range AND fractional — which is every `base_strength` the `aihub#412` corpus
  carried — still takes the RANGE error, because that is the constraint it violated
  first.
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
    the text cannot drift from either, with both halves held by
    `internal/mcp/tools_memory_type_vocab_test.go`
    (`TestRememberTypeIsNotPublishedAsAClosedEnum`) and
    (`TestRememberTypeDescriptionStatesWhatIsEnforced`). This is `aihub#238`'s rule a
    second time (a
    value the server does not validate is not published as a closed enum, as
    `internal/mcp/resource_schema_test.go`
    (`TestDeclaredResourcesProp_DescribesURIAndIntent`) already requires of
    `intent`), and the exact inverse of `aihub#463`, where the vocabulary really was
    closed so the repair was to make the server enforce it.
  - `memories.type` now carries `memories_type_check`, term-for-term equal to the Go
    predicate (`starts_with` is `strings.HasPrefix`, `strpos(type,'|') = 0` is
    `!strings.Contains`) — `internal/domain/memory_type_check_test.go`
    (`TestMemoryTypeCheckMatchesTheGoPrefixes`) is where the two are compared term
    for term. `internal/domain/memory_type_check_test.go` parses the
    migration and fails if the two stop naming the same set, in the direction that
    matters: a CHECK stricter than Go would turn a 400 naming the field into a 500
    carrying the driver's constraint text, which is the `aihub#433` failure mode in
    a new place.
- **§6.1 T1-4 — the caller-facing half, applied to `visibility` by `aihub#495`
  (2026-09-09).** T1-4 says a vocabulary a DB CHECK enforces must also be checked in
  Go and answered with a 400 naming the field, because "the CHECK is the last line
  of defence and never the one facing the caller" — the repo-wide register of that
  policy, this column's row included, is
  `internal/domain/db_check_policy_test.go`
  (`TestDBCheckRegistry_MirrorsMatchTheMigration`). `aihub#434` did that half. What
  it left is the half above it: the vocabulary the caller is SHOWN. Published as
  four of five values, `visibility` had a Go guard that would refuse nothing a
  caller sent — the caller simply never sent the fifth, because hop 1 said it did
  not exist.
  <!-- prose-only: because=history -->
  `internal/mcp/tools_memory.go` (`rememberVisibilityParamDesc`) builds
  the string from `internal/domain/memory.go` (`MemoryVisibilityList`), and
  `internal/mcp/visibility_vocab_publication_test.go` asserts the SET both ways — a
  legal value hop 1 hides fails, and an offered value the column refuses fails too,
  which is the direction a shortening back to a literal would take.
  ⚠️ **Scoped to this tool and `pf_update_memory`, and the arm says exactly what it
  covers rather than quietly measuring less than its name.** `pf_update_memory`'s
  `visibility` used to name no values at all ("New visibility (omit to keep
  current)") and since `aihub#529` shares this tool's derivation — both descriptions
  concatenate `internal/mcp/tools_memory.go` (`visibilityVocabAndConsequence`) — so
  `internal/mcp/visibility_vocab_publication_test.go`
  (`TestPublishedMemoryVisibilityVocabularyIsTheEnforcedOne`) asserts its set and
  its `public` consequence exactly as it does this tool's. `pf_save_artifact`'s
  `visibility` still carries the same four-value literal — the count recorded and
  checked against the live schema by
  `internal/mcp/remember_visibility_scope_test.go`
  (`TestOnlyTheScopedToolsPublishTheWholeVisibilityVocabulary`) — and it writes this
  column, and it is the file scope of another work item in this batch. The fix
  there is one entry in that gate's `visibilityVocabTools`, which is also what
  moves the population of `internal/mcp/remember_visibility_scope_test.go`
  (`TestOnlyTheScopedToolsPublishTheWholeVisibilityVocabulary`): it derives the set
  of tools publishing the column from the live tool list and partitions it against
  the two maps both ways, so a third such tool, a widened literal, and a stale
  exemption are each red rather than each silent.
- **§6.2 T2-19** — `pf_recall`'s `min_strength` must be put on the same scale, after
  T1-3 lands.

## Open

- **§6.4 item 4 — CLOSED by `aihub#445`, by removing the dependency rather than by
  taking the measurement.** The live distinct `type` set is *still* unread from
  outside the deployment — production Postgres sits inside the compose network
  (`docs/deployment.md`) — so the item was closed the only honest way available: the
  migration no longer cares.
  <!-- prose-only: because=external-state -->
  It adds the constraint `NOT VALID`, which enforces every
  INSERT and UPDATE immediately and withholds only the scan of rows already there,
  then counts the outliers itself and runs `VALIDATE CONSTRAINT` when there are none
  — `internal/domain/memory_type_check_db_test.go`
  (`TestMigration0034_SelfValidatesOnACleanTable`) drives the clean branch through a
  Down/Up round trip and requires `convalidated` back.
  A clean database therefore ends byte-identical to a plain validated CHECK; a dirty
  one succeeds anyway, with every offending type and its row count written to the
  constraint's `COMMENT`, still refusing every new write, and reaching `convalidated`
  once the outliers are settled and the one-line `VALIDATE` is run —
  `internal/domain/memory_type_check_db_test.go`
  (`TestMigration0034_StillAppliesWhenTheTableAlreadyHoldsAnOutlier`) constructs
  exactly that losing case and walks it to the repair.
  The `COMMENT` is where the measurement lives because a `RAISE WARNING` does not
  survive the deploy: measured on goose v3.28.0, a `goose up`
  over a table holding four outlier rows logged the migration `OK` and discarded the
  warning.
  <!-- prose-only: because=measurement -->
  Read it with `obj_description` on `pg_constraint`. What remains open is a
  fact about the data rather than about this ruling: whether production holds any
  such rows is unknown until the migration runs there.
  Re-checked
  2026-09-09: `aihub#445` is `wrapped` (closed 2026-09-08), so the migration is
  merged and this is now a question about a deployment rather than about work in
  flight — read `obj_description` on `pg_constraint` wherever it has run.
  <!-- prose-only: because=external-state -->
