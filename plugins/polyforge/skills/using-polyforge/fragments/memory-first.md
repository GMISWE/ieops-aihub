## Memory-First Principle

In a polyforge workspace **all memory lives in aihub** (recall with `pf_recall`, write with
`pf_remember` / `pf_save_artifact`). The harness's local Claude `.md` memory is deprecated
here — do not read or write local memory files.

Before every substantive action:

```
pf_recall(project=<current>, query=<user_intent>, type=["experience.*","rule.*"], top_k=5, fields="brief")
```

Display each result with `effective_strength >= 0.3` under `💡 Relevant history (not binding)`
as `· [<type>] <title> — <age, strength>` (`brief` returns that line, no bodies).

For any memory the LLM judges as actually useful: `pf_activate_memory(id)`.
