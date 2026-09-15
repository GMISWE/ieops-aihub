package domain

// codeProducedEventTypes is the exact event-type set aihub#684's no-steps-recorded
// gate (FnCompleteAttempt) treats as "this work item produced code". Source-derived,
// not observed from a sample: emitCodingEvent (internal/mcp/tools_coding.go) is the
// only producer of any of these three, and pf_ship — the one caller that could emit
// more than one — emits all three conditionally on which stages ran rather than a
// distinct type of its own. See event_types.go's "written by a CALLER through POST
// /v1/events" group, which names the same three for the same reason.
//
// This set is FORCED narrow by what the server can see, not a curated definition of
// "code produced": it does not close the known holes named on the gate itself —
// the raw-git / `polyforge commit` CLI path emits none of these events at all, and
// emitCodingEvent discards its own errors, so a successful push whose event POST
// failed leaves no record either. code_produced_event_types_drift_test.go is what
// keeps this set from drifting open silently as new emitCodingEvent call sites are
// added; it does not, and cannot, keep the two holes above from existing.
var codeProducedEventTypes = map[string]bool{
	"commit":    true,
	"push":      true,
	"pr_opened": true,
}
