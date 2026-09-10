# pf_predict_conflicts — contract card

```json
{
  "tool": "pf_predict_conflicts",
  "description_sha256": "21efef2052245dd6164941753069ea379387987353eaa9f0e94e4f2054ba4552",
  "input_schema_sha256": "29efdb34fe3e10d154b80b891f9799666320719ef59ee735eb86b6a69c0b02b1",
  "params": {
    "declared_resources": {
      "type": "array",
      "required": true
    },
    "dry_run": {
      "type": "boolean",
      "required": false
    },
    "project": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "predictions",
    "severity",
    "will_unlock"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Four parameters, one required.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `declared_resources` | array | yes | `{type, uri, intent}` + optional `repo` |
| `work_item_id` | string | no | id or slug; "the only way this call learns which running work item is YOU" |
| `project` | string | no | namespaces `file_scope` checks; optional when `work_item_id` is set |
| `dry_run` | boolean | no | "do not mutate state" |

🔴 **This tool was measured untrustworthy in both directions; the self-report
direction is now fixed and the read-intent direction still stands.** It used to
report an attempt's OWN locks and declarations back to it as conflicts after a
claim — fixed in two halves, `aihub#510` (2026-09-09) for the declaration rules
and `aihub#564` (2026-09-10) for the lock-table rules — and it still
false-negatives on read intent. The `aihub#387` ruling that withdrew
`pf_get_ready_queue`'s `non_conflicting` was made against the both-directions
reading; the surviving direction alone still supports it, because building on
this predicate would inherit the read-intent false negative.

**The self-report fix landed in two halves because the two halves are different
claims.** The four rules that read `declared_resources` — 2, 4, 5 and 6 — stopped
reporting the caller back to itself with `aihub#510`, which
`internal/domain/delocking_db_test.go`
(`TestDeLockingPredictReportsAdvisoryEntries`) drives rule by rule against a second
running work item declaring the same name.
The two that read the **lock table** — 1 (`hard_block`) and 3 (`file_scope`) —
followed with `aihub#564`, which
`internal/domain/predict_lock_self_exclusion_db_test.go`
(`TestPredictLockRulesLeaveTheCallerOut`) drives in both directions: the caller's
own lock stops being reported, and ANOTHER attempt's lock still answers
`hard_block` (`soft_block` from rule 3 under `dry_run`).
The lock half was deliberately the later, sharper half: rule 1 answers "would
taking this lock collide", it `return`s on the first hit and suppresses every rule
after it — pinned as a property of the whole ladder by
`internal/domain/predict_rule_shape_test.go`
(`TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt`) — and it decides
the value `pf-work`'s pre-claim gate branches on. Before the fix that meant a
claimed work item re-predicting its own `path` declaration handed the gate a
`hard_block` naming the caller itself, and a self-held row could suppress a REAL
foreign conflict later in the same payload — the mixed-payload arm of
`TestPredictLockRulesLeaveTheCallerOut` measured exactly that on the pre-fix
build (2026-09-10) before the fix turned it green.

⚠️ **The exclusion is opt-in, and it cannot be otherwise:** it needs
`work_item_id`, so a predict that names nobody is unchanged, which the anonymous
create-preview arms of `internal/domain/delocking_db_test.go`
(`TestDeLockingPredictReportsAdvisoryEntries`) and
`internal/domain/predict_lock_self_exclusion_db_test.go`
(`TestPredictLockRulesLeaveTheCallerOut`) pin where it stands, one per rule
family. That is correct for the create-preview path, which names nobody because
the work item does not exist yet — but it means an agent that omits the parameter
still gets itself back.

The only reliable conflict signal in this system is **the return value of
`pf_claim_work_item`**, which reports what it actually took.
<!-- prose-only: because=judgement -->

