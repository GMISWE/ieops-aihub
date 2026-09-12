# pf_recall — contract card

```json
{
  "tool": "pf_recall",
  "description_sha256": "6b2d9c495ef9d6eb408fda58df6c60e6c3e84854b0bc51d8bb109bd8589f28fb",
  "input_schema_sha256": "e649e667644e71892c74bc35c230396c6a75033e6308fbd12177832aa9c6b11e",
  "params": {
    "cursor": {
      "type": "string",
      "required": false
    },
    "fields": {
      "type": "string",
      "required": false,
      "enum": [
        "brief"
      ]
    },
    "include_archived": {
      "type": "boolean",
      "required": false
    },
    "min_strength": {
      "type": "number",
      "required": false
    },
    "project": {
      "type": "string",
      "required": true
    },
    "query": {
      "type": "string",
      "required": false
    },
    "similarity_threshold": {
      "type": "number",
      "required": false
    },
    "top_k": {
      "type": "string",
      "required": false
    },
    "type": {
      "type": "array",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "items",
    "next_cursor",
    "request_adjusted",
    "total",
    "unmatched_types"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Ten parameters. This is the tool `aihub#148` was filed against, and the split
between "published" and "forwarded" here is the repo's reference example of a hop-2
defect.
<!-- prose-only: because=history -->

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | project name |
| `query` | string | no | semantic search query |
| `type` | array | no | ARRAY of type names; `.*` is a prefix wildcard and `\|` is NOT a separator — both held by `TestUnmatchedTypes` |
| `work_item_id` | string | no | filter by work item — canonical id or slug |
| `top_k` | string | no | default 20, ceiling 200; a JSON number is accepted (`TestWireQueryRecallTopKAcceptsAJSONNumber`) |
| `similarity_threshold` | number | no | cosine 0-1, vector half only, **OFF by default** |
| `cursor` | string | no | TEXT-path paging only |
| `min_strength` | number | no | effective strength = `base_strength` (1-5) after decay; default 0.3 filters nothing |
| `include_archived` | boolean | no | default false |
| `fields` | enum | no | `brief` — first line only, drops `related`/`tags` |

`recency_weight` was published here from the day this tool was added (`50bfc35`)
until `aihub#469` withdrew it, and bound by `handleRecall` into
`domain.RecallRequest.RecencyWeight`. Forwarding came later and in two steps:
`6eda521` began forwarding it from an inline conditional, and only `7cb982f`
factored that into the slice now called `recallNumberParams`. For the 20 commits
between `50bfc35` and `6eda521` it was published and NOT forwarded, along with
`similarity_threshold` and `min_strength` — that gap is `aihub#148`.
<!-- prose-only: because=history -->

No read ever reached the ranking: on the tree as withdrawn, none of the six
functions in `internal/domain` that take a `*RecallRequest` touched the field, so
every value returned the page the caller would have got without it, no error and
no warning. Not "nothing ever read it" — `Recall` held a self-default
(`if req.RecencyWeight <= 0 { req.RecencyWeight = 0.3 }`) until `9064a35`
deleted it on 2026-06-05, and `aihub#424` counted exactly that shape as a read.
The accurate claim is narrower and survives either way: no read of this field
could ever change a result.

Its hop 0-1 promise was `default 0.3`, while the source comment — at the head of
`recallText` in `internal/domain/memory.go`, not on the field itself — said the
default was deliberately NOT applied. The row asserted exactly one thing and that
was the thing least true.

It is gone rather than implemented, and not for want of demand — implementing it
was measured to be a regression. `docs/design/polyforge-v1-design.md` §7.5
specified `sim×(1-w) + normalized_recency×w + normalized_strength×0.1` at w=0.3,
the same shape as the fused score `aihub#311` removed from the vector path as a
defect. The embedding model packs a result set's cosines into a band ~0.04 wide,
so a 0.3-weighted recency term spans several times the whole spread of the signal
it blends into: replayed on a real 20-row result set, the highest-cosine row fell
from rank 1 to rank 10 and similarity inversions rose from 16/190 to 98/190. And
there was nothing to win, because recency was never absent — see hop 4.

