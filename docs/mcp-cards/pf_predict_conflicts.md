# pf_predict_conflicts — contract card

```json
{
  "tool": "pf_predict_conflicts",
  "description_sha256": "21efef2052245dd6164941753069ea379387987353eaa9f0e94e4f2054ba4552",
  "input_schema_sha256": "d88d52410ef100adb7f9725effa8cfc66bc8516c5b5f24a3e2718379299ca096",
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
| `work_item_id` | string | no | "optional, for context" |
| `project` | string | no | namespaces `file_scope` checks; optional when `work_item_id` is set |
| `dry_run` | boolean | no | "do not mutate state" |

🔴 **This tool has been measured untrustworthy in both directions**, and that is the
single most important thing a caller can know about it: it reports an attempt's OWN
locks as conflicts after a claim, and it false-negatives on read intent. The
`aihub#387` ruling that withdrew `pf_get_ready_queue`'s `non_conflicting` cites
exactly that, on the grounds that building on this predicate would have produced a
second untrustworthy one.

The only reliable conflict signal in this system is **the return value of
`pf_claim_work_item`**, which reports what it actually took.

🔴 **`aihub#416` moved the SEVERITY CEILING for `repo` and `service` entries, and
the description now says so.** A payload of only those two types can no longer
return `hard_block`: they derive no lock, so the lock-table rule cannot fire for
them. A repo overlap reports `soft_block` (rule 2 or 4) and a service overlap
`info` (rule 6), both from a join on other running work items' declarations.
`path` / `document` / `section` are unaffected and can still return `hard_block`.

That change is published rather than left to be measured because it is invisible
to a caller otherwise — same parameters, same response shape, same 200 — and
`pf-work`'s pre-claim gate branches on `severity`. **Read `severity: "info"` on a
service as "somebody else declares it, judge for yourself", not as "checked, no
conflict":** the exclusion that value used to stand behind no longer exists.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_conflicts.go` (`registerConflictTools`) checks only that
`declared_resources` is present and passes the **whole argument map** to
`pkg/client/client.go` (`PredictConflicts`) → `POST /v1/conflicts/predict`, bound by
`internal/server/router.go` (`handlePredictConflicts`).

Wholesale forwarding again, so there is no hop-2 drift surface. The entry shape is
published through the shared `internal/mcp/tools_lifecycle.go`
(`declaredResourcesProp`), which is where the `type`-versus-lock-type distinction and
the per-type URI scheme rules are stated.

## hop 4 — what it actually does

- Reports predicted lock conflicts for the supplied resources and `will_unlock` —
  which blocked work item would be unblocked.
- **`intent: "read"` is honoured on `path`/`document`/`section` only.** On a repo or
  service entry `read` is inert for a different reason since `aihub#416`: those two
  take no lock under any intent, so there is nothing for it to suppress.
- **Three of the six declared types derive NO lock**: `external_ref` (always), plus
  `repo` and `service` since `aihub#416`. `external_ref` additionally produces no
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
- **Rule 6 is new** (`service`, `info`, not gated on `dry_run`). Without it, retiring
  `deploy_env` would have left a service declaration with no rule at all.
- **`last_active_age_seconds`** rides on the repo and service predictions: how long
  ago that attempt reported activity. It is what makes deploy preflight an existing
  call rather than a new endpoint. ⚠️ It is an AGE, not a lease — nothing expires
  on it and no code branches on it.
- `file_scope` keys are namespaced by project, and a `path` entry without `repo`
  keeps the two-segment key form that conflicts with every repo's copy of that path.

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
  NOT do is exclude a work item's own attempt from rules 2, 4 and 6 — see the next
  bullet, which it leaves exactly as it found it.
- The two measured untrustworthy directions are recorded, not fixed. No adjudicated
  row commits to fixing them, and `aihub#416` (landed 2026-09-09) did not change
  either: rule 2 read the caller's own lock row before and joins the caller's own
  declaration now, so "reports an attempt's OWN locks as conflicts" survives the
  rewrite unchanged in kind.
