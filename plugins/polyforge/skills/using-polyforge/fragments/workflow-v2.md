## Workflow v2 - stored steps & the pinned flow (aihub#708)

A claimed wi walks ONE of two step graphs, and `pf_get_workflow(work_item_id=<wi>)` says
which:

- `steps_version == 0` + `steps: null` - no workflow: the LOCAL scenario step graph (the
  legacy path, unchanged: `/pf-execute` + the `wi_type` routing table).
- `steps_version > 0` - a PINNED DB workflow generation: the stored flow is the authority
  for what runs next, in what order, pinned to exact skill versions.

### Reading stored state (server-enforced facts)

- Progress arrives per step: status, review_verdict, step_attempt_id, open_invocation_id,
  approved, rhs. Results live in the workflow tables and intentionally create NO legacy
  `wi_step_state` rows - a workflow wi's `step_state` is absent by design, not "no steps".
- The next action is one of: `run` (a step may start) / `open_invocation` (record its
  result, or reconcile it away) / `repair_required` (a step failed or its review failed;
  only an authorized repair reopens it) / `approval_required` (an rhs step completed but
  no human decision applies to its artifact yet) / `complete` (every step done; wrap
  remains an explicit act).
- Step invocations are SERVER-minted (`pf_start_workflow_step`); a worker result must
  echo the invocation identity exactly (`pf_workflow_result`) and can never carry an
  approval - a result that tries is rejected outright.
- Approval (`pf_approve_workflow`) is HUMAN-ONLY: the server refuses a machine
  credential whatever role it holds, and there is no actor parameter - the authenticated
  principal is the actor. An approval is a distinct act from an artifact annotation
  (`/pf-revise`): resolving annotations never approves a step, and approving never
  resolves annotations.
- Recovery: `pf_repair_workflow` (kind=retry for a provider_error; kind=episode after a
  review FAIL - repair producer + fresh verification + fresh independent review, each
  started with the episode id) and `pf_reconcile_workflow` (fences a dead or paused
  attempt's open invocations so the CURRENT attempt can start replacements).
- A review FAIL pauses the attempt - recoverable, never terminal.

### Callable three-mode protocol

For a claimed pinned-flow wi, the main harness does not improvise the loop and does not
feed symbolic `{step_id,output}` descriptors to a model. Use the controller protocol:

```text
polyforge engine workflow --work-item='<wi>' --continue
```

Its statuses are actionable:

- `prepare_required` - run the printed `--prepare` command. The response opens ONE
  server-fenced invocation and returns the exact pinned skill entry/support files, params,
  access-checked concrete predecessor values, and `expected_output_schema`. Discuss those
  instructions in this main human session. Do not edit/rewrite the bundle; author only the
  submit object `{invocation:<unchanged preparation.invocation>, output:<schema value>}`.
- `prepared` - continue the human discussion (grill questions one at a time; spec revisions
  preserve the user's decisions), then write that submit object to a private temporary JSON
  file and call `polyforge engine workflow --work-item='<wi>' --submit='<file>'`. Submission
  validates the pinned schema, saves through the existing attempt-authorized methodology
  artifact API, and records the controller result with the exact returned id/version/hash.
- `approval_required` - show the named exact artifact/version/hash to the human. Do not call
  approval from model judgment. Only after the authenticated human explicitly says
  **approved** or **rejected** for that displayed tuple may the human-session caller invoke
  `pf_approve_workflow` with the response's exact `steps_version`, `step_id`, and `artifact`.
  Missing approval stays here. Rejection becomes `revision_required`; `--continue` then
  points back to `--prepare` for the SAME step and never starts a successor.
- `run` automatic steps reached by `--continue` execute through the same `controller.RunStep`
  used by drain. Repeat `--continue` after each result/gate. `complete` still requires the
  ordinary explicit wrap action; the protocol never wraps by implication.

The three modes share server transitions: unattended (`requires_human_session=false`) is
owned by drain; automatic steps inside a human session are driven by `--continue`; manual
or effective-RHS steps use `--prepare`/`--submit`. Effective human gating remains
**wi RHS AND step RHS**. A workflow read/error never falls back to the scenario graph.

### Who drives (mode split)

- `requires_human_session=false`: `polyforge drain` (Layer 3) claims, drives and wraps;
  a session that claimed such a wi dispatches it rather than pacing it.
- `requires_human_session=true`: the human paces with the callable protocol above. The main
  harness runs `--continue`, uses `--prepare`/`--submit` for discussion-authored steps, and
  repeats `--continue` for automatic steps. Approval remains a separate authenticated human
  act bound to the exact artifact; the protocol never synthesizes it.
- Composing/revising a flow: `pf_update_workflow` (author-tier; CAS via
  `expected_steps_version`; `requires_human_session` explicit on every revision; refused
  while a worker is in progress). New flows pin EXACT skill versions; `skill_version: 0`
  ("latest accessible") is resolved and frozen at pin time - never a silent fallback a
  running flow silently re-resolves.
- Wrapping: explicit, as always (`/pf-stop --wrap`); for a pinned-flow wi, pass the
  no-steps reason naming the workflow result tables when the gate asks.

### Compatibility (what did NOT change)

The legacy scenario path is untouched: `wi_type`s and their step graphs, `/pf-spec` /
`/pf-plan` / `/pf-execute`, pf-crystallize's scenario branch, and the `wi_type` routing
table for every wi WITHOUT a pinned flow. "A claim always walks the step graph" reads as:
the graph the wi actually has - scenario or stored. The seeded reference bundles behind
new flows (grill-me, spec, plan, code-change, review, verification, ship, ci) live in the
skill registry; see docs/workflow-v2/ in the aihub repo for the seed, its licenses and the
pinning rules.