⚠️ That row is also the reason `aihub#469` exists rather than being caught: the
card gate's K4 arm (`internal/mcp/contract_cards_gate_test.go`) requires only that a
published parameter be named in backticks somewhere in the prose. The row was four cells wide and its whole hop-1 promise
was the two words "default 0.3" — but it did carry the backticked name, so K4 was
satisfied. K4 cannot tell "documented" from "listed", which is the gap the hop-4
arm added by this work item closes from the other side.

`visibility` was published here as "Filter by visibility" from the day this tool
was added (`50bfc35`, where all three hops are already present — unlike
`recency_weight`, forwarding never lagged publication) until `aihub#484` withdrew
it on 2026-09-09, forwarded in
`internal/mcp/tools_memory.go` (`recallStringParams`) and bound by
`internal/server/routes_memory.go` (`handleRecall`) into
`internal/domain/memory.go` (`RecallRequest`).
<!-- prose-only: because=history -->
It is the second instance of the
class above and the reason the two rows read differently: it was found by
`internal/mcp/recall_hop4_reader_gate_test.go` on that gate's FIRST run, having
been written for `recency_weight`.

Hops 1-3 were intact and hop 4 was empty. None of the six functions in
`internal/domain` that take a `*RecallRequest` read the field, so a caller
sending `visibility=project` got the whole page — no error, no warning, and a
response byte-identical to the one they would have got without it. The
visibility predicates that DO exist on both recall paths are authorization
scoping derived from `CallerRole`/`CallerUserID` — `AND (visibility != 'private'
OR author_user_id = $n)` and `AND visibility != 'admin'`, SQL literals rather
than the caller's filter, with all three of those held by
`internal/domain/recall_card_claims_test.go`
(`TestRecallVisibilityScopingIsCallerDerivedOnBothPaths`): both predicates present
on both paths, each emitted only under a guard that reads the caller's role, and
no bindable request field a caller could name. That is why a name-only search made the field look
read, and why withdrawing it changes nothing about who can see what.

⚠️ Its disposition was **not** the same call as `recency_weight`'s, which is why
it was tracked for a work item of its own rather than folded in.
<!-- prose-only: because=history --> `recency_weight`
duplicated ordering the code already had, so implementing it was measurably a
regression. There is no such duplicate here: the recall path genuinely cannot
filter by visibility, so implementing it would have been a real capability, and
choosing not to build one is an owner's call rather than an engineering finding.
The owner ruled withdraw on 2026-09-09, on **demand rather than harm** — over the
transcript corpus (850 raw `pf_recall` calls deduplicated by `tool_use` id to
835, spanning 2026-06-23 to 2026-09-09) ZERO carried a `visibility` argument. A
second corpus window recorded inside this repo agrees:
`../audits/aihub-412-corpus-facts/param-types-vs-schema.md` marks this tool's
`visibility` row "published, never observed". Nothing published was ever used, so
the withdrawal costs no caller a capability they had.

The `type` description exists because three skill templates taught a single
pipe-separated string in place of the array, nothing split it, and the resulting
empty set read as "no relevant memory". (The offending literal is deliberately not
reproduced here: `internal/cli/skill_recall_type_test.go` sweeps this directory and
fails on any `type=` value containing a pipe, which is the correct behaviour — a
document that spells the anti-pattern out is a document somebody copies from.) `fields` is `propEnum` rather than `prop` because `fields` conventionally
names a field LIST, so `fields="id,type"` is a natural guess that would silently
return the full response — the exact cost the parameter exists to remove.
<!-- prose-only: because=counterfactual -->

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildRecallParams`) renders the arguments into a
query string for `pkg/client/client.go` (`Recall`) → `GET /v1/memories`, bound by
`internal/server/routes_memory.go` (`handleRecall`). Three forwarding classes:

- `recallStringParams` — through `scalarArg`, because `top_k` is published as a
  string and "max results: 10" is naturally written as a number, so both spellings
  have to reach the wire as the same value: pinned by
  `internal/mcp/recall_params_wiring_test.go`
  (`TestRecallForwardsEveryPublishedParamByValue`, whose `top_k` probes cover the
  string and the JSON number alike) and on a recorded request by
  `internal/mcp/recall_wire_query_test.go`
  (`TestWireQueryRecallTopKAcceptsAJSONNumber`).
- `recallNumberParams` — through `parseNumArg`, which tolerates the *string*
  spelling of a number and REFUSES text it cannot read, both directions driven in
  `internal/mcp/recall_wire_query_test.go` by
  `TestWireQueryRecallStillAcceptsEveryReadableSpelling` and
  `TestWireQueryRecallRefusesUnreadableNumbers`. The string half is
  aihub#148 defect 2: a bare numeric read returns 0 for
  `similarity_threshold: "0.99"`, and 0 is this tool's "not specified", so a caller
  who quoted the value had the filter silently discarded.
  <!-- prose-only: because=history -->
  The refusal half is
  `aihub#432`: until it, unparseable text ALSO came back as 0, so
  `similarity_threshold: "notanumber"` disabled the filter and no hop said so.
  <!-- prose-only: because=history -->
  A present-but-unreadable number is now refused by name before the request leaves
  this process — Rule 1 (`internal/server/queryparam.go`) applied one hop upstream
  of where it was written, and the refusal naming the parameter while making no
  request at all is asserted by `internal/mcp/recall_wire_query_test.go`
  (`TestWireQueryRecallRefusesUnreadableNumbers`), with every number this tool
  forwards required to carry such a probe by
  `internal/mcp/recall_params_wiring_test.go`
  (`TestRecallEveryNumericParamHasARejectionProbe`). A zero still means "not
  specified"; that ambiguity is separate and unfixed.
