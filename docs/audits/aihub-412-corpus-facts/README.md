# aihub#412 — polyforge MCP corpus facts

The local Claude Code transcript corpus is the **only** end-to-end record of the
`polyforge → MCP → aihub` request/response chain: the server keeps no per-request
log, and `agent_events` is a whitelisted semantic stream, not a wire log. This
directory turns that corpus into committed, regenerable facts, so that later work
on the MCP contract argues from measured traffic instead of from recollection.

Everything here except this file and the sibling fixture README is **generated**.
Do not hand-edit a generated file: fix `scripts/pf_corpus_extract.py` and rerun.

The generated files carry corpus-derived text, so the extractor re-scans
everything it writes for secret shapes and **exits non-zero rather than
reporting success** if any survives. Its `--self-test` proves that scanner
detects a planted secret of every shape while leaving clean output alone.

| file | what it answers |
|---|---|
| `tool-census.md` / `.csv` | per tool: calls, errors, error rate, first/last seen, result kind |
| `error-taxonomy.md` | every failure, normalized, at two levels of granularity |
| `param-types-vs-schema.md` | per param: observed JSON types and values vs the published contract |
| `sequence-inventory.md` | real lifecycle flows, classified, with anonymized example traces |
| `response-keys/pf_*.json` | hop5 key inventory: the union of response keys callers were handed, per tool |
| `run-manifest.json` | the denominators every number below is measured against |

Sanitized request/response fixtures live separately, next to the code that will
consume them: `internal/mcp/testdata/corpus/` (see the README there).

## Regenerating

```sh
# 1. the schema contract to compare against, from the tree you are on
go build -o /tmp/pf-dump ./cmd/polyforge
/tmp/pf-dump dump-mcp-schemas "$(git rev-parse HEAD)" > /tmp/pf-tool-schemas.json

# 2. the extraction, one streaming pass over the corpus (~46s for 3.2 GiB)
PF_CORPUS_DIR="$HOME/.claude/projects" python3 scripts/pf_corpus_extract.py \
  --schemas /tmp/pf-tool-schemas.json \
  --docs-out docs/audits/aihub-412-corpus-facts \
  --fixtures-out internal/mcp/testdata/corpus
```

The corpus root is **never** hard-coded — `--corpus`, or `$PF_CORPUS_DIR`. The
script is python3 stdlib only and reads nothing outside the corpus root and the
schema dump. It never reads `~/.polyforge/config.toml`.

## Method, and the trap in counting this corpus

**The counted unit is a `tool_use` content block whose `name` starts with
`mcp__plugin_polyforge_polyforge__pf_`, paired to its `tool_result` by
`tool_use_id`.**

Counting occurrences of the tool-name *string* instead overcounts by roughly 20×,
because every session's tool listing repeats all 50 names. Measured: a `grep -c`
for `"…pf_cut_alpha"` returns **4,462** hits for a tool with **zero** real calls.
Any figure in this directory that disagrees with a grep is not thereby wrong —
check what was counted first.

Pairing is single-pass and in-file: a `tool_result` always follows its `tool_use`
in the same transcript. `run-manifest.json` reports `unpaired_tool_use`, so a
cross-file flow would show up rather than being silently dropped.

## Headline numbers

Run `2026-09-07T13:29:03Z`, schema contract `a8ad8c0`.

- corpus: **2,088** `*.jsonl` files, **656,373** lines, 3.22 GiB, subagent
  transcripts included (`*/subagents/*.jsonl`)
- **14,091** paired `pf_*` calls, pairing rate **100.00%**, unpaired **0**
- calls span **2026-06-23 .. 2026-09-07** (the *files* span 2026-05-29 onward)
- **626** failed calls — **4.44%** overall — across **18** distinct error codes
  and **147** distinct normalized `(tool, status, code, message)` rows
- **42** of **50** published tools have at least one real call
- **93.13%** of results are strict-JSON objects; **6.87%** are prose slim renders
- **2,756** `(transcript, work item)` groups over 14 flow classes
- parse failures: **0** of **32,827** attempted line parses (**0.00%**); of all
  656,373 lines, **0** were not object-shaped

### 🔴 The corpus is live, so these numbers move

The session doing the measuring writes to the corpus it is measuring. Successive
runs during this one piece of work measured 14,011 → 14,020 → 14,067 → 14,070
calls. Every number here is pinned to the `generated_at` in `run-manifest.json`;
treat a small disagreement with a fresh run as expected, not as a defect.

## Deltas against the aihub#412 wi body

The wi body's figures came from an 8-largest-files sample taken earlier the same
day. Re-measured on the full census they hold up, with two premises refuted:

