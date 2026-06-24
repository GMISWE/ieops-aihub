## Repo Routing (task → repo)

NL Routing above maps *intent → skill*. To decide *which repo/worktree* a task belongs to,
use the CLAUDE.md `## Workspace` repo map from Step 0:

- **Which project / repo** — match the task against each repo's `positioning` (and its
  `change_scenarios` for the kind of change); pick the repo whose role + typical changes
  fit best.
- **Where to execute** — the claimed wi's worktree for that repo (`pf.<shortid>/<repo>/`);
  `main_modules` points to the relevant directories/files inside it.
- **How to change** — `main_modules` locates the code, `change_scenarios` tells you whether
  the task is a known pattern, `tech_stack` sets tooling expectations (build/test commands).

When a task spans repos, route each part by the same map. If no repo clearly fits, ask the
user rather than guessing.
