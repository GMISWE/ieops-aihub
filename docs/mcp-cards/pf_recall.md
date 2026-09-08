# pf_recall — contract card

```json
{
  "tool": "pf_recall",
  "description_sha256": "52355ed415a03b181b816da58b68327c4e4c5ca44cfab692bdbeb2d8bbc00b0a",
  "input_schema_sha256": "b34d958f730c3fa9e79a1e47d105363ec4df53f492f5262e3c1342022a45960e",
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
    "visibility": {
      "type": "string",
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

Twelve parameters. This is the tool `aihub#148` was filed against, and the split
between "published" and "forwarded" here is the repo's reference example of a hop-2
defect.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | project name |
| `query` | string | no | semantic search query |
| `type` | array | no | ARRAY of type names; `.*` is a prefix wildcard; `\|` is NOT a separator |
| `visibility` | string | no | filter by visibility |
| `work_item_id` | string | no | filter by work item |
| `top_k` | string | no | default 20, ceiling 200; a JSON number is accepted |
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
card gate's K4 arm requires only that a published parameter be named in backticks
somewhere in the prose. The row was four cells wide and its whole hop-1 promise
was the two words "default 0.3" — but it did carry the backticked name, so K4 was
satisfied. K4 cannot tell "documented" from "listed", which is the gap the hop-4
arm added by this work item closes from the other side.

The `type` description exists because three skill templates taught a single
pipe-separated string in place of the array, nothing split it, and the resulting
empty set read as "no relevant memory". (The offending literal is deliberately not
reproduced here: `internal/cli/skill_recall_type_test.go` sweeps this directory and
fails on any `type=` value containing a pipe, which is the correct behaviour — a
document that spells the anti-pattern out is a document somebody copies from.) `fields` is `propEnum` rather than `prop` because `fields` conventionally
names a field LIST, so `fields="id,type"` is a natural guess that would silently
return the full response — the exact cost the parameter exists to remove.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildRecallParams`) renders the arguments into a
query string for `pkg/client/client.go` (`Recall`) → `GET /v1/memories`, bound by
`internal/server/routes_memory.go` (`handleRecall`). Three forwarding classes:

- `recallStringParams` — through `scalarArg`, because `top_k` is published as a
  string and "max results: 10" is naturally written as a number.
- `recallNumberParams` — through `parseNumArg`, which tolerates the *string*
  spelling of a number and REFUSES text it cannot read. The string half is
  aihub#148 defect 2: a bare numeric read returns 0 for
  `similarity_threshold: "0.99"`, and 0 is this tool's "not specified", so a caller
  who quoted the value had the filter silently discarded. The refusal half is
  `aihub#432`: until it, unparseable text ALSO came back as 0, so
  `similarity_threshold: "notanumber"` disabled the filter and no hop said so. A
  present-but-unreadable number is now refused by name before the request leaves
  this process — Rule 1 (`internal/server/queryparam.go`) applied one hop upstream
  of where it was written. A zero still means "not specified"; that ambiguity is
  separate and unfixed.
- `type` — a JSON array joined comma-separated; a bare string is also accepted.

Two deliberate asymmetries:

- **`fields` is NOT forwarded.** The projection is a property of what this process
  hands the model, and this process is the last hop before the model, so it is
  consumed exactly where it is read. Adding it to the loop would make the server
  ignore an unread query param — a third instance of `aihub#148`.
- **`recall_algo` is forwarded but not published**, from an explicit argument or the
  `POLYFORGE_RECALL_ALGO` environment variable. Its exemption reason is contract
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
    is the deterministic tiebreaker for rows sharing a reference time, and
    `aihub#239`'s cursor key, not a second signal. There is no similarity
    score on this path to blend it against.
  - **text, `recall_algo=lexical`** — `ts_rank` first, then `tanh` of effective
    strength, which carries `exp(-days/stability)`.
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
  `aihub#311` fixed. At w=0.3 a 30-day age gap overturns a cosine gap of 0.2709,
  6.8× the whole band; keeping cosine dominant would need w < 0.06.
- **`similarity_threshold` has no default and must keep none.** Measured on one
  project with limit=200: a pure-punctuation noise query scores 0.4712 at its WORST
  hit while a real query whose top hit is correct scores 0.4798 at its BEST — 0.0086
  apart, and the wrong way round for six of the noise query's hits. No global cutoff
  separates noise from signal. A threshold that matches nothing returns an empty list
  rather than falling back to text search: empty is the intended answer.
