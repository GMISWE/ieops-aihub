# pf_get_ready_queue — contract card

```json
{
  "tool": "pf_get_ready_queue",
  "description_sha256": "ee016b0c85960c28e900f6134683d38c7cbed004139104d9595d80763fb6c48e",
  "input_schema_sha256": "50cc9e20d3f3579b279c43ab35dacdf4fda68bb80946013fa2df79d8366302f3",
  "params": {
    "max": {
      "type": "string",
      "required": false
    },
    "project": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "items",
    "needs_human_session",
    "paused",
    "running",
    "stale_running",
    "stalled",
    "unclassified"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters, and this tool is the canonical example in this repo of a published
parameter that reached the wire and was read by nothing.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `project` | string | yes | "Project name" |
| `max` | string | no | "Max items in EACH queued section — items, needs_human_session and unclassified take it as their own LIMIT (default 10)" |

`non_conflicting` was published here and forwarded as `?non_conflicting=true` from
the day the tool was added until `aihub#387` withdrew it. Nothing ever read it:
`handleGetReadyQueue` reads `project` and `max` only, and the domain function takes
no such argument — measured 2026-09-07, a repo-wide grep across
`internal/server`, `internal/domain`, `pkg` and `cmd` returned zero hits. Passing it
returned the ordinary queue with no error and no warning.

It is gone rather than implemented, by owner decision: "non-conflicting" has no
agreed definition here — predicted from `declared_resources`, or read off the locks
actually held? — and this repo has measured `pf_predict_conflicts` untrustworthy in
both directions, so building on it would have produced a second untrustworthy
predicate; `internal/mcp/ready_queue_published_claims_test.go`
(`TestTheWithdrawnNonConflictingParamIsGoneFromTheSchemaAndTheWire`) holds that it is
still gone, requiring the published schema to be exactly these two parameters and a
call passing the withdrawn name to put nothing on the wire for it.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) renders both into a
query string for `pkg/client/client.go` (`GetReadyQueue`) →
`GET /v1/work_items/ready`, bound by `internal/server/router.go`
(`handleGetReadyQueue`). That route is registered before `/work_items/:id`, and the
literal `ready` reaches it rather than binding as an id:
`internal/server/ready_route_precedence_test.go`
(`TestTheLiteralReadyPathResolvesToTheQueueRouteWhicheverOrder`) resolves the path
against the real router, and its third arm registers the two the other way round and
gets the same answer — so echo preferring a static segment to a parameter is what
decides this, and the registration order is a convention rather than the mechanism.

`max` goes through `scalarArg`, not `strArg`. It is published as a string but "max
5" is most naturally written as a JSON number, and `strArg` returns `""` for a
non-string — so the value was dropped and the server fell back to its own default
of 10, at no hop with an error.
<!-- prose-only: because=history -->
Same defect and same fix as `limit` on `pf_list_work_items`.

🔴 `internal/mcp/ready_queue_param_wiring_test.go` fails on any parameter this
schema publishes that the handler does not read. Do not re-add a parameter here
without a hop-3 reader to go with it.

## hop 4 — what it actually does

Returns the seven-section LCRS view for one project. The `items[]` section uses the
**same SQL predicate** as `pf_list_work_items`' `ready_only` — one shared constant —
but **not the same page**: this defaults to 10 ordered by priority desc, that one to
50 ordered by `created_at` desc. With more ready items than either limit the two
return different subsets, which is stated on `ready_only`'s own description because
the natural reading is that they agree —
`internal/domain/ready_queue_page_divergence_test.go`
(`TestTheReadyQueueAndReadyOnlyShareOnePredicateAndPageDifferently`) holds both halves
at their source, one shared predicate constant and two pages that differ in limit and
in ordering, and `internal/mcp/ready_queue_published_claims_test.go`
(`TestPublishedReadyOnlyStatesItPagesDifferentlyFromTheQueue`) reads that description
off a live session and requires it to go on saying so.

`max` is a PER-SECTION page size and reaches three of the seven sections: `items`,
`needs_human_session` and `unclassified` each take it as their own `LIMIT`, while
`running`, `stalled`, `paused` and `stale_running` take none —
`internal/domain/ready_queue_segment_contract_test.go`
(`TestReadyQueueMaxPagesThreeOfTheSevenSegments`) censuses all seven queries in source
order and fails both on a paged segment that stops paging and on an unbounded one that
starts. So it is not a budget
over the response, and one section arriving full says nothing about the others. The
description said "Max items in ready section" until `aihub#449` — measured wrong in
`aihub#401`, which was cancelled before it could say so.