- `type` — a JSON array joined comma-separated; a bare string is also accepted,
  both shapes pinned by value in `internal/mcp/recall_params_wiring_test.go`
  (`TestRecallForwardsEveryPublishedParamByValue`).

Two deliberate asymmetries:

- **`fields` is NOT forwarded** (`TestRecallFieldsNeedsNoServerHop`). The projection is a property of what this process
  hands the model, and this process is the last hop before the model, so it is
  consumed exactly where it is read — the empty wire value pinned by
  `internal/mcp/recall_params_wiring_test.go`
  (`TestRecallForwardsEveryPublishedParamByValue`) and the upstream request shown
  unchanged by a brief call in `internal/mcp/recall_brief_wiring_test.go`
  (`TestRecallFieldsNeedsNoServerHop`). Adding it to the loop would make the server
  ignore an unread query param — a third instance of `aihub#148`.
- **`recall_algo` is forwarded but not published**, from an explicit argument or the
  `POLYFORGE_RECALL_ALGO` environment variable, with both sources and the
  explicit-wins precedence driven by `internal/mcp/recall_params_wiring_test.go`
  (`TestRecallUnpublishedForwardedParamsAreDocumented`), which also refuses an
  entry that has since become published. Its exemption reason is contract
  surface, not a dead end: nothing in any response advertises it, so no caller is
  shown a value it cannot use.

`similarity_threshold` was published here, fully implemented in domain, and carried
by **neither** hop in between. Confirmed live on the pre-fix build: passing 0.99 and
passing nothing returned the same 20 items in the same order.

## hop 4 — what it actually does

- **Ordering, and why there is no knob for recency.** All three paths already rank
  by recency, so there was never a dimension for a caller to turn up:
  - **text, default** — `ORDER BY GREATEST(last_activated_at, created_at) DESC,
    id DESC` (`memRefTimeSQL`). Recency is the only ranking SIGNAL — `id DESC`
    is the deterministic tiebreaker for rows sharing a reference time and
    `aihub#239`'s cursor key rather than a second signal, which
    `internal/domain/recall_card_claims_test.go`
    (`TestRecallOrderingSignalsAreTheOnesTheCardNames`) holds as a TERM COUNT
    against the clause quoted here, built from `memRefTimeSQL` so the card and
    the constant cannot move apart. There is no similarity
    score on this path to blend it against.
  - **text, `recall_algo=lexical`** — `ts_rank` first, then `tanh` of effective
    strength, which carries `exp(-days/stability)`, the order of the two terms and
    the decay factor inside the second both required by
    `internal/domain/recall_card_claims_test.go`
    (`TestRecallOrderingSignalsAreTheOnesTheCardNames`).
  - **vector** — cosine bucketed to 0.01 first, then effective strength inside a
    bucket, same decay term. `aihub#311` made cosine primary after a fused
    `0.7*cosine + 0.3*tanh(strength)` score ranked a memory below a LESS similar
    one for being older and more activated: the target held the set's highest
    cosine (0.7227) and came second to 0.7202. Reweighting was measured
    insufficient — the model packs a result set's cosines into a band ~0.04 wide,
    so any non-trivial second term flips gaps that small. Bucketing keeps recency
    deciding only genuine near-ties.

  This is the hop `recency_weight` was withdrawn from (`aihub#469`), and the two
  facts are one fact: the knob was never read, and had it been honoured with the
  formula the design document specified, it would have re-created the defect
  `aihub#311` fixed.
  <!-- prose-only: because=counterfactual -->
  At w=0.3 a 30-day age gap overturns a cosine gap of 0.2709,
  6.8× the whole band; keeping cosine dominant would need w < 0.06.