- **`cursor` works on the TEXT path only.** The vector path and the hybrid merge both
  set an empty cursor and return a nil `next_cursor`, so paging a semantic recall
  gets page one forever.
- **`cursor` is composite, and only half of it is validated.** The token is
  `<RFC3339Nano>|<id>` — reference time plus the `id DESC` tiebreaker `aihub#239`
  added — so a malformed one is refused with a 400 naming the parameter
  (`aihub#435`), but the check reads the TIMESTAMP half only. The id half stays
  opaque on purpose: constraining it would put a second copy of the id format in
  the handler, which starts refusing cursors this server is still issuing the day
  the format moves. Before `aihub#435` the whole token went raw into
  `$n::timestamptz` and a token the server never issued came back 500 with the
  driver's text. Cursors minted before `aihub#239` carry the timestamp alone and
  still pass.
- **`work_item_id` must be the canonical id**; a slug matches nothing and answers 200
  with an empty list.
- **`type` entries that match nothing come back in `unmatched_types`**, which is what
  distinguishes a wrong type name from a project that genuinely holds none.
- **The mixed-type blind spot is CLOSED, and the residual limitation is a different
  one.** `internal/domain/memory.go` (`recallRouted`) splits the caller's `type` filter
  with `internal/domain/embedding.go` (`partitionTypesByEmbeddable`) and runs a text
  complement whenever a NAMED type the vector path cannot serve is present — so
  `["experience.*","methodology.spec"]` returns both halves. What still holds is
  narrower and deliberate: an **empty** `type` filter does not qualify for the
  complement, so a semantic recall with no filter never returns a `methodology.*` row,
  and a `work_item_id` filter skips the vector path entirely because that query carries
  no work-item predicate at all.

## hop 5 — what comes back

`internal/mcp/recall_slim.go` (`slimRecallResultMode`) is a **delete-list**, and this
is the tool where that mattered most. It **mutates the incoming map and returns the
same map** — nothing is copied at the top level or per item, so nothing can be
forgotten there. It was a keep-list once, and that shape dropped `total` and then
`content_truncated`/`content_full_len` silently, each surfacing as a separate bug
filed weeks later: the REST endpoint looked correct and MCP quietly served less.
`aihub#418` converted it, which is the conversion §6.1 T1-5 records as already landed.

**The residual keep-list is exactly two keys, and it is documented in the file
itself** (`internal/mcp/recall_slim.go`, `recallItemNarrowedKeys`): `attrs` is
narrowed to `{structured_payload}` and `commits` to the human insight, so a NEW key
inside either does not arrive. That residual is asserted by a test rather than merely
described.

`fields="brief"` selects `internal/mcp/recall_slim.go` (`briefRecallItem`), which
replaces each body with its first line (≤120 runes) and drops `related`/`tags`;
`content_truncated` marks the cut and `pf_get_memory` returns the full text. Brief is
deliberately a keep-list while full is a delete-list — the two carry opposite burdens
of proof, which is why a field can be conditional in one and unconditional in the
other.

## Policy

- **§6.1 T1-5** — delete-list is the only projection shape, and the ruling records
  that "the last keep-list was converted after this table was written"
  (`already-landed:aihub#418`). **That last keep-list was this function.** The two
  narrowed keys above are the measured residual, not a surviving exception.
- **§6.1 T1-2** — `top_k`'s ceiling is a clamp, and the ruling widens the numeric
  gate's scope to hop 2 rather than narrowing the policy; this file is one of the
  three instances that escaped the package-scoped gate.
- **§6.1 T1-6 — LANDED** (`aihub#435`) — a malformed `cursor` is a 400 at the
  handler. This tool is the one that made the ruling non-trivial: its cursor is
  the only composite one, so "validate it as RFC3339Nano" taken literally would
  have rejected every cursor Recall has ever issued.
- **§6.2 T2-19 — LANDED** with T1-3 (`aihub#433`), and in that order for the reason
  the ruling gave: a threshold and the value it thresholds must be published on the
  same scale, so `pf_remember`'s range was fixed first. `min_strength` now says which
  scale it is on — it thresholds `base_strength` (1-5) after decay, which makes the
  0.3 default **below every legal value**, i.e. it filters nothing. The default is
  unchanged; only the statement of what it means is new.

## Open

- Both scale rulings have landed, but **the 0.3 default was deliberately left
  alone**, so `min_strength` still defaults to a value that cannot exclude anything.
  Whether that default should move is not settled by any adjudicated row.
- The two narrowed keys are a stated residual, not a closed question: a new key
  inside `attrs` or inside a commit still does not reach the model, and no adjudicated
  row says whether that should change.
