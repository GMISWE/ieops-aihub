package mcp

// This file has no imports: every rule is a map lookup and a comparison. That is
// a deliberate ceiling, not a coincidence — anything needing a parser or a
// numeric conversion to decide a drop is doing more than restating the response.

// Work-item LIST projection (aihub#278).
//
// pf_list_work_items answered with `jsonResult(result)` — the REST body
// verbatim, 27 fields per item, no projection. This file removes bytes from it.
//
// ─── Why this is a delete-list, and why that is not a stylistic choice ───────
//
// The obvious shape is slimRecallResult's: a keep-list. It is the wrong shape
// HERE, and the reason is written down next door. recall_slim.go's keep-list has
// silently swallowed a newly-added field three times — `total` (aihub#249) and
// the truncation pair (aihub#269), which its INVARIANT note names, plus
// `unmatched_types` (aihub#289), recorded ninety lines lower at the conditional
// copy that fixed it. wi_echo_slim.go (aihub#281) was then deliberately built
// the other way round "so it cannot become the fourth instance".
//
// A keep-list here would be instances four and five ON ARRIVAL, not someday:
// domain.WorkItem already carries two conditionally-populated fields that no
// keep-list drafted from a sample response would contain, because a sample
// response does not have them.
//
//	similarity   aihub#273, present only on the ?query= semantic path
//	step_state   aihub#280, present only under include_step_state=true — which
//	             pf-retro sends on its first call, and pf-status sends on the
//	             first call of its single-wi branch (its global branch makes no
//	             pf_list_work_items call at all)
//
// aihub#280 landed `include_step_state` weeks ago; a keep-list would have taken
// it straight back out, and — this is the whole hazard of this work item —
// nothing would have said so. The consumer is a language model. Project an API
// used by code and the compiler names who broke. Project an API used by a model
// and the model reads a smaller object, concludes "this wi has no step state",
// and reports that confidently.
//
// ─── The rule every deletion below obeys ────────────────────────────────────
//
// A field is removed only when the response still STATES the same thing without
// it — either because the value carries no information at all, or because it is
// byte-reconstructible from a field that survives.
//
// 🔴 That rule is NECESSARY, not sufficient, and the difference cost this change
// two revisions. `seq` satisfies it outright (slug is `GENERATED ALWAYS AS
// (project || '#' || seq)`) and so does `scenario` (a CHECKed constant). BOTH are
// kept, because measured consumer behaviour says removing either would be
// noticed — silently. Losslessness tells you what can go without loss of
// INFORMATION; it says nothing about loss of a READER, and on this API the reader
// cannot report either one. The line that survived is the gate, not the field:
// **a key may be dropped only when its value is null.** See the long note at the
// end of slimListWorkItem for why, and for the measurement that got there.
//
// No field is removed for "looks unused" either. That judgement has no
// error-detection path on an LLM-facing API, and the evidence that would have to
// back it does not exist. The skill documentation is measurably out of sync with
// this response: using-polyforge/fragments/output-format.md:27 tells the model to
// render a multi-wi list with an `owner_display` column, and no work item served
// by this endpoint has ever had such a field (domain.WorkItem has
// reporter_display; owner_display belongs to the ready queue's ReadyItem and
// RunningItem). So "no skill mentions field X" is not evidence that nothing
// reads X.
//
// Note the asymmetry, and note that it closes BOTH directions rather than
// leaving one open: the docs over-claim, so a field they name may be dead and
// naming proves nothing about consumption — and a field they do not name may
// still be read, so silence proves nothing about non-consumption. Neither
// direction is usable, which is why the rules below start from losslessness
// rather than from usage: losslessness is the only property here that is
// decidable without a consumer that can report its own breakage.
//
// Usage still gets the last word, though — it just has to come from measured
// behaviour rather than from documentation. That is what the null-gate gives:
// see the closing note.
//
// This is the same gate suppressContentEcho uses (wi_echo_slim.go): drop bytes
// only when a check ON THE VALUES ITSELF says the drop is lossless, so
// "lossless" is verified per call rather than asserted about the server's usual
// behaviour. Every rule below is written that way, including the `content` one,
// which reads as unconditional but is not — see the note at that line for the
// version-skew reason it must not be.
//
// Two tests carry that between them, and neither covers the other's half:
//
//	TestSlimListWorkItems_ProjectionIsReconstructible rebuilds the full item
//	from the projected one and demands byte equality, so adding an
//	UNCONDITIONAL delete of a non-reconstructible field turns red.
//
//	TestSlimListWorkItems_KeepsNonNullValuesOfNullDroppedFields feeds real
//	values to every conditional rule, so relaxing a rule's GUARD turns red.
//	Reconstruction is blind to that: with the fixture's nulls the guarded and
//	unguarded projections are byte-identical.
//
// ─── What this deliberately does NOT do ─────────────────────────────────────
//
// The measured field census (four projects, 24 windows, 334 KB) puts `attrs` at
// 42% of the aihub project's list bytes — far more than everything removed here
// — and dropping it, or reducing it to its key names, is where the work item's
// headline 57-78% actually lives. It is not done here because it is not
// lossless, and no available evidence says the model does not read it. That
// share is also not a property of the endpoint: in the three control projects
// attrs is 14%, ~1% and ~0%. The aihub figure is the observer effect this work
// item warned about in its own acceptance criteria — the fattest attrs in the
// sample belong to the token-accounting work items that produced the estimate.
//
// ─── Unconditional, no flag ─────────────────────────────────────────────────
//
// Same reasoning as suppressContentEcho, and the same measurement behind it:
// pf_get_work_item has carried `brief` since aihub#212 and was still called 489
// times for 1,537,444 B in the sample window because nobody passes it. A
// default-off flag here would save nothing until every skill call site changed,
// and would put the saving behind a plugin release. What makes flagless safe is
// not nerve, it is that every removal below is a null.
//
// One thing the flag would have bought is discoverability, and that is paid for
// separately: the tool description now carries a one-sentence note that an
// absent key means null, because nothing else reaches the caller at call time.
// Measured at +70 B / ~27 tokens per request against ~7 KB off every full page.
// The arithmetic that killed a `fields` PARAMETER clears for a sentence — and it
// is the same arithmetic that kept it to a sentence: an enumerating draft cost
// +220 B / ~86 tokens, the same order as the parameter itself.

