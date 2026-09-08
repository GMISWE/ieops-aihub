# pf_save_artifact — contract card

```json
{
  "tool": "pf_save_artifact",
  "description_sha256": "ab4084d04ce966ddcef924811147a590ae516f5f03f620f5f3ba2f5627a8e31e",
  "params": {
    "content": {
      "type": "string",
      "required": false
    },
    "html": {
      "type": "string",
      "required": false
    },
    "path": {
      "type": "string",
      "required": false
    },
    "structured_payload": {
      "type": "object",
      "required": false
    },
    "supersedes_memory_id": {
      "type": "string",
      "required": false
    },
    "type": {
      "type": "string",
      "required": true,
      "enum": [
        "methodology.execute",
        "methodology.plan",
        "methodology.retro",
        "methodology.review",
        "methodology.spec",
        "methodology.wrap_summary"
      ]
    },
    "visibility": {
      "type": "string",
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

Eight parameters, two required. `type` is a real enum from the domain list, so the
SDK refuses an out-of-vocabulary value before the handler runs.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `type` | enum | yes | one of the six `methodology.*` kinds |
| `work_item_id` | string | yes | which work item this artifact belongs to |
| `content` | string | no | inline content; provide `content` OR `path`, not both |
| `path` | string | no | a local UTF-8 markdown file, read by the LOCAL process |
| `structured_payload` | object | no | optional structured payload |
| `visibility` | string | no | `private\|project\|team\|admin`, default `project` |
| `supersedes_memory_id` | string | no | memory this supersedes |
| `html` | string | no | pre-rendered HTML stored verbatim |

`path` is the one parameter in the whole toolset whose value is consumed by the
**local filesystem** rather than by the server: it must resolve inside the workspace
and be at most 1 MiB. A path outside the workspace is refused, which is why an
artifact staged in `/tmp` fails with "path escapes workspace".

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildSaveArtifactBody`) sends the body of
`POST /v1/memories` via `pkg/client/client.go` (`Remember`), bound by
`internal/server/routes_memory.go` (`handleRemember`) — **the same endpoint
`pf_remember` uses**. The difference is credentials: this body carries `attempt_id`,
`claim_epoch` and `session_secret` from the state file, and that is what selects the
server's methodology branch.

Two renames at this hop:

- **`path` has no landing of its own.** `resolveArtifactContent` collapses `path` and
  `content` into one value before the body is built, reading the file where
  necessary, so only `content` appears on the wire.
- **`html` lands as `rendered_html`.** A guard that matched names rather than values
  would call that a drop; the wire test walks values.

`visibility`, `structured_payload`, `supersedes_memory_id` and `html` are forwarded
only when non-empty.

## hop 4 — what it actually does

- **`internal/server/routes_memory.go` (`enforceMethodologyAttemptGate`) is the
  branch point.** Its methodology arm binds to the TARGET memory's own work item,
  while its non-methodology arm demands the request's `work_item_id` whenever
  credentials are present. That asymmetry is why `pf_reinforce_memory`'s missing
  `work_item_id` broke every non-methodology reinforce while methodology traffic —
  this tool's — was unaffected and nobody noticed.
- `methodology.*` types are **not embedded** (`internal/domain/embedding.go`,
  `EmbeddablePrefixes`), so an artifact is never returned by the vector path:
  `pf_recall` finds it by text or by `work_item_id`, never by cosine. Naming a
  `methodology.*` type explicitly DOES get it back — `internal/domain/memory.go`
  (`recallRouted`) runs a text complement for any named non-embeddable type — but a
  semantic recall with no `type` filter at all does not, by design, because the text
  path cannot score relevance and topping the request up would spend half its budget on
  whichever artifact bodies happen to be newest.
- `html` overrides the server's markdown auto-render for the artifact viewer.

## hop 5 — what comes back

`jsonResult`, no projection; `memory_id` is what the caller keeps and what the step
`artifact_summary` cites. The corpus record above spans 392 calls at a 9.44% error
rate, which is consistent with a credentialed write whose state file may be missing.

## Policy

- **§6.2 T2-6** — the type list here IS enforced (it is a `propEnum` the SDK checks
  and a server-side branch), which is the contrast the ruling draws with
  `pf_remember`'s 13-value list.
- **§6.1 T1-5** — no projection at all on this response.

## Open

- The recall blind spot above — `methodology.*` never being embedded — is measured
  and stated, not fixed. Nothing in this tool's schema warns a caller that the
  artifact they just saved is unreachable by semantic search.
