# 03 — Contradictions corrected in the shipped skill prose

aihub#708 Batch 4A. Seven shipped instructions or seed contracts said things the platform
does not do, conflated things the platform keeps apart, or described a composition that
could not pass its own typed-input validation. Each fix below names the tool-surface truth
the prose now matches. Every correction is additive where compatibility allows: the
legacy behavior is still described, correctly this time.

## 1. pf-stop `--pause`: locks are RELEASED, not kept

**Was** (flags list + pause mechanic): "`--pause` - release lease, keep locks"; the
mechanic's step 2 said "Release lease (keep locks so no one else can claim the same
resources)".

**Truth**: pausing — via `pf_pause_attempt` or `pf_complete_attempt(status="paused")` —
ends the attempt and **releases file_scope locks** (the whole derived set; only
non-file_scope lock types would be retained, and since aihub#416 the server derives only
file_scope, so normally *everything* is released). The state file is kept for
`/pf-work <slug> --resume`; on resume the claim re-derives file_scope locks from
`declared_resources`, so a concurrent claimant may legitimately hold one by then — a lock
conflict on resume is resolved by coordination or waiting, never by force. "Keep locks so
nobody else can claim the resources" was precisely backwards: it taught agents to expect
protection that does not exist.

**Now**: the flag line and the mechanic state the release, the resume re-derivation, and
the conflict expectation. (`skills/pf-stop/SKILL.md`)

## 2. pf-retro: before the terminal credentials, or explicitly-degraded post-wrap — never a re-claim

**Was**: "Required: a recently-wrapped wi"; a NOTE taught option (a) "re-claim the wi
first: `pf_claim_work_item(...)`" to make `pf_save_artifact` work after wrap.
`/pf-stop --wrap` even suggested retro *after* the wrap call.

**Truth**: `pf_wrap` / `pf_complete_attempt(wrapped|failed)` **delete the state file** —
and a wrapped wi is terminal; a claim against it is refused (the claimable states are
queued/paused/blocked-adjacent, not terminal). The taught path could never work: it sent
every standalone retro down a 409. `pf_save_artifact` needs live attempt credentials;
`pf_remember`, `pf_read_events` and `pf_list_work_items` do not.

**Now**: retro's preferred placement is **before** the terminal call (artifacts saveable);
standalone post-wrap retro is **memory-only by design** (analysis + `pf_remember`), and
the prose says so instead of teaching a re-claim. `/pf-stop --wrap`'s suggestion now
carries the timing note. A genuinely authorized post-wrap artifact path does not exist
yet — tracked in `04-unresolved-migration-references.md` §3, not papered over.
(`skills/pf-retro/SKILL.md`, `skills/pf-stop/SKILL.md`)

## 3. pf-revise: no raw curl; an approval is not an annotation

**Was**: Step 7a taught posting a clarifying reply with a raw `curl` to
`/v1/memories/{id}/commit/{cid}/reply`, using `POLYFORGE_API_KEY` from the environment.

**Truth**: IR3 — the workspace's own iron rule — forbids raw-HTTP fallbacks; and there is
**no MCP tool for a threaded annotation reply** (`pf_resolve_commit` is resolve-only).
The only sanctioned surface an agent has for that endpoint does not exist. Separately,
"resolve the review comments" language let two different human acts blur: document
feedback (annotations) and workflow approval.

**Now**: Step 7a leaves the annotation open, stops before superseding either artifact,
emits a `note` with the annotation id and a concrete clarification question, and reports
the revision step blocked. It does **not** guess, revise, resolve, or mark the step
complete. The note is only an attention signal; the reviewer clarifies in /ui and reruns
`/pf-revise`. A separate section pins the distinction: an **annotation** is document
feedback resolved by `pf_resolve_commit`; an **approval** is a human-only, authenticated
decision on a step's exact artifact (`pf_approve_workflow`, machine credentials refused
whatever their role, no actor parameter). Resolving annotations never approves a step;
approving never resolves annotations. The missing `pf_reply_commit` surface is recorded
in `04-unresolved-migration-references.md` §4.
(`skills/pf-revise/SKILL.md`)

## 4. pf-spec: the debug variant and the wi_type routing agree

**Was**: "Required: a currently-claimed `feature` / `critical_bug` wi" + "Flags: none" —
while the resident routing table (`fragments/post-claim-routing.md`) sends `fix_bug` wis
to `/pf-spec --debug` when the root cause is unclear, and spells the invocation with a
flag the skill said does not exist.

**Truth**: the routing table is the authority for which wi_type routes where: `fix_bug`
→ primary `/pf-execute`, alternate `/pf-spec --debug` (root cause unclear);
`critical_bug` → primary `/pf-spec --debug`; `feature` → alternate `/pf-spec` (scope
unclear). A `fix_bug` wi running `/pf-spec --debug` is legal and expected; the old
"Required" line made it look like a contract violation, and "Flags: none" contradicted
every `--debug` spelling around it.