🔴 **`aihub#416` moved the SEVERITY CEILING for `repo` and `service` entries, and
the description now says so** — read off a live session and compared with the
severity vocabulary the server enforces, in
`internal/mcp/predict_published_ceiling_test.go`
(`TestPredictPublishedSeverityCeilingUsesTheEnforcedVocabularies`). A
payload of only those two types can no longer
return `hard_block`: they derive no lock, so the lock-table rule cannot fire for
them — the two halves of that are held by
`internal/domain/read_intent_scope_test.go`
(`TestReadIntentIsHonouredOnlyOnTheTypesThatDeriveALock`) and by
`TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt`, and its published
statement by `TestPredictPublishedSeverityCeilingUsesTheEnforcedVocabularies`.
A repo overlap reports `soft_block` (rule 2 or 4) and a service overlap
`info` (rule 6), both from a join on other running work items' declarations, which
`internal/domain/delocking_db_test.go`
(`TestDeLockingPredictReportsAdvisoryEntries`) drives and
`TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt` pins per rule.
`path` / `document` / `section` are unaffected and can still return `hard_block`,
driven for a path by `internal/domain/file_scope_repo_key_db_test.go`
(`TestFileScopeRepoKey_PredictRule1NoHardBlockAcrossRepos`) and held for all three
types by `TestResourceToLock_PathStillDerivesFileScope`.

That change is published rather than left to be measured because it is invisible
to a caller otherwise — same parameters, same response shape, same 200 — and
`pf-work`'s pre-claim gate branches on `severity`.
<!-- prose-only: because=cross-repo -->
**Read `severity: "info"` on a
service as "somebody else declares it, judge for yourself", not as "checked, no
conflict":** the exclusion that value used to stand behind no longer exists.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_conflicts.go` (`registerConflictTools`) checks only that
`declared_resources` is present and passes the **whole argument map** to
`pkg/client/client.go` (`PredictConflicts`) → `POST /v1/conflicts/predict`, bound by
`internal/server/router.go` (`handlePredictConflicts`) — all three observed on the
request a fake aihub really received, in `internal/mcp/predict_wire_shape_test.go`
(`TestPredictForwardsTheWholeArgumentMapAndChecksOnlyForTheDeclaration`).

Wholesale forwarding again, so there is no hop-2 drift surface. The entry shape is
published through the shared `internal/mcp/tools_lifecycle.go`
(`declaredResourcesProp`), which is where the `type`-versus-lock-type distinction and
the per-type URI scheme rules are stated — the sharing itself by
`internal/mcp/predict_published_ceiling_test.go`
(`TestPredictPublishesTheSharedDeclaredResourcesEntryShape`), the two rules by
`internal/mcp/resource_schema_test.go`
(`TestDeclaredResourcesProp_EnumeratesDeclaredTypesNotLockTypes`) and
(`TestDeclaredResourcesProp_PublishesTheEnforcedURISchemes`).

## hop 4 — what it actually does

- Reports predicted lock conflicts for the supplied resources and `will_unlock` —
  which blocked work item would be unblocked — with the rules driven in
  `internal/domain/delocking_db_test.go`
  (`TestDeLockingPredictReportsAdvisoryEntries`) and the `will_unlock` half in
  `internal/server/dependencies_slug_db_test.go`
  (`TestDependencyEndpointsResolveSlugs`).
- **`intent: "read"` is honoured on `path`/`document`/`section` only.** On a repo or
  service entry `read` is inert for a different reason since `aihub#416`: those two
  take no lock under any intent, so there is nothing for it to suppress —
  `internal/domain/read_intent_scope_test.go`
  (`TestReadIntentIsHonouredOnlyOnTheTypesThatDeriveALock`) walks the live declared
  type vocabulary against every intent and holds both halves of that "only".
- **Three of the six declared types derive NO lock**: `external_ref` (always), plus
  `repo` and `service` since `aihub#416`, each checked at the mapper by
  `internal/domain/lock_derivation_retired_test.go`
  (`TestResourceToLock_RepoAndServiceDeriveNoLock`). `external_ref` additionally produces no
  warning, so it remains the one entry that can be declared, accepted, and produce
  no signal of any kind. `repo` and `service` do produce a signal — rules 2, 4
  and 6 below.