`max` above 200 is clamped, and since `aihub#432` the response SAYS SO: `ReadyQueue`
carries `request_adjusted`, and `newReadyQueue` in `internal/domain/work_items.go` is
the single site that both bounds the page size and appends the
`{param: "max", requested, applied}` entry —
`internal/domain/ready_queue_disclosure_test.go` drives that constructor over the four
classes Rule 2 distinguishes
(`TestReadyQueueDisclosesTheMaxItAdjusted`) and holds the single-site property by
reading `GetReadyQueue`'s own body
(`TestGetReadyQueueBuildsItsResponseThroughTheDisclosingConstructor`). Until then this
was the one self-declared exemption from the disclosure convention — the struct had no
field to report the clamp in, so `max=5000` and `max=200` returned byte-identical
responses.
<!-- prose-only: because=history -->

A non-positive `max` takes the endpoint default of 10 and is disclosed the same way;
`max=0` is not, because zero and absent are the same `int` by the time the domain
function sees them (`handleGetReadyQueue` forwards `queryInt`'s value without its
present flag), and both cases are subtests of
`internal/domain/ready_queue_disclosure_test.go`
(`TestReadyQueueDisclosesTheMaxItAdjusted`). The key is ABSENT, not an empty list, when
nothing was adjusted.

## hop 5 — what comes back

`jsonResult`, no projection — which is what lets `request_adjusted` reach the model
at all, and is held as such by `TestWireQueryReadyQueueDisclosureReachesTheModel`. That
is now a checked property rather than a fact of the current
implementation: `internal/mcp/recall_wire_query_test.go`
(`TestWireQueryReadyQueueDisclosureReachesTheModel`) drives the registered tool over
an in-memory session and fails both if a disclosure the server sent is dropped and if
an absent one is invented. pf_recall's projection has already swallowed three
server-side fields this way (`total`, the truncation pair, `unmatched_types`).

The section count is now ONE number in three places (`aihub#449` / `aihub#411` T2-20).
`internal/domain/work_items.go` (`ReadyQueue`) marshals seven — `items`, `running`,
`stalled`, `paused`, `needs_human_session`, `unclassified`, `stale_running` — the
description says seven, and the Ready Queue block of
`docs/design/polyforge-v1-design.md` draws seven. Before that wi the three said 7, 6
and "6 plus four fields that cannot exist", and each was individually plausible,
which is why nothing caught it. `internal/mcp/ready_queue_section_count_test.go`
(`TestReadyQueueSectionCountIsOneNumber`) reads the struct and the live description
and fails when they disagree; its second arm reads the design doc's block and fails
both on a missing segment and on any of the four dead fields coming back.

🔴 **That was a WIRE change, and the only one in `aihub#449`.** An empty queue used to
serialise with no `stale_running` key at all and now carries `"stale_running": []`,
because the field lost its `omitempty` and `newReadyQueue` initialises it like the
other six. The other repairs cost nothing on the wire: deleting `ReadyItem`'s
`UnblockedAt` leaves the bytes identical — it had no writer anywhere, so `omitempty`
was already hiding it on every item — and `expires_at`, `kind` and `owner_user_type`
existed only in the design doc's sketch. The first went with the ownership model in
v1.21 and the second was deleted from `work_items` in v1.22; the third is a fourth
dead field `aihub#411` T2-20 did not list, and `RunningItem` has never carried it —
`internal/mcp/ready_queue_section_count_test.go`
(`TestTheDeadReadyQueueFieldsAreDeclaredNowhereInGo`) refuses all four on any of the
five types that marshal into this response, which is the authority the design-doc arm
above does not read.

🔴 **An empty segment now always means "nothing is here" (`aihub#500`).** Making the
seven keys always-present only pays off if an empty one is trustworthy, and until
this wi it was not: FIVE segments — `stalled`, `paused`, `needs_human_session`,
`unclassified` and `stale_running` — wrapped their whole drain in `if err == nil {…}`
with no else, so a `pool.Query` that failed to send rendered that segment as `[]`
inside an HTTP 200; `TestReadyQueueAnswersEveryQueryError` holds the repaired shape for
all seven. All seven now return `500 INTERNAL_ERROR "failed to query <segment> items"`
instead, matching what `items` and `running` already did —
`internal/domain/ready_queue_segment_contract_test.go`
(`TestEveryReadyQueueSegmentQueryErrorIsA500NamingThatSegment`) holds the code and the
message segment by segment, distinctly, and reads the 500 off the error mapping rather
than spelling it out.

