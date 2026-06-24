## Memory-First Principle

In a polyforge workspace **all memory lives in aihub** (recall with `pf_recall`, write with
`pf_remember` / `pf_save_artifact`). The harness's local Claude `.md` memory is deprecated
here — do not read or write local memory files; see [Memory: unified to polyforge](#memory-unified-to-polyforge-local-md-deprecated).

Before every substantive action:

```
pf_recall(project=<current>, query=<user_intent>, type="experience.*|rule.*", top_k=5)
```

Display results where `effective_strength >= 0.3`:

```
💡 Relevant history (for reference — not binding):
  · [experience.pitfall] OAuth token clock-skew bug
    activated 3×, last 14 days ago, confidence ★★★★
  · [rule.scheduling] v3.1 wi must wait for v3.0 backlog to clear
    immortal, by alice
```

For any memory the LLM judges as actually useful: `pf_activate_memory(id)`.