// slimListWorkItemsResult projects the items of a pf_list_work_items response in
// place and returns the SAME top-level map.
//
// Returning the same map, rather than building `res := map[string]any{"items":
// ...}`, is deliberate and is the aihub#249 lesson applied structurally: a
// rebuilt top-level map drops every key nobody remembered to copy, which is how
// `total` vanished from pf_recall. Here `next_cursor` — and any top-level key
// added to ListWorkItemsResult later — survives because it is never touched.
// Pinned by TestSlimListWorkItems_KeepsUnknownTopLevelKeys.
func slimListWorkItemsResult(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	items, ok := result["items"].([]any)
	if !ok {
		return result
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			slimListWorkItem(m)
		}
	}
	return result
}

// slimListWorkItem removes, from one list item, the keys whose value is null and
// which say nothing the key's absence does not.
func slimListWorkItem(m map[string]any) {
	// `content` is not in the SELECT. Both list paths — buildListWorkItemsQuery
	// and listWorkItemsByVector, which the comment there calls the "same
	// 26-column SELECT (lockstep Scan sites)" — read 26 columns and content is
	// not among them, so domain.WorkItem.Content is a nil *string on every item
	// this endpoint returns today. `"content":null` is therefore not "this work
	// item has no body"; it is "this endpoint does not serve bodies", which is a
	// fact about the endpoint and not about the row.
	//
	// 🔴 Gated on the value being null anyway, and the guard is the whole point.
	// This package is compiled into the `polyforge` BINARY and reaches aihub over
	// HTTP (pkg/client), so the client and the server version independently —
	// that asymmetry is why /pf-doctor exists. An unconditional delete here would
	// be a claim about a SELECT list in a different process, one this code can
	// neither see nor be recompiled against: the day the server adds content (or
	// a snippet) to that SELECT, every deployed polyforge would strip it in
	// silence and the model would conclude the work item has no body. The read of
	// the SELECT above is what makes the deletion WORTH doing; the guard is what
	// makes it SAFE, and only one of those two survives a version skew.
	//
	// Value-gating also makes this rule the same shape as suppressContentEcho and
	// as the null loop below — after two revisions, every rule in this file is
	// gated on a null, and the note at the end explains why that is the whole
	// design rather than a coincidence.
	//
	// A null content is a delete and not an aihub#269-style handle because there
	// is no per-item fact to leave behind: a `content_len` here would have to be
	// invented. pf_get_work_item is where a body is read.
	if v, present := m["content"]; present && v == nil {
		delete(m, "content")
	}

	// For the fields named in listWorkItemNullMeansNone, a JSON null and the
	// key's absence state the same thing. That is already this response's
	// contract for `similarity` and `step_state` (both `omitempty`), so
	// absence-means-none is what a reader of this endpoint has always had to
	// understand — not a convention introduced here.
	//
	// This is the largest rule by bytes: `"external_share_type":null` spends 27
	// bytes to say nothing, and does so on 100% of rows.
	for k := range listWorkItemNullMeansNone {
		if v, present := m[k]; present && v == nil {
			delete(m, k)
		}
	}
}