**Now**: the skill declares `Pattern: /pf-spec [--debug]`, documents `--debug` as the
debug variant (violated-Requirement-first grammar, unchanged), and lists the per-wi_type
expectations with the routing table named as authority. A step-bracket honesty note was
added: if the wi's graph names the phase something other than `spec`, bracket THAT step
id (`pf_get_step` → `current_step`) — a hardcoded id that is not the graph's is a silent
no-op bracket. (`skills/pf-spec/SKILL.md`; the same note went into pf-plan)

## 5. pf-crystallize: DB publication with privacy, not a scenario-file PR

**Was**: the whole mechanic wrote `{wi_type}[.{project}].md` into polyforge-coding and
shipped it by PR — presenting the scenario file as *the* product of crystallization.

**Truth (post-aihub#708)**: the durable, permissioned home for reusable step bodies is
the **skill registry**: closed, versioned bundles with license + provenance, private by
default, shared explicitly per exact version. Scenario files remain the legacy fleet
mechanism — and crystallizing "the same flow" into both unasked would duplicate the flow
in two authorities.

**Now**: the primary product is registry publication (author per-step bodies, declare
contracts from the step's role, publish under the expected-latest CAS with a
content_digest, idempotent by digest comparison — a match means pin, don't republish),
with privacy as the default and sharing never performed by the skill itself; the
recommended composition is recorded as data (exact `(skill_id, version)` pins) because
flows are per-work-item generations, composed explicitly via `pf_update_workflow` — no
automatic fallback re-pins "the" flow for a wi_type. The scenario-file branch is kept,
labeled LEGACY, and the skill says to ask which product the user wants rather than write
both. The registry's MCP write surface does not exist yet, so the skill says so and stops
rather than falling back to raw HTTP — the gap is tracked in
`04-unresolved-migration-references.md` §2. (`skills/pf-crystallize/SKILL.md`)

## 6. Seed composition: predecessor results are typed inputs, not params

**Was**: the recommended chain ordered `grill-me → spec → plan → code-change → review →
verification → ship → ci`, but the contracts declared only params/output schemas. `plan`
and `code-change` read predecessor artifacts from params, while review, verification,
ship and CI had no compatible input properties at all. Ordering alone does not pass a
value; any honest `{name, step_id, output}` wiring failed because the consumer's
`InputSchema` was missing.

**Truth**: composition validation resolves each input against a named producer output and
a named consumer input, then requires those two property schemas to canonicalize
identically. Params are authored invocation configuration; predecessor results are
inputs. A runnable chain therefore needs explicit, matching properties at every arrow.

**Now**: every cross-step value in the recommended composition has a matching
input/output property pair, and every body reads it from `inputs.<name>`. Goal/mode,
review scope, verification commands/base, and shipping message/PR copy remain params.
The exact wiring table is in `02-seed-and-import.md`; the optional interview input lets
an unattended flow omit `grill-me`, while the remaining consumer inputs are required.
(`internal/skillregistry/seed.go`, `docs/workflow-v2/02-seed-and-import.md`)

## 7. Verification: pipelines and incomplete file selection cannot look green

**Was**: the seeded verification body said only to run `params.commands` and record an
exit status. It did not require `pipefail`, did not define how tracked plus untracked
changes become the verification scope, and had no negative control proving that status
capture detects a failing pipeline. A last-command success or a test selector that
omitted an untracked package could therefore be reported as a pass.

**Truth**: evidence is trustworthy only when the shell preserves pipeline failures and
captures `$?` before any log-processing command. Scope is complete only when it combines
the tracked diff from an explicit merge base with ignored-rule-respecting untracked
files, then demonstrates that every changed package/directory is covered. A control that
must fail distinguishes real capture from a harness that always returns success.

**Now**: verification requires an explicit `base_ref`; NUL-safe
`git diff --name-only -z "$base" --` plus
`git ls-files --others --exclude-standard -z`; reconciliation against the producer's
`inputs.changed_files`; package coverage including deletions and untracked files; Go's
concrete `GOWORK=off go test ./...` broad check (or enumerated affected packages); and
positive/negative `bash -o pipefail` controls. Each supplied command has an explicit name,
command, and working directory, executes with `bash -o pipefail`, and records status
before reading the log. Empty selection is not silently green.
(`internal/skillregistry/seed.go`, `docs/workflow-v2/02-seed-and-import.md`)

## Non-corrections, on purpose

- **pf-release** stays the inert truth (aihub#448's unpublishing, the dead `since=`
  filter, `scenario=release` being unreachable) — untouched.
- **post-claim-dispatch.md / post-claim-routing.md**: not edited. The resident dispatch
  fragment is pinned by its own gates, and "a claim always walks the step graph" reads
  correctly as "the graph the wi actually has" — the stored-flow reading is stated once,
  in `fragments/workflow-v2.md`, instead of rewording a pinned rule.
