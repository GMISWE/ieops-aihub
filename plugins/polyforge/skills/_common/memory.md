# _common/memory.md — Memory-First + team-memory sync (injected for pf-execute)

Recall and remember are polyforge lifecycle, independent of which engine writes the content.

## Before the engine — Memory-First recall

```
pf_recall(project=<current>, query=<wi.goal>, type=@@RECALL_TYPE@@, top_k=5, fields="brief")
```

`brief`: display-only, no bodies (aihub#313; see `memory-conventions.md`).

The router substitutes that slot with a JSON **array**; do NOT wrap it in quotes. `type` is a
list, so a single string containing `|` is one type name matching nothing — a 400 (aihub#289).

Display results with `effective_strength >= 0.3` (💡 prefix); `pf_activate_memory(id)` for any the
model judges actually useful. The display format and the Memory-First principle come from the
session-start `memory-first` fragment, already in your context.

## After the engine — record useful learnings

If the step surfaced a pitfall, a reusable approach or a constraint worth keeping:

```
pf_remember(type=<ONE concrete type — e.g. experience.pitfall / fact.architecture / rule.work>,
            project=<current>, content=<finding>,
            work_item_id=<current>, visibility="project")
```

Don't over-save — only findings that would genuinely help someone later. (`/pf-retro` does the
systematic extraction post-wrap; this is in-step capture only.)
