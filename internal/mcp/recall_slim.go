package mcp

import (
	"encoding/json"
	"math"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ─── Recall RESPONSE projection (aihub#418: keep-list -> delete-list) ───────
//
// THE PATTERN THIS FILE USED TO BE THE LAST INSTANCE OF. Both projections here
// were keep-lists: a per-item whitelist of 11 field names, and a top level that
// rebuilt the response as map[string]any{"items": …} and then conditionally
// copied five named keys. Under that shape the cheapest outcome of the server
// adding a field is that the model never sees it — and this file recorded three
// separate occasions when exactly that happened, each fixed by adding one more
// conditional-copy line rather than by changing the shape:
//
//	total             aihub#249  a caller could not tell "that's everything"
//	                             from "keep paging"
//	content_truncated aihub#269  the model reasoned on a snippet believing it
//	 + content_full_len          was the whole memory
//	unmatched_types   aihub#289  "your type filter matched nothing" became
//	                             indistinguishable from "no such memory"
//
// aihub#281 built list_wi_slim.go the other way round and said in its header
// "so it cannot become the fourth instance"; aihub#388 converted the claim
// response and folded this one. This is that fold, paid off.
//
// ─── The rule, and the three boundaries ─────────────────────────────────────
//
// 1. TOP LEVEL — delete-list, and nothing is deleted. The response map is
//    mutated in place: `items` is replaced with the projected items and every
//    other key is forwarded because nobody copies it. next_cursor, total,
//    unmatched_types, unmatched_types_error and request_adjusted are no longer
//    mentioned by name anywhere in this file, which is the point — the three
//    incidents above are now unreachable at this level rather than individually
//    patched.
//
// 2. ITEM, FULL MODE — delete-list. recallItemWithheldKeys below names every
//    dropped field WITH a reason; anything else the server sends arrives. The
//    reasons are the payload: a delete-list whose entries carry none degrades
//    into a keep-list written backwards, because the next reader cannot tell a
//    deliberate exclusion from a leftover.
//
// 3. ITEM, BRIEF MODE — deliberately still a keep-list, and this is the
//    boundary the work item asked to have stated precisely rather than blurred.
//    briefRecallItem builds from briefFields, so a field added tomorrow does NOT
//    appear in brief output. That is not the defect above, it is the mode's
//    purpose: brief is 25.4% of full BECAUSE it forwards nothing by default, and
//    a delete-list there would re-admit every new bookkeeping column into the
//    projection whose entire job is to remove them. What makes it safe is a
//    different property, and it is a contract rather than a hope: brief is lossy
//    BY DECLARATION and carries its own escape hatch — `id` to re-read with, and
//    content_truncated + content_full_len to say that there is more. Anything
//    brief drops is retrievable with one pf_get_memory call, which is exactly
//    what the swallowed fields above were NOT.
//
//    ONE FIELD IS CONDITIONAL rather than declared, and aihub#429 added it:
//    `status` is forwarded when it is not "active". A pure keep-list left a
//    caller that asked for archived rows unable to tell which rows those were,
//    and adding `status` to briefFields outright would have spent ~6.8% of a
//    brief response transmitting the constant "active" on every recall that did
//    not ask. See briefStatusIsInformative for the measurement and for the two
//    alternatives that were rejected.
//
//    Note also what brief does to `content`: it REPLACES the value with a
//    first-line summary. That is content transformation, not key dropping, and
//    the distinction matters because the key is still there — a caller reading
//    `content` gets a shorter true thing, not a missing thing, and the truncation
//    pair tells it so.
//
// ⇒ RESIDUAL, stated rather than left to be discovered: `attrs` and `commits`
//    are NARROWED, not dropped, and their interiors are still keep-lists —
//    attrs down to structured_payload, commits down to body/by/replies. A new
//    SUBFIELD inside either will not arrive. They are nested blobs whose bulk is
//    bookkeeping, so forwarding them wholesale would give back most of what this
//    projection saves; the trade is deliberate, it is narrower than the one this
//    change removes, and recallItemNarrowedKeys records it so it is countable
//    instead of invisible.
//
// ─── Canonical measurement (aihub#313) ──────────────────────────────────────
//
// Every ratio in this file comes from ONE sample, so that no two comments quote
// the same statistic from different runs: one live no-top_k pf_recall against
// prod aihub, 2026-09-01, counted with the real tokenizer
// (POST /v1/messages/count_tokens, fixed overhead 7 subtracted).
//
//	20 items, full  = 6,966 tok (349/item) — content 60.4%, related 15.9%,
//	                  tags 0.3%, bookkeeping + JSON glue 23.4%
//	20 items, brief = 1,766 tok = 25.4% of full (cut 74.6%), still 20 items
//
// Brief keeps only what the model needs to decide whether a memory is worth
// reading and the `id` to read it with. This is a projection, NOT a filter: the
// item COUNT is unchanged, because recall breadth is the value of recall —
// narrowing top_k would trade away the thing worth keeping.
//
// 25.4% slightly misses the wi's <=25% target, and the 0.6pp is a deliberate
// purchase: see briefRoundDigits. Rounding to 3dp instead reaches 24.8% and can
// flip pf-retro's `similarity > 0.85` branch, which is a correctness bug traded
// for half a percentage point.

// recallItemWithheldKeys names the per-item keys the FULL projection removes,
// and why. Grouped by the reason they share, because 19 individually-worded
// entries would obscure that there are really four decisions here.
//
// 🔴 A key absent from this map is FORWARDED. That is the inversion aihub#418
// bought: adding a field to domain.Memory now costs nothing to expose and one
// line here to hide, where before it cost one line to expose and nothing to
// lose. Every entry is quantified by TestRecallResultCarriesEveryMemoryField,
// which reflects over the struct, so a field added tomorrow is covered that day.
var recallItemWithheldKeys = map[string]string{
	// ── Volume. rendered_html is the single largest field a memory can carry:
	//    a complete standalone HTML document on methodology.* artifacts. The
	//    model cannot use markup, and one such field can exceed the whole rest
	//    of the response.
	"rendered_html": "a full standalone HTML document on methodology.* artifacts — the largest " +
		"field a memory can carry, and unusable by a model. /ui and the artifact HTML viewer " +
		"serve it; pf_get_memory returns the markdown source.",
	"backlinks": "the reverse edge of `related`, which IS forwarded. Both directions is double the " +
		"pointer volume for one graph, and the model can only act on a pointer by spending a " +
		"pf_get_memory call either way.",

	// ── Decay bookkeeping. effective_strength IS forwarded and is the only
	//    number any skill acts on (four of them gate display on >= 0.3). These
	//    are the inputs it is computed FROM, so forwarding them pays for the
	//    model to re-derive a value it already has.
	"base_strength":     "an input to effective_strength, which is forwarded; nothing reads the input.",
	"stability_days":    "same — a decay parameter, not a fact about the memory's content.",
	"activation_count":  "same — reinforcement bookkeeping behind effective_strength.",
	"last_activated_at": "activation bookkeeping; recency judgements use created_at, which is forwarded.",
	"last_activated_by": "who last activated it. Provenance of a read, not content, and not actionable.",
	"is_immortal":       "a decay exemption flag, i.e. another effective_strength input.",
	"expires_at": "lifecycle bookkeeping; an expired memory is not returned at all, so a " +
		"forwarded value could only ever say \"not yet\".",

	// ── Identity the caller already fixed or cannot act on. project comes from
	//    the request; author/visibility/status are governance, not content.
	"project": "the caller supplied it in the request — echoing it back per item pays for a " +
		"value the caller already holds.",
	"author_user_id": "an opaque internal id the model cannot resolve or act on.",
	"author_display": "provenance rather than content; pf_get_memory carries it for the one memory " +
		"a caller actually opens.",
	"visibility": "an access-control fact already enforced server-side — anything the caller " +
		"cannot see is not in this list at all, so the field can only ever confirm the obvious.",
	// ⚠️ `status` is NOT in this list, and the first draft of this change had it
	// here with the reason "recall returns live rows, so this is always active".
	// That reason is false in a reachable case: the recall predicate is
	// `status IN ('active')` by default but `IN ('active','archived')` when the
	// request sets include_archived (internal/domain/memory.go), so a caller who
	// explicitly asked for archived rows gets a mixed result set — and dropping
	// `status` would remove precisely the answer to the question that caller just
	// asked. That is the aihub#249 harm, not a token saving. It costs about 6
	// tokens an item (~1.7% of the canonical 20-item full response).
	//
	// ⚠️ `latest_id` is NOT in this list either, and aihub#429 is why. It WAS
	// here, withheld as "supersession bookkeeping. Recall already resolves to the
	// head version, so this points at the row the caller is holding" — the SAME
	// defect the paragraph above records for `status`, made a second time in the
	// same commit: a per-drop reason that is true of the default predicate and
	// false of the include_archived one. Three facts in internal/domain/memory.go
	// settle it, and all three are load-bearing:
	//
	//	:1279-1285  a new row is inserted with latest_id = its OWN id (aihub#201's
	//	            self-head trick), so a head's latest_id equals itself
	//	:1205       a supersede ARCHIVES the old head
	//	:1333-1336  and then repoints every row WHERE latest_id = oldHead at the
	//	            new id — which matches the just-archived old head itself,
	//	            precisely because its latest_id equalled its own id
	//
	// So for an archived row, latest_id is NOT "the row the caller is holding":
	// it is the pointer to the current head, and it is the only field in the
	// response that can get the caller there. Withholding it left a caller that
	// asked for archived rows holding a superseded body with no forward edge —
	// recoverable only by spending a pf_get_memory on every item to find out
	// which ones even needed it.
	//
	// FORWARDED rather than re-worded, which was the other option the work item
	// offered. Scoping the reason to the non-archived case would leave the field
	// dropped for the one caller who needs it and merely be honest about doing
	// so; the field is load-bearing for following a version chain, it is one
	// short id, and under a delete-list forwarding costs a deleted line rather
	// than an added one.
	//
	// WHAT IT COSTS, stated because every other entry here is quantified: about 9
	// tokens an item, ~2.6% of the canonical 20-item full response — the same
	// order as `status` above at 1.7%, and paid on every full recall, including
	// the ones where every row is active and latest_id is therefore the row's own
	// id (the aihub#201 self-head trick).
	//
	// AND NO, IT IS NOT MADE CONDITIONAL HERE the way brief mode makes `status`
	// conditional, even though the "on an active row the value is redundant"
	// argument reads identically. The two modes carry opposite burdens of proof
	// and that is the whole point of boundaries 2 and 3 in the file header. FULL
	// is a delete-list: forwarding is the default and hiding is what needs a
	// reason, because three separate incidents (aihub#249, #269, #289) came from
	// a field being silently absent. Adding a value-conditional rule here would
	// re-introduce exactly the per-field cleverness aihub#418 removed, to save
	// 2.6%. BRIEF is a keep-list with the burden the other way round, which is
	// why the same argument wins there and loses here.
	"source_artifact_id": "an internal provenance pointer to the artifact a memory was extracted " +
		"from; not resolvable by the model.",
	"updated_at": "row-mutation bookkeeping. created_at is the one a recency judgement uses and is " +
		"forwarded; updated_at moves on a reinforce and would read as recency it does not mean.",

	// ── Embedding internals. Pure infrastructure.
	"emb_model": "which embedding model produced the vector — an infrastructure fact with no " +
		"bearing on what the memory says.",
	"emb_dims": "the vector's dimensionality; same.",
}

// recallItemNarrowedKeys documents the two keys that are NARROWED in place
// rather than forwarded or dropped, and is the honest record of this
// projection's remaining keep-list surface.
//
// It is a map with reasons for the same purpose as the delete-list above, and it
// is asserted: TestRecallResultNarrowsAttrsAndCommits pins both the value that
// survives and the fact that the interior is still a whitelist, so the residual
// is measured rather than described.
var recallItemNarrowedKeys = map[string]string{
	"attrs": "kept ONLY as {structured_payload}. The rest of attrs is per-type internal " +
		"bookkeeping; structured_payload is the half a caller wrote deliberately (spec " +
		"acceptance criteria, review findings). Residual: a new key inside attrs does not arrive.",
	"commits": "kept as the human INSIGHT only — body, author_display as `by`, and reply bodies — " +
		"with ids, author_user_id, timestamps, anchors and thread structure stripped. Residual: a " +
		"new key inside a commit does not arrive.",
}

// slimRecallResult projects a pf_recall response to what a model can act on:
// bookkeeping columns and heavy blobs are removed, `content` is kept verbatim
// (zero information loss). opt3 Phase 1; see
// ieops-docs/polyforge-aihub-improvement/25-optimization3-phase1.md.
func slimRecallResult(result map[string]any) map[string]any {
	return slimRecallResultMode(result, false)
}

// slimRecallResultMode is slimRecallResult with the aihub#313 brief projection
// switched in. It exists as a separate entry point so every pre-existing test
// keeps calling the one-argument form and keeps guarding FULL mode: brief mode
// is additive, and the tests that lock `total`, the truncation pair and
// unmatched_types must not silently start describing the new shape.
//
// IT MUTATES `result` AND RETURNS THE SAME MAP. That is the aihub#249 lesson
// applied structurally instead of remembered: a rebuilt map drops every key
// nobody thought to copy, which is how `total` vanished. Nothing is copied at
// the top level, so nothing can be forgotten there.
func slimRecallResultMode(result map[string]any, brief bool) map[string]any {
	if result == nil {
		return result
	}
	items, ok := result["items"].([]any)
	if !ok {
		return result
	}
	slim := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			slim = append(slim, it)
			continue
		}
		// The item is projected IN PLACE for the same reason the top level is.
		for k := range recallItemWithheldKeys {
			delete(m, k)
		}
		narrowRecallAttrs(m)
		narrowRecallCommits(m)
		if brief {
			m = briefRecallItem(m)
		}
		slim = append(slim, m)
	}
	result["items"] = slim
	return result
}

