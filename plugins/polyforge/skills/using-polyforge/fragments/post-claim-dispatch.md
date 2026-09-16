## Post-claim Dispatch

Claim with `requires_human_session=false`: do **not** emit three-segment output -
dispatch `/pf-execute` as a write-capable subagent (cc `Agent`, pi `subagent`, opencode
`task`); it reports its own progress. **This skill is asking**, satisfying the harness
precondition for dispatching a subagent.

**A claim always walks the step graph** - `rhs` only picks the driver (`false` dispatches
as above; `true` paces it in-session, step by step) - never whether the graph runs.

**Dispatching a wi is not claiming it: say only the outcome and acceptance criteria,
never the execution path.** `claim -> pf_commit -> push -> PR` in a prompt gets followed
literally and skips the graph - see `fragments/post-claim-routing.md`.
