package domain

// aihub#314 — one generic disclosure field for "the server changed the value you
// sent", shared by every response that has such a value to disclose.
//
// ─── Why one generic field and not one named field per clamp ────────────────
//
// The forcing shape is next door in internal/mcp/recall_slim.go: pf_recall's
// projection is an opt-in WHITELIST, so a field added to the REST response is
// dropped by default until somebody edits that whitelist. That has already eaten
// three fields — `total` (aihub#249), the truncation pair (aihub#269) and
// `unmatched_types` (aihub#289) — which means the price of telling a caller
// "I changed your parameter" has been a cross-package edit, while the price of
// saying nothing has been zero. aihub#309 paid exactly that price: it deleted a
// clamp rather than disclose it, and wrote down why (see normalizeRecallTopK).
//
// A per-clamp field (`top_k_clamped`, `limit_clamped`, …) would reproduce the
// problem once per clamp. One field that every clamp APPENDS to is edited into
// the whitelist once, and the next clamp costs nothing to disclose.
//
// ─── No new input parameter, deliberately ───────────────────────────────────
//
// There is no flag to turn this on. aihub#279 measured that adding a parameter
// to a low-frequency tool can be NET NEGATIVE (−1.7×): the schema is charged on
// every request that lists the tool, the saving is charged per call. This field
// costs nothing on a request that was not adjusted (see the shape note below),
// so there is nothing for a flag to buy.
//
// ─── The shape when nothing was adjusted: the key is ABSENT ─────────────────
//
// aihub#278 left the rule that governs this: *a key may only be omitted when its
// value is null*, because a value-gated drop hands the caller a `null` where a
// real value used to be and nothing reports it. Two candidate shapes obey it in
// different ways; this one omits the key, and the reason is that the two states
// being conflated are not distinguishable states:
//
//	`request_adjusted: []`  and  no `request_adjusted` key
//
// both say "nothing about your request was changed". An empty list carries no
// information the absence does not, so omitting it is a NULL-gated drop, not a
// value-gated one — the same argument `unmatched_types` (aihub#289) already
// makes on this very response, and the same one the MCP projections make for
// `milestone` and friends. A non-empty list is never omitted, by anything, and
// that is the half the rule actually protects.
//
// ⚠️ The cost of that choice, stated rather than hidden: absence now means BOTH
// "nothing was adjusted" and "this server predates aihub#314". A caller cannot
// tell them apart, and there is no version field on these responses to tell them
// with. That is acceptable here and would not be if the field ever grew a
// meaning like "we checked and found nothing wrong" — because then absence would
// be a claim, and an old server would be making it silently. It does not: an
// absent `request_adjusted` asserts nothing, and every claim this field makes is
// carried by an entry that is present.
//
// ─── Scope: values that ARRIVED and were then changed ───────────────────────
//
// This field can only report an adjustment to a parameter the server actually
// received. It is not a fix for, and must not be described as covering:
//
//   - a parameter that never arrives. aihub#148's `similarity_threshold` was
//     published in pf_recall's schema, implemented here, and forwarded by neither
//     hop in between, so passing 0.99 and passing nothing returned byte-identical
//     bodies. There is no "requested" value to report.
//   - a parameter dropped on a type mismatch: pf_recall read `top_k` with strArg,
//     so a JSON NUMBER was silently ignored and the server saw no top_k at all.
//     Same reason — nothing arrives, so nothing was adjusted.
//
// 🟢 BOTH were fixed by aihub#148 (7cb982f), which is the point rather than a
// footnote: they were wiring defects one hop upstream, and they were fixed BY
// WIRING, not by disclosure. `limit` had the identical type-mismatch defect and
// was fixed the identical way in 374f851. A disclosure field that appeared to
// cover this class would be worse than one that does not — it would let a caller
// read "no adjustment" as "your parameter was honoured", when in fact the
// parameter never reached the code that could have adjusted it.
//
// The boundary is therefore permanent and not a to-do: this field reports what
// the server DID to a value it HELD. What the server never received is a
// different defect with a different fix.

// RequestAdjustment is one entry of a response's `request_adjusted` list: the
// parameter, the value the caller sent, and the value the server used instead.
//
// Requested and Applied are `any` so that a future adjustment to a non-integer
// parameter (a rewritten sort key, a coerced enum) can be reported by the same
// field rather than by a fourth silently-dropped one. The constructor below is
// int-typed because both of today's callers clamp an int; add a sibling rather
// than widening this one, so each caller's "was it really adjusted?" test stays
// exact.
type RequestAdjustment struct {
	Param     string `json:"param"`
	Requested any    `json:"requested"`
	Applied   any    `json:"applied"`
}

// appendIntAdjustment appends an entry when an integer parameter that ARRIVED
// was changed on the way in, and returns dst untouched otherwise.
//
// Two guards, and they are not the same guard:
//
//	requested == applied  nothing happened; reporting it would make every
//	                      response carry a "we honoured your request" entry and
//	                      train the reader to skip the field.
//
//	requested == 0        we cannot tell whether it arrived. Both of today's
//	                      callers hold the parameter in a plain `int`, so "the
//	                      caller sent 0" and "the caller sent nothing" are the
//	                      same value by the time it gets here — the zero-value /
//	                      absent-field ambiguity. Claiming `requested: 0` would
//	                      be inventing a request the caller may never have made,
//	                      so the honest report is silence.
//
// 🔴 The second guard is a REAL blind spot, not a formality, and closing it needs
// a *int (or a "was present" bool) threaded from whichever layer parses the query
// string — not a change here. Its live reach is small and worth stating exactly:
//
//   - GET /v1/work_items — handleListWorkItems defaults filter.Limit to 50 and
//     only overwrites it when the parsed value is > 0, so a caller-supplied 0,
//     a negative, and a malformed `limit=abc` all reach ListWorkItems as 50 and
//     are indistinguishable from sending nothing. The clamp's `<= 0` branch is
//     therefore unreachable over HTTP; `limit=500 -> 50` is the whole live case,
//     and it is disclosed.
//   - GET /v1/memories — handleRecall assigns req.TopK straight from the parsed
//     value, so `top_k=0` and `top_k` absent are both 0 here, while `top_k=-3`
//     and `top_k=500` arrive intact and are disclosed.
func appendIntAdjustment(dst []RequestAdjustment, param string, requested, applied int) []RequestAdjustment {
	if requested == 0 || requested == applied {
		return dst
	}
	return append(dst, RequestAdjustment{Param: param, Requested: requested, Applied: applied})
}