- **Rule 2 reads DECLARATIONS, not locks.** It used to hardcode
  `resource_type='git_branch'` in SQL, bypassing the mapper; retiring the derivation
  without rewriting it would have left it matching zero rows forever, which is
  byte-identical to "no conflict" — `aihub#238`'s fake all-clear, in the same
  function. Its description changed with it, from "is working on the same repo
  **branch**" to "**declares** the same repo", because no branch name participates
  in the judgement any more.
- **Rule 6 is new** (`service`, `info`, and it is not gated on `dry_run`), which
  `internal/domain/delocking_db_test.go`
  (`TestDeLockingPredictReportsAdvisoryEntries`) drives under both values of
  `dry_run` and `TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt` holds
  as a property of the ladder. Without it, retiring
  `deploy_env` would have left a service declaration with no rule at all.
- **`last_active_age_seconds`** rides on the repo and service predictions: how long
  ago that attempt reported activity. It is what makes deploy preflight an existing
  call rather than a new endpoint. ⚠️ It is an AGE, not a lease — nothing expires
  on it and no code branches on it.
- **The four declaration rules exclude the caller** (`aihub#510`). All of them join
  `work_items` on `status='running'` plus a declaration overlap, and a claimed work
  item asking about its own declarations satisfies both halves — so it used to be
  reported back to itself as a `soft_block` (2, 4) or an `info` (5, 6), and the
  top-level `severity` rose with it. The exclusion is bound as a parameter inside
  the two shared containment fragments (`internal/domain/conflicts.go`
  (`notCallersOwnWISQL`)) rather than at the four call sites, so a fifth rule
  written with them inherits it — the inheritance itself is what
  `internal/domain/predict_self_exclusion_test.go`
  (`TestPredictSelfExclusionIsBoundInsideTheSharedContainmentFragments`) holds,
  since no behavioural arm can reach a rule that does not exist yet.
- **The two lock-table rules exclude the caller too** (`aihub#564`, 2026-09-10 —
  symmetric with the bullet above). Rules 1 and 3 read `resource_locks`, and a
  claimed work item re-predicting its own declarations found the very locks its
  own claim took: rule 1 answered `hard_block` naming the caller, and under
  `dry_run` rule 3 answered `soft_block` the same way. The exclusion predicate is
  the claim path's own answer to the same question
  (`internal/domain/run_attempts.go` (`foreignLockHolderSQL`), `aihub#207`): a
  resume re-takes locks its earlier attempt still holds — driven by
  `internal/domain/run_attempts_resume_test.go`
  (`TestResumeOwnLocks_NoSelfConflict`) — so predict answering `hard_block` there
  predicted a collision the claim it fronts for would never raise. Driven in both directions by
  `internal/domain/predict_lock_self_exclusion_db_test.go`
  (`TestPredictLockRulesLeaveTheCallerOut`) — self-held lock no longer reported,
  foreign holder still `hard_block`/`soft_block` — and bound inside a shared
  fragment (`internal/domain/conflicts.go` (`notCallersOwnLockHolderSQL`)) so a
  third lock-table rule inherits it, which
  `internal/domain/predict_self_exclusion_test.go`
  (`TestPredictLockRuleExclusionIsBoundInTheSharedFragment`) holds the same way
  the declaration-family twin above is held.
