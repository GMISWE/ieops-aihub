# pf_reinforce_memory — contract card

```json
{
  "tool": "pf_reinforce_memory",
  "description_sha256": "9954803dee8b3d7e57f17d935200e67175ab432db2335542cb4cbd19c0deb5b5",
  "input_schema_sha256": "9d1569dc2ba4152358e48e3f61b56e44a86d2179208ba3d0387fc13e08a0d11d",
  "params": {
    "additional_context": {
      "type": "string",
      "required": true
    },
    "memory_id": {
      "type": "string",
      "required": true
    },
    "strength_delta": {
      "type": "number",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "activation_count",
    "base_strength",
    "memory_id"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Four parameters, three required — held against the live registry's own `required`
list by `internal/mcp/memory_published_word_test.go`
(`TestPublishedParamCountSentencesAreTheEnforcedOnes`), which walks every card
stating its own arithmetic rather than this one — and `work_item_id` being
required here is the whole story of `aihub#325`.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID" |
| `additional_context` | string | yes | additional context for the memory |
| `work_item_id` | string | yes | "Work item ID (for credential injection)" |
| `strength_delta` | number | no | held by `TestReinforceClampBoundsAreNamedConstantsNotLiterals` (the saturation bounds) and `TestReinforceMemory_ResponseMatchesTheStoredBaseStrength` (the stored result): "Integer delta added to the memory's stored strength; the sum saturates at 1-5 rather than being refused, so a delta that overflows is applied only in part. A fractional delta is refused with a 400: strength is a whole number. The response reports the value actually stored." — the fractional refusal by `TestReinforceRefusesFractionalStrengthDeltaBeforeThePool` |

The parenthetical on `work_item_id` understates it. The server VERIFIES the attempt
credentials against that work item and writes it into the reinforcement's
provenance, so it is not merely the key that names a state file.

`strength_delta`'s promise was the bare words "strength delta" until `aihub#475`.
True, and useless: it named the units without naming the granularity, so nothing
told a caller that `0.5` was a no-op — and the 200 body reported the untruncated
arithmetic, so the call looked like it had worked. That text stated the granularity
and pointed at the response, and stopped short of saying a fractional delta was
refused or rounded, because it was neither.

**`aihub#459` settled it on 2026-09-09: refused.** So the description no longer
explains truncation, and the removal is deliberate rather than tidying — a caller
told "a delta under 1 usually changes nothing" will still send `0.5` and then
wonder, while one told it is refused cannot; the withdrawn model is banned by name
on the live string by `internal/mcp/tools_memory_test.go`
(`TestPublishedStrengthDeltaSaysWhatReinforceEnforces`), which refuses both
"truncated" and "usually changes nothing" appearing in it again. The truncation has not gone anywhere;
it has stopped being reachable through this parameter, and a description that keeps
explaining an unreachable mechanism teaches the wrong model.

**The saturation is published as of `aihub#506` (owner ruling 2026-09-09), and that
is the whole of that work item — no behaviour moved.**
<!-- prose-only: because=external-state -->
The old text's "then clamped
to 1-5" was true and insufficient: it sat one sentence away from "a fractional delta
is refused", so a caller had every reason to read an overflowing sum the same way.
The two outcomes are not interchangeable. A refusal tells the caller their delta did
not land; the clamp answers 200 having stored a value the caller did not name, and
the reported value is the only place the size of the loss is visible.
`internal/mcp/tools_memory_test.go`
(`TestPublishedStrengthDeltaSaysWhatReinforceEnforces`) now gates both halves of the
string — the bounds, built from `internal/domain/memory.go` (`MaxBaseStrength`)
rather than typed out, and the word `saturat` — so the older wording is red rather
than merely stale.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildReinforceMemoryBody`) renders the arguments
and the resolved state file into the body of `PATCH /v1/memories/<id>/reinforce` via
`pkg/client/client.go` (`ReinforceMemory`), bound by
`internal/server/routes_memory.go` (`handleReinforceMemory`).

- `memory_id` is **absent from the body on purpose** — it is the path segment, and
  the ABSENCE is asserted as an exact body key set by
  `internal/mcp/memory_wire_shape_test.go`
  (`TestReinforceAndUpdateSendCredentialsAndTheWorkItemAndNoMemoryId`), which pins
  the method and the endpoint in the same call. That
  is asserted on the real request URL by `internal/mcp/memory_tools_wire_test.go`
  rather than exempted on trust: "it goes in the path" is a claim, and an unchecked
  claim is how a published parameter goes missing.
- `strength_delta` is forwarded only when present, driven both ways — set and
  omitted, everything else identical — by `internal/mcp/memory_wire_shape_test.go`
  (`TestMemoryToolsForwardAnOptionalParamOnlyWhenItCarriesAValue`), whose set call
  is the control, since an absence assertion alone is satisfied perfectly by a
  renderer that forwards nothing, which is `aihub#325`'s own defect wearing the
  opposite sign.
