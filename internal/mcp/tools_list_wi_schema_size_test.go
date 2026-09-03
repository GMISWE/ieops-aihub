package mcp

import "testing"

// listWorkItemsSchemaBudget is a CEILING on the wire size of
// pf_list_work_items' published InputSchema, in bytes.
//
// Why a test and not a comment. An InputSchema sits in the prefix of EVERY
// request, so prose added to a param description is a standing per-request
// charge — the same arithmetic the tool's own Description comment records,
// where +70 B cleared its saving by ~20x and +220 B did not clear at all.
// aihub#276/#277 measured this schema at 3,722 B before the change, 6,000 B on
// a first draft (+2,278 B ≈ +570 tok/request), and trimmed it back before
// shipping.
//
// That measurement lived only in a comment, which meant the next description
// edit would silently invalidate it with nothing turning red. This is the
// missing gate. It is deliberately a ceiling with headroom rather than an
// equality: pinning the exact byte count would fail on every wording tweak and
// would be reflexively re-baselined, which is how a ratchet becomes a rubber
// stamp. Raising it is fine — but it is a decision someone has to make on
// purpose, with the per-request cost in front of them, which is the entire
// point.
//
// 🔴 If this fails, do not just raise the number. First check whether the new
// sentence belongs in docs/mcp-tools.md instead, which is not resident and
// therefore free.
const listWorkItemsSchemaBudget = 5400

func TestListWorkItemsSchemaStaysWithinItsWireBudget(t *testing.T) {
	got := len(listWorkItemsSchema())
	if got > listWorkItemsSchemaBudget {
		t.Errorf("pf_list_work_items InputSchema is %d B, over the %d B budget by %d B "+
			"(~%d tokens on EVERY request). Move the prose to docs/mcp-tools.md, or raise "+
			"listWorkItemsSchemaBudget deliberately and say why.",
			got, listWorkItemsSchemaBudget, got-listWorkItemsSchemaBudget, (got-listWorkItemsSchemaBudget)/4)
	}
	// The floor is not decoration: it is what catches the schema being emptied
	// or a props map failing to render, which would make the ceiling above pass
	// for the worst possible reason.
	if got < 1000 {
		t.Errorf("InputSchema is only %d B — it is not rendering; the ceiling above would "+
			"pass vacuously", got)
	}
	t.Logf("pf_list_work_items InputSchema: %d B of a %d B budget", got, listWorkItemsSchemaBudget)
}
