## Post-claim Dispatch (`requires_human_session=false`)

On a successful claim with `requires_human_session=false`: do **not** emit three-segment
output - dispatch `/pf-execute` as a write-capable general-purpose subagent (cc
`Agent`, pi `subagent`, opencode `task`); it reports its own progress. Claiming and
then stopping to report is the failure here, not the safe default.

**This skill is asking**, which is what the harness rule "do not dispatch a subagent
unless the user, a CLAUDE.md file, or a skill asks for it" requires before dispatching.
