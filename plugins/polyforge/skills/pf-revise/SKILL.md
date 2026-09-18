---
name: pf-revise
description: >
  Use when a human reviewer has left open annotations on a spec or plan artifact in
  the /ui viewer and wants the agent to apply that feedback and resolve the annotations.
---

# pf-revise - Annotation-Driven Artifact Revision

## Usage

**Purpose**: Read open annotations (commit entries) on the current wi's head spec/plan artifact(s), revise the artifact content to address each annotation, supersede a new version, and resolve every addressed annotation with a per-annotation reply via `pf_resolve_commit`.

**Pattern**: `/pf-revise`

**Required**: a currently-claimed wi (state file present); the head spec or plan artifact must have at least one open annotation written by a human reviewer in the /ui artifact viewer.

**Flags**: none

## When to use

- After a human reviewer has added section annotations to a spec or plan artifact in the
  polyforge /ui viewer and wants the agent to act on the feedback.
- Triggered by "revise the spec/plan" / "edit per annotations" / "handle the annotations" / "resolve the review comments"
  / "revise per annotations" / "handle the review" / "apply reviewer feedback".
- The reviewer does **not** have to mark the wi or claim anything - `/pf-revise` is always
  run by the agent inside the claimed wi session.
- After `/pf-revise` completes, the reviewer may annotate the NEW head version to start
  another round; this is the standard multi-round review loop.

## Mechanic

### Step 1: Memory-First recall

```
pf_recall(
  project=<current>,
  query=<wi.goal + " annotation revision">,
  type=["experience.*", "rule.*"],
  top_k=3,
  fields="brief"
)
```

Display relevant memories; activate those the LLM judges as useful.

