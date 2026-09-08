# pf_resolve_commit — contract card

```json
{
  "tool": "pf_resolve_commit",
  "description_sha256": "871900ea3d9a6c052811c628cf28306450bde29f301c24b9d39961997c56582b",
  "input_schema_sha256": "d2c60621b3317daed0a4464b8140087acfb6e6e0062fcd2c05529da13a867783",
  "params": {
    "commit_id": {
      "type": "string",
      "required": true
    },
    "memory_id": {
      "type": "string",
      "required": true
    },
    "reply": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three parameters, all required.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID" — the artifact carrying the annotation |
| `commit_id` | string | yes | "Commit annotation ID" |
| `reply` | string | yes | what was changed, or why the annotation is resolved |

"Commit" here means a **review annotation on a spec/plan artifact**, not a git
commit. That collision is worth stating on the card because `pf_commit` is a
different tool in a different file that does mean git.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildResolveCommitBody`) sends exactly `{reply}` to
`POST /v1/memories/<id>/commit/<commit_id>/resolve` via
`pkg/client/client.go` (`ResolveCommit`), bound by
`internal/server/routes_memory.go` (`handleResolveCommit`).

**Two of the three parameters are path segments**, not body fields, which is why the
body is a single key. No attempt credentials are sent.

There is a sibling route, `.../commit/<commit_id>/reply`, bound by
`internal/server/routes_memory.go` (`handleV1ReplyCommit`), that this tool does not
call: replying and resolving are different operations and only the second is
published here.

## hop 4 — what it actually does

- Marks the annotation `status=resolved` and emits a `memory_commit_resolved` event,
  so the resolution is on the timeline rather than only in the artifact.
- The annotations themselves live on the memory's commit list and are what the
  artifact viewer renders; the same vocabulary is written by the web UI's own
  handlers (`internal/server/routes_artifacts.go`, `handleUIArtifactResolveCommit`),
  which is a second writer of the same state from a different entry point.
- `reply` is stored, not merely acknowledged, which is why it is required rather than
  optional.

## hop 5 — what comes back

`jsonResult`, no projection. **`response_keys_observed` is `null`** — the
`aihub#412` corpus holds no record for this tool, so nothing here is measured
traffic. That is a statement about usage, not about correctness.

The key is pinned anyway, just not here: `aihub#482`'s K10 in
`internal/mcp/card_response_keys_live_e2e_db_test.go` drives this tool against a
live server — seeding the annotation row directly, since annotations are made in
the `/ui` viewer and no MCP tool reaches it — and holds the result to `ok`,
declared in `docs/mcp-cards/live-response-keys.json`.

## Policy

- **§6.2 T2-7** — the "does anything read this?" question the ruling asks about
  adopt/close/ignore applies to the annotation flow too, and the ruling's own caveat
  names that flow explicitly as the thing that was not checked.
- **§6.2 T2-5** — `memory_commit_resolved` is another free-text event type with no
  published vocabulary.

## Open

- **§6.4 item 3** — whether the UI flow and this tool are two writers of one state
  that can disagree is exactly what T2-7's caveat says was not checked. This card
  records the second writer rather than asserting they agree.
