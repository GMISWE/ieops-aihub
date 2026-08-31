package mcp

// Work-item content echo suppression (aihub#281).
//
// pf_create_work_item / pf_update_work_item answer with the WHOLE work-item
// record, and `content` is 76-78% of those bytes (measured over 873 calls,
// 5,273,338 B of response). In 624 of them the caller had sent that exact
// string in the same call, so the round trip billed the model a second time for
// bytes it already had in its own context.
//
// ─── This is a delete-list, NOT a keep-list ─────────────────────────────────
//
// slimRecallResult next door projects onto a whitelist, and the INVARIANT note
// at the top of recall_slim.go records that the whitelist has swallowed a
// newly-added field three times: `total` (aihub#249), the truncation pair
// (aihub#269) and `unmatched_types` (aihub#289). A field added downstream is
// dropped by default there until somebody remembers to list it.
//
// This file is deliberately built the other way round so it cannot become the
// fourth instance: it names the ONE key it removes and copies nothing. A field
// added to domain.WorkItem tomorrow reaches the caller with no edit here, and
// TestSuppressContentEchoLeavesEveryOtherFieldAlone pins that with a key the
// code has never heard of.
//
// ─── Why the drop is gated on equality, not on "the caller sent something" ───
//
// The value in the response is NOT a buffer we echo back. UpdateWorkItem
// commits, calls refreshWorkItemEmbeddingBestEffort (a synchronous network
// call, work_items.go), and only then re-reads the row via GetWorkItem — so
// hundreds of milliseconds separate "your write landed" from "this value was
// read". A concurrent writer inside that window makes the response content
// differ from what you sent, in exactly the case where you most needed to see
// it.
//
// So the gate is the strongest one available: drop the bytes only when the
// stored string is byte-identical to the string the caller sent in this same
// call. Then "lossless" is not a claim about how the server usually behaves —
// it is checked per call, on the two values themselves. When they differ the
// content is returned IN FULL, which is both the safe answer and the only way
// the caller can see that it happened.
//
// This supersedes the alternative of returning a short hash of the stored
// content (the repo already has that spelling — `old_content_hash`, sha256[:8]
// hex, in the wi_content_updated event). A hash lets the caller detect a
// divergence it must then re-fetch to resolve; comparing server-side detects
// the same divergence and simply answers with the content. It is also worth
// noting that the caller here is a language model, which cannot compute sha256
// over its own request to compare against.

// contentSentByCaller returns the content string the caller supplied in this
// call, and whether it supplied one at all.
//
// The type assertion is load-bearing and a bare `_, ok := args["content"]`
// presence check is WRONG. parseArgs is a plain json.Unmarshal into
// map[string]any, so an explicit `content: null` puts the key in the map with a
// nil value — while the server's `Content *string` treats null and absent
// identically and leaves the stored body untouched. Keyed on presence alone we
// would then delete the echo of the EXISTING content, which the caller never
// sent and cannot reconstruct. (Zero occurrences in the 873-call sample; a
// latent defect, not an observed one.)
func contentSentByCaller(args map[string]any) (string, bool) {
	s, ok := args["content"].(string)
	return s, ok
}

// dropContentEcho removes `content` from a work-item response and leaves
// `content_len` in its place. Reports whether it removed anything.
//
// content_len is the length in BYTES, matching Go's len() and the
// `new_content_length` field the wi_content_updated event already carries for
// the same string (work_items.go). Note that the DB CHECK on the column is
// `length(content) <= 20000`, which Postgres counts in CHARACTERS — the two
// units differ for non-ASCII content, and content_len is not the number to
// compare against that 20000.
//
// Leaving the length behind is the aihub#269 rule: removing information is
// fine, but leave a handle that makes an anomaly visible. Its PRESENCE is the
// larger half of that — without it a caller cannot tell "the content was
// suppressed" from "this work item has no content".
func dropContentEcho(result map[string]any) bool {
	if result == nil {
		return false
	}
	stored, ok := result["content"].(string)
	if !ok {
		// Absent, or JSON null for a work item that has no body. Nothing to
		// suppress, and emitting content_len: 0 here would claim a stored empty
		// string where the record actually holds NULL.
		return false
	}
	delete(result, "content")
	result["content_len"] = len(stored)
	return true
}

// suppressContentEcho drops the content echo from a create/update response, but
// only when the stored value is byte-identical to what the caller sent in the
// same call. Reports whether it dropped anything.
//
// Unconditional — there is no flag to turn it on, and that is the point. A
// default-false flag would leave the saving waiting on a plugin release that
// changes every call site, and this repo has the measurement showing what that
// is worth: pf_get_work_item has had `brief` since aihub#212 and was still
// called 489 times for 1,537,444 B in the sample window because essentially
// nobody passes it. What makes going flagless safe here is not boldness, it is
// the equality gate above: the only bytes removed are bytes the caller is
// holding as it reads the reply.
func suppressContentEcho(args, result map[string]any) bool {
	sent, ok := contentSentByCaller(args)
	if !ok || result == nil {
		return false
	}
	if stored, ok := result["content"].(string); !ok || stored != sent {
		return false
	}
	return dropContentEcho(result)
}