- **This tool declared `work_item_id` required, refused the call without one, and
  then built a body that did not contain it.** Every non-methodology reinforce
  answered `400 work_item_id is required when attempt_id/session_secret are
  provided`, because the handler always sends credentials from the state file and the
  server's gate demands the work item whenever credentials are present.
  <!-- prose-only: because=history -->
  `methodology.*` memories take the other branch, which binds to the TARGET memory's
  own work item — held by `internal/server/routes_memory_methodology_test.go` in two
  parts, because one of them is not enough: (`TestMethodologyGateBindsToTheTargetMemorysWorkItem`)
  drives a call carrying credentials and NO caller-supplied work item, where the
  methodology branch reaches the credential verifier and the other branch answers
  400 without reading anything, which pins WHICH BRANCH is taken; and
  (`TestMethodologyGateVerifiesTheMemorysWorkItemAndNotTheCallers`) pins WHAT that
  branch binds to, positionally over the function's own parameters, because passing
  the caller's id to the verifier instead of the memory's left the first arm green
  — a mutated branch still reaches the verifier and still dies on the nil pool, and
  a panic cannot say which argument produced it — which is why `pf_save_artifact`
  traffic was unaffected and nobody noticed.

## hop 4 — what it actually does

- Appends the context to the memory's reinforcement list with `from_wi` provenance,
  read back out of `attrs.reinforcements[0].from_wi` against a real database by
  `internal/mcp/reinforce_wi_e2e_db_test.go`
  (`TestE2EReinforceMemoryWorkItemIDReachesTheGate`), and adjusts strength by
  `strength_delta` within the server's clamp, which
  `internal/server/routes_memory_reinforce_returning_db_test.go`
  (`TestReinforceMemory_IntegralDeltaStillMoves`) drives both under the top and past
  it. Since `aihub#433` that clamp reads `internal/domain/memory.go`
  (`MinBaseStrength`) and (`MaxBaseStrength`) rather than two literals that happened
  to agree with them — a code property, held by
  `internal/server/routes_memory_reinforce_clamp_test.go`
  (`TestReinforceClampBoundsAreNamedConstantsNotLiterals`), which requires both
  selectors to appear in an ordered comparison in this handler AND no numeric
  literal to appear anywhere in a comparison naming one, so `> MaxBaseStrength +
  0.5` is red as well as `> 5.0`.
- **The clamp is not the last thing that touches the value, and until `aihub#475`
  the code said it was** (`TestBaseStrengthIsTruncatedByThePgxInt2Codec` measures
  what touches it after). `memories.base_strength` is `SMALLINT`, so pgx encodes the
  float64 through its int2 codec, which truncates toward zero and returns no error —
  measured at the pinned version in both wire formats (`3.5`→`3`, `4.999`→`4`,
  `0.9`→`0`, `-0.5`→`0`) by `internal/domain/memory_base_strength_range_test.go`
  (`TestBaseStrengthIsTruncatedByThePgxInt2Codec`), with the column's own `SMALLINT`
  declaration read out of migration 0006 by
  (`TestBaseStrengthBoundsAreTheColumnsOwn`). It happens client-side, so Postgres never sees the
  fraction and no server-side rounding rule applies.
