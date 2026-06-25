---
name: pf-sync
description: >
  External system sync — pull Jira/GitHub issues into aihub as wi's, or push a
  wrapped wi back to Jira/GitHub. Phase 2 feature: v1 stubs both directions and
  emits a note event.
---

# pf-sync — External System Sync (Phase 2 stub)

## Usage

**Purpose**: Sync work items between polyforge and external trackers (Jira / GitHub Issues). Currently a Phase 2 stub — emits a note event only.

**Pattern**: `/pf-sync { pull | push } [<external_id>]`

**Required**: a sub-mode (see below)

**Flags**: none (v1 is a stub; both directions emit a note event and surface a not-yet-implemented message)

## When to use

When the user wants to sync work items between polyforge and an external tracker
(Jira, GitHub Issues). Currently a Phase 2 feature — v1 emits a diagnostic note
and tracks the sync request as a follow-up work item.

## Mechanic

### `/pf-sync pull`

**Intent**: Import open Jira issues or GitHub Issues as polyforge wi's.

**v1 behavior** (stub):

1. Emit informational note:
   ```
   pf_emit_event(
     work_item_id=<current or create tracking wi>,
     event_type="note",
     payload={text: "pf-sync pull requested — external sync not yet implemented in v1. Tracked in Phase 2 backlog."}
   )
   ```

2. Output three-segment format:
   ```
   ## Result
   pf-sync pull is not yet implemented in v1.
   
   ## Status
   | feature | pf-sync pull         |
   | status  | Phase 2 backlog      |
   | tracker | External sync (Jira / GitHub Issues) |
   
   ## Next steps
   - Manually create wi's with `/pf-work --goal "..."` for urgent items
   - Track this feature request: create wi with `/pf-work --goal "implement pf-sync pull"`
   ```

---

### `/pf-sync push`

**Intent**: Publish a locally-wrapped wi back to Jira/GitHub (close issue, update status).

**v1 behavior** (stub):

1. Emit note:
   ```
   pf_emit_event(
     work_item_id=<current>,
     event_type="note",
     payload={text: "pf-sync push requested — external push not yet implemented in v1."}
   )
   ```

2. Output three-segment format (same pattern as pull).

---

## Phase 2 Design (reference)

When implemented, `pf-sync pull` will:
1. Authenticate with Jira/GitHub via stored credentials
2. `pf_sync_pull(project, source="jira"|"github", filter="open")` → list of imported wi's
3. For each imported wi: `pf_create_work_item(source="jira_import"|"github_import", ...)`
4. Show summary of imported wi's

`pf-sync push` will:
1. `pf_sync_push(work_item_id, target_ref="jira:PROJ-123"|"github:owner/repo#42")`
2. Closes/transitions the external ticket
3. Emits `synced_to_external` event

## NL Triggers

- "sync" / "sync" / "sync to jira" / "sync to github"
- "import from jira" / "import from jira"
- "push to github" / "push to github"
- `/pf-sync pull` / `/pf-sync push`