`fields="brief"` (aihub#313): display-and-activate only. The spec/plan reads in Step 3 pass
no `fields` on purpose - those DO consume `content` + `commits`.

### Step 2: Mark step in_progress

```
wi_info = pf_get_step(work_item_id=<current>)
current_step_id = wi_info.current_step or "revise"

sa_id = new_ulid()
pf_update_step(
  work_item_id=<current>,
  step_id=current_step_id,
  status="in_progress",
  step_attempt_id=sa_id
)
```

The `pf_get_step` here stays - this skill genuinely consumes `current_step`. What is gone is
`expected_version`: the server never bound it (aihub#290), so passing it did nothing. Where a
step bracket needs only the version, drop the `pf_get_step` entirely.

The `step_attempt_id` is client-generated - `pf_update_step` returns only the step status (plus `next_step` when the call was a fused advance), so generate the id yourself and pass it back on the completing call.

### Step 3: Load head artifact(s)

Fetch the active (non-superseded) head of each artifact type for this wi:

```
spec_results = pf_recall(
  project=<current>,
  work_item_id=<current>,
  type=["methodology.spec"],
  top_k=1
)

plan_results = pf_recall(
  project=<current>,
  work_item_id=<current>,
  type=["methodology.plan"],
  top_k=1
)
```

The first (and only) entry returned for each type is the current head (pf_recall returns
the most-recent non-superseded version). Record: `head_spec_id`, `head_plan_id`, and
their `content` + `commits` arrays.

These two calls must NOT pass `fields="brief"` (aihub#313): brief replaces the body with
its first line and drops `commits`, and this step revises the artifact from exactly those
two. Note the body you get here is still capped at 800 runes by the server, so for a long
spec the honest read is `pf_get_memory(head_spec_id)` - check `content_truncated`.

### Step 4: Identify open annotations

Filter each artifact's `commits` array to OPEN entries only:

- An entry is **open** when `entry.status` is absent, `null`, or `"open"`.
- An entry is **resolved** when `entry.status == "resolved"`.

Each open entry has:
- `id` - commit id (pass to `pf_resolve_commit` as `commit_id`)
- `anchor.heading_id` + `anchor.heading_text` - nearest enclosing heading at annotation time
- `anchor.quote` *(optional)* - exact selected text (W3C TextQuoteSelector style, ≤2000 chars)
- `anchor.prefix` / `anchor.suffix` *(optional)* - up to 64 chars of context on each side (for disambiguating multiple matches of `quote`)
- `replies[]` *(optional)* - threaded replies already posted on this commit entry (`{id, author_display, body, created_at}`)
- `body` - the reviewer's requested change

**Anchor resolution - priority order** (apply the first rule that resolves):

a. **`anchor.quote` present** -> search the CURRENT head document text for `quote`
   (exact substring match). If `prefix`/`suffix` are present and the quote appears more
   than once, select the occurrence whose surrounding context best matches them. Apply
   feedback to that exact region.

b. **`quote` absent or not found in current text** -> fall back to `anchor.heading_text`
   section matching (existing behavior: locate the section by heading text and apply
   feedback there).

c. **Neither resolves** -> treat as document-level; apply as best you can; note in the
   resolve reply: "anchor no longer locatable in current version; applied at document level".

**Legacy compatibility**: entries that carry only `heading_id`/`heading_text` (no
`anchor.quote`) follow rule (b) exactly as before. Entries with no anchor at all go
straight to (c). No behavioral change for either legacy shape.

**Edge case - no open annotations found**: print a Result block stating "No open
annotations on the current head spec/plan - nothing to revise." then mark step completed
and stop. Do NOT create a spurious new artifact version.

**Edge case - anchor not resolvable** (quote changed AND heading gone, or no anchor):
covered by rule (c) above - apply at document level; note in the resolve reply:
"anchor no longer locatable in current version; applied at document level".

**Ambiguity preflight**: before Step 5, decide whether every open annotation's requested
change is concrete enough to apply safely. If any annotation body needs a human decision,
follow Step 7a immediately. Do not partially revise or supersede either artifact first.

### Step 5: Apply the revision rule (spec -> plan coupling)

> **IMPORTANT**: annotations on a PLAN change ONLY the plan artifact. Annotations on a
> SPEC change the spec AND require re-deriving / re-saving the plan (because spec drives
> plan).

**Case A - only plan annotations**: revise and supersede the plan artifact only.

**Case B - only spec annotations**: revise and supersede the spec artifact, then
re-derive the full plan from the updated spec and supersede the plan artifact too (even
if the plan had no direct annotations).

**Case C - both spec and plan annotations (same round)**:
1. Revise and supersede the spec (applying spec annotations).
2. Re-derive the plan from the updated spec.
3. Apply any plan-only annotations on top of the re-derived plan.
4. Supersede the plan with the final merged content.

This ordering ensures spec authority is preserved even when both are annotated in one
round.

### Step 6: Write the revised artifact(s)

One review round = one supersede per artifact. Collect ALL open annotations on each
artifact and apply them together; do not supersede multiple times in one `/pf-revise` run.

**Revise the spec (if spec annotations exist)**:

```
new_spec_id = pf_save_artifact(
  type="methodology.spec",
  work_item_id=<current>,
  content=<revised full spec markdown - complete document, not a diff>,
  supersedes_memory_id=<head_spec_id>,
  visibility="project"
)
```

**Revise / re-derive the plan (if spec annotations exist OR plan annotations exist)**:

```
new_plan_id = pf_save_artifact(
  type="methodology.plan",
  work_item_id=<current>,
  content=<revised full plan markdown - complete document, not a diff>,
  supersedes_memory_id=<head_plan_id>,
  visibility="project"
)
```

`supersedes_memory_id` archives the old head and makes the new one the active head.

### Step 7: Resolve each open annotation

For EACH open annotation addressed in this round, call `pf_resolve_commit` targeting the
artifact ID that CARRIED the annotation (i.e. the OLD head - the one being superseded,
not the newly created version):

```
# For each open annotation on the spec:
pf_resolve_commit(
  memory_id=<head_spec_id>,   # <- the OLD head that had the annotation
  commit_id=<entry.id>,
  reply="<1-2 sentences describing exactly how the revision addressed this request>"
)

# For each open annotation on the plan:
pf_resolve_commit(
  memory_id=<head_plan_id>,   # <- the OLD plan head
  commit_id=<entry.id>,
  reply="<1-2 sentences describing the plan change>"
)
```

**Why the old head?** The annotation lives on the memory entry that was annotated.
After `pf_save_artifact(..., supersedes_memory_id=<old_id>)`, the old head is archived
but remains addressable by id. `pf_resolve_commit` targets the memory by id, so resolve
on the OLD head id. The resolved status is preserved on the archived entry and the UI
will reflect it correctly.

Every open annotation must be concrete and addressed before this call proceeds; then every
one is resolved. There is NO carry-forward after superseding: if the reviewer still
disagrees with a resolution, they annotate the NEW version and trigger another
`/pf-revise` round. Ambiguous feedback never reaches this point; Step 7a leaves it open on
the unchanged current head.

### Step 7a: Ambiguous feedback - leave it open and ask

If a commit entry's `body` is ambiguous and you cannot safely make the requested change
without human clarification, **do not guess, do not revise from that annotation, and do
not resolve it**. There is no MCP surface for a threaded annotation reply today. The raw
HTTP endpoint is NOT a sanctioned path (IR3 forbids raw-HTTP fallbacks; a
`pf_reply_commit` tool does not exist yet - tracked in
`docs/workflow-v2/04-unresolved-migration-references.md`).

Treat clarification as a blocking human decision:

1. Stop before superseding either artifact in this round. This keeps the ambiguous
   annotation open on the current head instead of archiving it on an old version or
   mixing a partial revision with an unresolved request.
2. Emit a `note` event that quotes the annotation id, states exactly what is ambiguous,
   and asks the reviewer a concrete question with the plausible choices:
   ```
   pf_emit_event(
     work_item_id=<current>,
     event_type="note",
     payload={
       "text": "annotation <entry.id> needs clarification: <question>; please clarify in /ui"
     }
   )
   ```
3. Report the revision step as blocked on that clarification. Leave the annotation open
   and do not mark the step completed. After the reviewer clarifies in /ui, rerun
   `/pf-revise` against the still-current head.

A note is only an attention signal; it is not an annotation reply, resolution, or
approval. Never use raw HTTP to fill the missing reply surface.

### Annotations are not approvals (aihub#708)

An annotation (commit entry) is DOCUMENT feedback on an artifact. A workflow APPROVAL is a
different, human-only act: `pf_approve_workflow` records an authenticated human decision
bound to a step's exact artifact on a pinned DB workflow, and a machine credential is
refused whatever role it holds. Resolving annotations NEVER approves a step; approving a
step NEVER resolves annotations. `/pf-revise` touches only annotations.


### Step 8: Emit revision note

```
pf_emit_event(
  work_item_id=<current>,
  event_type="note",
  payload={
    "text": "revision round complete",
    "spec_superseded": "<head_spec_id> -> <new_spec_id>",  // omit if no spec change
    "plan_superseded": "<head_plan_id> -> <new_plan_id>",  // omit if no plan change
    "annotations_resolved": <count>
  }
)
```

### Step 9: Mark step completed

```
pf_update_step(
  work_item_id=<current>,
  step_id=current_step_id,
  status="completed",
  step_attempt_id=sa_id,
  artifact_summary="revised <spec|plan|spec+plan>: <N> annotations resolved"
)
```

### Step 10: Output three-segment format

"Next steps" follows the Post-claim Routing table for the current `wi_type`. That table is
**not** in your session context - `Read` `fragments/post-claim-routing.md` under the
`using-polyforge` skill directory first (it is on-demand by design; see
`using-polyforge/references/manifest-notes.md`). Append these revision-specific additions after the
table-derived rows:

- If the spec was revised: offer `/pf-plan` (if the plan was not auto-re-derived or
  needs manual reconcile) and offer another review round ("reviewer may annotate the new
  version in /ui to start another round").
- If only the plan was revised: suggest `/pf-execute` to begin execution on the updated plan.
- Always note: "reviewer may annotate the new version in /ui to request another revision round."

## Edge Cases (summary)

| Situation | Behavior |
|-----------|----------|
| No open annotations on either head | Report "nothing to revise", stop without creating new artifact version |
| `anchor.quote` present but not found in current text | Fall back to heading-text match (rule b); if also absent, apply at document level (rule c) |
| Annotation anchor not resolvable (quote + heading both gone, or no anchor) | Apply at document level; note in resolve reply (rule c) |
| Feedback is ambiguous - agent needs clarification | Step 7a: stop before superseding, leave the annotation open, emit a note with a concrete clarification question, and report the step blocked; never guess, resolve, or use raw HTTP |
| Only plan annotated | Revise plan only; spec untouched |
| Only spec annotated | Revise spec + re-derive plan (spec drives plan) |
| Both annotated in one round | Revise spec -> re-derive plan -> apply plan-only annotations; one supersede per artifact |
| No head plan exists yet | If spec revised, run `/pf-plan` flow to create initial plan rather than supersede a non-existent one |

## NL Triggers

- "revise the spec" / "revise the plan" / "edit per annotations" / "revise the artifact"
- "handle the annotations" / "address the review comments" / "apply the annotations"
- "revise per annotations" / "revise per review" / "act on the review comments"
- "resolve the review comments" / "handle reviewer feedback"
- "are the annotations addressed?" / "is it fixed yet?"
- After a review round: "reviewer has annotated the new version, please revise again"
