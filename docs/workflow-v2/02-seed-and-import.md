# 02 — The seed set and the import path

aihub#708 Batch 4A. The code: `internal/skillregistry/seed.go` (the set + the idempotent
plan) and `internal/skillregistry/import.go` (directory tree → closed bundle). Pure
package, no SQL, no network, no clock — publication is `CreateSkill` +
`PublishSkillVersion` in `internal/domain`, under the caller's authorization, **private by
default**.

## The canonical seed set

Eight skills, each a closed, versioned bundle (entry `SKILL.md`) plus a runtime capability
contract. Names are registry-legal (`^[a-z][a-z0-9-]{0,63}$`); the task vocabulary's
underscored names map to kebab-case — **code_change → code-change**. That mapping is the
one spelling decision this batch made; it is recorded here because the registry's CHECK
is the enforcement and underscores are refused.

| name | capabilities | interactive | role in a flow |
|---|---|---|---|
| `grill-me` | authoring | **true** | the interview: interrogate a goal until it is sharp. Interactive-only → a `requires_human_session=false` flow cannot pin it (the server refuses at pin time) |
| `spec` | authoring | false | the OpenSpec-grammar spec (same grammar as `methodology.spec` artifacts, so /ui annotations and /pf-revise keep working) |
| `plan` | authoring | false | the ordered plan with `Touched files:` lines, so declared-resources derivation stays possible |
| `code-change` | authoring | false | the WRITE producer: implements the plan; the artifact the gates inspect |
| `review` | review | false | the independent review gate (pass/warn/fail + blockers) |
| `verification` | verification | false | the deterministic-checks gate (pass/fail/**skip** — a skip is never a pass) |
| `ship` | shipping | false | commit/push/PR through the work-item-gated tools |
| `ci` | verification | false | the post-ship CI watcher (pass/fail/**pending** on timeout) |

Contract shapes live in the code (input/params/output schemas inside the fail-closed JSON
Schema subset; `SeedSet()` re-validates every bundle and contract on every call, so a
future edit that breaks the closed rules fails at the seed, not at the first publish).
Gate/producer separation is structural: `review`/`verification`/`ci` declare gate
capabilities only, `code-change`/`ship` declare producer capabilities only — the registry
refuses a contract that mixes them, and the server derives grants from exactly these
values (`review`/`verification` → read_only + independent producer; `shipping`/`authoring`
→ write + shared).

### Licenses and provenance (MIT where applicable)

Every seed bundle carries `license` (required, even when the answer is "Proprietary") and
`provenance`:

- **`grill-me`** — `MIT`, with upstream provenance pinned to the ACTUAL upstream:
  **[mattpocock/skills](https://github.com/mattpocock/skills)** at commit
  `3cca18b368ae95cdbdebbff572ccafa662551015` (v1.2.3 — the copy this fleet has installed in
  its plugin cache), whose `grill-me`/`grilling` skills are where the seed takes both its
  NAME and its relentless-interview pattern from. The body is a fresh polyforge
  composition adapted from that upstream, not a byte-for-byte import — and because adapted
  MIT content must preserve the upstream notice, `license.notice` carries the upstream's own
  MIT text **verbatim, `Copyright (c) 2026 Matt Pocock` line included** (read from the
  installed copy's LICENSE, not transcribed from memory). An earlier revision of this
  seed misattributed the upstream to `obra/superpowers` and stamped a GMI copyright line on
  the notice — wrong on both counts (the installed `obra/superpowers` has no grill family at
  all), corrected here by measurement. The full notice rides in the bundle, so it is
  self-describing wherever it is shared (spec D2: preserve the upstream notice).
- **The seven role bundles** (`spec`, `plan`, `code-change`, `review`, `verification`,
  `ship`, `ci`) — `Proprietary` (GMI Engineering), provenance `source` pointing at the
  local plugin paths they were condensed from (`plugins/polyforge/skills/...`,
  scenario step graphs). The aihub repository ships no open-source license file, so
  Proprietary is the honest label; whether GMI re-licenses these is an open question
  recorded in `04-unresolved-migration-references.md`, not a decision this batch makes.

### Private by default; sharing is a separate, explicit act

`PublishSkillVersion` always inserts `visibility='private'`; the seed plan never flips a
visibility and never creates a grant. Making a seeded version reachable by others is the
owner's explicit act (`ShareSkillVersionWithProject` / `SetSkillVersionVisibility`, per
exact version, revocable, re-checked on every read). The seed bodies quote
worktree-derived content patterns — treat them as confidential until the owner decides
otherwise.

### Idempotency is content-addressed

`PlanSeed(existing)` compares the seed entries' content digests
(`VersionDigest` = sha256 over canonical bundle + contract bytes) against the digests of
the versions the caller already has, and returns one action per skill. The input map's key
PRESENCE is itself an input: an **absent** key means the identity does not exist; a
**present** key with an empty version list means the identity exists but nothing is
published yet.

- `create` — no such skill identity: create it, then publish v1 (`expected_latest=0`);
- `publish` — the content is not published yet: publish the next version under the
  current `latest_version` as the CAS token — which is `0` for an existing identity with
  no versions (CreateSkill would refuse that identity as a duplicate; publishing v1 is
  what it needs) and `N` for a skill with versions but no digest match (latest+1);
- `present` — a version with the seed digest already exists: **do nothing**; pin the
  matching version (the highest, so identical re-publishes degrade to the newest copy).
  Re-running the seed over an unchanged registry is a no-op, not a duplicate-publish storm.
  "A skill named `spec` exists" is *not* evidence the seed content is published — only the
  digest is.

### No silent latest fallback for pinned new flows

Every `SeedAction.Version` is an exact pin (> 0). Flows composed from the seed pin those
exact versions. `skill_version: 0` — "the caller's latest accessible" — is a pin-time
convenience for human authors: the server resolves it inside the author's transaction and
freezes the result into the generation. It is never a fallback a running flow silently
re-resolves: a generation stores exact pins, results bind the generation, and revisions
are new generations with their own CAS token. The seed, the skill prose and this
documentation all teach the same rule.

### Input/output schemas and compose references

Each seed contract carries its authored-configuration inputs (`params_schema`), its
predecessor-result inputs (`input_schema`), and its output (`output_schema`) — all inside
the fail-closed JSON Schema subset, validated on every `SeedSet()` call. The table mirrors
the contracts' own names (the code is the authority); which producer output feeds which
consumer input — the typed wiring a composed step must reference — is the input-references
table under "The recommended composition" below.

| skill | params (authored) | inputs (from predecessors) | output |
|---|---|---|---|
| `grill-me` | `topic` (required), `context` | — | `record`, `distilled_requirements`, `open_questions` |
| `spec` | `goal` (required), `mode` (feature\|debug) | `interview_requirements`, `interview_open_questions` (both optional — an unattended flow omits them with `grill-me`) | `spec`, `requirements[]` |
| `plan` | — | `spec` (required) | `plan`, `steps[]` (id + `touched_files`) |
| `code-change` | — | `plan` (required) | `summary`, `changed_files[]` |
| `review` | `scope` | `spec`, `plan`, `changed_files` (all required) | `verdict` (pass\|warn\|fail), `summary`, `blockers[]` |
| `verification` | `base_ref` (required), `commands[]` (required; objects: `name`, `command`, `working_directory`) | `changed_files` (required) | `summary`, `checks[]` (`name`/`outcome`/`detail`) |
| `ship` | `message`, `pr_title`, `pr_body` (all required) | `changed_files`, `review_verdict`, `verification_checks` (all required) | `pr_url`, `pr_number`, `commits[]` |
| `ci` | — | `pr_url` (required) | `summary`, `checks[]` (pass\|fail\|pending\|skip) |

`steps[].touched_files` (plan's output) is the file set a later reader can derive
resource locks from; a skip in `verification.checks` is never a pass, and `ship` refuses on
a `fail` verdict, a `fail`/`skip` check, or a claimed path missing from `changed_files`.


### The recommended composition (docs, deliberately not code)

A flow is per-work-item state, not registry state — so the seed ships skills, **not a flow
template**; a template inside the seed would be a second, weaker place where composition
rules live. The composition the seed set was shaped for, for a `requires_human_session`
wi (the schema table above separates each step's authored params from its predecessor
inputs; the typed arrows are the input references below):

```
grill-me (interactive; rhs) → spec → plan → code-change → review → verification → ship → ci
```

That arrow is typed data, not narrative shorthand. The composition uses these explicit
input references (the consumer property schema is byte-semantically identical after
canonicalization to the named producer output property schema, including constraints):

| consumer input | producer output |
|---|---|
| `spec.interview_requirements` / `spec.interview_open_questions` | `grill-me.distilled_requirements` / `grill-me.open_questions` |
| `plan.spec` | `spec.spec` |
| `code-change.plan` | `plan.plan` |
| `review.spec` / `review.plan` / `review.changed_files` | `spec.spec` / `plan.plan` / `code-change.changed_files` |
| `verification.changed_files` | `code-change.changed_files` |
| `ship.changed_files` / `ship.review_verdict` / `ship.verification_checks` | `code-change.changed_files` / `review.verdict` / `verification.checks` |
| `ci.pr_url` | `ship.pr_url` |

`goal`, `mode`, review `scope`, verification `base_ref` + command objects, and ship's
message/PR fields are invocation params because they are authored configuration, not
predecessor results. Every seeded body reads predecessor values from `inputs.<name>` and
configuration from `params.<name>`; it does not smuggle a prior step's output through
params. `spec.interview_requirements` and `spec.interview_open_questions` are optional so
the unattended composition can omit both `grill-me` and those inputs. All other table
entries are required by their consumer.
A composed step must list the corresponding `{name, step_id, output}` references; merely
putting these skills in order does not materialize inputs.

Verification command params are objects with `name`, `command`, and
`working_directory`, plus an explicit `base_ref`. Its result `checks` always carries
`name`, `outcome`, and `detail`, which is the exact shape consumed by `ship`.

Server policy already encodes the load-bearing parts of that shape: a shipping step must
be preceded by distinct review and verification gates after the latest write
(`validateShippingEventOrder`); gates must not share a producer with the writes they gate
(producer independence); an interactive-only step cannot be pinned on an unattended wi.
For an unattended wi, drop `grill-me` (the server refuses it) and let the human approve
out-of-band only where a step is marked rhs — which an unattended wi cannot have either,
so approval waits for a human session by construction.

## The import path (tree → closed bundle)

`BuildImportedBundle(root, ImportOptions{Entry, License, Provenance})` is how a body that
already exists as files enters the same closed shape:

- walks `root`, takes every **regular** file (symlinks, special files and `.git` segments
  are refused by name), validates each path with `ValidateBundlePath`, sorts by path;
- text stays UTF-8, non-UTF-8 bytes travel base64 — the two encodings a bundle accepts;
- license is required (an unlabelled bundle is a redistribution hazard the registry
  cannot audit later); MIT content carries the upstream notice in `license.notice`;
- **does not stamp the clock**: `ImportedAt` is the caller's to set. An import that
  stamped its own timestamp could never be idempotent — re-importing an unchanged tree
  would produce different bytes and a different digest. Leaving time out is what makes
  "import again" safely a no-op (`ContentDigest` over the result, compared against the
  existing version digests — the same comparison `PlanSeed` runs for the seed).

## How the seed gets applied

After applying migrations 0043 and 0044, an unscoped writer/admin runs:

```text
polyforge skills seed
```

The CLI's authenticated `*client.Client` implements `SeedStore`; it lists the caller's
accessible identities and versions, creates missing identities, and publishes only new
content with the exact expected-latest CAS and digest. Every response is checked to remain
`private`. The JSON result reports `create`, `publish`, or `present` and the exact pin for
each skill. Running it again against unchanged content returns eight `present` entries and
publishes nothing. A project-scoped key is refused by the server; there is no anonymous,
startup, migration-hook, or raw-HTTP fallback.

For a local closed-bundle import, supply the runtime contract and licensing explicitly:

```text
polyforge skills import --name=my-skill --root=./skill --entry=SKILL.md \
  --contract-file=contract.json --license-name=Proprietary \
  --provenance-source=team/skill
```

Optional license notice/upstream flags are documented by `polyforge help`. Import walks
only the named local directory, refuses symlinks and `.git`, does not stamp a clock, and
uses the same digest idempotency/private CAS route as the canonical seed.

The implementation layers are:

- **`SeedStore`** (`seed.go`) — the narrow authenticated port: read one skill's
  id/versions/existence, create an identity, publish the next version under the
  expected-latest CAS with a digest the store verifies. The controller or CLI implements
  it over `pkg/client` (`CreateSkill`, `PublishSkillVersion`, the skill read) with a
  credential that satisfies the registry's own write rules (authenticated writer/admin,
  NOT project-scoped). There is deliberately **no anonymous mode**: a nil store is an
  error, never a degraded run, and the port cannot express a visibility flip or a share.
- **`ApplySeed(ctx, store)`** — plans (PlanSeed) then performs, deterministically, in plan
  order; every publish carries the exact CAS token and the seed digest; re-running over a
  seeded registry publishes nothing (all-present with the same pins). Partial results are
  returned alongside the first store error.
- **`ApplyImport(ctx, store, name, root, opts, contract)`** (`import.go`) — the import
  half through the same port: build the closed bundle, digest it with the caller's
  contract, publish only when the content is new. The contract is REQUIRED — a version
  without one cannot be pinned into a flow, and inventing capabilities here would be a
  silent lie.

Both operations are pinned by `internal/skillregistry/seed_test.go` /
`import_test.go`; the real HTTP/client/migration smoke is
`internal/server/workflow_v2_seed_smoke_db_test.go`, and CI runs it with an explicit
`AIHUB_TEST_DB` while rejecting any SKIP. No server startup path invokes the seed.
