# _common/memory.md — Memory-First + team-memory sync (always injected)

> Injected by `hooks/pf-skill-router` for EVERY pf-spec / pf-plan / pf-execute step, in
> **BOTH** the superpowers branch and the native branch. Recall and remember are polyforge
> lifecycle — they run regardless of which engine writes the content.

## Before the engine — Memory-First recall

```
pf_recall(project=<current>, query=<wi.goal>, type="@@RECALL_TYPE@@", top_k=5)
```

Display results with `effective_strength >= 0.3` (💡 prefix). For any memory the model
judges actually useful: `pf_activate_memory(id)`. See the session-start `memory-first`
fragment for the display format and the Memory-First principle.

## After the engine — record useful learnings

If the step surfaced anything worth remembering across sessions (a pitfall, a reusable
approach, a constraint), capture it:

```
pf_remember(type="experience.*|fact.*|rule.*", project=<current>, content=<finding>,
            work_item_id=<current>, visibility="project")
```

Don't over-save — only findings that would genuinely help someone later. (The post-wrap
`/pf-retro` does the systematic learning extraction; this is the in-step capture only.)
