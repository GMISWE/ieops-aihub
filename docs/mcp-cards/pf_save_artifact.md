# pf_save_artifact — contract card

```json
{
  "tool": "pf_save_artifact",
  "description_sha256": "cd615eceedc57b064f28a94572a572edc8a4c2ff9af71b760c1ac9ee1d966418",
  "input_schema_sha256": "d6574f66687976273e8a59f6d4946362a61e99c38e081dde7967e8a48770bec9",
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
      "required": true
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

Eight parameters, two required. `type` is a plain string whose ENFORCED rule is a
PREFIX, not a list: it must start with `methodology.`, and the six named kinds are
suggestions. It was published as a 6-value enum from `aihub#211` until `aihub#499`
(2026-09-09) withdrew it — see Policy below for why the names went and the prefix
stayed.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `type` | string | yes | starts with `methodology.`; the six kinds are suggested, not exhaustive |
| `work_item_id` | string | yes | which work item this artifact belongs to |
| `content` | string | no | inline content; provide `content` OR `path`, not both |
| `path` | string | no | a local UTF-8 markdown file, read by the LOCAL process |
| `structured_payload` | object | no | optional structured payload; a non-object — including a JSON-encoded string of one — is a 400 |
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

**One refusal happens BEFORE the wire** (`validatePfSaveArtifactArgs`, `aihub#499`):
`type` must start with `methodology.`, and it is checked here rather than in domain
because this tool and `pf_remember` share `POST /v1/memories` — the server cannot
narrow to `methodology.*` without breaking `pf_remember`, which must accept the
other three prefixes. So the error carries no HTTP status: the request never
leaves the process. Its mirror is `validatePfRememberArgs`, which refuses that same
prefix, and between them the two tools partition the four prefixes.

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
- **`structured_payload` must be a JSON object** (`aihub#465`). It is merged into
  `attrs.structured_payload` by unmarshalling into an `any`, which succeeds for a
  JSON string just as happily as for an object — so a stringified payload was
  stored under that key as a string, answered 200, and then read back by
  `internal/server/routes_artifacts.go` (`reviewPayload`), which expects an
  object and finds nothing. Measured live twice. It is now a 400 naming the type
  and the byte length, with `details.string_decodes_to` saying whether the quoted
  text was valid JSON; nothing is coerced. Unlike `pf_remember`'s `attrs`, this
  field has no stored-data exemption, because no path in the repo feeds a stored
  `structured_payload` back into a write — `UpdateMemory` carries the whole merged
  `attrs` object instead and leaves this field unset.

## hop 5 — what comes back

`jsonResult`, no projection; `memory_id` is what the caller keeps and what the step
`artifact_summary` cites. The corpus record above spans 392 calls at a 9.44% error
rate, which is consistent with a credentialed write whose state file may be missing.

## Policy

- **§6.2 T2-6 — RESOLVED** (`aihub#499`, 2026-09-09). This card used to say the type
  list here IS enforced, "a `propEnum` the SDK checks and a server-side branch",
  and drew that as the contrast with `pf_remember`'s 13-value list. `aihub#445`
  measured both halves and neither held: the SDK checks nothing on this
  registration path (`aihub#463`, read on go-sdk v1.6.0: polyforge registers
  through the untyped `(*mcp.Server).AddTool` method, whose `callTool` invokes the
  handler with no schema step), and no server branch pinned the SIX names, so
  `methodology.anything` passed `internal/domain/memory.go` (`Remember`) and
  stored. `aihub#445` recorded that as a gap; `aihub#499` closed it, and the two
  halves moved in OPPOSITE directions:

  - **The six names were withdrawn**, following `aihub#445`'s shape one layer
    along. §6.2 T2-6's ruling is "keep the leniency", and the accepted set is open
    in fact and not only in principle: measured live 2026-09-09 across all ten
    projects, 1,185 `methodology.*` rows carry **3** that are off the six
    (`methodology.playbook`, `ieops` `wi_TYllxcv1` — operator handover documents
    that no member of the six describes). Enforcing the names would have refused
    real artifacts.
  - **The `methodology.` prefix is now enforced** —
    `internal/mcp/tools_memory.go` (`validatePfSaveArtifactArgs`), the mirror of
    `validatePfRememberArgs`. 🔴 This card's earlier claim that the server enforced
    that prefix was ALSO wrong: `internal/server/routes_memory.go`
    (`handleRemember`) only BRANCHES on it — its non-methodology arm merely
    verifies the supplied credential — and `Remember` accepts all four of
    `MemoryTypePrefixes`, so `pf_save_artifact(type="fact.note")` with a live
    claim put a non-artifact through the artifact door. The check makes the older
    published claim honest rather than adding a new promise.

    ⚠️ Scope of that last measurement, because the links were verified
    separately and not end to end: hop 2 forwarding `fact.note` is measured (drop
    the new prefix arm and the argument is accepted and sent), and each later
    link is independently covered — `handleRemember`'s arm selection, `Remember`'s
    four-prefix loop, and `memories_type_check` mirroring that same list
    (`internal/domain/memory_type_check_test.go`). No single test drives the whole
    path, so "it stored" is DERIVED from those four, not observed once.

  The check lives at hop 2, not in domain, because this tool and `pf_remember`
  share `POST /v1/memories` and the server cannot narrow to `methodology.*`
  without breaking `pf_remember`, which must accept the other three prefixes. A
  tool-level narrowing is only expressible where the tool is known.

  ⚠️ One consequence of keeping the set open, stated because nothing in the schema
  says it: an off-list `methodology.*` type IS stored, but it is **not
  pre-rendered** and does **not** appear in the work item's artifact-links
  section — `internal/domain/memory.go` (`defaultRenderTypes`) and
  `internal/server/ui_handlers_wi.go` (`fetchArtifactLinks`) both name the six
  literally. `internal/server/ui_embed.go` (`uiFuncMap`) holds the one
  consumer deliberately written for any `methodology.*` type — its
  `artifactInitial` helper reads the segment after the last dot, so the avatar
  letter works for a name outside the six. The `type`
  description says this; the three `methodology.playbook` rows above are living
  with it.
- **§6.1 T1-5** — no projection at all on this response.

## Open

- The recall blind spot above — `methodology.*` never being embedded — is measured
  and stated, not fixed. Nothing in this tool's schema warns a caller that the
  artifact they just saved is unreachable by semantic search.