- **`similarity_threshold` has no default and must keep none.** Measured on one
  project with limit=200: a pure-punctuation noise query scores 0.4712 at its WORST
  hit while a real query whose top hit is correct scores 0.4798 at its BEST — 0.0086
  apart, and the wrong way round for six of the noise query's hits, with the
  OFF-by-default half held over four argument shapes by
  `internal/mcp/recall_params_wiring_test.go` (`TestRecallThresholdHasNoDefault`)
  and on a recorded request by `internal/mcp/recall_wire_query_test.go`
  (`TestWireQueryRecallOmitsThresholdByDefault`). No global cutoff
  separates noise from signal. A threshold that matches nothing returns an empty list
  rather than falling back to text search: empty is the intended answer.
- **`cursor` works on the TEXT path only.** The vector path and the hybrid merge both
  set an empty cursor and return a nil `next_cursor`, so paging a semantic recall
  gets page one forever — held by `internal/domain/recall_card_claims_test.go`
  (`TestRecallCursorIsPromisedOnTheTextPathOnly`), which merges two halves that
  BOTH carry a cursor, since merging two cursorless ones would prove nothing.
- **`cursor` is composite, and only half of it is validated**
  (`TestCursor_MalformedIsRejectedBeforeTheQuery`,
  `TestCursor_RecallIdHalfIsNotConstrained`). The token is
  `<RFC3339Nano>|<id>` — reference time plus the `id DESC` tiebreaker `aihub#239`
  added — so a malformed one is refused with a 400 naming the parameter
  (`aihub#435`), but the check reads the TIMESTAMP half only, both halves held in
  `internal/server/cursor_validation_test.go` by
  `TestCursor_MalformedIsRejectedBeforeTheQuery` (six plausible malformed values,
  each refused before the query runs, with the parameter name anchored at the
  start of the message) and `TestCursor_RecallIdHalfIsNotConstrained` (five id
  halves, including an empty one, all answered 200 and passed through verbatim). The id half stays
  opaque on purpose: constraining it would put a second copy of the id format in
  the handler, which starts refusing cursors this server is still issuing the day
  the format moves. Before `aihub#435` the whole token went raw into
  `$n::timestamptz` and a token the server never issued came back 500 with the
  driver's text. Cursors minted before `aihub#239` carry the timestamp alone and
  still pass — the empty-id-half case `TestCursor_RecallIdHalfIsNotConstrained`
  answers 200.
