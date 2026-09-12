# _common/memory.md - Memory-First + team-memory sync

## Before the engine - Memory-First recall

```
pf_recall(project=<current>, query=<wi.goal>, type=@@RECALL_TYPE@@, top_k=5, fields="brief")
```

`brief`: display-only, no bodies (aihub#313; see `memory-conventions.md`).

Display results with `effective_strength >= 0.3`; `pf_activate_memory(id)` for any the
model judges actually useful.

## After the engine - record useful learnings

If the step surfaced a pitfall, a reusable approach or a constraint worth keeping:

```
pf_remember(type=<ONE concrete type - e.g. experience.pitfall / fact.architecture / rule.work>,
            project=<current>, content=<finding>,
            work_item_id=<current>, visibility="project")
```

Don't over-save - only findings that would genuinely help someone later; `/pf-retro` does the
systematic extraction post-wrap.
