---
name: pf-stop
description: >
  Use when the user is done working on the current wi and wants to pause it, wrap it
  as a terminal success, or mark it a terminal failure.
---

# pf-stop — Stop / Complete Work Item

## Usage

**Purpose**: Terminate or pause work on the currently-claimed wi.

**Pattern**: `/pf-stop { --pause | --wrap | --fail }`

**Required**: exactly one of the flags below

**Flags**:
- `--pause` — release lease, keep locks, status → paused (resumable via `/pf-work <slug> --resume`)
- `--wrap` — terminal success; emits wrap note, calls `pf_wrap`, deletes state file (destructive)
- `--fail` — terminal failure; emits failure note, calls `pf_complete_attempt(failed)`, deletes state file (destructive)

## When to use

When the user is done working (wrap), needs to pause and come back later (pause), or
encounters a terminal failure that cannot be resolved in this session (fail).

## Mechanic

### Mode: pause (`/pf-stop --pause`)

1. If any step is in `in_progress` state:
   ```
   pf_update_step(
     work_item_id=<current>,
     step_id=<in_progress step>,
     status="failed",
     step_attempt_id=<current step_attempt_id>
   )
   ```
   This resets the step so it can be retried on resume.

2. Release lease (keep locks so no one else can claim the same resources):
   ```
   pf_complete_attempt(
     work_item_id=<wi_id>,
     status="paused"
   )
   ```

3. State file (`<workspace>/.polyforge/state/<wi_id>.json`) is kept for `/pf-work --resume`.

4. Output three-segment format.

---

### Mode: wrap (`/pf-stop --wrap`)

> **Pass the wrap note as `note=` on the terminal call** — one call, not two. The note is
> recorded server-side before the attempt completes, which is the only order that works:
> `pf_wrap` and `pf_complete_attempt(wrapped)` delete the state file, so a `pf_emit_event`
> after them fails for want of credentials. Fusing them removes both the ordering hazard and
> the round-trip (aihub#290).
>
> **Compatibility**: if `pf_wrap` / `pf_complete_attempt` do not publish a `note` parameter,
> the server binary predates aihub#290 — fall back to a separate
> `pf_emit_event(event_type="note", payload={text: ...})` issued **before** the terminal call.
> Do not pass `note` to a tool that does not publish it.

1. Call the terminal wrap with the note — **coding scenario**:
   ```
   pf_wrap(
     workspace_root=<ws>,
     work_item_id=<current>,
     note="wrapped: <1-sentence summary of what was accomplished>"
   )
   ```
   Check `note_emitted` in the response: a note that failed to record does **not** fail the
   wrap, so it is reported rather than raised.

   ⚠️ **Retrying is not free of the note.** The note is emitted *before* the completion, so a
   `pf_wrap` that fails at `complete_attempt` has already recorded it — and this is the
   common failure, because `pf_wrap` never sets `force_terminate_step`, so wrapping with a
   step still `in_progress` always fails there. **On any retry, drop `note=`** (or the note
   lands twice). The error text tells you which case you are in: *"the closing note WAS
   already recorded"* → retry without `note=`; *"the closing note was NOT recorded either"* →
   retry with it.

   `pf_wrap` = push + PR + `pf_complete_attempt(wrapped)` + delete the state file. It does NOT
   remove the `pf.<slug>/` worktree dirs — clean those up manually or with
   `polyforge doctor --fix`.
   It is idempotent only when a PR on the branch already covers local HEAD; commits no PR
   covers are pushed, and a new PR is opened if the only PR is merged/closed (aihub#226).
   Check `pr_action` in the response (`reused_existing_pr` / `pushed_to_existing_pr` /
   `pushed_and_created_pr`) to see which happened — `ok:true` alone does not mean anything
   was delivered, and by then the credentials are gone.
   Credentials (`attempt_id`, `claim_epoch`, `session_secret`) are injected automatically
   by the MCP server from the state file.

   **Non-coding scenario**:
   ```
   pf_complete_attempt(
     work_item_id=<current>,
     status="wrapped",
     note="wrapped: <1-sentence summary of what was accomplished>"
   )
   ```

2. State file is deleted by `pf_wrap` / `pf_complete_attempt`; no manual delete needed.

3. Suggest: "Run `/pf-retro` to save learnings to team memory."

4. Output: "Is this session's workflow worth crystallizing as a wi_type? (enter a name to crystallize, or press Enter to skip)"

   - User enters a name → call pf-crystallize, passing:
     - source_wi_id=<wrapped_wi_id>
     - wi_type_name=<user input>
   - Skip / empty input → end

5. Output three-segment format with wrap summary:
   - Goal achieved
   - Key decisions made
   - Follow-up items (if any)

---

### Mode: fail (`/pf-stop --fail`)

> **Pass the failure reason as `note=` on the same call.** The terminal call deletes the state
> file, so a separate `pf_emit_event` only works before it — folding the note in removes that
> ordering hazard and the extra round-trip (aihub#290).
>
> **Compatibility**: if `pf_complete_attempt` does not publish a `note` parameter, the server
> binary predates aihub#290 — emit
> `pf_emit_event(event_type="note", payload={text: "failed reason: ..."})` **first**, then call
> `pf_complete_attempt(status="failed")` without `note`.

1. Mark the attempt as failed, carrying the reason:
   ```
   pf_complete_attempt(
     work_item_id=<current>,
     status="failed",
     note="failed reason: <user description of why it failed>"
   )
   ```
   `note_emitted` in the response says whether the note landed; a failed note does not fail
   the terminal call.

   ⚠️ The note is emitted *before* the completion, so a call that fails at the completion has
   already recorded it. **On any retry, drop `note=`** unless the error says the note was NOT
   recorded either.

2. State file is deleted by `pf_complete_attempt`; no manual delete needed.

3. Output three-segment format. Suggest: "Create a bug wi with `/pf-work --goal 'bug: ...'`
   to track the root cause."

## NL Triggers

- "pause" / "pause" / "save and come back later" / "take a break"
- "complete" / "done" / "wrap" / "all set" / "ship it" / "finished"
- "fail" / "failed" / "give up" / "abandon" / "this is broken"