// narrowRecallAttrs reduces attrs to {structured_payload}, or removes it when
// there is no structured_payload to keep. See recallItemNarrowedKeys.
func narrowRecallAttrs(m map[string]any) {
	attrs, ok := m["attrs"].(map[string]any)
	if !ok {
		// Not an object (absent, null, or some other shape): nothing to narrow,
		// and forwarding an un-narrowed attrs is what this exists to prevent.
		delete(m, "attrs")
		return
	}
	sp, ok := attrs["structured_payload"]
	if !ok {
		delete(m, "attrs")
		return
	}
	m["attrs"] = map[string]any{"structured_payload": sp}
}

// narrowRecallCommits reduces each commit to the human insight — body, who said
// it, and reply bodies — and removes the key when nothing insightful survives.
// Empty commits stay absent (zero cost). Flagged useful in report review
// 2026-07-29. See recallItemNarrowedKeys.
func narrowRecallCommits(m map[string]any) {
	commits, ok := m["commits"].([]any)
	if !ok || len(commits) == 0 {
		delete(m, "commits")
		return
	}
	notes := make([]any, 0, len(commits))
	for _, c := range commits {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		note := map[string]any{}
		if b, ok := cm["body"]; ok {
			note["body"] = b
		}
		if a, ok := cm["author_display"]; ok {
			note["by"] = a
		}
		if reps, ok := cm["replies"].([]any); ok && len(reps) > 0 {
			rb := make([]any, 0, len(reps))
			for _, r := range reps {
				if rm, ok := r.(map[string]any); ok {
					if b, ok := rm["body"]; ok {
						rb = append(rb, b)
					}
				}
			}
			if len(rb) > 0 {
				note["replies"] = rb
			}
		}
		if len(note) > 0 {
			notes = append(notes, note)
		}
	}
	if len(notes) == 0 {
		delete(m, "commits")
		return
	}
	m["commits"] = notes
}

