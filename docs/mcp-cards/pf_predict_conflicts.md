# pf_predict_conflicts — contract card

```json
{
  "tool": "pf_predict_conflicts",
  "description_sha256": "65919e1709fa50824eb5ffbbb479145976ff0d174996a62a326acac34b685978",
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
- **`intent: "read"` is honoured on `path`/`document`/`section` only.** A repo entry
  still takes `git_branch` and a service entry `deploy_env` whatever the intent says,
  so `read` on those two is inert rather than permissive. That asymmetry is one half
  of the false-negative behaviour above.
- **`external_ref` derives NO lock and produces no warning** — it is the one declared
  type that can be declared, accepted, and produce no signal of any kind, under a
  field whose own description says "Declared resource locks".
- `file_scope` keys are namespaced by project, and a `path` entry without `repo`
  keeps the two-segment key form that conflicts with every repo's copy of that path.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above spans 406 calls at a 2.71%
error rate — high usage for a predicate the repo does not trust, which is itself
worth recording.

## Policy

- **§6.2 T2-12 (owner ruling)** — repo and service entries become **advisory** and
  derive no lock. What this tool then reports for an advisory entry is named as an
  open question rather than answered.
- **§6.2 T2-13** — the empty-`repo` key form must stay byte-identical; tidying the
  two shapes into one three-segment key strands every live lock.
- **§6.1 T1-2** — no numeric parameters here, so neither numeric rule applies.

## Open

- **§6.4 item 6** — **what `pf_predict_conflicts` reports for an advisory entry is
  listed in `aihub#416`'s `spec_must_answer` and is open by design.** This card must
  not be read as saying the current behaviour is the intended one.
- The two measured untrustworthy directions are recorded, not fixed. No adjudicated
  row commits to fixing them.