// ─── Why every rule above is gated on the value being NULL ───────────────────
//
// Not a coincidence and not conservatism: it is the line the measured consumer
// behaviour actually draws, and finding it cost this change two reversals.
//
// Two fields qualified for removal under the losslessness rule and are kept
// anyway. `seq` is reconstructible by the schema — slug is `TEXT GENERATED
// ALWAYS AS (project || '#' || seq) STORED` (0002_work_items.sql). `scenario` is
// CHECKed to (coding|writing|data) with CreateWorkItem rejecting all but coding,
// so it is a constant. Earlier revisions deleted both.
//
// What the observed readers do with a removed key splits cleanly, and it splits
// on the GATE, not on the field:
//
//	null-gated drop   the key only ever goes when its value was null, so a
//	                  reader that renders it by path sees no change at all in jq
//	                  ("\(.closed_at)" prints `null` either way), and a reader
//	                  that subscripts it in Python raises KeyError — LOUD, and
//	                  self-correcting on the next turn.
//
//	value-gated drop  the key goes while holding a REAL value, so the same
//	                  renderer silently prints `null` where it used to print
//	                  `coding`. Verified: "\(.scenario)" gives `coding` before
//	                  and `null` after, and (.scenario|type) flips string->null.
//	                  Nothing raises. Nothing reports. That is the exact failure
//	                  mode this whole work item exists to avoid.
//
// So the rule is one line: a key may be dropped only when its value is null. A
// loud failure is an acceptable price for the bytes; a silent wrong value is
// not, at any price. `seq` and `scenario` are both value-gated drops, so both
// are out — and the earlier revision that kept seq but dropped scenario was
// applying the rule to whichever field the evidence had been counted for, which
// is not a rule.
//
// ⚠️ The evidence that produced this, described accurately, because an earlier
// revision of this comment described it wrongly and the mistake is instructive.
// 315 real pf_list_work_items calls were read out of this machine's session
// transcripts. 104 returned a payload too large for the caller's context, and it
// recovered by shell-processing the spilled sidecar: **145 such commands, of
// which 125 are Python and only 21 are jq.** That population is the closest
// thing this API has to a consumer declaring its own keep-set.
//
// The first version of this comment called all 145 "jq filters" and ranked
// fields by a BARE SUBSTRING count over the command text, then presented the
// result as structured `.field` references. Over the 39 jq programs actually
// extracted, the real path counts are `.items` 35, `.status` 24, `.slug` 14,
// `.seq` 13, `.goal` 11 — so the claim that `.seq` led the field is false;
// `.status` and `.slug` outrank it. The substring count put seq at 92-95, close
// enough to the true 13-of-39 ordering's top group that nothing looked wrong.
// The conclusion (keep seq) survived; its stated reason did not.
//
// The half that IS robust — it holds under every counting rule tried, in both
// languages — is the zero set: `project`, `source`, `reporter_user_id`,
// `current_attempt_id`, `external_share_type` and `external_share_key` are named
// zero times. Four of those six are dropped here anyway, but only when null, and
// none is dropped BECAUSE of that count. "Named zero times" is not evidence on
// this API; it is the absence of evidence, which is why it decides nothing.