// briefContentMax bounds the summary a brief item carries, in runes.
//
// A cap is NOT optional here, and "first line" alone is not a projection: for a
// memory with no newline the first line IS the whole (already 800-rune-truncated)
// body, so an uncapped rule saves nothing on those while reading as though it saved
// everything. Two samples, each labelled because they differ:
//
//	the live 20-item response above : 9 of 20 items newline-free (45%)
//	2,541 items across 659 recalls  : 37.5% newline-free, 44.2% of first
//	                                  lines over 200 chars, median 111
//
// Sweep on the live sample, before briefLine existed: uncapped 29.8% of full,
// cap 120 -> 28.5%, cap 80 -> 27.9%. 120 is the knee — it leaves the corpus median
// first line (111 chars) intact and the return below it is ~0.6 points per 40
// runes, paid for by cutting real headlines in half.
const briefContentMax = 120

// briefFields are the item fields brief mode keeps UNCONDITIONALLY. Deliberately
// NOT the slim whitelist minus content: `related` (15.9% of a real response) and
// `tags` are dropped too, because a pointer the model cannot act on without a
// second read is exactly the bulk this projection exists to remove.
//
// `status` is deliberately NOT here, and briefRecallItem forwards it
// CONDITIONALLY instead — see briefStatusIsInformative for the whole argument.
// The short version: unconditional would have cost ~6 tokens an item, i.e. ~120
// of the canonical 1,766-token brief response (6.8%), taking brief from 25.4% of
// full to ~27.1% — and this file already treats 0.6pp as worth arguing about (see
// briefRoundDigits). Every one of those tokens would have spelled "active", the
// only value the default predicate can return.
//
// `id` must stay in this list: it is the ONLY mechanism that carries the id into a
// brief item, and criterion 2 of aihub#313 (full text stays retrievable per item)
// rests on it. An earlier draft also had a fallback `if b["id"] == nil` block after
// the loop; review showed it was unreachable — b lacks id iff m lacks id — and that
// its presence made TestBriefRecallItem_AlwaysCarriesID blind to `id` being removed
// from HERE, the actual mechanism. Locked now by
// TestBriefFields_KeepsIDAsTheRetrievalMechanism.
var briefFields = []string{"id", "type", "similarity", "effective_strength", "created_at"}

