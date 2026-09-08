# Golden polyforge MCP fixtures, captured from real traffic

Every file under this tree is a real `(request, response)` pair from a real
polyforge session, extracted from the local Claude Code transcript corpus by
`scripts/pf_corpus_extract.py` (aihub#412). They exist so that tests of the MCP
surface — projections, slim renders, response-shape ratchets — can be written
against shapes callers were actually handed, rather than against shapes we
believe we emit.

Layout: `pf_<tool>/<role>.json`, where role is one of

| role | selection rule |
|---|---|
| `happy` | the successful call with the **most** input parameters, so the richest request shape is covered |
| `error` | the **first** failed call for that tool |
| `edge` | the call with the **largest** total payload, excluding whichever record already won `happy`/`error` |

Current tree: **111 files** across **42 tools** — the 42 with any real call. 29
tools have all three roles; 11 have no observed failure; 2 have only a `happy`.
The 8 tools published at capture time with zero calls (`pf_adopt_artifact`,
`pf_close_artifact`, `pf_cut_alpha`, `pf_ignore_artifact`, `pf_promote`,
`pf_resolve_commit`, `pf_rotate_identifier`, `pf_update_user`) have no directory:
**a missing tool here means "never called in this corpus", not "broken".**
Five of those eight have since been retired for exactly that disuse —
`pf_cut_alpha` / `pf_promote` by `aihub#448`, `pf_adopt_artifact` /
`pf_close_artifact` / `pf_ignore_artifact` by `aihub#446` — so this paragraph
describes the corpus window, not today's published set.

## Provenance and version alignment

Each fixture carries a `provenance` block: a hash of its transcript path, the
observation timestamp, and the Claude Code version. The transcript path itself is
never stored — it names the home directory and the session's cwd.

🔴 **These fixtures span ~11 plugin versions (1.1.7 … 1.1.23), and the schema
they were compared against is a single dump at one SHA.** A request field present
here may have been legal when it was sent and withdrawn since; a response key
absent here may simply postdate that call. This is not hypothetical — it happened
twice within a day of capture:

- `pf_claim_work_item.mode` was published, forwarded and bound until commit
  `a8ad8c0`, which landed *while this fixture tree was being generated*.
- `pf_update_work_item.kind` was published until commit `81c2333`, hours before.

So: **a fixture is evidence of what one version once accepted or returned. It is
not a contract.** Do not turn one into an assertion that today's server must
match it without first checking when it was captured.
`docs/audits/aihub-412-corpus-facts/README.md` carries the full caveat and the
version-signal coverage numbers.

## Sanitization is a gate, not a step

`internal/mcp/corpus_fixture_sanitize_test.go` re-scans this whole tree on every
`go test ./...` and **fails** on any of these shapes:

| pattern | rule |
|---|---|
| `hex64` | 64 hex characters standing alone (session secrets, sha256) |
| `sk-token` | `sk-` followed by 10 or more alphanumerics |
| `bearer` | `Bearer` followed by any non-space run |
| `api_key-field` | `"api_key"` with a **non-empty** string value |
| `session_secret-field` | `"session_secret"` with a **non-empty** string value |
| `absolute-home-path` | `/root/`, `/home/<user>`, `/Users/<user>` |

It re-reads the committed bytes rather than trusting the producer, because a
fixture can also be hand-added or hand-edited later.

There is a **second** gate on the other side. The Go test scans this tree only,
and the generated docs under `docs/audits/aihub-412-corpus-facts/` carry
corpus-derived text too (error samples, observed parameter values). So
`pf_corpus_extract.py` re-scans every file it wrote — both destinations — and
exits non-zero rather than leaving a leaking artifact behind. Verified by
mutation: making its `sanitize_text` a no-op produces **28** leaks across both
directories and the run refuses. `python3 scripts/pf_corpus_extract.py
--self-test` checks that scanner against a planted secret of every shape, and
against clean input, so it cannot pass by matching nothing or by matching
everything.

The extractor applies these rules on the way out, and its replacement strings are
chosen so its own output cannot match them. Two are worth stating because they
are easy to "fix" wrongly:

- **Secret-named fields are set to `""`, not to a placeholder.** The gate's
  pattern requires a non-empty value, so `"api_key": "<redacted>"` would still
  match it, and the cheapest way to make that green would have been to allowlist
  the placeholder inside the gate's own file. `""` cannot match, so compliance is
  cheaper than evasion.
- **Free text is replaced by a hash+length stub** —
  `"<redacted-text sha256=… len=…>"` — for the fields `goal`, `content`, `note`,
  `reason`, `goal_change_reason`, `reclassify_reason`, `spec`, `body`, `summary`,
  `artifact_summary`, `description`, `text`. The fixtures exist to pin **shape**;
  the prose is somebody's actual work. Error *messages* are deliberately **not**
  covered and survive verbatim, because for an `error` fixture the message is the
  point. If a future fixture genuinely needs its text, exempt that one field in
  the extractor and record why here.

Machine identity is also normalized: absolute paths become `<HOME>` /
`<WORKSPACE>` / `<CLAUDE_HOME>` / `<TMP>`, UUIDs become `<uuid>`, and
`machine_id`-style fields become `<machine>`.

### What proves the gate has teeth

Not the clean scan. **No response in the corpus carries a `session_secret`,
`api_key` or `token` field at any depth** — including all 787
`pf_claim_work_item` responses. The secret lives in the state file, which the
server writes and never echoes. So the secret-field rules fired **zero** times on
real data, and "0 hits" is equally consistent with a scanner that can no longer
match anything.

The teeth are two negative controls in the same test file:

- `TestCorpusSecretScanDetectsPlantedSecrets` plants one secret of **every** shape
  above into a throwaway tree and drives the **same** `scanCorpusTree` the real
  gate uses. A regexp typo, a walk that stops early, or a pattern that stopped
  matching turns this red. Pointing a scanner only at a clean tree proves nothing
  about whether it can see a dirty one.
- `TestCorpusSecretScanFloorsRejectEmptyTree` pins the other failure mode: the
  gate asserts floors (≥ 100 files, ≥ 42 tools) so it cannot pass by scanning
  nothing. Deleting this tree fails the build instead of quietly reporting
  "0 secrets found" over 0 files.

Verified end to end when this landed: a real 64-hex `session_secret` planted into
`pf_claim_work_item/happy.json` failed the gate on two patterns at once (`hex64`
and `session_secret-field`); the file was then restored and the restore confirmed
byte-exact by sha256 before the suite was re-run green. (The hash itself is not
quoted here on purpose — regeneration changes these files, so a pinned digest
would rot silently.)

## Regenerating

See `docs/audits/aihub-412-corpus-facts/README.md`. Regeneration **replaces** the
per-tool directories wholesale and leaves this README in place. Because the
corpus is append-only and live, a rerun on a different day picks different
representative calls — that is expected, and the gate is what keeps it safe.