- **Reinforce still CLAMPS where the create path REJECTS, and that asymmetry is
  deliberate**: this call applies a delta to a stored value, so a sum outside the
  range is arithmetic rather than a stated intent, while a caller-supplied
  `base_strength` outside the range is a statement the server can refuse — the
  clamping half driven past the top by
  `internal/server/routes_memory_reinforce_returning_db_test.go`
  (`TestReinforceMemory_IntegralDeltaStillMoves`), which requires `MaxBaseStrength`
  back rather than an error, and the refusing half by
  `internal/domain/memory_base_strength_range_test.go`
  (`TestValidateBaseStrengthRejectsWhatTheColumnWouldRefuse`).
- **That asymmetry was adjudicated on 2026-09-09 (`aihub#506`): keep the clamp and
  write it into the contract.**
  <!-- prose-only: because=external-state -->
  The parallel that made it a question was real — the
  integrality ruling one field over refused the same "answered 200 having stored
  something you did not name" shape — and the owner separated them on exactly the
  ground above, that a SUM is arithmetic where a named value is an intent. The two
  candidates that lost were a 400 on an overflowing sum and a `clamped: true` field
  in the response; the second is why the response shape is untouched, and what holds
  it untouched is the SATURATING call's body asserted as an exact three-key set by
  `internal/server/routes_memory_reinforce_returning_db_test.go`
  (`TestReinforceMemory_IntegralDeltaStillMoves`) — the one request a withdrawn
  disclosure field would have had something to say on. An earlier revision of this
  card credited that unchanged shape to K10's declared key set, which overstated the
  gate: this tool has no entry in `docs/mcp-cards/live-response-keys.json`, so the
  declared set K10 grades a live reinforce against is this card's own
  `response_keys_observed`, and the walk in
  `internal/mcp/card_response_keys_live_e2e_db_test.go`
  (`TestE2ELiveResponseKeysAreDeclaredOnTheCards`) drives its one reinforce with no
  `strength_delta`, so the only reinforce body K10 ever reads is a non-saturating
  one, where a clamp-engagement disclosure key would have had nothing to disclose
  (corrected 2026-09-11, `aihub#531`). So a delta of `+4` on a row stored at `4`
  still answers 200 with `base_strength` `5` — the difference is that the caller is told in
  advance that it will, which
  `internal/server/routes_memory_reinforce_returning_db_test.go`
  (`TestReinforceMemory_IntegralDeltaStillMoves`) drives by pushing a `+99` onto a
  row already at the top and requiring `MaxBaseStrength` back. "Unchanged" is a claim with an arm behind it rather than an
  assurance: `internal/server/routes_memory_reinforce_returning_db_test.go`
  (`TestReinforceMemory_IntegralDeltaStillMoves`) drives a delta past the top
  against a real database and requires `MaxBaseStrength` back.
- **A non-integral `strength_delta` is a 400 since `aihub#459` (owner ruling
  2026-09-09), and the refusal is on the DELTA rather than on the sum**
  (`TestReinforceRefusesFractionalStrengthDeltaBeforeThePool`). Checking
  only the sum would be sufficient for storage and useless for the caller: `0.5` on
  a row stored at `3` is a well-formed sum of `3.5`, and "3.5 is not a whole number"
  names a value nobody typed — observable rather than argued:
  `internal/server/routes_memory_reinforce_integral_test.go`
  (`TestReinforceRefusesFractionalStrengthDeltaBeforeThePool`) runs the handler with
  a nil pool, so a refusal that arrives cannot have read the row and therefore
  cannot have been about the sum, and
  (`TestReinforceAcceptsWholeAndAbsentStrengthDelta`) is the control that stops a
  handler refusing everything from satisfying it. The check sits with the other argument checks, above
  the first query — `TestReinforceRefusesFractionalStrengthDeltaBeforeThePool` proves
  the ordering with a nil pool — so the answer is a fact about the argument rather
  than about the memory: a caller with a malformed delta and an invisible memory is
  told about their delta. The rule is `internal/domain/memory.go`
  (`ValidateIntegralStrength`), shared with the create path, and
  `internal/server/routes_memory_reinforce_integral_test.go` pins the placement by
  running the handler with a nil pool: a refusal cannot have touched the database,
  and a legal delta can only demonstrate that it got through by dying on it.
