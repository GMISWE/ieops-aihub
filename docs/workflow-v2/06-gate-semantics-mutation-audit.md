# Gate semantics and mutation audit prototype (aihub#718)

`internal/contractaudit` is a deliberately isolated, pure prototype for the
`0.2-gate-semantics-and-mutation-audit` contract. It accepts a closed audit
document plus a caller-supplied **authority boundary** and returns one deterministic,
typed audit report. It does not read a clock, file, database, registry, or
runtime state, and it never performs the mutation being audited.

## Boundary

The package's public entry points are `DecodeStrict`, `Validate`, and
`Evaluate`. `DecodeStrict` rejects exact and case-folded duplicate object
members, non-canonical JSON member casing, unknown fields (including nested
pointer objects), **every explicit `null`** (including scalar strings,
integers, booleans, and timestamps), and trailing JSON before semantic
evaluation. `Validate` checks the closed
domain (including enum values, IDs, timestamps, digest format, references,
and matrix/inventory closure). Invalid documents return `*ValidationError`
with a JSON pointer and typed `INVALID_*`, `UNKNOWN_FIELD`, or
`INVALID_DOMAIN_VALUE` code; they do not produce a partial report.

`Evaluate` is fixture-only. Its `boundary *VersionToken` and the input's
`checked_at` are injected, so freshness is deterministic and production wall
clock reads are forbidden. The package has no registry, composer, scenario,
workflow controller/runtime, server/MCP/CLI, seed, migration, or database
adapter dependency. In particular, it is neither a workflow gate nor an
execution authorization.

## Evidence, authority, and replay closure

Each `Probe.Request` is a closed `ProbeRequest` value (operation ID/class,
target, subject reference, and payload digest), never an unconstrained JSON or
Go value. Every probe is listed by exactly one matrix cell and exactly one
same-polarity designated control; evaluation processes all of them.

Effects are typed `ObservedEffect` records with target, operation ID, logical
change, and distinct pre/post digests. The effect reader has a typed selector
that must exactly bind the expected target, operation ID, cardinality, and
intended change; it cannot select an unrelated surface. Effects are checked
against the probe's operation-bound expectation: intended change, forbidden
changes, and declared cardinality. Positive controls require at least one observed effect and exactly the declared intended mutation; negative controls require a non-empty, scoped pre/post observation and never treat an empty list as proof. Every observed effect must remain within the authorized target/operation/change inventory, otherwise the result fails closed as `UNEXPECTED_MUTATION`.

Matrix coverage is per writable **operation ID + target field**, not per
operation class or tier: each status/actor/tier combination for every writable
operation surface needs its own matrix cell and explicitly bound probe. Inventory
operation schema digests must equal the authority schema digest. The start
authority, every effect reader, and the boundary must agree exactly on
reader/source/scope and every version-token member, including `observed_at`.
Reader evidence intervals must contain both that exact authority observation and
the probe execution, while the injected safe-age policy bounds the start,
probe, and boundary observations. The evaluator never reads a clock.

Replay input identity includes request ID and replay key. A prior record must
bind both values and this contract version. The output digest is calculated
only after verdict/reasons/coverage are final (with replay transport status
normalized to prevent a circular digest); serialization errors are returned.

Policy reasons and reader/source values are closed enums. Every matrix cell,
including allow cells, must declare an `expected_reason`. Precedence rules are
closed conflict declarations: the supported `state_and_permission_conflict`
requires a terminal status plus a non-reporter actor, and
`terminal_and_working_conflict` requires a terminal status plus the working tier.
Each cell's `precedence_when` must resolve to a declared rule, and both its
expected outcome and reason must equal the evaluator-derived decision. The
previously unsupported RHS/authority conflict is not part of this contract.
The evaluator compares observations to its own derived reason.

## Result interpretation

Every well-formed result has literal `publication_status: "not_publishable"`
and `side_effects: []`.

- **pass** has no reason codes and requires closed matrix and inventory
  coverage, executed controls, verified effects/non-effects, and fresh
  authority. `PASS_COMPLETE` is the documented pass basis, not an emitted
  unresolved reason.
- **fail** records a concrete contradiction such as `POLICY_MISMATCH`,
  `ERROR_KIND_MISMATCH`, `UNEXPECTED_MUTATION`, or `FORBIDDEN_MUTATION`.
  Failure wins over a concurrent evidence hold.
- **hold** is fail-closed for incomplete coverage, skipped/vacuous probes,
  unclassified writable surfaces, insufficient effect evidence, stale or
  changed authority, and replay mismatches.

Replay is also pure: an identical prior input/output identity is reported as
`identical_replay`; a changed input or output is a hold unless an independent
failure already exists, in which case the failure is preserved. Replay checks
run before final verdict/coverage calculation, and the canonical final output
digest is bound only after all replay-dependent reasons and coverage are fixed.
Skipped probes are held, but any effects they carry are still audited and can
fail the report; skipped status never evades effect accounting.

## Legacy workflow evidence

`steps_version=0` remains an explicit fact in output. A complete independent
fixture matrix may pass while reporting `steps_version: 0` and
`step_history_available: false`. Any claim requiring workflow step history
instead holds with `EVIDENCE_NOT_AUTHORITATIVE`; wrapped state, prose, memory,
or a supplied step ID cannot synthesize that evidence.

## Test status

| catalog | status | evidence |
|---|---|---|
| malformed JSON/domain, per-operation matrix/inventory closure | always-on unit | strict decoder, explicit-null rejection, typed validation errors, and same-class second-operation escape regression |
| authority/reader/effect/precedence closure | always-on unit | exact `observed_at` token regression, structurally bound reader selector, evaluator-derived conflict winner |
| skipped controls, stale/changed boundary, replay, legacy zero-step | always-on unit | injected tokens and timestamps |
| DB P2 / DB N2 cardinality checks | U/hold | intentionally absent until a uniquely owned ephemeral DB preflight can be proved |

No optional DB reader, fallback DSN, production connection, or DB mutation is
included in this prototype. An unavailable or unsafe DB is evidence for a
named hold, never a pass.