- **There is no caller-facing visibility filter, and there never was one.** The
  `visibility` clauses in both recall paths are authorization scoping the server
  derives from the caller's own role and id, so they narrow a page the same way
  whether or not the caller says anything, and this tool publishing no selector for
  it is held by `internal/mcp/recall_visibility_selector_test.go`
  (`TestRecallOffersNoVisibilitySelectorAndWithholdsTheFieldItself`), which also
  sends the withdrawn name anyway and requires the query string to stay clean of
  it. The published parameter that appeared
  to offer control over them was withdrawn by `aihub#484` on 2026-09-09 — see
  hop 0-1.

  🔴 **A caller cannot read a recall row's `visibility` either, and this bullet used
  to say they could.** The MCP projection withholds the field per item with its
  reason recorded beside it (`internal/mcp/recall_slim.go`,
  `recallItemWithheldKeys`: "an access-control fact already enforced
  server-side") — withheld end to end by
  `TestRecallOffersNoVisibilitySelectorAndWithholdsTheFieldItself` — so a recall
  page never carries it; the one MCP read that does is
  `pf_get_memory`, which projects nothing. Both directions are driven by
  `internal/mcp/recall_visibility_selector_test.go`
  (`TestRecallOffersNoVisibilitySelectorAndWithholdsTheFieldItself`) and the
  withheld set as a whole by `internal/mcp/recall_projection_test.go`
  (`TestRecallResultWithholdsExactlyTheDocumentedKeys`). The behaviour is
  deliberate; the sentence was wrong, and it was the consoling half — it told a
  caller who had just lost the filter that they still had the field.
- **`work_item_id` takes a canonical id OR a slug.** `Recall` resolves the
  reference to the id `memories.work_item_id` really holds before anything compares
  it to a column (`aihub#363`), at the single domain entry all three
  `RecallRequest` builders share, and a reference naming nothing still answers 200
  with an empty list — held end to end against a database by
  `internal/server/recall_work_item_slug_db_test.go`
  (`TestRecallResolvesWorkItemIdOrSlug`, nine arms including the
  indistinguishable-from-absent ones) and at the seam by
  `internal/domain/recall_card_claims_test.go`
  (`TestRecallResolvesTheWorkItemReferenceBeforeAnyColumnComparison`), which pins
  the resolution AHEAD of the router.

  🔴 **This bullet used to say the opposite** — "must be the canonical id; a slug
  matches nothing" — which was true until `aihub#363` and stale after it. False in
  the harmless direction, so nothing in the tree could redden on it: the advice
  merely cost a caller a `pf_get_work_item` they no longer need, and read as a
  reason to skip a filter that works. The last live copy of that claim —
  `pf_get_step`'s published description (`internal/mcp/tools_step.go`) telling a
  caller to pass the canonical id because `pf_recall` and `pf_read_events` "return
  nothing for a slug" — was corrected by `aihub#590` (2026-09-10), which also made
  this tool's own `work_item_id` property say "canonical id or slug" instead of
  "Filter by work item ID", the form `internal/mcp/slug_publication_test.go`
  (`TestSlugAcceptanceIsPublishedByRecall`) requires off a live session.
  `TestSlugDenialIsNowhereInThePublishedSurface` in the same file refuses the
  stale clause, by its exact words, in every published description and schema, so
  the sentence cannot migrate to a tool the per-tool arms do not read.
- **`type` entries that match nothing come back in `unmatched_types`**, which is what
  distinguishes a wrong type name from a project that genuinely holds none — the
  diagnostic itself held by `internal/domain/memory_unmatched_test.go`
  (`TestUnmatchedTypes`, with `TestUnmatchedTypes_AgreesWithRecall` requiring it to
  agree with what recall really returned) and its survival through the MCP
  projection by `internal/mcp/recall_slim_test.go`
  (`TestSlimRecallResult_CarriesUnmatchedTypes`).
- **The mixed-type blind spot is CLOSED, and the residual limitation is a different
  one.** `internal/domain/memory.go` (`recallRouted`) splits the caller's `type` filter
  with `internal/domain/embedding.go` (`partitionTypesByEmbeddable`) and runs a text
  complement whenever a NAMED type the vector path cannot serve is present — so
  `["experience.*","methodology.spec"]` returns both halves, which
  `internal/domain/memory_recall_router_db_test.go`
  (`TestRecallRouterMixedUnionReturnsBothHalves`) drives against a database and
  `internal/domain/memory_recall_hybrid_test.go` (`TestPartitionTypesByEmbeddable`)
  holds at the split itself. What still holds is
  narrower and deliberate: an **empty** `type` filter does not qualify for the
  complement, so a semantic recall with no filter never returns a `methodology.*` row,
  and a `work_item_id` filter skips the vector path entirely because that query carries
  no work-item predicate at all — the two exceptions held by
  `internal/domain/memory_recall_router_db_test.go`
  (`TestRecallRouterEmptyTypeFilterStaysSemantic` and
  `TestRecallRouterWorkItemScopedBypassesVectorPath`).

## hop 5 — what comes back

`internal/mcp/recall_slim.go` (`slimRecallResultMode`) is a **delete-list**, and this
is the tool where that mattered most — the direction that makes it one is held by
`internal/mcp/recall_projection_test.go`
(`TestRecallResultPassesThroughAFieldTheStructDoesNotHaveYet`), which requires an
envelope key and an item key this projection has never heard of to reach the model
anyway, and by `internal/mcp/recall_brief_test.go`
(`TestSlimRecallResultMode_DivergesFromPreChangeOnlyOnUnknownKeys`). It **mutates the incoming map and returns the
same map** (`TestSlimRecallResultMutatesAndReturnsTheSameMap` pins the identity) —
nothing is copied at the top level, so nothing can be forgotten there. It was a keep-list once, and that shape dropped `total` and then
`content_truncated`/`content_full_len` silently, each surfacing as a separate bug
filed weeks later: the REST endpoint looked correct and MCP quietly served less.
`aihub#418` converted it, which is the conversion §6.1 T1-5 records as already landed.
<!-- prose-only: because=history -->

**The residual keep-list is exactly two keys, and it is documented in the file
itself** (`internal/mcp/recall_slim.go`, `recallItemNarrowedKeys`): `attrs` is
narrowed to `{structured_payload}` and `commits` to the human insight, so a NEW key
inside either does not arrive — the narrowing driven through the real tool by
`internal/mcp/recall_projection_test.go` (`TestRecallResultNarrowsAttrsAndCommits`,
which requires `attrs` to carry exactly one key) and the declaration itself kept
live by `internal/mcp/recall_slim_test.go`
(`TestRecallItemNarrowedKeys_IsNotAnInertDeclaration`), which fails on an entry
this file has no probe for. That residual is asserted by a test rather than merely
described.

A per-item CONDITIONAL key rides that delete-list since aihub#504 (2026-09-12):
`embedded_len`, present iff the item's stored vector embeds a strict prefix of its
content (the embedding input budget cut it), valued at how many leading runes the
vector covers — its arrival in full mode is pinned by
`internal/mcp/recall_embedded_len_test.go` (`TestRecallFullModeForwardsEmbeddedLen`)
and brief mode's refusal of it (brief is a keep-list, see below) by
(`TestBriefRecallItemDropsEmbeddedLen`). The presence rule is
`internal/domain/memory.go` (`finalizeEmbeddedLen`), applied on both recall paths —
`internal/domain/memory.go` (`scanMemoryLite`) and `internal/domain/memory_vector.go`
(`RecallWithVector`) — and calibrated by `internal/domain/embedded_len_test.go`
(`TestFinalizeEmbeddedLen_SuppressesFullCoverage`). On this tool the warning is
doubly worth reading: the similarity such an item was ranked by is a claim about the
embedded prefix, not about the tail. Do not
confuse it with `content_truncated`, which reports a response-side snippet cut;
`embedded_len` is a write-side fact about the vector.
<!-- prose-only: because=judgement -->

Since `aihub#360` (2026-09-12) a recall that carries a `query` returns a SECOND
top-level section, `lexical`: verbatim-substring retrieval — every whitespace
token of the query must appear, case-insensitively, in a row's content — over
the same scoped corpus, parallel to the semantic ranking and never merged into
it (`internal/domain/recall_lexical_db_test.go`), because a cosine and a
substring match are incomparable and a fused score is the shape `aihub#311`
removed as a defect. Its hits carry no `similarity`,
its `total` is explicit even at 0, its empty `items` is `[]` rather than an
absent key, and the section is present exactly when the request carried a
non-empty query, whichever path served `items` — all driven against a live
pgvector database by `internal/domain/recall_lexical_db_test.go`
(`TestRecallLexicalSectionRetrievesWhatTheVectorPathCannot`), whose anchor
reproduces `aihub#367`'s measured failure shape (an excerpt of a stored
document cannot retrieve its parent through the single-vector unchunked index;
recall@1 0/42, measured 2026-09-06) and requires the lexical section to
retrieve the parent the vector page missed, in the same response. The scoped
arm of `TestRecallLexicalSectionRetrievesWhatTheVectorPathCannot` covers the
one shape where the query text used to do nothing at all — `work_item_id` plus
`query`, where the router skips the vector path and the text path ignores the
query. The tokenizer (case-insensitive dedup,
longest-first, capped at 16 with the drop disclosed as `tokens_dropped`), the
ILIKE metacharacter escaping and the snippet rules are pinned by
`internal/domain/lexical_test.go` (`TestLexicalTokens`, `TestLexicalPattern`,
`TestLexicalSnippet`). The section reaches the model through the delete-list
untouched (`TestRecallResultPassesThroughAFieldTheStructDoesNotHaveYet`), and
its K10 declaration lives in `live-response-keys.json` rather than on this
card's `response_keys_observed`, because the generated corpus predates the key
(`internal/mcp/card_response_keys_live_e2e_db_test.go`).