- **The consequence is that the clamp and the codec can no longer disagree.** A
  stored value is `SMALLINT` and therefore whole, an accepted delta is whole, and
  the clamp's two bounds are integers — so every sum this handler can produce is
  already whole and the int2 truncation is unreachable from the API, which
  `internal/server/routes_memory_reinforce_clamp_test.go`
  (`TestEverySumTheReinforceClampCanProduceIsAWholeNumber`) asserts as the
  CONCLUSION over every (stored value, accepted delta) pair rather than as its three
  premises: widening `MaxBaseStrength` to `5.5` leaves the constants arm green and
  takes this one red, which is the only ordering that catches a premise quietly
  ceasing to hold. The `RETURNING` clause stays anyway: what makes it right is that
  the response cannot disagree with the row rather than that a disagreement happens
  to be reachable today, and the clause plus both of its report sites are held by
  `internal/server/routes_memory_reinforce_honesty_test.go`
  (`TestReinforceResponseReportsTheStoredBaseStrength`), a source-level gate that
  needs no database and so cannot be switched off by an unprovisioned Postgres,
  with the row-versus-body equality driven by
  `internal/server/routes_memory_reinforce_returning_db_test.go`
  (`TestReinforceMemory_ResponseMatchesTheStoredBaseStrength`).
- **Sending credentials without the work item they belong to is the one combination
  the gate rejects outright** (`TestMemoryToolsSendCredentialsWithTheirWorkItem`),
  which is why the parameter is required rather than optional-with-a-default.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed — over **6 calls, 5 of them errors (83.33%, the highest
rate in the whole census)**. One of those five is the defect above, verbatim:
`docs/audits/aihub-412-corpus-facts/error-taxonomy.md` records
`400 BAD_REQUEST — work_item_id is required when attempt_id/session_secret are
provided` against this tool — a citation K6 cannot resolve, because both of its
anchor patterns require a `.go` suffix, so it is resolved instead by
`internal/mcp/memory_published_word_test.go`
(`TestReinforceCardCorpusCitationResolvesInTheAudit`), which requires that file to
exist, to carry the quoted error, and to still name this tool. The other four are
ordinary caller errors (a missing `memory_id`, a missing `additional_context`, a
missing `work_item_id`, an absent state file), so the rate is not attributable to
the defect alone.
<!-- prose-only: because=measurement -->

## Policy

- **§6.1 T1-3 (owner ruling) — LANDED** (`aihub#433`). The reinforce clamp's bounds
  were named as the authority for the range `pf_remember` should validate against;
  both now read the same exported constants, taken from the column's own DDL — the
  constants being the DDL's is held by
  `internal/domain/memory_base_strength_range_test.go`
  (`TestBaseStrengthBoundsAreTheColumnsOwn`), which parses migration 0006's CHECK
  and DEFAULT, and the clamp being what reads them by
  `internal/server/routes_memory_reinforce_clamp_test.go`
  (`TestReinforceClampBoundsAreNamedConstantsNotLiterals`).
- **§6.2 T2-3** — one status code for "invalid attempt credential" across all tools;
  this is one of the credentialed memory paths that classification has to cover.

## Open

- Nothing this card can settle. The clamp question that stood here was ruled on
  2026-09-09 (`aihub#506`, owner: keep the saturation and publish it), and it is
  recorded where the behaviour is — hop 0-1 and hop 4 — rather than here.
  <!-- prose-only: because=external-state -->
