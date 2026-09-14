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
//
// aihub#652 added the `claimed_by` param (attempt-claimant filter,
// complementing the reporter-only `user_id`). Even a same-shape property with
// an EMPTY description still adds ~48 B of pure JSON structure
// (`,"claimed_by":{"type":"string",...}`), and this schema had only ~44 B of
// headroom left at 5400 — a floor no wording choice could clear. Measured
// 5356 B before this param.
//
// The param's FIRST description ("Filter by attempt claimant, exact match.
// See docs/mcp-tools.md.", 63 B) brought the schema to 5467 B (+111 B ≈
// +28 tok/request over the 5356 B baseline). A code review (mem_dors6nNu)
// then flagged that description as non-disclosing — it never said the filter
// only matches the CURRENT/LATEST attempt, unlike `user_id`'s own disclosure
// two properties up — so review_fix reworded it to "Filter by CURRENT
// attempt's claimant only, exact match." (55 B). That is 8 B SHORTER than the
// original despite disclosing more, landing the schema at 5459 B: +103 B over
// the 5356 B baseline, net -8 B from the first (non-disclosing) draft.
//
// aihub#656 added `owner_display` and `reporter_display` (attempt-owner and
// reporter DISPLAY-NAME filters, case-insensitive ILIKE-contains — unlike
// `user_id`/`claimed_by` right above them, which are exact-id matches). The
// same wi's router.go hop-3 gap also covers a third field, `watcher_user_id`,
// but that one is deliberately NOT published here: docs/mcp-cards/
// pf_list_work_items.md's "Open" section already ruled (aihub#652) that
// omitting a watcher filter from the published MCP schema is a scope
// decision, not an oversight, and this wi restates rather than reverses it.
//
// Measured 5459 B before this change. Both new descriptions disclose the
// contains-vs-exact distinction explicitly, the same lesson review_fix drew
// for `claimed_by` above. Landed at 5798 B: +339 B over the 5459 B baseline
// (~85 tokens/request) for two params.
//
// Set to the exact measured 5798 B, no padding — not a round number with
// slack, so the next addition hits this same gate rather than inheriting
// borrowed headroom.
const listWorkItemsSchemaBudget = 5798

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
