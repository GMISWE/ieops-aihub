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
// TestUpdateKeepsFieldsThisCodeHasNeverHeardOf pins that with a key the code has
// never heard of.
//
// That framing covers what this file REMOVES. It does not cover what this file
// WRITES: content_len goes into a map this package does not own, so a
// domain.WorkItem that ever gained a field serialised as `content_len` would be
// clobbered in silence — and the unknown-field test cannot see it, because it
// probes a name nothing produces, which is the opposite of a collision.
// TestDomainWorkItemHasNoContentLenField is the guard for that half.
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
// The type assertion earns its place, but on a far narrower input than the
// original rationale here claimed, and that claim is worth correcting rather
// than deleting: it read confidently enough that nobody would re-derive it.
//
// The setup is real. parseArgs is a plain json.Unmarshal into map[string]any, so
// an explicit `content: null` puts the key in the map with a nil value, while
// the server's `Content *string` reads null and absent alike and leaves the
// stored body untouched.
//
// What does NOT follow — and what the old comment asserted — is that a bare
// presence check would delete the echo of an existing body. It would not. A
// presence check yields sent == "", and suppressContentEcho drops only when
// stored == sent, so any work item with a body is refused by the equality gate
// regardless. The gate subsumes almost all of this guard, which is exactly why
// swapping the assertion for a presence check survived the whole test suite
// until the case below was written.
//
// The assertion buys precisely one input: a work item whose stored content is
// the empty string, updated with `content: null`. There sent == "" == stored,
// the gate is satisfied, and "leave the body alone" would be answered with the
// field deleted and content_len: 0 — a caller told a suppression happened when
// it sent nothing at all. Small, but it is the whole difference between the two
// spellings, and TestUpdateWithNullContentAgainstAnEmptyStoredBody pins it.
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
		//
		// This is also what brief=true does on a bodyless work item: `content:
		// null` survives and no length is reported. Deliberate, and the published
		// description says so. Under brief the reply then carries either
		// "content_len: N" (there is a body, this big) or "content: null" (there
		// is none) — always a positive signal. Deleting the null instead would
		// leave ABSENCE carrying the meaning, which is the aihub#269 ambiguity
		// the content_len handle exists to remove, not to relocate.
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
	// The !ok half is defence in depth, not a gate: dropContentEcho repeats the
	// same type assertion and refuses on its own, so dropping !ok here is a
	// provably equivalent mutation (it can only be reached with sent == "", and
	// dropContentEcho then bails anyway). Kept because the alternative is code
	// that reads as though a failed assertion compares "" against the caller's
	// string, and because it stops being equivalent the moment dropContentEcho's
	// guard is relaxed.
	if stored, ok := result["content"].(string); !ok || stored != sent {
		return false
	}
	return dropContentEcho(result)
}
