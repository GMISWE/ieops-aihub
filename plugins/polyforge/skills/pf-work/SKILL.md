---
name: pf-work
description: >
  Use when the user wants to begin working on something: start a new work item, claim
  a queued one, resume a paused one, or take over a stalled or idle one from another
  agent.
---

# pf-work — Work Item Lifecycle Entry

## Usage

**Purpose**: Enter the wi lifecycle — create a new wi, claim a queued one, resume a paused one, or force-takeover a stalled one.

**Pattern**: `/pf-work [<slug>] [--resume | --force]`

**Required**: none (no-arg = create-new dialog; with `<slug>` = claim/resume/takeover)

**Flags**:
- `--resume` — resume a paused wi (Mode C)
- `--force` — force-takeover an idle/expired wi (Mode D); destructive against current claimer, requires `reason`
- `--silent` / silent-mode trigger — Mode A: create + queue without prompting to claim (NL trigger, not a literal CLI flag)

## When to use

Any time the user wants to begin working on something — new task, picking up a queued
item, resuming yesterday's work, or taking over a stalled wi from another agent.

## Architecture rules

pf-work is the **only creation entry point** for the wi lifecycle. Whether human or AI
(including a problem discovered mid-step during execution), creating a wi must go through
this skill.

Invocation modes:
- **dialog mode** (default): a human/AI creates a wi during session discussion -> after creation, ask whether to claim
- **silent mode**: an AI creates a wi mid-step during execution -> state "use silent mode" or "silent create" when invoking -> only create and put on the queue, do not ask

## Mechanic

### Post-claim routing

`## Post-claim Next steps Routing` is the **single source of truth** for what to suggest in
"Next steps" after any claim, and applies to **all** skills that emit three-segment output
(not just `pf-work`). It lives in `fragments/post-claim-routing.md` under the
`using-polyforge` skill directory.

