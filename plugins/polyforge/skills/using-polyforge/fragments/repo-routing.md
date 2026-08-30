## Repo Routing (task → repo)

NL Routing above maps *intent → skill*. To decide *which repo/worktree* a task belongs to,
use the CLAUDE.md `## Workspace` repo map from Step 0:

- **Which project / repo** — match the task against each repo's one-line `positioning`.
- **Where to execute** — the claimed wi's worktree for that repo (`pf.<shortid>/<repo>/`).
- **How to change** — each project links to `.polyforge/repo-map/<project>.md`
  (`main_modules` / `change_scenarios` / `tech_stack`); `Read` just that file. No link =
  older format, detail inlined under each repo. Neither = say "repo map missing — run
  `polyforge init`", then `codegraph_*` / `Grep`. Never infer internals from positioning.

When a task spans repos, route each part by the same map. If no repo clearly fits, ask the
user rather than guessing.
