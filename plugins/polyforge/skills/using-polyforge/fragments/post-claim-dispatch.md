## Post-claim Dispatch (`requires_human_session=false`)

On a successful claim with `requires_human_session=false`: do **not** emit three-segment
output — dispatch `/pf-execute` as a write-capable subagent (Claude Code: `Agent`,
`subagent_type: "general-purpose"`); it reports its own progress. Claiming and then
stopping to report is the failure here, not the safe default.

**This skill is asking**, which is what the harness rule "do not use the Agent tool
unless the user, a CLAUDE.md file, or a skill asks for it" requires before dispatching.