🔴 **`Read` that file before emitting the list — do not answer from session context.** It is
deliberately NOT part of the auto-injected session-start payload (that payload has a hard
size budget; see `using-polyforge/references/manifest-notes.md`), so it is *not* already
in your context.
This SKILL.md previously claimed the backreference "resolves reliably" because
`using-polyforge` is auto-loaded; that was false — the section sat at character 5,856 of an
18,286-character payload that the harness truncated to a ~2,000-character preview, so it
reached no model at all (aihub#285). Resolve it by reading the file, not by recall.

### Mode A — New wi (default, triggered by intent to start something new)

1. **Memory-First** (using-polyforge handles this at session start; surface results).

2. **Resolve wi_type from scenario repo**:

   Read the project's scenario clone from `.repo/` (cloned by `polyforge init`):
   ```bash
   scenario_url  = project.scenario  // from .polyforge.yaml
   scenario_name = <last path segment of URL, strip .git>
                   // "git@github.com:GMISWE/polyforge-coding.git" → "polyforge-coding"
   scenario_path = <workspace_root>/.repo/<scenario_name>/

   // Scenario not cloned yet?
   if scenario_path does not exist:
       STOP: "⚠️ Scenario repo not cloned yet; please run polyforge init first."

   // Infer valid wi_type from .md file names
   // List all *.md files under scenario_path, extract the {wi_type} prefix (before the first .)
   // Exclude "default"
   available_wi_types = [
       f.split(".")[0]
       for f in os.listdir(scenario_path)
       if f.endswith(".md") and not f.startswith("default")
   ]

   // Validation (when creating a wi):
   // Check that at least one of {wi_type}.{project}.md or {wi_type}.md exists
   // has_step_sections: the file contains at least one ^## Step: line
   def has_step_sections(filepath):
       with open(filepath) as f:
           return any(re.match(r"^## Step: \w+", line) for line in f)

   def validate_wi_type(wi_type, project, scenario_path):
       specific = f"{wi_type}.{project}.md"
       generic  = f"{wi_type}.md"
       for path, tag in [(specific, "ok"), (generic, "warn")]:
           full = f"{scenario_path}/{path}"
           if os.path.exists(full):
               if not has_step_sections(full):
                   return "error", None  # file exists but has no ## Step: sections
               return tag, path
       return "error", None      # reject creation

   // requires_human_session: read from the .md file frontmatter
   // project-specific file takes priority; fallback to the generic file; default true if neither exists
   def get_rhs(wi_type, project, scenario_path):
       for path in [f"{wi_type}.{project}.md", f"{wi_type}.md"]:
           full = f"{scenario_path}/{path}"
           if os.path.exists(full):
               fm = parse_frontmatter(full)
               return fm.get("requires_human_session", True)
       return True  # default
   ```

   AI infers wi_type from goal description + complexity, **matching against available_wi_types**:
   - Bug, root cause clear, small change → `fix_bug`
   - Bug, large impact or root cause unknown → `critical_bug`
   - Feature needing design decisions → `feature`
   - Simple maintenance, no design needed → `chore`
   - …other wi_types defined by .md files in the project's scenario repo

   **If no project scenario configured** OR **validate_wi_type returns "error"**:
   → Fall back to built-in `default` wi_type (`requires_human_session=true`, steps=[]).
   Notify user: "⚠️ Could not match wi_type, using default (requires human session)."

   **If validate_wi_type returns "warn"**:
   → Proceed with the generic .md flow; notify user:
   "⚠️ {wi_type}.{project}.md not found, will use the generic flow {wi_type}.md."

2b. **AI extracts content draft from conversation**:
    From the current session conversation, extract a content draft describing the problem:
    - Background: why does this wi exist, what triggered it
    - Context: relevant information, known constraints, related discussions
    - Do NOT include solution approach (that belongs in spec/plan)

    **Split the facts into two labelled groups — every claim goes in one of them:**
    - **Measured** — you ran it or read it this session; cite the command or `file:line`.
    - **Unverified / inferred** — say so, and name the one command that would settle it.

    A plausible reading that was never run is the usual way a wi ships a spec whose premise
    is false; the executing agent then either loses the run to it or delivers a no-op that
    looks like a fix. If you cannot label a claim, it belongs in the unverified group.

    Show the draft to the user for confirmation/modification:
    ```
    --- content draft ---
    <extracted background and context>

    Confirm (press Enter) or modify:
    ```

    If conversation context is insufficient for meaningful content, skip (content is optional).
    Pass the confirmed draft as `content=<draft>` to pf_create_work_item.

3. **Conflict preview** (before creating):
   ```
   pf_predict_conflicts(declared_resources=<new wi's resources>, dry_run=true)
   ```
   Show impact. If hard conflict → stop and explain.

4. **Create** (do NOT claim yet):
   ```
   pf_create_work_item(
     project=<from .polyforge.yaml>,
     goal=<user_goal>,
     wi_type=<inferred>,
     requires_human_session=<from get_rhs(wi_type, project, scenario_path)>,
     priority=<inferred: urgent/high/normal/low>,
     labels=[...],
     content=<confirmed draft>
   )
   ```
   - `400 PROJECT_NOT_FOUND` → prompt to create project first
   - `409 DUPLICATE` → show existing wi, ask: "Continue new / Claim existing / Cancel"
   - `409 CANDIDATES` → show candidate list, ask user to choose

5. **Interactive confirmation** (dialog mode) / **Silent** (silent mode):

   **dialog mode** (default):
   Output: "Created <slug> (<goal[:40]>). Claim and start working on it now?"
   
   → human says "yes" / "do it" / "claim" → claim directly (skip predict_conflicts, the wi was just created and has no locks):
     ```
     pf_claim_work_item(
       work_item_id=<wi_id>,
       idempotency_key=<client ULID>,
       mode="fresh"
     )
     ```
     then recall wi-linked memories:
     ```
     pf_recall(project=<wi.project>, work_item_id=<wi_id>, top_k=10)
     ```
     ⚠️ No `fields="brief"` on any claim path (aihub#313): these are wi-scoped handoff notes
     where the BODY is the payload. Briefing them makes a resuming agent fetch all ten by id.
     **rhs routing** (wi.requires_human_session):
     - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
     - `true`  → emit three-segment output ("Next steps" decided per the Post-claim routing table — `Read` `fragments/post-claim-routing.md` in `using-polyforge`, it is NOT in context (see §Post-claim routing above)), wait for human session.
   
   → human says "no" / "not now" / "leave it" → emit three-segment output, wi stays on the queue.

   **silent mode** (state "use silent mode" or "silent create" when invoking):
   emit three-segment output directly, do not ask, do not claim, wi stays on the queue.

   **Filing several at once (silent mode only)**: use `pf_batch_create_work_items` instead of
   one `pf_create_work_item` per item — repeated single calls cost one MCP round-trip each,
   and a round-trip costs the whole request prefix rather than the size of its response
   (aihub#290: 134 measured adjacent create→create pairs, 0.171% of billed input).

   ```
   pf_batch_create_work_items(
     project=<from .polyforge.yaml>,
     items=[
       {goal:..., wi_type:..., priority:..., labels:[...], content:...},
       {goal:..., wi_type:...},
     ]
   )
   ```

   - Items are created independently; one failure does not stop the rest. Read `created` and
     `failed` separately — **`ok:true` is not implied by the call returning**.
   - Each `failed` entry carries its `index`, so retry by resending only those items. Do not
     resend the whole array: the ones that already landed would then trip dedup.
   - Duplicate detection still runs per item, so a `409 DUPLICATE` / `409 CANDIDATES` on one
     item is a normal per-item outcome — surface it the same way Step 4 does for a single wi.
   - This is silent mode only. Dialog mode confirms one draft at a time, so batching there
     would skip the confirmation each wi is supposed to get.
   - **Compatibility**: if `pf_batch_create_work_items` is not among the available tools, the
     server binary predates aihub#290 — fall back to one `pf_create_work_item` per item.

6. Output three-segment format.

---

### Mode B — Claim existing queued wi (`/pf-work <slug>`)

1. `pf_predict_conflicts(work_item_id=<slug>, dry_run=true)` → conflict preview
2. `pf_claim_work_item(work_item_id=<slug>, mode="fresh", ...)`
3. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
   ⚠️ No `fields="brief"` on any claim path (aihub#313): these are wi-scoped handoff notes
   where the BODY is the payload. Briefing them makes a resuming agent fetch all ten by id.
4. **rhs routing** (wi.requires_human_session):
   - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
   - `true`  → emit three-segment output ("Next steps" decided per the Post-claim routing table — `Read` `fragments/post-claim-routing.md` in `using-polyforge`, it is NOT in context (see §Post-claim routing above)), wait for human session.

---

### Mode C — Resume paused wi (`/pf-work <slug> --resume`)

1. ```
   pf_claim_work_item(
     work_item_id=<slug>,
     mode="resume",
     idempotency_key=<client ULID>
     // Do NOT pass scenario_ref — COALESCE on server preserves the original pinned SHA
   )
   ```
   Restores: prepared workspace + step state from the previous attempt.

   > ⚠️ If this wi was originally claimed on a different machine, the pinned
   > `scenario_ref` SHA may not exist in the local clone. pf-execute will auto-fetch
   > if needed, but verify local scenario clone is current: `polyforge init`.
2. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
   ⚠️ No `fields="brief"` on any claim path (aihub#313): these are wi-scoped handoff notes
   where the BODY is the payload. Briefing them makes a resuming agent fetch all ten by id.
3. Show step progress: "Resuming at step 2/4 (review)".
4. **rhs routing** (wi.requires_human_session):
   - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
   - `true`  → emit three-segment output (including step progress; "Next steps" decided per the Post-claim routing table — `Read` `fragments/post-claim-routing.md` in `using-polyforge`, it is NOT in context (see §Post-claim routing above)), wait for human session.

---

### Mode D — Force takeover (`/pf-work <slug> --force`)

Permission rules:
- `writer` can take over any running wi (claim is static ownership; takeover is always explicit)
- `admin` can take over any attempt at any time (must supply `reason`)

Steps:
1. `pf_force_takeover(work_item_id=<slug>, reason=<user input>)`
2. `pf_claim_work_item(mode="fresh", ...)` — fresh claim.
4. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
   ⚠️ No `fields="brief"` on any claim path (aihub#313): these are wi-scoped handoff notes
   where the BODY is the payload. Briefing them makes a resuming agent fetch all ten by id.
5. **rhs routing** (wi.requires_human_session):
   - `false` → do not emit three-segment output; immediately dispatch `/pf-execute` as a subagent (the subagent emits its own execution progress).
   - `true`  → emit three-segment output ("Next steps" decided per the Post-claim routing table — `Read` `fragments/post-claim-routing.md` in `using-polyforge`, it is NOT in context (see §Post-claim routing above)), wait for human session.

---

### State file management

After a successful claim, `<workspace>/.polyforge/state/<wi_id>.json` holds the
`config.StateFile` struct — these keys and no others:
```json
{
  "wi_id": "wi_xxx",
  "slug": "aihub#322",
  "project": "aihub",
  "attempt_id": "ra_xxx",
  "claim_epoch": 1,
  "session_secret": "<64 hex, mode 0600>",
  "claimed_at": "2026-09-01T00:00:00Z",
  "claimed": true,
  "idem_key": "<uuid>",
  "worktrees": {"repo-name": "/abs/path/to/pf.<project>-<seq>/repo-name"}
}
```
`session_secret` is written here by the MCP server and is never shown in output.
Every key except `wi_id`, `attempt_id`, `claim_epoch`, `session_secret` and
`claimed` is `omitempty`, so it is absent rather than empty when unset.

⚠️ This block used to list `workspace_root`, `repo` and `task_branch`. **None of
the three has ever been a key of `StateFile`** — the workspace root is resolved
at use time from `POLYFORGE_WORKSPACE_ROOT` or by walking up for
`.polyforge.yaml`; a claim covers every repo in the project, so the per-repo
paths live in `worktrees`; and `task_branch` is a field of a `declared_resources`
entry, not of this file. The names do occur elsewhere — `workspace_root` is a
parameter on the `pf_*` coding tools — so do not go looking for them here.

### Task branch naming

The claim creates one worktree per project repo at
`<workspace>/pf.<project>-<seq>/<repo>/`, on a branch named

```
polyforge/<project>-<seq>-<short-kebab-goal>     e.g. polyforge/aihub-322-readable-task-branch-names
```

It is **computed at claim time and stored nowhere** — not in the state file, not
on the work item. Nothing downstream re-derives it either: `pf_ship`, `pf_pr`,
`pf_push` and `pf_wrap` all read the current branch out of the worktree with
`git rev-parse --abbrev-ref HEAD`.

Degradations, in order — the goal is free text and frequently Chinese, and the
result must always be a legal git ref:

| Situation | Branch |
|---|---|
| normal | `polyforge/<project>-<seq>-<kebab goal>` |
| goal has no `[a-z0-9]` (Chinese-only, punctuation-only, empty) | `polyforge/<project>-<seq>` |
| **seq** has no `[a-z0-9]` | `polyforge/<project>[-<kebab goal>]` |
| **project** has no `[a-z0-9]` | `polyforge/<seq>[-<kebab goal>]` |
| neither project nor seq has any `[a-z0-9]` | `polyforge/<ulid8>` (the pre-1.1.18 name) |

`<project>` is included because `<seq>` is unique per project, not per repo, and
one repo may be listed under two projects in `.polyforge.yaml`.

### Which branch a claim attaches to

Branches created before plugin 1.1.18 are named `polyforge/<ulid8>` — the last 8
characters of the wi id. **They keep those names.** And because the name above is
derived from the goal, which is editable, the name a claim computes today need
not be the name the branch was created under. So every claim first looks for a
branch to attach to, and creates one only when nothing matches.

**The lookup must be able to find every name in the table above**, because those
are the names this scheme creates. Exact names are tried first and exhaustively;
one glob comes last:

1. the current name, `polyforge/<project>-<seq>-<kebab goal>`;
2. the legacy `polyforge/<ulid8>`;
3. the bare `polyforge/<project>-<seq>` — table row 2, which a Chinese-only goal
   produces, and which is common rather than exotic. It comes *after* the legacy
   name because a stem-shaped branch has a second producer: the
   `declared_resources[].task_branch` field is set by hand, and work items do
   carry values like `polyforge/ieops-549` in it. So a stem-shaped branch may
   belong to someone else, while `polyforge/<ulid8>` can only ever have been
   created by this system for this work item;
4. any **single** branch matching `polyforge/<project>-<seq>-*`. This covers a
   goal edited after the claim. Note it does **not** match the bare
   `polyforge/<project>-<seq>` — a glob with a trailing `-*` never can — which is
   exactly why step 3 exists as its own exact lookup. Two matches means the goal
   was edited twice, and the lookup declines rather than guess. Skipped entirely
   unless *both* `<project>` and `<seq>` survived: half a stem is not an identity,
   it is a glob over other people's branches.

Each of the four is looked for as a local branch **and then as `origin/<name>`**,
so a local head deleted by a cleanup pass or missing from a fresh clone does not
cause pushed work to be orphaned by a new branch off `origin/main`. Whether the
local branch exists is re-checked immediately before the worktree is created, so
a branch found via `origin/` that also exists locally is checked out rather than
re-created.

Mapping the table onto the steps, so the two sections cannot drift apart: row 1
→ step 1, row 2 → step 3, row 5 (`polyforge/<ulid8>`) → step 2. Rows 3 and 4 —
`polyforge/<project>[-<kebab goal>]` and `polyforge/<seq>[-<kebab goal>]`, one
component unusable — are reached by step 1 only, since that degraded form *is*
the name computed today. Step 4 is skipped for them, so for those two rows alone
a goal edited after the claim is **not** recoverable and the claim starts a new
branch. That bites specifically on the **goal-bearing** variant: with no goal
text the name does not move when the goal is edited, so there is nothing to
lose. It is the accepted cost of a work item whose project or seq contains no
`[a-z0-9]` at all; the earlier branch still exists under its own name and
nothing is lost from it.

⚠️ This applies on **every** claim — fresh, resume and force takeover alike. It
is decided from what exists in the clone, never from the `mode` argument. Modes
D (`takeover`) and B (`/pf-work <slug>` without `--resume`) both send
`mode="fresh"` at a work item that already has a branch and commits, and `mode`
is optional so it can be absent entirely.

None of this is reached while the worktree directory
`<workspace>/pf.<project>-<seq>/<repo>/` still exists — that is reused as-is,
whatever branch it happens to be on.

## NL Triggers

- "start" / "new task" / "let's start" / "I want to work on"
- "claim [slug]" / "pick up [slug]"
- "resume [slug]" / "continue [slug]" / "pick this back up"
- "takeover [slug]" / "take over [slug]" / "force claim [slug]"