| fact | wi body | measured (full census) | verdict |
|---|---|---|---|
| corpus size | 3.2 GB | 3.22 GiB / 3.46 GB | same, GiB vs GB |
| transcript files | 2,037 | **2,088** | +51 (corpus grew) |
| `pf_*` calls | 13,447 | **14,091** | +644 (corpus grew) |
| tools with calls | 42 / 50 | **42 / 50** | confirmed |
| the 8 zero-call tools | listed | **the same 8, exactly** | confirmed |
| `tool_use_id` pairing | 100% on one sampled file | **100.00% of all 14,091** | confirmed, now full-census |
| strict-JSON results | ~90% | **93.13%** | confirmed |
| plugin versions spanned | ~10 | **11** observed | +1 (`1.1.20`, no longer cached locally) |
| date span | 2026-05-29 .. 09-07 | **files** 05-29..09-07; **calls** 06-23..09-07 | the *call* span is 25 days shorter |
| "claim responses carry `session_secret`" | asserted, as the reason sanitization is a gate | **0 of 788** claim responses — and 0 of all 14,091 — carry `session_secret`/`api_key`/`token` at any depth ≤ 6 | 🔴 **refuted** |
| "the `pf-schemas` orphan branch has per-version dumps" | asserted | `origin/pf-schemas` has **1 commit**: one dump, for one SHA | 🔴 **refuted** |

Deltas are reported, not folded back into the arithmetic: the wi's numbers were
right for the corpus of a few hours earlier.

The refuted `session_secret` premise does not weaken the gate, it relocates its
justification. The secret lives in `<workspace>/.polyforge/state/<wi_id>.json`,
which the MCP server writes and never echoes — as `pf-work`'s own SKILL.md says.
So the sanitizer's secret-*field* rules are prophylactic: they fired **0** times
on this corpus. What proves the gate has teeth is therefore not its output but
its negative controls — see the fixture README.

Also worth stating plainly, because it is the wi's own framing:
`recall`/`create`/`list` were described as the prose slim renders. Directionally
right, but `pf_recall` is **86% strict JSON** (105 prose of 754 calls). The
genuinely prose-dominated tools are `pf_list_projects` (76%),
`pf_reinforce_memory` (83%), `pf_get_memory` (52%) and `pf_diff` (100%).

## 🔴 Version alignment — read before calling anything drift

`param-types-vs-schema.md` compares traffic from ~11 plugin versions against
**one** schema dump at **one** SHA. Per-version dumps do not exist: the
`pf-schemas` orphan branch holds exactly one commit (verify with
`git log --oneline origin/pf-schemas`).

So **"observed but not published" is a candidate, never a proven regression.**
Of the **32** candidates, **4** are already explained, and two of those are
worked examples of the caveat itself:

- `pf_claim_work_item.mode` — published, forwarded and bound until commit
  `a8ad8c0`, *"stop publishing pf_claim_work_item.mode"*. That commit landed on
  `main` **while this wi was being executed**: the first extraction run compared
  against `1ec3bdc`, where `mode` was still published and did not appear as a
  candidate at all. Every observed call sending `mode` was correct at send time.
- `pf_update_work_item.kind` — published and forwarded until commit `81c2333`,
  *"stop publishing pf_update_work_item.kind"*, authored **2026-09-07 09:55**,
  about three hours before this wi was created.
- `pf_list_work_items.nonexistent_param_380` — planted by the aihub#380 contract
  audit (commit `8607d56`) to confirm the server silently drops unknown params.
- `pf_update_work_item.bogus_probe_383` — the same probe for aihub#383.

Both probes were sent exactly once. A parameter observed once, whose name
contains a wi number, is a probe, not drift. That leaves **28** candidates
unadjudicated.

The per-transcript version signal is the versioned skill path
(`polyforge/1.1.N/skills`) appearing in the transcript; the JSONL envelope
carries only the Claude Code version, never the plugin's. That signal covers
**396 of 2,088** files, and the earliest locally cached plugin (1.1.7,
2026-08-27) postdates the start of the corpus, so most of the span has **no**
version signal at all. Settling a candidate means finding the version that sent
it and that day's server schema.

"Published but never observed" (**21** params) is a plain fact about this corpus
and says nothing about correctness. A never-called tool likewise carries **no
frequency argument**: nothing here says it works.

## Unresolved

- No per-version schema dumps exist, so the **28** unexplained
  observed-but-unpublished candidates cannot be adjudicated from in-repo data
  alone. Producing a per-version dump series is separate work.
- `unclassified` still holds **1,065** of 2,756 sequence groups. The
  group-size table in `sequence-inventory.md` splits that residue and computes
  its own share, so read the number there rather than one restated here.
- The `state_file_missing` detector was hand-validated at **141/141** precision
  against the error texts that trigger it. The other 13 detectors are spelled
  out in `sequence-inventory.md` so they can be falsified, but were not
  individually precision-audited.
- `internal/render`'s three `TestSpikeArtifact_*` tests fail on this workstation,
  identically on a clean `origin/main` tree, and pass in CI. Pre-existing and
  unrelated to this work; not investigated here.