// listWorkItemNullMeansNone is a SET. It is spelled map[string]struct{} rather
// than map[string]bool because the loop above ranges the keys and never reads a
// value, so a `"milestone": false` entry would read as "excluded" and behave as
// "included" — an unforgeable set costs nothing and removes the discrepancy.

// listWorkItemNullMeansNone names the fields of a work-item list response whose
// JSON null carries no information the key's absence does not carry.
//
// ─── Why this is an allowlist and not `for k, v := range m { if v == nil }` ──
//
// The blanket rule is what this started as, and it was wrong on a field this
// work item was explicitly warned about. `requires_human_session` is a
// *bool, and its NULL is not "no value" — it is a THIRD classification,
// "unclassified", with its own ready-queue segment (domain.ReadyQueue.
// Unclassified, populated in work_items.go) and its own GC alert sweep
// (RunUnclassifiedWIAlert, gc.go). Migration 0002 says it outright: "NULL wi goes
// to unclassified[] queue, NOT items[] (prevents auto-execution)".
//
// Measured against the live server 2026-09-01: 77 of 1,935 work items are in it
// — 3.98% — and the number that matters is narrower and much larger: **45 of the
// 174 `queued` items, 26%**. Queued is exactly the population the ready queue
// segments and the population pf-execute is pointed at, so this is not a tail
// case, it is a quarter of the work waiting to be picked up.
//
// Drop that null and a model reading the smaller object sees no
// requires_human_session at all. pf-execute/engine.native.md selects its
// execution mode from that field, and absence reads as false, so an
// unclassified work item would silently be executed unattended. Same for a null
// `wi_type`: pf-execute resolves its step graph as `{wi_type}.{project}.md`, and
// "untyped" is a state the graph resolution must see rather than infer.
//
// Note what the reconstruction test CANNOT do for those two, because it is the
// reason they need naming here rather than a guard. Rebuilding "absent -> null"
// round-trips them perfectly; the loss is not in the bytes, it is in what a
// reader concludes from absence. A byte-level losslessness proof is blind to it.
//
// The failure mode of forgetting to add a nullable field here is bytes, not
// correctness — which is why the list is opt-in in this direction, the opposite
// of slimRecallResult's item whitelist, where forgetting costs a field.
//
// ⚠️ Every entry below is a licence to delete the key ONLY when its value is
// null. Nothing about the name makes that true; it is the `v == nil` guard at
// the call site, and that guard is the one this projection's live data depends
// on — a real milestone, a real closed_at or a live current_attempt_id are all
// information. TestSlimListWorkItems_KeepsNonNullValuesOfNullDroppedFields is
// its negative control, because with a null-only fixture the guarded and
// unguarded loops produce identical output and every other test stays green.
// Live null rates 2026-09-01, over 1,935 items: external_share_type and
// external_share_key 100%, milestone 96.9%, parent_work_item_id 94.8%,
// current_attempt_id 19.2%, closed_at 12.2%.
var listWorkItemNullMeansNone = map[string]struct{}{
	// Sharing is off for every row that exists (null on 1,935/1,935 sampled
	// 2026-09-01); when a share is created these become a type and a key, and
	// say so.
	"external_share_type": {},
	"external_share_key":  {},
	// "no milestone" / "no parent" / "not closed" / "no live attempt". Each is
	// also stated positively elsewhere in the same item — `status` says whether
	// the item is closed, and current_attempt_epoch survives to say whether it
	// has ever been claimed.
	"milestone":           {},
	"parent_work_item_id": {},
	"closed_at":           {},
	"current_attempt_id":  {},
}