`fields="brief"` selects `internal/mcp/recall_slim.go` (`briefRecallItem`), which
replaces each body with its first line (≤120 runes) and drops `related`/`tags`;
`content_truncated` marks the cut and the `id` brief keeps is what leaves the full
text one `pf_get_memory` away — the projection held by
`internal/mcp/recall_brief_test.go`
(`TestBriefRecallItem_ProjectsBodyAndKeepsRetrievalPath` for the first line and the
dropped keys, `TestBriefRecallItem_CapsBodylessOfNewline` for the rune cap on a
body with no newline in it, and `TestBriefFields_KeepsIDAsTheRetrievalMechanism` for
the escape), and its arrival through a real call by
`internal/mcp/recall_brief_wiring_test.go` (`TestRecallFieldsBriefArrivesAndProjects`). Brief is
deliberately a keep-list while full is a delete-list — the two carry opposite burdens
of proof, which is why a field can be conditional in one and unconditional in the
other.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape, and the ruling records
  that "the last keep-list was converted after this table was written"
  (`already-landed:aihub#418`), the shape held from both sides by
  `internal/mcp/recall_projection_test.go`
  (`TestRecallResultPassesThroughAFieldTheStructDoesNotHaveYet` for the
  pass-through and `TestRecallResultWithholdsExactlyTheDocumentedKeys` for the
  documented exceptions). **That last keep-list was this function.** The two
  narrowed keys above are the measured residual, not a surviving exception.