That is an error-contract change, from silently-partial to failing, and it is the
right direction here because each of those five already returned `dbErrCause` from
`rows.Err()` a dozen lines below for the same underlying failure — best-effort on one
error path and fatal on the other is not a policy, it is an unfinished check.
<!-- prose-only: because=judgement -->
The reachable case is narrow: a pool that is down already fails at `items`, the first
query, so this only shows up when the pool degrades BETWEEN segments — precisely when
a partial queue is most misleading (`TestReadyQueueMaxPagesThreeOfTheSevenSegments`
holds that `items` really is the first query the function sends). `aihub#500` opened
describing `stale_running` as the only such segment; that was measured wrong, and the
count arm of `internal/domain/ready_queue_query_errors_test.go`
(`TestReadyQueueAnswersEveryQueryError`) now holds the shape for all seven and for any
eighth added later.

The corpus record above spans 155 calls with a 0.00%
error rate. Four of those results were prose rather than strict JSON, which is why
the census counts `json_object_results` and `prose_results` separately — a prose
result contributes no keys, and reading that as "returns nothing" is the trap the
corpus README warns about.
<!-- prose-only: because=measurement -->

## Policy

- **§6.1 T1-2 / T1-12** — the two numeric rules stand: unparseable is 400, out of
  range is clamped AND disclosed via `request_adjusted`, and both halves are held for
  this endpoint: `internal/server/queryparam_policy_test.go`
  (`TestPolicyRule1_MalformedParamsAreRejectedEverywhere`) carries a `max=abc` row and
  a `max=5x` row for it, and `internal/domain/ready_queue_disclosure_test.go`
  (`TestReadyQueueDisclosesTheMaxItAdjusted`) drives the clamp together with its
  disclosure. This tool WAS the named
  counter-example, obeying half of rule 2; `aihub#432` closed the exemption by giving
  `ReadyQueue` the disclosure field, as the ruling directed, rather than narrowing the
  policy to match the code.
- **§6.1 T1-9** — `non_conflicting` was withdrawn rather than left as prose that
  contradicts hop 3, which is the disposition that rule requires;
  `internal/mcp/ready_queue_published_claims_test.go`
  (`TestTheWithdrawnNonConflictingParamIsGoneFromTheSchemaAndTheWire`) holds the
  withdrawal itself and `internal/mcp/ready_queue_param_wiring_test.go`
  (`TestReadyQueueEveryPublishedParamIsReadByTheHandler`) holds the rule it is an
  instance of, over every parameter this tool publishes.
- **§6.2 T2-20** — LANDED as `aihub#449`. The ruling offered either wording ("6 plus
  `stale_running`") or shape (drop the `omitempty`); the shape was taken, because the
  rule this repo already wrote for absent keys decides it. `internal/domain/request_adjusted.go`
  states it and `internal/domain/ready_queue_disclosure_test.go`
  (`TestReadyQueueOmitsTheDisclosureKeyWhenNothingWasAdjusted`) pins it: an absent key
  is acceptable only while the absence asserts NOTHING. An absent `request_adjusted`
  asserts nothing, so it keeps its `omitempty`. An absent `stale_running` asserted "no
  work item has been running untouched for 24h" — an ownership reminder — while also
  meaning "this server predates the field", which is two meanings on one absence and
  one of them actionable.
  <!-- prose-only: because=history -->
  Wording alone would have published "six sections and a
  seventh you may or may not see" as the contract. `UnblockedAt` is deleted rather than
  written: **no code in this repository ever wrote it** — the identifier scan in
  `internal/mcp/ready_queue_section_count_test.go`
  (`TestTheDeadReadyQueueFieldsAreDeclaredNowhereInGo`) parses every Go file in the tree
  and finds it in none of them — and `aihub#387` settled the disposition for a
  no-writer field on this same tool. The `created_at` asymmetry between the segments is
  settled as designed and is not to be re-opened.
  <!-- prose-only: because=judgement -->

## Open

- The published `max` description now states its SCOPE — three of the seven sections,
  named — but still mentions neither the 200 ceiling nor the disclosure, where
  `limit`'s neighbouring description on `pf_list_work_items` says both. That half is
  `aihub#411` T1-2/T1-12's and was left to it deliberately rather than edited twice
  into the same string by two work items. The response already tells a caller what
  happened, which is the half that could not be worked around. `aihub#411`, which
  owns that half, wrapped 2026-09-07; re-checked 2026-09-08.
- `response_keys_observed` above is a corpus census taken before `aihub#432` and
  before `aihub#449`, so it neither lists `request_adjusted` nor implies that
  `stale_running` is optional — the 155 calls it spans predate both.
  <!-- prose-only: because=measurement -->
  It records what callers HAVE seen, not what the response can contain. Both cited work
  items had wrapped by the 2026-09-08 re-check.