// briefRoundDigits is the precision brief mode keeps for similarity and
// effective_strength.
//
// 4, not 3, and the reason is a real caller rather than taste. The first draft
// rounded to 3dp and justified it as "the consumers are an ordering and a >= 0.3
// threshold". Review found that wrong: pf-retro branches on `max_similarity > 0.85`
// and `> 0.65` to choose REINFORCE-existing vs CREATE-new memory
// (plugins/polyforge/skills/pf-retro/SKILL.md), and four skill files gate display on
// `effective_strength >= 0.3`. 3dp moves a value by up to 5e-4, enough to flip a
// true 0.85049 to 0.85 and turn a reinforce into a duplicate. 4dp bounds the error
// at 5e-5 and costs about one character per value.
const briefRoundDigits = 1e4

// trimSubsecond drops the fractional-seconds group from an RFC3339 timestamp,
// leaving second precision and the original offset (Z or +hh:mm). Anything that is
// not recognisably an RFC3339 fractional second is returned untouched.
//
// The shape check is deliberately strict, because an earlier draft only looked for
// "a dot followed by digits" and its doc comment claimed non-timestamps were safe.
// Review disproved that with three counterexamples: "v1.2.3" -> "v1.3",
// "0.5.1" -> "0.1", "3.14 is pi" -> "3 is pi". Unreachable in practice
// (domain.Memory.CreatedAt is a time.Time and always serialises as RFC3339Nano),
// but a comment that asserts a guarantee the code does not provide is how the next
// caller gets surprised. So the guarantee is now real, and it is anchored on the one
// thing an RFC3339 fractional second always has: the dot is preceded by ":SS", and
// the digit group is followed by end-of-string or a zone designator. ("12.5" and
// "a12.5Z" both reach the digit test and are rejected only by the colon.)
func trimSubsecond(s string) string {
	dot := strings.IndexByte(s, '.')
	// Need ":SS." before the dot — the colon is what separates a timestamp's
	// seconds field from any other "digits dot digits" string.
	if dot < 3 || s[dot-3] != ':' || !isDigit(s[dot-1]) || !isDigit(s[dot-2]) {
		return s
	}
	end := dot + 1
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	if end == dot+1 {
		return s // a dot with no digit group is not a fractional second
	}
	// The fractional group must end the timestamp or hand off to a zone.
	if end < len(s) && s[end] != 'Z' && s[end] != 'z' && s[end] != '+' && s[end] != '-' {
		return s
	}
	return s[:dot] + s[end:]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// briefStatusIsInformative decides whether a recall item's `status` earns its
// place in a brief response. "active" does not; anything else does.
//
// THE PROBLEM (aihub#429). Brief mode dropped `status` outright, so a caller that
// set include_archived AND fields="brief" got back a mixed active/archived result
// set with nothing on any item saying which was which — the exact harm aihub#418
// rescued `status` from in full mode, surviving intact in the other mode.
//
// WHY NOT JUST ADD IT TO briefFields, which is the obvious fix and the one the
// work item leaned toward. Because of what the value would BE. The recall
// predicate is status IN ('active') unless the request sets include_archived
// (internal/domain/memory.go:2198-2200), so on every recall that does not set
// that flag — the overwhelming majority — `status` is the constant "active" on
// every item. Unconditional forwarding therefore spends ~6 tokens an item, ~120
// of the canonical 1,766-token 20-item brief response (6.8%), to transmit a value
// that is already implied by the request. Brief exists to be 25.4% of full; that
// would make it ~27.1%, and briefRoundDigits above shows this file weighing
// 0.6pp. Spending 1.7pp on a constant is not a trade this projection makes.
//
// WHY NOT THREAD include_archived DOWN FROM THE REQUEST, the other candidate. It
// would work — buildRecallParams already decides the flag (with parseBoolArg
// since aihub#464; with boolArg when this note was written) and the same read
// could be passed through — but it buys nothing over reading the value. A caller
// can set include_archived and still match no archived rows, and that request
// would then pay the full 6.8% to label twenty rows "active". Reading the datum
// yields a strict subset of what the flag yields, and every byte in the
// difference is provably non-informative. It also keeps briefRecallItem's
// signature, which eighteen existing call sites depend on.
//
// THE RESIDUAL, stated rather than left to be found: absence now means "active".
// That is already this mode's idiom — content_truncated appears only when the
// body was cut, and briefRecallItem skips any briefFields entry the item lacks —
// but it is a convention, and a convention nobody wrote down is how the next
// reader gets surprised. It is written down here and asserted by the brief arms
// of the aihub#429 tests.
//
// It is deliberately NOT added to pf_recall's `fields` description, and that is a
// budget decision rather than an oversight. A tool description is RESIDENT: it is
// injected on every request whether or not the tool is called, so the clause
// would cost every request in the session, while the caller it helps is one that
// sets include_archived AND fields="brief" — and that caller is already looking
// at items labelled "archived" next to items that are not. Paying a per-request
// tax to spell out an inference the labelled rows already make is the wrong side
// of that trade. Revisit if brief+include_archived stops being rare.
//
// The test is `!= "active"` rather than `== "archived"` deliberately. Recall
// cannot return a `redacted` row today (neither status set admits it), but a
// status this function has never heard of is exactly the case where saying
// nothing is worst, so the unknown one is forwarded. That is the delete-list
// disposition applied to a value instead of a key.
func briefStatusIsInformative(status string) bool { return status != "active" }

// briefRecallItem projects one already-slimmed item down to a pointer: enough to
// judge whether the memory is worth reading, plus the id to read it with via
// pf_get_memory. aihub#313.
//
// The escape hatch is aihub#269's, reused rather than reinvented: an item whose
// body was cut carries content_truncated + content_full_len, both already on
// slimRecallResult's whitelist and already named in pf_recall's tool description.
// That is what keeps this a projection instead of silent loss — and it is why
// content_full_len below must stay the TRUE full length. handleRecall may have
// already truncated to 800 runes and recorded the real length; taking the length
// of the string we hold would overwrite 3,587 with 800 and tell the model its
// snippet was nearly whole. Prefer the server's value whenever it is larger.
func briefRecallItem(m map[string]any) map[string]any {
	b := make(map[string]any, len(briefFields)+3)
	for _, k := range briefFields {
		v, ok := m[k]
		if !ok {
			continue
		}
		// Noise trimming, brief mode only — full mode keeps every digit. These two
		// are not cosmetic: on the canonical sample they are ~250 of ~1,770 brief
		// tokens, i.e. ~3.5 points of the 25.4% result.
		//
		// similarity arrives as 0.2891933706162386 and effective_strength as
		// 2.991597364792209; see briefRoundDigits for why the cut is at 4 decimals
		// and not 3. created_at arrives with microseconds
		// (2026-05-21T20:48:26.806541Z) and recency judgements do not resolve below a
		// second. Timestamps stay RFC3339 and parseable — the DATE is NOT truncated,
		// which was the cheaper trim (another ~200 tok) and was rejected because
		// time-of-day is real information.
		switch k {
		case "similarity", "effective_strength":
			// The finiteness guard is not defensive noise about implausible data: it
			// is about what ROUNDING itself would introduce. For a finite but
			// enormous f, f*briefRoundDigits overflows to +Inf and json.Marshal then
			// FAILS on +Inf — which fails the whole tools/call, so brief mode would
			// error on an item full mode serialises fine. Verified through the real
			// MCP transport with effective_strength 1e306. Real values are bounded
			// (similarity -1..1, effective_strength 0..~3) so it cannot fire today;
			// the invariant is that brief mode never fails where full mode succeeds.
			if f, ok := v.(float64); ok && !math.IsInf(f, 0) && !math.IsNaN(f) && math.Abs(f) < 1e15 {
				v = math.Round(f*briefRoundDigits) / briefRoundDigits
			}
		case "created_at":
			if s, ok := v.(string); ok {
				v = trimSubsecond(s)
			}
		}
		b[k] = v
	}
	// aihub#429: `status`, forwarded only when it says something.
	if s, ok := m["status"].(string); ok && briefStatusIsInformative(s) {
		b["status"] = s
	}
	// Only synthesise a `content` key if the item actually had one. Emitting
	// content:"" for a bodyless item would ADD tokens to the response this
	// projection exists to shrink, and would also let a stray content_full_len
	// flag an empty body as truncated.
	// Only synthesise a `content` key if the item actually had one: emitting
	// content:"" for a bodyless item would ADD bytes to the response this
	// projection exists to shrink.
	rawContent, hadContent := m["content"]
	content, _ := rawContent.(string)
	if !hadContent {
		return b
	}

	// "Is there more than what I am showing?" is decided against the body with
	// trailing whitespace removed, so that "hello\n" is NOT advertised as truncated
	// with content_full_len 6 — inviting a pf_get_memory round-trip that buys a
	// newline. The REPORTED length still prefers the server's value: handleRecall
	// may already have cut this item to 800 runes and recorded 3,587, and taking the
	// length of the string in hand would tell the model its snippet was nearly whole.
	full := len([]rune(strings.TrimRight(content, " \t\r\n")))
	// JSON numbers arrive as float64 through the REST client's map[string]any.
	if v, ok := m["content_full_len"].(float64); ok && int(v) > full {
		full = int(v)
	}
	line := briefLine(content)
	b["content"] = line
	if len([]rune(line)) < full {
		b["content_truncated"] = true
		b["content_full_len"] = full
	}
	return b
}

// briefLine picks the first line of content worth showing, capped at
// briefContentMax runes.
//
// "The first line" is NOT good enough, and this is the failure a code review
// caught rather than a hypothetical. Taken literally, `content[:IndexByte('\n')]`
// returns "" for a body that opens with a blank line and "---" for one that opens
// with YAML frontmatter — both entirely plausible in a markdown memory corpus, and
// both produce a brief item carrying ZERO information while still costing four keys
// and still reporting itself as a successful projection. The model's only recovery
// is the full read this projection exists to avoid, so the degenerate case is not
// merely ugly: it converts a saving into an induced round-trip, and one round-trip
// in this workspace costs ~192k tok.
//
// So: skip lines that are blank or pure frontmatter/rule fences, and if the body
// has no informative line at all, fall back to its head with whitespace collapsed
// (never return empty for a non-empty body).
func briefLine(content string) string {
	cap := func(s string) string {
		if r := []rune(s); len(r) > briefContentMax {
			return string(r[:briefContentMax])
		}
		return s
	}
	for _, raw := range strings.Split(content, "\n") {
		// TrimRight also removes the CR of a CRLF body, which would otherwise ride
		// into the payload as a stray control character.
		line := strings.TrimRight(strings.TrimLeft(raw, " \t"), " \t\r")
		if line == "" || strings.Trim(line, "-") == "" {
			continue
		}
		return cap(line)
	}
	// Whitespace-only or fence-only body: strings.Fields collapses every run of
	// whitespace, so this yields "" only when the body is genuinely blank.
	return cap(strings.Join(strings.Fields(content), " "))
}

// jsonResultCompact marshals v WITHOUT indentation. opt3: results the LLM
// consumes do not need pretty-printing; saves ~20-30% whitespace tokens vs the
// default MarshalIndent path.
func jsonResultCompact(v any) (*sdkmcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
	}, nil
}
