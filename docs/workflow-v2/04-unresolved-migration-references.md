# 04 — Remaining limitations and migration references

This file began as Batch 4A's unresolved map. Batch5 resolved the callable seed/import
wiring and CI registration; the remaining gaps below are deliberate limitations, not
implied completion.

## 1. Seed/import is now explicitly wired

`internal/skillregistry/seed.go` and `import.go` are driven by
`pkg/client/skill_seed.go` through `polyforge skills seed|import`
(`internal/cli/skills.go`, registered in `cmd/polyforge/main.go`). The caller must carry an
unscoped authenticated writer/admin credential. Publication remains private, uses the
content digest plus exact expected-latest CAS, and is idempotent. It is never run as a
server startup or migration side effect. The deterministic real-DB route fixture is
`internal/server/workflow_v2_seed_smoke_db_test.go`.

## 2. No MCP surface for the skill registry

`internal/server/routes_skills.go` + `pkg/client/skills.go` expose create / publish /
list / get / share / visibility over REST; **`internal/mcp` registers none of them**. In
a session, an agent therefore cannot publish, share, or even read a skill version through
MCP today. Consequences shipped honestly:

- `pf-crystallize`'s registry path says "surface unavailable → stop" instead of teaching
  raw HTTP (IR3).
- `workflow-v2.md` points at `pf_get_workflow` (which exists) but never at skill-content
  reads (which don't exist as tools).
- **Needed**: `pf_create_skill`, `pf_publish_skill_version`, `pf_list_skill_versions`,
  `pf_get_skill_version` (+ share/visibility, admin-or-owner gated), then mcp-cards for
  each. Until then, "DB workflow is the authority for newly composed flows" is composable
  only by callers that can reach the REST surface (CLI, server internals).

## 3. No authorized post-wrap artifact path

`pf-retro` now teaches: artifacts while the attempt is live, memories after wrap. That is
honest against today's surfaces but leaves a real gap — a wrapped wi can never receive a
retro artifact, even one a human explicitly wants recorded. Options (a server-authorized
post-wrap artifact write for the reporter/maintainer, or a retro-owns-the-artifact flow
that runs strictly pre-wrap) belong to a design decision, not to skill prose.

## 4. No annotation-reply tool

The raw-curl block is gone from `pf-revise`. Without an MCP reply surface, ambiguous
feedback is now a **blocking clarification**, not an invitation to guess: the skill stops
before superseding, leaves the annotation open, emits a work-item note with a concrete
question, and waits for the reviewer to clarify in /ui. The proper fix is a
`pf_reply_commit` MCP tool over the existing reply endpoint (thread the reply through
`internal/mcp/tools_memory.go`, card it in `docs/mcp-cards/`), so the clarification can
live on the annotation thread. Until then the note is only an attention signal; it never
resolves the annotation and never counts as workflow approval.

## 5. DB-workflow cleanup and the interactive driver remain bounded limitations

- **Worktree cleanup**: drain deliberately skips automatic cleanup for pinned-workflow
  attempts. On a successful wrap it reports cleanup deferred and preserves the worktree;
  on timeout it stops the active process group before pausing, and an unconfirmed stop
  retains the running claim and locks. This is safer than applying the legacy forceful
  cleanup to dirty/unshipped/partially detached work.
- **Interactive main/session driver**: step execution now has the approved three-mode
  split through one controller. Human-facing, non-isolated authoring may use
  `--prepare` / `--submit`; automatic steps use `RunStep`; and an effective-RHS review,
  verification, shipping, or otherwise independent-producer contract is refused by the
  main producer and routed by `--continue` to that same preflighted worker path. The
  worker therefore uses the pinned model/effort and server-minted isolation grant rather
  than the main session's identity. A legacy/forged submit for such a gate returns a typed
  actionable hold before artifact or result mutation; it never echoes a producer id into
  local authority. The remaining limitation is bounded to lifecycle handoff: this
  low-level adapter does not capture claim, invoke the authenticated human approval
  endpoint, pause/wrap, or clean up on the human's behalf. Approval/refusal remains data,
  and callers must not report whole-lifecycle success until those outer seams act.
- **`pf-update-work-item` / create-with-steps as author-facing composition**: the flow
  revision tool (`pf_update_workflow`) exists; the *skill* that walks a human author
  through composing a flow (which seed skills, what order, what the policy will refuse)
  does not exist yet and should be written against the real surfaces, not ahead of them.

## 6. Licenses of the seven role bundles

`grill-me` ships MIT with upstream provenance. `spec`, `plan`, `code-change`, `review`,
`verification`, `ship`, `ci` ship **Proprietary** because the aihub repository carries no
open-source license; the bodies are condensed from GMI-authored plugin content. If the
fleet wants these shareable across organizations, that is a licensing decision for the
owner — the bundle fields make either answer a one-line change per bundle, and
`PlanSeed`'s digest idempotency means re-licensing publishes as a new version, never a
silent rewrite.

## 7. Documentation seams that intentionally lag

- `docs/mcp-tools.md` and `docs/mcp-cards/` document the workflow tools. A registry MCP
  surface is still absent; seed/import uses the authenticated CLI/client route rather than
  raw HTTP.
- `docs/superpowers/` and the scenario repo (`polyforge-coding`) are untouched; when the
  registry becomes the default for new flows, the scenario repo's role shrinks to the
  legacy path and its README should say so — deliberately not edited by this batch.
- The seeded bodies are versioned v1 content. They are exercised by deterministic
  seed/contract fixtures; opt-in real harness qualification remains environment-specific
  and must never target a live queue or global model/skill installation.