- ⚠️ **`work_item_id` may be an id OR a slug, and the exclusion depends on the
  `aihub#357` resolution of the two.** A slug matches no `work_items.id`, so a
  filter bound to the raw parameter would silently do nothing for the spelling
  `pf-work`'s own Mode B sends, which the by-slug arm of
  `internal/domain/delocking_db_test.go`
  (`TestDeLockingPredictReportsAdvisoryEntries`) drives on its own, and the
  by-slug arm of `internal/domain/predict_lock_self_exclusion_db_test.go`
  (`TestPredictLockRulesLeaveTheCallerOut`) drives for the lock family — where
  the raw parameter is also a `*string`, so binding it un-resolved would
  additionally cross `nil` as `NULL` and silence rule 1 for EVERY caller, the
  `aihub#238` fake all-clear on the hard gate.
- `file_scope` keys are namespaced by project
  (`internal/domain/conflicts_predict_test.go`
  (`TestPredictConflicts_FileScopeProjectScoped`)), and a `path` entry without `repo`
  keeps the two-segment key form that conflicts with every repo's copy of that path
  (`internal/domain/file_scope_repo_key_db_test.go`
  (`TestFileScopeRepoKey_UnqualifiedDeclarationStillConflictsWithQualifiedHolder`)).

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above spans 406 calls at a 2.71%
error rate — high usage for a predicate the repo does not trust, which is itself
worth recording.

## Policy

- **§6.2 T2-12 (owner ruling)** — repo and service entries become **advisory** and
  derive no lock. `aihub#416` landed that, and answered the follow-on question this
  card used to carry open: an advisory repo entry reports `soft_block`, an advisory
  service entry `info`, both from a declaration join, both carrying
  `last_active_age_seconds`.
- **§6.2 T2-13** — the empty-`repo` key form must stay byte-identical; tidying the
  two shapes into one three-segment key strands every live lock.
- **§6.1 T1-2** — no numeric parameters here, so neither numeric rule applies.

## Open

- **§6.4 item 6 is CLOSED for this tool as of `aihub#416` (2026-09-09).** What a
  prediction reports for an advisory entry was that item's open question; the answer
  is in hop 4 above and in the tool description. What that work item deliberately did
  NOT do is exclude a work item's own attempt from rules 2, 4 and 6; `aihub#510`
  (2026-09-09) did that, for those three plus rule 5.
- **The LOCK half of the self-report is CLOSED as of `aihub#564` (2026-09-10).**
  What stood here was the sharper half of the self-report, measured 2026-09-09: a
  claimed work item re-predicting its own `path` declaration used to get, with
  `dry_run=false`, `severity: "hard_block"` and a single rule-1 prediction
  reading `Resource lock is already held by another attempt` whose
  `work_item_slug` **was the caller**, and with `dry_run=true` — rule 1 skipped —
  a rule-3 `soft_block` / `File path overlaps with another running attempt`,
  again naming the caller; never both visible, because rule 1 returns on its
  first hit. `aihub#510` (2026-09-09) left it alone deliberately, because
  rule 1 decides the value `pf-work`'s pre-claim gate branches on — the
  suppression property `internal/domain/predict_rule_shape_test.go`
  (`TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt`) pins; `aihub#543`
  (2026-09-10) ruled it a `known-defect` ledger row rather than a probe, on the
  grounds that a probe pinning that answer would arrive red on the day of the fix;
  and `aihub#564` (2026-09-10) is that fix, so the rows went with it as
  adjudicated. The gate readings were re-measured on the fix's own before/after
  arms (2026-09-10): for `pf-work` Mode B's exact call shape
  (`work_item_id=<slug>`, `dry_run=true`) the answer moved from `soft_block`
  naming the caller to `info` with no predictions, and a `dry_run=false`
  re-predict moved from `hard_block` to `info` — while a path held by ANOTHER
  running attempt still answers `hard_block`, both held by
  `internal/domain/predict_lock_self_exclusion_db_test.go`
  (`TestPredictLockRulesLeaveTheCallerOut`).
- **The read-intent false negative is still unfixed**, and no adjudicated row
  commits to fixing it. `aihub#416` (landed 2026-09-09), `aihub#510` (2026-09-09)
  and `aihub#564` (2026-09-10) all left it alone.