- **§6.1 T1-2** — `top_k`'s ceiling is a clamp, and the ruling widens the numeric
  gate's scope to hop 2 rather than narrowing the policy; this tool's clamp is one
  the gate resolves at that second hop, because `Recall` appends the disclosure
  around `normalizeRecallTopK` instead of inside it, with the ceiling's
  reachability held by `internal/domain/memory_topk_test.go`
  (`TestNormalizeRecallTopK_CeilingIsReachable`), the clamp being disclosed rather
  than silent by `internal/citest/clampdisclosure/clampdisclosure_test.go`
  (`TestEveryClampDisclosesOrCarriesANamedWaiver`, with
  `TestDisclosureResolvesAtHopTwo` for the hop-2 resolution itself), and the
  disclosure reaching the model by `internal/mcp/request_adjusted_e2e_db_test.go`
  (`TestE2ERequestAdjustedDisclosesRecallTopKCap`).
- **§6.1 T1-6 — LANDED** (`aihub#435`) — a malformed `cursor` is a 400 at the
  handler, held by `internal/server/cursor_validation_test.go`
  (`TestCursor_MalformedIsRejectedBeforeTheQuery`), with this card's own record of
  the ruling checked by `TestCursor_CardsRecordTheRuling`. This tool is the one that made the ruling non-trivial: its cursor is
  the only composite one, so "validate it as RFC3339Nano" taken literally would
  have rejected every cursor Recall has ever issued.
- **§6.2 T2-19 — LANDED** with T1-3 (`aihub#433`), and in that order for the reason
  the ruling gave: a threshold and the value it thresholds must be published on the
  same scale, so `pf_remember`'s range was fixed first. `min_strength` now says which
  scale it is on — it thresholds `base_strength` (1-5) after decay, which makes the
  0.3 default **below every legal value**, i.e. it filters nothing, and the
  published description is bound to the enforced range by
  `internal/mcp/tools_memory_test.go`
  (`TestRecallMinStrengthPublishesWhichScaleItIsOn`), which builds the expected
  scale from `domain.MinBaseStrength`/`domain.MaxBaseStrength` so the constants and
  the sentence cannot drift apart. The default is
  unchanged; only the statement of what it means is new.

## Open

- Both scale rulings have landed, but **the 0.3 default was deliberately left
  alone**, so `min_strength` still defaults to a value that cannot exclude anything.
  Whether that default should move is not settled by any adjudicated row.
- The two narrowed keys are a stated residual rather than a closed question: a new
  key inside `attrs` or inside a commit is dropped before the model sees it — which
  `internal/mcp/recall_projection_test.go` (`TestRecallResultNarrowsAttrsAndCommits`)
  holds by requiring `attrs` to arrive with exactly one key — and no adjudicated
  row says whether that should change.
