package mcp

import (
	"encoding/json"
	"math"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// slimRecallResult projects a pf_recall response to the fields an LLM actually
// uses, dropping bookkeeping columns and heavy blobs (commits, attrs internals)
// the model never reads. content is kept verbatim (zero info loss). opt3 Phase 1;
// see ieops-docs/polyforge-aihub-improvement/25-optimization3-phase1.md.
func slimRecallResult(result map[string]any) map[string]any {
	return slimRecallResultMode(result, false)
}

// slimRecallResultMode is slimRecallResult with the aihub#313 brief projection
// switched in. It exists as a separate entry point so every pre-existing test
// keeps calling the one-argument form and keeps guarding FULL mode: brief mode
// is additive, and the tests that lock `total`, the truncation pair and
// unmatched_types must not silently start describing the new shape.
//
// aihub#313. CANONICAL MEASUREMENT for every ratio in this file, so that no two
// comments quote the same statistic from different samples: one live no-top_k
// pf_recall against prod aihub, 2026-09-01, counted with the real tokenizer
// (POST /v1/messages/count_tokens, fixed overhead 7 subtracted).
//
//	20 items, full  = 6,966 tok (349/item) — content 60.4%, related 15.9%,
//	                  tags 0.3%, bookkeeping + JSON glue 23.4%
//	20 items, brief = 1,766 tok = 25.4% of full (cut 74.6%), still 20 items
//
// Brief drops the body past its first line plus `related` and `tags`, keeping only
// what the model needs to decide whether a memory is worth reading and the `id` to
// read it with. This is a projection, NOT a filter: the item COUNT is unchanged,
// because recall breadth is the value of recall — narrowing top_k would trade away
// the thing worth keeping.
//
// 25.4% slightly misses the wi's <=25% target, and the 0.6pp is a deliberate
// purchase: see briefRoundDigits. Rounding to 3dp instead reaches 24.8% and can
// flip pf-retro's `similarity > 0.85` branch, which is a correctness bug traded for
// half a percentage point.
func slimRecallResultMode(result map[string]any, brief bool) map[string]any {
	if result == nil {
		return result
	}
	items, ok := result["items"].([]any)
	if !ok {
		return result
	}
	// INVARIANT: this whitelist is opt-in, so a field added to the REST response
	// downstream is dropped here by default until it is listed. That has now bitten
	// twice — `total` (aihub#249) and the truncation pair below (aihub#269). When you
	// add a field to RecallResponse or domain.Memory, decide here whether the model
	// needs it; do NOT widen it wholesale, the dropped bookkeeping columns are the
	// bulk of the opt3 Phase 1 token saving (locked by
	// TestSlimRecallResult_StillDropsBookkeeping; attrs and commits are rewritten
	// rather than dropped, locked by TestSlimRecallResult_RewritesAttrsAndCommits).
	keep := map[string]bool{
		"id": true, "type": true, "content": true, "effective_strength": true,
		"similarity": true, "work_item_id": true, "tags": true, "related": true,
		"created_at": true,
		// aihub#269: content is truncated to 800 runes by handleRecall (PR #245), which
		// flags the cut with these two. Without them the model reasons on a snippet
		// believing it is the whole memory, and has no full length to tell it a
		// pf_get_memory follow-up is warranted — the escape hatch PR #245 declared,
		// which aihub#269 also gave a tool. Both are `omitempty`, so untruncated
		// items pay nothing.
		"content_truncated": true, "content_full_len": true,
	}
	slim := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			slim = append(slim, it)
			continue
		}
		out := make(map[string]any, len(keep)+1)
		for k, v := range m {
			if keep[k] {
				out[k] = v
			}
		}
		if attrs, ok := m["attrs"].(map[string]any); ok {
			if sp, ok := attrs["structured_payload"]; ok {
				out["attrs"] = map[string]any{"structured_payload": sp}
			}
		}
		// commits: keep the human INSIGHT (comment + reply bodies, and who said it) but
		// strip bookkeeping (ids, author_user_id, timestamps, anchors, thread structure).
		// Empty commits stay omitted (zero cost). Flagged useful in report review 2026-07-29.
		if commits, ok := m["commits"].([]any); ok && len(commits) > 0 {
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
			if len(notes) > 0 {
				out["commits"] = notes
			}
		}
		if brief {
			out = briefRecallItem(out)
		}
		slim = append(slim, out)
	}
	res := map[string]any{"items": slim}
	if nc, ok := result["next_cursor"]; ok && nc != nil {
		res["next_cursor"] = nc
	}
	// aihub#249: total (count of memories matching the request's filters,
	// independent of pagination) must survive slimming — otherwise pf_recall
	// callers have no way to distinguish "that's everything" from "keep
	// paging", the exact gap this wi exists to close. Same conditional-copy
	// pattern as next_cursor above.
	if total, ok := result["total"]; ok && total != nil {
		res["total"] = total
	}
	// aihub#289: unmatched_types names the `type` entries that matched no row. It
	// exists solely to be READ BY THE MODEL — dropping it here would reinstate the
	// silence the field was added to end, on the one caller that matters most. The
	// server omits it when there is nothing to report, so healthy recalls pay
	// nothing. Third instance of this whitelist swallowing a new field (total,
	// aihub#249; the truncation pair, aihub#269); see the INVARIANT note above.
	if um, ok := result["unmatched_types"]; ok && um != nil {
		res["unmatched_types"] = um
	}
	// ...and its failure half. Forwarding the list but not the error would put the
	// silence straight back: the model would read "no unmatched_types" as "your type
	// filter is fine" in exactly the case where nothing was actually checked.
	if ue, ok := result["unmatched_types_error"]; ok && ue != nil {
		res["unmatched_types_error"] = ue
	}
	return res
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

// briefFields are the item fields brief mode keeps. Deliberately NOT the slim
// whitelist minus content: `related` (15.9% of a real response) and `tags` are
// dropped too, because a pointer the model cannot act on without a second read
// is exactly the bulk this projection exists to remove.
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
